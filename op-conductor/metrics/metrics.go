package metrics

import (
	"strconv"

	"github.com/ethereum-optimism/optimism/op-conductor/consensus"
	"github.com/ethereum-optimism/optimism/op-service/httputil"
	opmetrics "github.com/ethereum-optimism/optimism/op-service/metrics"
	"github.com/prometheus/client_golang/prometheus"
)

const Namespace = "op_conductor"

type Metricer interface {
	RecordInfo(version string)
	RecordUp()
	RecordStateChange(leader bool, healthy bool, active bool)
	RecordLeaderTransfer(success bool)
	RecordStartSequencer(success bool)
	RecordStopSequencer(success bool)
	RecordHealthCheck(success bool, err error)
	RecordLoopExecutionTime(duration float64)
	RecordRollupBoostConnectionAttempts(success bool, source string)
	RecordWebSocketClientCount(count int)
	// RecordBinaryCommitDuration records end-to-end handler duration for
	// POST /commit-unsafe-payload requests. The equivalent metric for the
	// JSON-RPC path is rpc_server_request_duration_seconds{method=conductor_commitUnsafePayload}.
	RecordBinaryCommitDuration(seconds float64, success bool)
	opmetrics.RPCMetricer
	consensus.ConsensusMetrics
}

// Metrics implementation must implement RegistryMetricer to allow the metrics server to work.
var _ opmetrics.RegistryMetricer = (*Metrics)(nil)
var _ consensus.ConsensusMetrics = (*Metrics)(nil)

type Metrics struct {
	ns       string
	registry *prometheus.Registry
	factory  opmetrics.Factory

	opmetrics.RPCMetrics

	info prometheus.GaugeVec
	up   prometheus.Gauge

	healthChecks                  *prometheus.CounterVec
	leaderTransfers               *prometheus.CounterVec
	sequencerStarts               *prometheus.CounterVec
	sequencerStops                *prometheus.CounterVec
	stateChanges                  *prometheus.CounterVec
	rollupBoostConnectionAttempts *prometheus.CounterVec

	loopExecutionTime prometheus.Histogram
	webSocketClients  prometheus.Gauge

	binaryCommitRequestDuration *prometheus.HistogramVec

	commitMarshalDuration   prometheus.Histogram
	commitRaftApplyDuration prometheus.Histogram
	commitPayloadSize       prometheus.Histogram
	fsmApplyDuration        prometheus.Histogram
	logStoreDuration        prometheus.Histogram
}

func (m *Metrics) Registry() *prometheus.Registry {
	return m.registry
}

var _ Metricer = (*Metrics)(nil)

func NewMetrics() *Metrics {
	registry := opmetrics.NewRegistry()
	factory := opmetrics.With(registry)

	return &Metrics{
		ns:       Namespace,
		registry: registry,
		factory:  factory,

		RPCMetrics: opmetrics.MakeRPCMetrics(Namespace, factory),

		info: *factory.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace,
			Name:      "info",
			Help:      "Pseudo-metric tracking version and config info",
		}, []string{
			"version",
		}),
		up: factory.NewGauge(prometheus.GaugeOpts{
			Namespace: Namespace,
			Name:      "up",
			Help:      "1 if the op-conductor has finished starting up",
		}),
		healthChecks: factory.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Name:      "healthchecks_count",
			Help:      "Number of healthchecks",
		}, []string{"success", "error"}),
		leaderTransfers: factory.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Name:      "leader_transfers_count",
			Help:      "Number of leader transfers",
		}, []string{"success"}),
		sequencerStarts: factory.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Name:      "sequencer_starts_count",
			Help:      "Number of sequencer starts",
		}, []string{"success"}),
		sequencerStops: factory.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Name:      "sequencer_stops_count",
			Help:      "Number of sequencer stops",
		}, []string{"success"}),
		stateChanges: factory.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Name:      "state_changes_count",
			Help:      "Number of state changes",
		}, []string{
			"leader",
			"healthy",
			"active",
		}),
		loopExecutionTime: factory.NewHistogram(prometheus.HistogramOpts{
			Namespace: Namespace,
			Name:      "loop_execution_time",
			Help:      "Time (in seconds) to execute conductor loop iteration",
			Buckets:   []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
		}),
		rollupBoostConnectionAttempts: factory.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Name:      "rollup_boost_connection_attempts_count",
			Help:      "Number of rollup boost connection attempts",
		}, []string{"success", "source"}),
		webSocketClients: factory.NewGauge(prometheus.GaugeOpts{
			Namespace: Namespace,
			Name:      "websocket_clients_connected",
			Help:      "Number of WebSocket clients currently connected to the hub",
		}),
		binaryCommitRequestDuration: factory.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: Namespace,
			Name:      "binary_commit_request_duration_seconds",
			Help: "End-to-end handler duration for POST /commit-unsafe-payload requests. " +
				"Directly comparable to rpc_server_request_duration_seconds{method=conductor_commitunsafepayload} " +
				"on the JSON-RPC path.",
			Buckets: []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5},
		}, []string{"success"}),
		commitMarshalDuration: factory.NewHistogram(prometheus.HistogramOpts{
			Namespace: Namespace,
			Name:      "commit_marshal_duration_seconds",
			Help:      "Time (in seconds) to SSZ-marshal the payload in CommitUnsafePayload",
			Buckets:   []float64{.0001, .00025, .0005, .001, .0025, .005, .01, .025},
		}),
		commitRaftApplyDuration: factory.NewHistogram(prometheus.HistogramOpts{
			Namespace: Namespace,
			Name:      "commit_raft_apply_duration_seconds",
			Help:      "Time (in seconds) for raft Apply (replication, storage, and FSM apply) in CommitUnsafePayload",
			Buckets:   []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5},
		}),
		commitPayloadSize: factory.NewHistogram(prometheus.HistogramOpts{
			Namespace: Namespace,
			Name:      "commit_payload_size_bytes",
			Help:      "SSZ-encoded payload size in bytes",
			Buckets:   []float64{1000, 10000, 50000, 100000, 500000, 1000000, 1500000, 2000000, 2500000},
		}),
		fsmApplyDuration: factory.NewHistogram(prometheus.HistogramOpts{
			Namespace: Namespace,
			Name:      "fsm_apply_duration_seconds",
			Help:      "Time (in seconds) spent in FSM Apply (SSZ decode plus unsafe-head update)",
			Buckets:   []float64{.0001, .00025, .0005, .001, .0025, .005, .01, .025},
		}),
		logStoreDuration: factory.NewHistogram(prometheus.HistogramOpts{
			Namespace: Namespace,
			Name:      "raft_log_store_duration_seconds",
			Help:      "Time (in seconds) spent writing raft log entries to the underlying log store",
			Buckets:   []float64{.0001, .00025, .0005, .001, .0025, .005, .01, .025, .05},
		}),
	}
}

func (m *Metrics) Start(host string, port int) (*httputil.HTTPServer, error) {
	return opmetrics.StartServer(m.registry, host, port)
}

// RecordInfo sets a pseudo-metric that contains versioning and
// config info for the op-proposer.
func (m *Metrics) RecordInfo(version string) {
	m.info.WithLabelValues(version).Set(1)
}

// RecordUp sets the up metric to 1.
func (m *Metrics) RecordUp() {
	m.up.Set(1)
}

// RecordHealthCheck increments the healthChecks counter.
func (m *Metrics) RecordHealthCheck(success bool, err error) {
	errStr := ""
	if err != nil {
		errStr = err.Error()
	}
	m.healthChecks.WithLabelValues(strconv.FormatBool(success), errStr).Inc()
}

// RecordLeaderTransfer increments the leaderTransfers counter.
func (m *Metrics) RecordLeaderTransfer(success bool) {
	m.leaderTransfers.WithLabelValues(strconv.FormatBool(success)).Inc()
}

// RecordStateChange increments the stateChanges counter.
func (m *Metrics) RecordStateChange(leader bool, healthy bool, active bool) {
	m.stateChanges.WithLabelValues(strconv.FormatBool(leader), strconv.FormatBool(healthy), strconv.FormatBool(active)).Inc()
}

// RecordStartSequencer increments the sequencerStarts counter.
func (m *Metrics) RecordStartSequencer(success bool) {
	m.sequencerStarts.WithLabelValues(strconv.FormatBool(success)).Inc()
}

// RecordStopSequencer increments the sequencerStops counter.
func (m *Metrics) RecordStopSequencer(success bool) {
	m.sequencerStops.WithLabelValues(strconv.FormatBool(success)).Inc()
}

// RecordLoopExecutionTime records the time it took to execute the conductor loop.
func (m *Metrics) RecordLoopExecutionTime(duration float64) {
	m.loopExecutionTime.Observe(duration)
}

// RecordRollupBoostConnectionAttempts increments the rollupBoostConnectionAttempts counter.
func (m *Metrics) RecordRollupBoostConnectionAttempts(success bool, source string) {
	m.rollupBoostConnectionAttempts.WithLabelValues(strconv.FormatBool(success), source).Inc()
}

// RecordWebSocketClientCount sets the current number of WebSocket clients connected.
func (m *Metrics) RecordWebSocketClientCount(count int) {
	m.webSocketClients.Set(float64(count))
}

func (m *Metrics) RecordBinaryCommitDuration(seconds float64, success bool) {
	m.binaryCommitRequestDuration.WithLabelValues(strconv.FormatBool(success)).Observe(seconds)
}

func (m *Metrics) RecordCommitDuration(marshalSec, raftApplySec float64) {
	m.commitMarshalDuration.Observe(marshalSec)
	m.commitRaftApplyDuration.Observe(raftApplySec)
}

func (m *Metrics) RecordCommitPayloadSize(payloadBytes float64) {
	m.commitPayloadSize.Observe(payloadBytes)
}

func (m *Metrics) RecordFSMApplyDuration(seconds float64) {
	m.fsmApplyDuration.Observe(seconds)
}

func (m *Metrics) RecordLogStoreDuration(seconds float64) {
	m.logStoreDuration.Observe(seconds)
}
