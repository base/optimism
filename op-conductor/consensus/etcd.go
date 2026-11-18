package consensus

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/log"
	"github.com/google/uuid"
	"go.etcd.io/etcd/client/pkg/v3/fileutil"
	"go.etcd.io/etcd/client/pkg/v3/types"
	"go.etcd.io/etcd/server/v3/etcdserver/api/rafthttp"
	stats "go.etcd.io/etcd/server/v3/etcdserver/api/v2stats"
	"go.etcd.io/raft/v3"
	pb "go.etcd.io/raft/v3/raftpb"
	"go.uber.org/zap"

	"github.com/ethereum-optimism/optimism/op-service/eth"
)

// CheckRaftImplementation rejects ambiguous or non-durable etcd configurations.
func CheckRaftImplementation(impl, token, storage string, noSync bool) error {
	switch impl {
	case "", "hashicorp":
		if token != "" {
			return errors.New("etcd cluster token requires etcd raft")
		}
	case "etcd":
		if token == "" {
			return errors.New("etcd raft requires a unique cluster token")
		}
		if noSync || normalizeRaftStorageBackend(storage) != RaftStorageBackendBoltDB {
			return errors.New("etcd raft always uses its own synced WAL; do not configure HashiCorp storage options")
		}
	default:
		return fmt.Errorf("unknown raft implementation %q", impl)
	}
	return nil
}

// NewConsensus selects the consensus engine without interpreting foreign storage.
func NewConsensus(logger log.Logger, cfg *RaftConsensusConfig, impl, token string) (Consensus, error) {
	if err := CheckRaftImplementation(impl, token, cfg.StorageBackend, cfg.LogStoreNoSync); err != nil {
		return nil, err
	}
	if impl == "etcd" {
		return newEtcdConsensus(logger, cfg, token)
	}
	return NewRaftConsensus(logger, cfg)
}

// The string IDs and addresses in the conductor RPC remain stable across engines.
func raftID(s string) uint64 {
	h := sha256.Sum256([]byte(s))
	return binary.BigEndian.Uint64(h[:8])
}

var _ Consensus = (*etcdConsensus)(nil)

type etcdConsensus struct {
	log           log.Logger
	cfg           RaftConsensusConfig
	addr          string
	id            uint64
	node          raft.Node
	store         *etcdStore
	transport     *rafthttp.Transport
	server        *http.Server
	leader        atomic.Uint64
	leaseDuration time.Duration
	started       time.Time
	bootID        uuid.UUID
	leaderSince   atomic.Int64
	verified      atomic.Int64
	leadership    chan bool
	stop          chan struct{}
	done          chan struct{}
	once          sync.Once
	changing      atomic.Bool
	pending       sync.Map     // UUID -> buffered completion channel, removed on timeout
	mu            sync.RWMutex // protects the published membership
	members       map[uint64]ServerInfo
	removed       map[uint64]bool
	version       uint64
	tracker       *unsafeHeadTracker
	// Owned by the Ready loop after startup.
	headSSZ                   []byte // preserve the accepted wire version for snapshot catch-up
	memberResults             []memberResult
	conf                      pb.ConfState
	snapIndex, bytesSinceSnap uint64
	terminal                  error
}

func newEtcdConsensus(logger log.Logger, cfg *RaftConsensusConfig, token string) (_ *etcdConsensus, err error) {
	if cfg.ServerID == "" || !filepath.IsLocal(cfg.ServerID) || filepath.Base(cfg.ServerID) != cfg.ServerID || cfg.ServerID == "." || cfg.StorageDir == "" {
		return nil, errors.New("expected a storage directory and single-component member ID")
	}
	if cfg.HeartbeatTimeout < 10*time.Millisecond || cfg.SnapshotThreshold == 0 {
		return nil, errors.New("etcd raft requires an election timeout >= 10ms and positive snapshot threshold")
	}
	lease := cfg.LeaderLeaseTimeout
	if lease == 0 {
		lease = cfg.HeartbeatTimeout / 2
	}
	if lease < cfg.HeartbeatTimeout/5 || lease > cfg.HeartbeatTimeout {
		return nil, errors.New("etcd leader lease must be between 2 heartbeat ticks and the election timeout")
	}
	lis, err := net.Listen("tcp", net.JoinHostPort(cfg.ListenAddr, fmt.Sprint(cfg.ListenPort)))
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = lis.Close()
		}
	}()
	addr := string(cfg.AdvertisedAddr)
	if addr == "" {
		addr = lis.Addr().String()
	}
	urls, err := peerURLs(addr)
	if err != nil {
		return nil, err
	}
	identity, err := json.Marshal([]string{cfg.ServerID, token})
	if err != nil {
		return nil, err
	}
	store, bootstrap, err := openEtcdStore(filepath.Join(cfg.StorageDir, cfg.ServerID), identity, cfg.Bootstrap)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = store.wal.Close()
		}
	}()
	c := &etcdConsensus{
		leaseDuration: lease, started: time.Now(), bootID: uuid.New(),
		tracker:    NewUnsafeHeadTracker(logger, cfg.Metrics),
		cfg:        *cfg,
		log:        logger,
		addr:       addr,
		id:         raftID(cfg.ServerID),
		store:      store,
		leadership: make(chan bool, 1),
		done:       make(chan struct{}),
		stop:       make(chan struct{}),
		members:    make(map[uint64]ServerInfo),
		removed:    make(map[uint64]bool),
	}
	snapshot, err := store.Snapshot()
	if err != nil {
		return nil, err
	}
	lg := zap.NewNop()
	tr := &rafthttp.Transport{
		ID:          types.ID(c.id),
		URLs:        urls,
		ClusterID:   types.ID(raftID(token)),
		Logger:      lg,
		Raft:        c,
		DialTimeout: defaultTimeout,
		Snapshotter: store.snapshots,
		ServerStats: stats.NewServerStats(cfg.ServerID, cfg.ServerID),
		LeaderStats: stats.NewLeaderStats(lg, cfg.ServerID),
		ErrorC:      make(chan error, 1),
	}
	if err = tr.Start(); err != nil {
		return nil, err
	}
	c.transport = tr
	defer func() {
		if err != nil {
			tr.Stop()
		}
	}()
	rc := &raft.Config{
		ID:                        c.id,
		Storage:                   store,
		Applied:                   snapshot.Metadata.Index,
		HeartbeatTick:             1,
		ElectionTick:              10,
		PreVote:                   true,
		CheckQuorum:               true,
		DisableProposalForwarding: true,
		StepDownOnRemoval:         true,
		MaxSizePerMsg:             1 << 20,
		MaxInflightMsgs:           16,
		MaxInflightBytes:          32 << 20,
		MaxUncommittedEntriesSize: 64 << 20,
	}
	if bootstrap {
		info, err := json.Marshal(memberChange{Member: ServerInfo{ID: cfg.ServerID, Addr: addr, Suffrage: Voter}})
		if err != nil {
			return nil, err
		}
		c.node = raft.StartNode(rc, []raft.Peer{{ID: c.id, Context: info}})
	} else {
		c.node = raft.RestartNode(rc)
	}
	if !raft.IsEmptySnap(snapshot) {
		if err = c.restore(snapshot); err != nil {
			c.node.Stop()
			return nil, err
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /conductor/member", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Conductor-Cluster", tr.ClusterID.String())
		_, _ = fmt.Fprint(w, c.cfg.ServerID)
	})
	// Snapshots are ordinary raft messages here; no external database file accompanies them.
	mux.Handle(rafthttp.RaftSnapshotPrefix, http.NotFoundHandler())
	mux.Handle("/", tr.Handler())
	c.server = &http.Server{Handler: mux, ReadHeaderTimeout: defaultTimeout}
	go func() {
		if serveErr := c.server.Serve(lis); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			select {
			case tr.ErrorC <- serveErr:
			case <-c.done:
			}
		}
	}()
	go c.run()
	return c, nil
}

func (c *etcdConsensus) run() {
	defer close(c.done)
	clockStop := make(chan struct{})
	clockDone := make(chan struct{})
	go c.clock(clockStop, clockDone)
	stopPurge := make(chan struct{})
	wd, we := fileutil.PurgeFileWithDoneNotify(zap.NewNop(), c.store.walDir, "wal", 2, time.Minute, stopPurge)
	sd, se := fileutil.PurgeFileWithDoneNotify(zap.NewNop(), c.store.snapDir, "snap", 2, time.Minute, stopPurge)
	defer func() {
		c.setLeader(0)
		close(clockStop)
		<-clockDone
		_ = c.server.Close()
		c.transport.Stop()
		c.node.Stop()
		close(stopPurge)
		<-wd
		<-sd
		c.terminal = errors.Join(c.terminal, c.store.wal.Close())
		if c.terminal != nil {
			c.log.Error("etcd raft stopped", "err", c.terminal)
		}
	}()
	for {
		select {
		case <-c.stop:
			return
		case c.terminal = <-we:
			return
		case c.terminal = <-se:
			return
		case c.terminal = <-c.transport.ErrorC:
			return
		case rd := <-c.node.Ready():
			if c.terminal = c.ready(rd); c.terminal != nil {
				return
			}
			c.node.Advance()
			// Raft drops a second configuration proposal until the previous
			// change is advanced. Only now may a caller submit its next change.
			for _, result := range c.memberResults {
				c.complete(result.id, result.err)
			}
			c.memberResults = c.memberResults[:0]
		}
	}
}

// Durably store the snapshot, entries and vote before sending Raft responses.
// Complete client proposals only while applying committed entries, never on append.
func (c *etcdConsensus) ready(rd raft.Ready) error {
	hasSnap := !raft.IsEmptySnap(rd.Snapshot)
	if hasSnap {
		if err := c.store.saveSnapshot(rd.Snapshot); err != nil {
			return err
		}
	}
	start := time.Now()
	if err := c.store.wal.Save(rd.HardState, rd.Entries); err != nil {
		return err
	}
	if len(rd.Entries) > 0 && c.cfg.Metrics != nil {
		c.cfg.Metrics.RecordLogStoreDuration(time.Since(start).Seconds())
	}
	if hasSnap {
		if err := c.store.wal.Sync(); err != nil {
			return err
		}
		if err := c.store.ApplySnapshot(rd.Snapshot); err != nil {
			return err
		}
		if err := c.restore(rd.Snapshot); err != nil {
			return err
		}
		if err := c.store.wal.ReleaseLockTo(rd.Snapshot.Metadata.Index); err != nil {
			return err
		}
	}
	if !raft.IsEmptyHardState(rd.HardState) {
		if err := c.store.SetHardState(rd.HardState); err != nil {
			return err
		}
	}
	if err := c.store.Append(rd.Entries); err != nil {
		return err
	}
	for _, e := range rd.CommittedEntries {
		if err := c.apply(e); err != nil {
			return err
		}
		c.bytesSinceSnap += uint64(len(e.Data))
		// A new voter cannot install a snapshot that omits its own ID. Once
		// compacted, refresh the snapshot at every membership change even if
		// another entry cannot be committed under the new quorum yet.
		refreshMembership := c.snapIndex != 0 && c.version == e.Index
		if refreshMembership || e.Index-c.snapIndex >= c.cfg.SnapshotThreshold || c.bytesSinceSnap >= 64<<20 {
			if err := c.snapshot(e.Index); err != nil {
				return err
			}
		}
	}
	// Apply committed membership before responding to a vote or advertising leadership.
	c.transport.Send(rd.Messages)
	if rd.SoftState != nil {
		c.setLeader(rd.Lead)
	}
	for _, rs := range rd.ReadStates {
		if len(rs.RequestCtx) != 24 || !bytes.Equal(rs.RequestCtx[:16], c.bootID[:]) {
			continue
		}
		issued := int64(binary.BigEndian.Uint64(rs.RequestCtx[16:]))
		if c.leader.Load() == c.id && issued >= c.leaderSince.Load() && issued > c.verified.Load() {
			c.verified.Store(issued)
		}
	}
	return nil
}

func (c *etcdConsensus) setLeader(id uint64) {
	if c.leader.Load() != id {
		c.verified.Store(0)
		c.leaderSince.Store(time.Since(c.started).Nanoseconds())
		c.leader.Store(id)
	}
}

// The clock must keep running while Ready is blocked in fsync. A proxy may
// advertise local leadership only while a recent quorum ReadIndex is valid.
// Expiry is measured from issue time, never from a delayed acknowledgment.
func (c *etcdConsensus) clock(stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	tick := time.NewTicker(c.cfg.HeartbeatTimeout / 10)
	defer tick.Stop()
	published := false
	publish := func(v bool) {
		if published == v {
			return
		}
		published = v
		select {
		case <-c.leadership:
		default:
		}
		c.leadership <- v
	}
	defer publish(false)
	var last time.Time
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
			c.node.Tick()
			publish(c.Leader())
			if c.leader.Load() != c.id || time.Since(last) < c.leaseDuration/3 {
				continue
			}
			last = time.Now()
			var key [24]byte
			copy(key[:], c.bootID[:])
			binary.BigEndian.PutUint64(key[16:], uint64(last.Sub(c.started)))
			ctx, cancel := context.WithTimeout(context.Background(), c.cfg.HeartbeatTimeout/10)
			_ = c.node.ReadIndex(ctx, key[:])
			cancel()
		}
	}
}

func (c *etcdConsensus) Addr() string          { return c.addr }
func (c *etcdConsensus) ServerID() string      { return c.cfg.ServerID }
func (c *etcdConsensus) LeaderCh() <-chan bool { return c.leadership }
func (c *etcdConsensus) Leader() bool {
	issued := c.verified.Load()
	return c.leader.Load() == c.id && issued != 0 && time.Since(c.started)-time.Duration(issued) < c.leaseDuration
}
func (c *etcdConsensus) LeaderWithID() *ServerInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	id := c.leader.Load()
	if id == c.id && !c.Leader() {
		return &ServerInfo{}
	}
	info := c.members[id]
	return &info
}

func (c *etcdConsensus) Shutdown() error {
	c.once.Do(func() { close(c.stop) })
	<-c.done
	return c.terminal
}

func (c *etcdConsensus) Process(ctx context.Context, m pb.Message) error {
	if m.To != c.id || m.Type == pb.MsgProp || raft.IsLocalMsg(m.Type) {
		return errors.New("invalid peer message")
	}
	return c.node.Step(ctx, m)
}

func (c *etcdConsensus) IsIDRemoved(id uint64) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.removed[id]
}

func (c *etcdConsensus) ReportUnreachable(id uint64) { c.node.ReportUnreachable(id) }
func (c *etcdConsensus) ReportSnapshot(id uint64, s raft.SnapshotStatus) {
	c.node.ReportSnapshot(id, s)
}

func (c *etcdConsensus) submit(propose func(context.Context, uuid.UUID) error) error {
	if !c.Leader() {
		return errors.New("not raft leader")
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	id := uuid.New()
	ch := make(chan error, 1)
	c.pending.Store(id, ch)
	defer c.pending.Delete(id)
	if err := propose(ctx, id); err != nil {
		return err
	}
	select {
	case err := <-ch:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return errors.New("etcd raft stopped")
	}
}

func (c *etcdConsensus) complete(id uuid.UUID, err error) {
	if ch, ok := c.pending.LoadAndDelete(id); ok {
		ch.(chan error) <- err
	}
}

func (c *etcdConsensus) propose(data []byte) error {
	return c.submit(func(ctx context.Context, id uuid.UUID) error { return c.node.Propose(ctx, append(id[:], data...)) })
}

// A committed empty command is a quorum barrier. A partitioned former leader
// cannot serve the head from its local tracker.
func (c *etcdConsensus) LatestUnsafePayload() (*eth.ExecutionPayloadEnvelope, error) {
	if err := c.propose(nil); err != nil {
		return nil, err
	}
	return c.tracker.UnsafeHead(), nil
}

func (c *etcdConsensus) CommitUnsafePayload(p *eth.ExecutionPayloadEnvelope) error {
	var b bytes.Buffer
	start := time.Now()
	if _, err := p.MarshalSSZ(&b); err != nil {
		return err
	}
	return c.commit(b.Bytes(), time.Since(start))
}

func (c *etcdConsensus) CommitUnsafePayloadSSZ(ssz []byte) error { return c.commit(ssz, 0) }
func (c *etcdConsensus) commit(data []byte, marshal time.Duration) error {
	if _, err := decodeUnsafeHead(data); err != nil {
		return err
	}
	start := time.Now()
	if err := c.propose(data); err != nil {
		return err
	}
	if c.cfg.Metrics != nil {
		c.cfg.Metrics.RecordCommitDuration(marshal.Seconds(), time.Since(start).Seconds())
		c.cfg.Metrics.RecordCommitPayloadSize(float64(len(data)))
	}
	return nil
}

func (c *etcdConsensus) apply(e pb.Entry) error {
	switch e.Type {
	case pb.EntryConfChange:
		return c.applyMember(e)
	case pb.EntryNormal:
		if len(e.Data) == 0 {
			return nil
		}
		if len(e.Data) < 16 {
			return errors.New("invalid raft command")
		}
		id := uuid.UUID(e.Data[:16])
		if len(e.Data) > 16 {
			advanced, err := c.tracker.applyPayload(e.Data[16:])
			if err != nil {
				return err
			}
			if advanced {
				c.headSSZ = e.Data[16:]
			}
		}
		c.complete(id, nil)
		return nil
	default:
		return fmt.Errorf("unexpected raft entry type %s", e.Type)
	}
}
