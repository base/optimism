package consensus

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"go.etcd.io/etcd/client/pkg/v3/types"
	"go.etcd.io/etcd/server/v3/etcdserver/api/snap"
	"go.etcd.io/etcd/server/v3/storage/wal"
	"go.etcd.io/etcd/server/v3/storage/wal/walpb"
	"go.etcd.io/raft/v3"
	pb "go.etcd.io/raft/v3/raftpb"
	"go.uber.org/zap"

	"github.com/ethereum-optimism/optimism/op-service/eth"
)

const etcdDir = "etcd-raft"

type etcdStore struct {
	*raft.MemoryStorage
	wal             *wal.WAL
	snapshots       *snap.Snapshotter
	snapDir, walDir string
}

// openEtcdStore opens only this implementation's WAL. A partial or foreign
// directory must never silently bootstrap a second cluster.
func openEtcdStore(base string, identity []byte) (*etcdStore, bool, error) {
	if err := os.MkdirAll(base, 0700); err != nil {
		return nil, false, err
	}
	files, err := os.ReadDir(base)
	if err != nil {
		return nil, false, err
	}
	for _, f := range files {
		if f.Name() != etcdDir || !f.IsDir() {
			return nil, false, fmt.Errorf("refusing non-etcd raft storage in %s", base)
		}
	}
	dir := filepath.Join(base, etcdDir)
	s := &etcdStore{MemoryStorage: raft.NewMemoryStorage(), snapDir: filepath.Join(dir, "snap"), walDir: filepath.Join(dir, "wal")}
	lg := zap.NewNop()
	s.snapshots = snap.New(lg, s.snapDir)
	if len(files) == 0 {
		if err = os.Mkdir(dir, 0700); err != nil {
			return nil, false, err
		}
		if err = os.Mkdir(s.snapDir, 0700); err != nil {
			return nil, false, err
		}
		s.wal, err = wal.Create(lg, s.walDir, identity)
		return s, true, err
	}
	valid, err := wal.ValidSnapshotEntries(lg, s.walDir)
	if err != nil {
		return nil, false, err
	}
	var marker walpb.Snapshot
	snapshot, err := s.snapshots.LoadNewestAvailable(valid)
	if err == nil {
		if err = s.ApplySnapshot(*snapshot); err != nil {
			return nil, false, err
		}
		marker = snapshotMarker(*snapshot)
	} else if !errors.Is(err, snap.ErrNoSnapshot) {
		return nil, false, err
	}
	var meta []byte
	var hs pb.HardState
	var entries []pb.Entry
	for attempt := 0; ; attempt++ {
		s.wal, err = wal.Open(lg, s.walDir, marker)
		if err != nil {
			return nil, false, err
		}
		meta, hs, entries, err = s.wal.ReadAll()
		if err == nil {
			break
		}
		_ = s.wal.Close()
		if attempt != 0 || !errors.Is(err, io.ErrUnexpectedEOF) || !wal.Repair(lg, s.walDir) {
			return nil, false, err
		}
	}
	if hs.Commit < marker.Index || hs.Commit > marker.Index+uint64(len(entries)) {
		_ = s.wal.Close()
		return nil, false, errors.New("committed index outside recovered log")
	}
	if !bytes.Equal(meta, identity) {
		err = errors.New("etcd WAL cluster token or member ID mismatch")
	}
	if err == nil {
		err = s.SetHardState(hs)
	}
	if err == nil {
		err = s.Append(entries)
	}
	if err != nil {
		_ = s.wal.Close()
		return nil, false, err
	}
	return s, false, nil
}

func snapshotMarker(s pb.Snapshot) walpb.Snapshot {
	return walpb.Snapshot{Index: s.Metadata.Index, Term: s.Metadata.Term, ConfState: &s.Metadata.ConfState}
}

func (s *etcdStore) saveSnapshot(snapshot pb.Snapshot) error {
	if err := s.snapshots.SaveSnap(snapshot); err != nil {
		return err
	}
	// Persist the new directory entry before making this checkpoint eligible
	// for log truncation. SaveSnap syncs the file, not its parent directory.
	dir, err := os.Open(s.snapDir)
	if err != nil {
		return err
	}
	if err = errors.Join(dir.Sync(), dir.Close()); err != nil {
		return err
	}
	return s.wal.SaveSnapshot(snapshotMarker(snapshot))
}

type etcdSnapshot struct {
	Members map[uint64]ServerInfo
	Removed map[uint64]bool
	Version uint64
	Head    []byte
}

func (c *etcdConsensus) snapshot(index uint64) error {
	state := etcdSnapshot{Members: c.members, Removed: c.removed, Version: c.version}
	if head := c.tracker.UnsafeHead(); head != nil {
		var b bytes.Buffer
		if _, err := head.MarshalSSZ(&b); err != nil {
			return err
		}
		state.Head = b.Bytes()
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	s, err := c.store.CreateSnapshot(index, &c.conf, data)
	if err != nil {
		return err
	}
	if err = c.store.saveSnapshot(s); err != nil {
		return err
	}
	if err = c.store.Compact(index); err != nil {
		return err
	}
	c.snapIndex = index
	c.bytesSinceSnap = 0
	return c.store.wal.ReleaseLockTo(index)
}

func (c *etcdConsensus) restore(s pb.Snapshot) error {
	var state etcdSnapshot
	if err := json.Unmarshal(s.Data, &state); err != nil {
		return err
	}
	if state.Members == nil || state.Removed == nil {
		return errors.New("missing snapshot membership")
	}
	var head *eth.ExecutionPayloadEnvelope
	var err error
	if len(state.Head) > 0 {
		head, err = decodeUnsafeHead(state.Head)
		if err != nil {
			return err
		}
	}
	c.transport.RemoveAllPeers()
	for id, m := range state.Members {
		if id != c.id {
			urls, err := peerURLs(m.Addr)
			if err != nil {
				return err
			}
			c.transport.AddPeer(types.ID(id), urls.StringSlice())
		}
	}
	c.mu.Lock()
	c.members, c.removed, c.version = state.Members, state.Removed, state.Version
	c.tracker.restoreHead(head)
	c.mu.Unlock()
	c.conf = s.Metadata.ConfState
	c.snapIndex = s.Metadata.Index
	c.bytesSinceSnap = 0
	return nil
}
