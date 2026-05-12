package node

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rpc"

	conductorRpc "github.com/ethereum-optimism/optimism/op-conductor/rpc"
	"github.com/ethereum-optimism/optimism/op-node/config"
	"github.com/ethereum-optimism/optimism/op-node/metrics"
	"github.com/ethereum-optimism/optimism/op-node/rollup/conductor"
	"github.com/ethereum-optimism/optimism/op-service/dial"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum-optimism/optimism/op-service/locks"
	"github.com/ethereum-optimism/optimism/op-service/retry"
)

// ConductorClient is a client for the op-conductor RPC service.
type ConductorClient struct {
	cfg     *config.Config
	metrics *metrics.Metrics
	log     log.Logger

	apiClient    locks.RWValue[*conductorRpc.APIClient]
	binaryClient locks.RWValue[*conductorRpc.BinaryCommitClient]

	// overrideLeader is used to override the leader check for disaster recovery purposes.
	// During disaster situations where the cluster is unhealthy (no leader, only 1 or less nodes up),
	// set this to true to allow the node to assume sequencing responsibilities without being the leader.
	overrideLeader atomic.Bool
}

var _ conductor.SequencerConductor = &ConductorClient{}

// NewConductorClient returns a new conductor client for the op-conductor RPC service.
func NewConductorClient(cfg *config.Config, log log.Logger, metrics *metrics.Metrics) conductor.SequencerConductor {
	return &ConductorClient{
		cfg:     cfg,
		metrics: metrics,
		log:     log,
	}
}

// Initialize initializes the conductor client.
func (c *ConductorClient) initialize(ctx context.Context) error {
	c.apiClient.Lock()
	defer c.apiClient.Unlock()
	if c.apiClient.Value != nil {
		return nil
	}
	endpoint, err := retry.Do[string](ctx, 10, retry.Exponential(), func() (string, error) {
		return c.cfg.ConductorRpc(ctx)
	})
	if err != nil {
		return fmt.Errorf("no conductor RPC endpoint available: %w", err)
	}
	metricsOpt := rpc.WithRecorder(c.metrics.NewRecorder("conductor"))
	conductorRpcClient, err := dial.DialRPCClientWithTimeout(context.Background(), c.log, endpoint, metricsOpt)
	if err != nil {
		return fmt.Errorf("failed to dial conductor RPC: %w", err)
	}
	c.apiClient.Value = conductorRpc.NewAPIClient(conductorRpcClient)

	if c.cfg.ConductorBinaryCommit {
		c.binaryClient.Lock()
		defer c.binaryClient.Unlock()
		// Reuse the conductor RPC endpoint URL — the binary commit handler is
		// served on the same HTTP server at conductorRpc.CommitUnsafePayloadPath.
		httpClient := &http.Client{Timeout: c.cfg.ConductorRpcTimeout}
		c.binaryClient.Value = conductorRpc.NewBinaryCommitClient(endpoint, httpClient)
		c.log.Info("Conductor binary commit endpoint enabled", "endpoint", endpoint)
	}
	return nil
}

// Enabled returns true if the conductor is enabled, and since the conductor client is initialized, the conductor is always enabled.
func (c *ConductorClient) Enabled(ctx context.Context) bool {
	return true
}

// Leader returns true if this node is the leader sequencer.
func (c *ConductorClient) Leader(ctx context.Context) (bool, error) {
	if c.overrideLeader.Load() {
		return true, nil
	}

	if err := c.initialize(ctx); err != nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.cfg.ConductorRpcTimeout)
	defer cancel()

	isLeader, err := retry.Do(ctx, 2, retry.Fixed(50*time.Millisecond), func() (bool, error) {
		result, err := c.apiClient.Get().Leader(ctx)
		if err != nil {
			c.log.Error("Failed to check conductor for leadership", "err", err)
		}
		return result, err
	})
	return isLeader, err
}

// CommitUnsafePayload commits an unsafe payload to the conductor log.
// Uses the SSZ-binary endpoint when --conductor.binary-commit is set;
// otherwise the existing JSON-RPC method.
func (c *ConductorClient) CommitUnsafePayload(ctx context.Context, payload *eth.ExecutionPayloadEnvelope) error {
	if c.overrideLeader.Load() {
		return nil
	}

	if err := c.initialize(ctx); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, c.cfg.ConductorRpcTimeout)
	defer cancel()

	commit := func() error {
		if bc := c.binaryClient.Get(); bc != nil {
			return bc.CommitUnsafePayload(ctx, payload)
		}
		return c.apiClient.Get().CommitUnsafePayload(ctx, payload)
	}
	return retry.Do0(ctx, 2, retry.Fixed(50*time.Millisecond), commit)
}

// OverrideLeader implements conductor.SequencerConductor.
func (c *ConductorClient) OverrideLeader(ctx context.Context) error {
	c.overrideLeader.Store(true)
	return nil
}

func (c *ConductorClient) Close() {
	c.apiClient.Lock()
	defer c.apiClient.Unlock()
	if c.apiClient.Value == nil {
		return
	}
	c.apiClient.Value.Close()
	c.apiClient.Value = nil
}
