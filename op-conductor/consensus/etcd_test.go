package consensus

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
	"github.com/stretchr/testify/require"

	"github.com/ethereum-optimism/optimism/op-service/eth"
)

func etcdTestConfig(t testing.TB, id string, bootstrap bool) *RaftConsensusConfig {
	return &RaftConsensusConfig{
		ServerID:          id,
		ListenAddr:        "127.0.0.1",
		StorageDir:        t.TempDir(),
		Bootstrap:         bootstrap,
		HeartbeatTimeout:  250 * time.Millisecond,
		SnapshotThreshold: 4,
	}
}

func startMember(t *testing.T, cfg *RaftConsensusConfig) Consensus {
	t.Helper()
	c, err := NewConsensus(log.New(), cfg, "etcd", "cluster-A")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, c.Shutdown()) })
	_, port, err := net.SplitHostPort(c.Addr())
	require.NoError(t, err)
	cfg.ListenPort, err = strconv.Atoi(port)
	require.NoError(t, err)
	return c
}

func waitLeader(t testing.TB, nodes ...Consensus) Consensus {
	t.Helper()
	var result Consensus
	require.Eventually(t, func() bool {
		for _, n := range nodes {
			if n.Leader() {
				result = n
				return true
			}
		}
		return false
	}, 10*time.Second, 20*time.Millisecond)
	return result
}

func testHead(height uint64) *eth.ExecutionPayloadEnvelope {
	one := hexutil.Uint64(1)
	hash := common.HexToHash("0xabcd")
	return &eth.ExecutionPayloadEnvelope{
		ParentBeaconBlockRoot: &hash,
		ExecutionPayload:      &eth.ExecutionPayload{BlockNumber: hexutil.Uint64(height), Timestamp: 42, BlockHash: hash, ExtraData: []byte{}, Transactions: []eth.Data{}, Withdrawals: &types.Withdrawals{}, ExcessBlobGas: &one, BlobGasUsed: &one},
	}
}

func TestEtcdCluster(t *testing.T) {
	aCfg, bCfg, cCfg := etcdTestConfig(t, "alpha", true), etcdTestConfig(t, "bravo", false), etcdTestConfig(t, "charlie", false)
	a, b, c := startMember(t, aCfg), startMember(t, bCfg), startMember(t, cCfg)
	waitLeader(t, a)
	m, err := a.ClusterMembership()
	require.NoError(t, err)
	require.Error(t, a.RemoveServer(a.ServerID(), m.Version))
	require.NoError(t, a.AddNonVoter(a.ServerID(), a.Addr(), m.Version)) // address update must not demote voter
	m, err = a.ClusterMembership()
	require.NoError(t, err)
	require.Equal(t, Voter, m.Servers[0].Suffrage)
	require.NoError(t, a.AddNonVoter(b.ServerID(), b.Addr(), m.Version))
	require.Error(t, a.AddVoter(b.ServerID(), b.Addr(), m.Version))
	require.NoError(t, a.AddVoter(b.ServerID(), b.Addr(), 0))
	require.NoError(t, a.AddVoter(c.ServerID(), c.Addr(), 0))
	require.Error(t, b.CommitUnsafePayload(testHead(1)))
	require.Error(t, a.CommitUnsafePayloadSSZ([]byte("invalid")))
	var buf bytes.Buffer
	expected := testHead(10)
	_, err = expected.MarshalSSZ(&buf)
	require.NoError(t, err)
	require.NoError(t, a.CommitUnsafePayloadSSZ(buf.Bytes()))
	require.NoError(t, a.CommitUnsafePayload(testHead(9)))
	require.NoError(t, a.Shutdown())
	leader := waitLeader(t, b, c)
	actual, err := leader.LatestUnsafePayload()
	require.NoError(t, err)
	require.Equal(t, expected, actual)
	other := b
	if other == leader {
		other = c
	}
	require.NoError(t, leader.TransferLeaderTo(other.ServerID(), other.Addr()))
	waitLeader(t, other)
	require.NoError(t, leader.Shutdown())
	require.Error(t, other.CommitUnsafePayload(testHead(20)))
	_, err = other.LatestUnsafePayload()
	require.Error(t, err)
	require.NoError(t, other.Shutdown())
	// No running peer can supply the old head: recover a quorum from disk.
	a = startMember(t, aCfg)
	b = startMember(t, bCfg)
	leader = waitLeader(t, a, b)
	actual, err = leader.LatestUnsafePayload()
	require.NoError(t, err)
	// A timed-out proposal can still be committed after a quorum returns.
	require.Contains(t, []*eth.ExecutionPayloadEnvelope{expected, testHead(20)}, actual)
	m, err = leader.ClusterMembership()
	require.NoError(t, err)
	require.Len(t, m.Servers, 3)
}

func TestEtcdRestart(t *testing.T) {
	for _, threshold := range []uint64{1, 1000} {
		t.Run(strconv.FormatUint(threshold, 10), func(t *testing.T) {
			cfg := etcdTestConfig(t, "alpha", true)
			cfg.SnapshotThreshold = threshold
			c := startMember(t, cfg)
			waitLeader(t, c)
			expected := testHead(100)
			require.NoError(t, c.CommitUnsafePayload(expected))
			require.NoError(t, c.Shutdown())
			_, err := NewConsensus(log.New(), cfg, "etcd", "different-cluster")
			require.Error(t, err)
			_, err = NewRaftConsensus(log.New(), cfg)
			require.Error(t, err)
			c = startMember(t, cfg)
			waitLeader(t, c)
			head, err := c.LatestUnsafePayload()
			require.NoError(t, err)
			require.Equal(t, expected, head)
		})
	}
}

// Exit the subprocess after acknowledgment, without running Shutdown or any Go defers.
func TestEtcdCrashRecovery(t *testing.T) {
	const env = "OP_CONDUCTOR_TEST_CRASH_DIR"
	cfg := etcdTestConfig(t, "alpha", true)
	cfg.SnapshotThreshold = 1000
	if dir := os.Getenv(env); dir != "" {
		cfg.StorageDir = dir
		c := startMember(t, cfg)
		waitLeader(t, c)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "port"), []byte(strconv.Itoa(cfg.ListenPort)), 0600))
		require.NoError(t, c.CommitUnsafePayload(testHead(123)))
		os.Exit(42)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestEtcdCrashRecovery$")
	cmd.Env = append(os.Environ(), env+"="+cfg.StorageDir)
	out, err := cmd.CombinedOutput()
	var status *exec.ExitError
	require.ErrorAs(t, err, &status, string(out))
	require.Equal(t, 42, status.ExitCode(), string(out))
	port, err := os.ReadFile(filepath.Join(cfg.StorageDir, "port"))
	require.NoError(t, err)
	cfg.ListenPort, err = strconv.Atoi(string(port))
	require.NoError(t, err)
	c := startMember(t, cfg)
	waitLeader(t, c)
	head, err := c.LatestUnsafePayload()
	require.NoError(t, err)
	require.Equal(t, testHead(123), head)
}

func TestEtcdRejectsForeignState(t *testing.T) {
	cfg := etcdTestConfig(t, "alpha", true)
	dir := filepath.Join(cfg.StorageDir, cfg.ServerID)
	require.NoError(t, os.Mkdir(dir, 0700))
	file := filepath.Join(dir, "raft-log.db")
	require.NoError(t, os.WriteFile(file, []byte("foreign state"), 0600))
	_, err := NewConsensus(log.New(), cfg, "etcd", "cluster-A")
	require.Error(t, err)
	content, err := os.ReadFile(file)
	require.NoError(t, err)
	require.Equal(t, "foreign state", string(content))
	require.NoDirExists(t, filepath.Join(dir, etcdDir))
}

func TestEtcdVoterJoinsCompactedCluster(t *testing.T) {
	cfg := etcdTestConfig(t, "alpha", true)
	cfg.SnapshotThreshold = 8
	a := startMember(t, cfg)
	waitLeader(t, a)
	for i := uint64(1); i <= 8; i++ {
		require.NoError(t, a.CommitUnsafePayload(testHead(i)))
	}
	b := startMember(t, etcdTestConfig(t, "bravo", false))
	require.NoError(t, a.AddVoter(b.ServerID(), b.Addr(), 0))
	head, err := a.LatestUnsafePayload()
	require.NoError(t, err)
	require.Equal(t, testHead(8), head)
	require.NoError(t, a.DemoteVoter(b.ServerID(), 0))
	// Removing a stopped learner must not need its vote.
	require.NoError(t, b.Shutdown())
	require.NoError(t, a.RemoveServer(b.ServerID(), 0))
	members, err := a.ClusterMembership()
	require.NoError(t, err)
	require.Equal(t, []ServerInfo{{ID: a.ServerID(), Addr: a.Addr(), Suffrage: Voter}}, members.Servers)
}

func TestEtcdConfig(t *testing.T) {
	for _, tc := range []struct {
		impl, token, storage string
		nosync, valid        bool
	}{
		{"", "", "", false, true},
		{"hashicorp", "", "wal", false, true},
		{"etcd", "fresh", "", false, true},
		{"etcd", "", "", false, false},
		{"etcd", "fresh", "wal", false, false},
		{"etcd", "fresh", "boltdb", true, false},
		{"hashicorp", "fresh", "", false, false},
		{"typo", "", "", false, false},
	} {
		err := CheckRaftImplementation(tc.impl, tc.token, tc.storage, tc.nosync)
		if tc.valid {
			require.NoError(t, err)
		} else {
			require.Error(t, err)
		}
	}
}

// BenchmarkDurableCommit measures end-to-end conductor writes through a local
// three-voter cluster. Setup, elections and final head verification are untimed.
func BenchmarkDurableCommit(b *testing.B) {
	for _, tc := range []struct{ name, engine, storage, token string }{
		{"hashicorp-boltdb", "hashicorp", "boltdb", ""},
		{"hashicorp-wal", "hashicorp", "wal", ""},
		{"etcd-wal", "etcd", "", "benchmark"},
	} {
		b.Run(tc.name, func(b *testing.B) {
			var leader Consensus
			for i := 0; i < 3; i++ {
				cfg := etcdTestConfig(b, strconv.Itoa(i+1), i == 0)
				cfg.StorageBackend = tc.storage
				cfg.SnapshotInterval = time.Minute
				cfg.SnapshotThreshold = 8192
				cfg.TrailingLogs = 10240
				cfg.HeartbeatTimeout = time.Second
				cfg.LeaderLeaseTimeout = 500 * time.Millisecond
				node, err := NewConsensus(log.New(), cfg, tc.engine, tc.token)
				require.NoError(b, err)
				b.Cleanup(func() { require.NoError(b, node.Shutdown()) })
				if i == 0 {
					leader = waitLeader(b, node)
				} else {
					require.NoError(b, leader.AddVoter(node.ServerID(), node.Addr(), 0))
				}
			}
			payload := testHead(1)
			payload.ExecutionPayload.Transactions = []eth.Data{make([]byte, 128<<10)}
			require.NoError(b, leader.CommitUnsafePayload(payload))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				payload.ExecutionPayload.BlockNumber = hexutil.Uint64(i + 2)
				if err := leader.CommitUnsafePayload(payload); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			head, err := leader.LatestUnsafePayload()
			require.NoError(b, err)
			require.Equal(b, payload, head)
		})
	}
}

func TestMixedEngineMembershipFailsClosed(t *testing.T) {
	ec := startMember(t, etcdTestConfig(t, "etcd", true))
	cfg := etcdTestConfig(t, "hashicorp", true)
	cfg.LeaderLeaseTimeout = 125 * time.Millisecond
	cfg.SnapshotInterval = time.Minute
	hc, err := NewConsensus(log.New(), cfg, "hashicorp", "")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, hc.Shutdown()) })
	waitLeader(t, ec)
	waitLeader(t, hc)
	for _, pair := range [][2]Consensus{{ec, hc}, {hc, ec}} {
		a, b := pair[0], pair[1]
		require.Error(t, a.AddVoter(b.ServerID(), b.Addr(), 0))
		members, err := a.ClusterMembership()
		require.NoError(t, err)
		require.Len(t, members.Servers, 1)
		require.NoError(t, a.CommitUnsafePayload(testHead(99)))
	}
}
func TestEtcdInterruptedBootstrap(t *testing.T) {
	for _, bootstrap := range []bool{true, false} {
		t.Run(strconv.FormatBool(bootstrap), func(t *testing.T) {
			cfg := etcdTestConfig(t, "alpha", !bootstrap)
			identity, err := json.Marshal([]string{cfg.ServerID, "cluster-A"})
			require.NoError(t, err)
			store, _, err := openEtcdStore(filepath.Join(cfg.StorageDir, cfg.ServerID), identity, bootstrap)
			require.NoError(t, err)
			require.NoError(t, store.wal.Close())
			c := startMember(t, cfg)
			if bootstrap {
				waitLeader(t, c)
				require.NoError(t, c.CommitUnsafePayload(testHead(1)))
			} else {
				require.Never(t, c.Leader, cfg.HeartbeatTimeout*3, 20*time.Millisecond)
				m, err := c.ClusterMembership()
				require.NoError(t, err)
				require.Empty(t, m.Servers)
			}
		})
	}
}
func TestEtcdSnapshotKeepsExplicitV4Root(t *testing.T) {
	cfg := etcdTestConfig(t, "alpha", true)
	cfg.SnapshotThreshold = 1
	c := startMember(t, cfg)
	waitLeader(t, c)
	expected := testHead(42)
	sentinel := common.HexToHash("0x1234")
	expected.ExecutionPayload.WithdrawalsRoot = &sentinel
	var b bytes.Buffer
	_, err := expected.MarshalSSZ(&b)
	require.NoError(t, err)
	// With empty dynamic fields, the V4 withdrawals root is the last 32 bytes.
	// Construct the legal explicit empty-root encoding the encoder would omit.
	require.True(t, bytes.HasSuffix(b.Bytes(), sentinel[:]))
	copy(b.Bytes()[b.Len()-32:], types.EmptyWithdrawalsHash[:])
	empty := types.EmptyWithdrawalsHash
	expected.ExecutionPayload.WithdrawalsRoot = &empty
	require.NoError(t, c.CommitUnsafePayloadSSZ(b.Bytes()))
	require.NoError(t, c.Shutdown())
	c = startMember(t, cfg)
	waitLeader(t, c)
	head, err := c.LatestUnsafePayload()
	require.NoError(t, err)
	require.Equal(t, expected, head)
}

type blockingWALMetric struct {
	armed            atomic.Bool
	entered, release chan struct{}
}

func (*blockingWALMetric) RecordCommitDuration(float64, float64) {}
func (*blockingWALMetric) RecordCommitPayloadSize(float64)       {}
func (*blockingWALMetric) RecordFSMApplyDuration(float64)        {}
func (m *blockingWALMetric) RecordLogStoreDuration(float64) {
	if m.armed.Swap(false) {
		close(m.entered)
		<-m.release
	}
}
func TestEtcdStalledPersistenceExpiresLeadership(t *testing.T) {
	cfg := etcdTestConfig(t, "alpha", true)
	m := &blockingWALMetric{entered: make(chan struct{}), release: make(chan struct{})}
	cfg.Metrics = m
	c := startMember(t, cfg)
	unblock := sync.OnceFunc(func() { close(m.release) })
	defer unblock()
	waitLeader(t, c)
	select {
	case leader := <-c.LeaderCh():
		require.True(t, leader)
	case <-time.After(time.Second):
		t.Fatal("no leadership notification")
	}
	m.armed.Store(true)
	result := make(chan error, 1)
	go func() { result <- c.CommitUnsafePayload(testHead(1)) }()
	select {
	case <-m.entered:
	case <-time.After(time.Second):
		t.Fatal("WAL path did not block")
	}
	select {
	case leader := <-c.LeaderCh():
		require.False(t, leader)
	case <-time.After(time.Second):
		t.Fatal("stalled leader did not withdraw")
	}
	require.False(t, c.Leader())
	require.Empty(t, c.LeaderWithID().ID)
	unblock()
	select {
	case err := <-result:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("commit did not finish")
	}
	waitLeader(t, c)
}
