package conductor

import (
	"flag"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/log"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v2"

	"github.com/ethereum-optimism/optimism/op-conductor/consensus"
	"github.com/ethereum-optimism/optimism/op-conductor/flags"
	"github.com/ethereum-optimism/optimism/op-service/testlog"
)

func TestConfigCheckRollupBoostAndNextMutuallyExclusive(t *testing.T) {
	cfg := &Config{
		ConsensusAddr:                 "127.0.0.1",
		ConsensusPort:                 9000,
		RaftServerID:                  "server-1",
		RaftStorageDir:                "/tmp/op-conductor",
		NodeRPC:                       "http://node.example",
		ExecutionRPC:                  "http://exec.example",
		RollupBoostEnabled:            true,
		RollupBoostNextEnabled:        true,
		RollupBoostNextHealthcheckURL: "http://rollupboost.example",
	}

	err := cfg.Check()
	require.Error(t, err)
	require.Contains(t, err.Error(), "only one of rollup-boost or rollup-boost next healthchecks can be enabled")
}

func TestConfigCheckRejectsInvalidRaftBackend(t *testing.T) {
	cfg := validConfig()
	cfg.RaftBackend = "badger"

	err := cfg.Check()
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid raft backend")
}

func TestConfigCheckRequiresMDBMaxSize(t *testing.T) {
	cfg := validConfig()
	cfg.RaftBackend = consensus.RaftBackendMDB
	cfg.RaftMDBMaxSize = 0

	err := cfg.Check()
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid raft mdb max size")
}

func TestNewConfigAppliesRaftDefaults(t *testing.T) {
	flagSet := flag.NewFlagSet("op-conductor", flag.ContinueOnError)
	for _, f := range flags.Flags {
		require.NoError(t, f.Apply(flagSet))
	}

	require.NoError(t, flagSet.Set(flags.ConsensusAddr.Name, "127.0.0.1"))
	require.NoError(t, flagSet.Set(flags.ConsensusPort.Name, "50050"))
	require.NoError(t, flagSet.Set(flags.RaftServerID.Name, "server-1"))
	require.NoError(t, flagSet.Set(flags.RaftStorageDir.Name, "/tmp/op-conductor"))
	require.NoError(t, flagSet.Set(flags.NodeRPC.Name, "http://node.example"))
	require.NoError(t, flagSet.Set(flags.ExecutionRPC.Name, "http://exec.example"))
	require.NoError(t, flagSet.Set(flags.HealthCheckInterval.Name, "10"))
	require.NoError(t, flagSet.Set(flags.HealthCheckUnsafeInterval.Name, "12"))
	require.NoError(t, flagSet.Set(flags.HealthCheckMinPeerCount.Name, "1"))

	ctx := cli.NewContext(cli.NewApp(), flagSet, nil)
	cfg, err := NewConfig(ctx, testlog.Logger(t, log.LevelWarn))
	require.NoError(t, err)
	require.Equal(t, consensus.DefaultRaftBackend, cfg.RaftBackend)
	require.EqualValues(t, consensus.DefaultRaftMDBMaxSize, cfg.RaftMDBMaxSize)
	require.Equal(t, time.Second, cfg.RaftSnapshotInterval)
	require.Equal(t, uint64(48), cfg.RaftSnapshotThreshold)
	require.Equal(t, uint64(32), cfg.RaftTrailingLogs)
}

func validConfig() *Config {
	return &Config{
		ConsensusAddr:         "127.0.0.1",
		ConsensusPort:         9000,
		RaftServerID:          "server-1",
		RaftStorageDir:        "/tmp/op-conductor",
		RaftBackend:           consensus.DefaultRaftBackend,
		RaftMDBMaxSize:        consensus.DefaultRaftMDBMaxSize,
		NodeRPC:               "http://node.example",
		ExecutionRPC:          "http://exec.example",
		RaftSnapshotInterval:  time.Second,
		RaftSnapshotThreshold: 48,
		RaftTrailingLogs:      32,
	}
}
