package consensus

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/log"
	"github.com/hashicorp/raft"

	"github.com/ethereum-optimism/optimism/op-service/eth"
)

var _ raft.FSM = (*unsafeHeadTracker)(nil)

// unsafeHeadTracker implements raft.FSM for storing unsafe head payload into raft consensus layer.
type unsafeHeadTracker struct {
	log        log.Logger
	metrics    ConsensusMetrics
	mtx        sync.RWMutex
	unsafeHead *eth.ExecutionPayloadEnvelope
}

func NewUnsafeHeadTracker(log log.Logger, metrics ConsensusMetrics) *unsafeHeadTracker {
	return &unsafeHeadTracker{
		log:     log,
		metrics: metrics,
	}
}

// Apply implements raft.FSM, it applies the latest change (latest unsafe head payload) to FSM.
func (t *unsafeHeadTracker) Apply(l *raft.Log) interface{} {
	_, err := t.applyPayload(l.Data)
	return err
}

func decodeUnsafeHead(data []byte) (*eth.ExecutionPayloadEnvelope, error) {
	if len(data) == 0 {
		return nil, errors.New("log data is nil or empty")
	}
	env := new(eth.ExecutionPayloadEnvelope)
	if err := env.UnmarshalSSZ(eth.BlockV4, uint32(len(data)), bytes.NewReader(data)); err != nil {
		if err := env.UnmarshalSSZ(eth.BlockV3, uint32(len(data)), bytes.NewReader(data)); err != nil {
			return nil, err
		}
	}
	return env, nil
}

func (t *unsafeHeadTracker) applyPayload(ssz []byte) (bool, error) {
	start := time.Now()
	defer func() {
		if t.metrics != nil {
			t.metrics.RecordFSMApplyDuration(time.Since(start).Seconds())
		}
	}()

	data, err := decodeUnsafeHead(ssz)
	if err != nil {
		return false, err
	}

	t.mtx.Lock()
	defer t.mtx.Unlock()
	t.log.Debug("applying new unsafe head", "number", uint64(data.ExecutionPayload.BlockNumber), "hash", data.ExecutionPayload.BlockHash.Hex())
	if t.unsafeHead == nil || t.unsafeHead.ExecutionPayload.BlockNumber < data.ExecutionPayload.BlockNumber {
		t.unsafeHead = data
		return true, nil
	}

	return false, nil
}

// Restore implements raft.FSM, it restores state from snapshot.
func (t *unsafeHeadTracker) Restore(snapshot io.ReadCloser) error {
	var buf bytes.Buffer
	_, err := io.Copy(&buf, snapshot)
	snapshot.Close()
	if err != nil {
		return fmt.Errorf("error reading snapshot data: %w", err)
	}

	data, err := decodeUnsafeHead(buf.Bytes())
	if err != nil {
		return err
	}
	t.restoreHead(data)
	return nil
}

func (t *unsafeHeadTracker) restoreHead(data *eth.ExecutionPayloadEnvelope) {
	t.mtx.Lock()
	defer t.mtx.Unlock()
	t.unsafeHead = data
}

// Snapshot implements raft.FSM, it creates a snapshot of the current state.
func (t *unsafeHeadTracker) Snapshot() (raft.FSMSnapshot, error) {
	t.mtx.RLock()
	defer t.mtx.RUnlock()

	return &snapshot{
		unsafeHead: t.unsafeHead,
	}, nil
}

// UnsafeHead returns the latest unsafe head payload.
func (t *unsafeHeadTracker) UnsafeHead() *eth.ExecutionPayloadEnvelope {
	t.mtx.RLock()
	defer t.mtx.RUnlock()

	return t.unsafeHead
}

var _ raft.FSMSnapshot = (*snapshot)(nil)

type snapshot struct {
	log        log.Logger
	unsafeHead *eth.ExecutionPayloadEnvelope
}

// Persist implements raft.FSMSnapshot, it writes the snapshot to the given sink.
func (s *snapshot) Persist(sink raft.SnapshotSink) error {
	if _, err := s.unsafeHead.MarshalSSZ(sink); err != nil {
		if cerr := sink.Cancel(); cerr != nil {
			s.log.Error("error cancelling snapshot sink", "error", cerr)
		}
		return fmt.Errorf("error writing data to sink: %w", err)
	}

	return sink.Close()
}

// Release implements raft.FSMSnapshot.
// We don't really need to do anything within Release as the snapshot is not gonna change after creation, and we don't hold any reference to closable resources.
func (s *snapshot) Release() {}
