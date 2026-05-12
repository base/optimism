package consensus

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
	"github.com/stretchr/testify/require"

	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum-optimism/optimism/op-service/testlog"
)

func TestCommitAndRead(t *testing.T) {
	tests := []struct {
		name    string
		backend string
	}{
		{name: "Bolt", backend: RaftBackendBbolt},
		{name: "MDB", backend: RaftBackendMDB},
		{name: "Pebble", backend: RaftBackendPebble},
		{name: "Badger", backend: RaftBackendBadger},
		{name: "LevelDB", backend: RaftBackendLevelDB},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.backend == RaftBackendMDB && runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
				t.Skip("raft-mdb trips a known cgo pointer check on darwin/arm64; validate on Linux")
			}

			log := testlog.Logger(t, log.LevelInfo)
			now := uint64(time.Now().Unix())
			storageDir := t.TempDir()

			raftConsensusConfig := &RaftConsensusConfig{
				ServerID:           "SequencerA",
				ListenPort:         0,
				ListenAddr:         "127.0.0.1", // local test, don't bind to external interface
				AdvertisedAddr:     "",          // use local address that the server binds to
				StorageDir:         storageDir,
				Backend:            test.backend,
				MDBMaxSize:         DefaultRaftMDBMaxSize,
				Bootstrap:          true,
				SnapshotInterval:   time.Second,
				SnapshotThreshold:  48,
				TrailingLogs:       32,
				HeartbeatTimeout:   1000 * time.Millisecond,
				LeaderLeaseTimeout: 500 * time.Millisecond,
			}

			cons, err := NewRaftConsensus(log, raftConsensusConfig)
			require.NoError(t, err)
			t.Cleanup(func() {
				require.NoError(t, cons.Shutdown())
			})

			waitForLeader(t, cons)

			baseDir := filepath.Join(storageDir, raftConsensusConfig.ServerID)
			switch test.backend {
			case RaftBackendBbolt:
				require.FileExists(t, filepath.Join(baseDir, "raft-log.db"))
				require.FileExists(t, filepath.Join(baseDir, "raft-stable.db"))
			case RaftBackendMDB:
				info, err := os.Stat(filepath.Join(baseDir, "mdb"))
				require.NoError(t, err)
				require.True(t, info.IsDir())
			case RaftBackendPebble:
				require.DirExists(t, filepath.Join(baseDir, "pebble"))
				require.DirExists(t, filepath.Join(baseDir, "pebble", "data"))
				require.DirExists(t, filepath.Join(baseDir, "pebble", "wal"))
			case RaftBackendBadger:
				require.DirExists(t, filepath.Join(baseDir, "badger"))
			case RaftBackendLevelDB:
				require.DirExists(t, filepath.Join(baseDir, "leveldb"))
			}

			// eth.BlockV1
			payload := &eth.ExecutionPayloadEnvelope{
				ExecutionPayload: &eth.ExecutionPayload{
					BlockNumber:  1,
					Timestamp:    hexutil.Uint64(now - 20),
					Transactions: []eth.Data{},
					ExtraData:    []byte{},
				},
			}

			err = cons.CommitUnsafePayload(payload)
			// ExecutionPayloadEnvelope is expected to fail when unmarshalling a blockV1
			require.Error(t, err)

			// eth.BlockV3
			one := hexutil.Uint64(1)
			hash := common.HexToHash("0x12345")
			payload = &eth.ExecutionPayloadEnvelope{
				ParentBeaconBlockRoot: &hash,
				ExecutionPayload: &eth.ExecutionPayload{
					BlockNumber:   2,
					Timestamp:     hexutil.Uint64(time.Now().Unix()),
					Transactions:  []eth.Data{},
					ExtraData:     []byte{},
					Withdrawals:   &types.Withdrawals{},
					ExcessBlobGas: &one,
					BlobGasUsed:   &one,
				},
			}

			err = cons.CommitUnsafePayload(payload)
			// ExecutionPayloadEnvelope is expected to succeed when unmarshalling a blockV3
			require.NoError(t, err)

			unsafeHead, err := cons.LatestUnsafePayload()
			require.NoError(t, err)
			require.Equal(t, payload, unsafeHead)
		})
	}
}

func TestCommitPersistsAcrossRestart(t *testing.T) {
	tests := []struct {
		name    string
		backend string
	}{
		{name: "Bolt", backend: RaftBackendBbolt},
		{name: "MDB", backend: RaftBackendMDB},
		{name: "Pebble", backend: RaftBackendPebble},
		{name: "Badger", backend: RaftBackendBadger},
		{name: "LevelDB", backend: RaftBackendLevelDB},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.backend == RaftBackendMDB && runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
				t.Skip("raft-mdb trips a known cgo pointer check on darwin/arm64; validate on Linux")
			}

			logger := testlog.Logger(t, log.LevelWarn)
			storageDir := t.TempDir()
			now := uint64(time.Now().Unix())
			cfg := &RaftConsensusConfig{
				ServerID:           "SequencerA",
				ListenPort:         0,
				ListenAddr:         "127.0.0.1",
				AdvertisedAddr:     "",
				StorageDir:         storageDir,
				Backend:            test.backend,
				MDBMaxSize:         DefaultRaftMDBMaxSize,
				Bootstrap:          true,
				SnapshotInterval:   time.Second,
				SnapshotThreshold:  48,
				TrailingLogs:       32,
				HeartbeatTimeout:   1000 * time.Millisecond,
				LeaderLeaseTimeout: 500 * time.Millisecond,
			}

			cons, err := NewRaftConsensus(logger, cfg)
			require.NoError(t, err)
			waitForLeader(t, cons)

			one := hexutil.Uint64(1)
			hash := common.HexToHash("0x12345")
			payload := &eth.ExecutionPayloadEnvelope{
				ParentBeaconBlockRoot: &hash,
				ExecutionPayload: &eth.ExecutionPayload{
					BlockNumber:   2,
					Timestamp:     hexutil.Uint64(now),
					Transactions:  []eth.Data{},
					ExtraData:     []byte{},
					Withdrawals:   &types.Withdrawals{},
					ExcessBlobGas: &one,
					BlobGasUsed:   &one,
				},
			}

			require.NoError(t, cons.CommitUnsafePayload(payload))
			require.NoError(t, cons.Shutdown())

			restartCfg := *cfg
			restartCfg.Bootstrap = false

			restarted, err := NewRaftConsensus(logger, &restartCfg)
			require.NoError(t, err)
			t.Cleanup(func() {
				require.NoError(t, restarted.Shutdown())
			})
			waitForLeader(t, restarted)

			unsafeHead, err := restarted.LatestUnsafePayload()
			require.NoError(t, err)
			require.Equal(t, payload, unsafeHead)
		})
	}
}

func waitForLeader(t *testing.T, cons *RaftConsensus) {
	t.Helper()
	require.Eventually(t, func() bool {
		return cons.Leader()
	}, 10*time.Second, 10*time.Millisecond)
}
