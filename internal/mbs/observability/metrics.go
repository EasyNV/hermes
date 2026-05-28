// Package observability bundles Prometheus metrics + a diagnostic HTTP
// server (/healthz, /readyz, /metrics, optional pprof) for hermes-mbs.
//
// Scope: service-local for now. When a second Hermes service adopts the
// same shape, we promote this to pkg/observability without an API
// break. Until then, keep it private to mbs.
//
// Metric naming: all metrics are namespaced "hermes_mbs_*" so they
// don't collide with other services in the shared Prometheus instance.
// Labels are LOW-CARDINALITY by design — gRPC method names (small
// finite set), outcome enums, state transitions. NEVER label by uid,
// tenant_id, or thread_id (those are stamped in trace_id instead).
package observability

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics is the bundled metric registry for one hermes-mbs process.
// Passed by pointer into handler, refresh ticker, session manager.
type Metrics struct {
	// ─── gRPC handler ───────────────────────────────────────────────
	// labels: method (RPC name), code (gRPC status code)
	RPCDuration *prometheus.HistogramVec

	// Outcome of bridge login attempts.
	// labels: outcome ∈ {success, prompt_2fa, invalid_creds,
	//                    preflight_rc19, preflight_rc4, network,
	//                    semaphore_full, internal}
	BridgeLogins *prometheus.CounterVec

	// Send latency split by outcome.
	// labels: outcome ∈ {success, burned, network, timeout, other}
	SendLatency *prometheus.HistogramVec

	// Total inbound messages observed (NATS publish point).
	InboundCount prometheus.Counter

	// Current active Listen subscribers across all uids.
	SubscribersGauge prometheus.Gauge

	// ─── Refresh ticker ─────────────────────────────────────────────
	RefreshAttempts   prometheus.Counter
	RefreshSuccesses  prometheus.Counter
	// labels: reason ∈ {token_invalidated, checkpoint, challenge,
	//                   consent, suspended}
	RefreshBurns      *prometheus.CounterVec
	RefreshNetworkErr prometheus.Counter
	RefreshDuration   prometheus.Histogram

	// ─── Session manager ────────────────────────────────────────────
	// Currently in session.Manager (connected to Meta).
	ConnectedSessions prometheus.Gauge

	// Current value of the bridge semaphore.
	BridgeInFlight prometheus.Gauge

	// State transitions across the session lifecycle.
	// labels: from, to ∈ {unspecified, active, suspended, burned, bridging}
	StateTransitions *prometheus.CounterVec
}

// New constructs and registers all metrics against reg. Passing
// prometheus.DefaultRegisterer is the common path; tests pass a
// fresh registry to avoid duplicate-register panics.
func New(reg prometheus.Registerer) *Metrics {
	f := promauto.With(reg)

	return &Metrics{
		RPCDuration: f.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "hermes_mbs_rpc_duration_seconds",
				Help:    "gRPC RPC latency in seconds.",
				Buckets: prometheus.DefBuckets,
			},
			[]string{"method", "code"},
		),
		BridgeLogins: f.NewCounterVec(
			prometheus.CounterOpts{
				Name: "hermes_mbs_bridge_login_total",
				Help: "Bridge login attempts by terminal outcome.",
			},
			[]string{"outcome"},
		),
		SendLatency: f.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "hermes_mbs_send_latency_seconds",
				Help:    "Message send latency in seconds.",
				Buckets: []float64{0.1, 0.25, 0.5, 1, 2, 5, 10, 30},
			},
			[]string{"outcome"},
		),
		InboundCount: f.NewCounter(prometheus.CounterOpts{
			Name: "hermes_mbs_inbound_msg_total",
			Help: "Total inbound messages published to NATS.",
		}),
		SubscribersGauge: f.NewGauge(prometheus.GaugeOpts{
			Name: "hermes_mbs_listen_subscribers",
			Help: "Current active Listen stream subscribers.",
		}),

		RefreshAttempts: f.NewCounter(prometheus.CounterOpts{
			Name: "hermes_mbs_refresh_total",
			Help: "Cookie refresh attempts dispatched by the ticker.",
		}),
		RefreshSuccesses: f.NewCounter(prometheus.CounterOpts{
			Name: "hermes_mbs_refresh_success_total",
			Help: "Cookie refresh attempts that succeeded (200 OK).",
		}),
		RefreshBurns: f.NewCounterVec(
			prometheus.CounterOpts{
				Name: "hermes_mbs_refresh_burn_total",
				Help: "Sessions burned by the refresh ticker, by reason.",
			},
			[]string{"reason"},
		),
		RefreshNetworkErr: f.NewCounter(prometheus.CounterOpts{
			Name: "hermes_mbs_refresh_network_err_total",
			Help: "Cookie refresh attempts that failed at the transport layer.",
		}),
		RefreshDuration: f.NewHistogram(prometheus.HistogramOpts{
			Name:    "hermes_mbs_refresh_duration_seconds",
			Help:    "Cookie refresh HTTP latency in seconds.",
			Buckets: prometheus.DefBuckets,
		}),

		ConnectedSessions: f.NewGauge(prometheus.GaugeOpts{
			Name: "hermes_mbs_connected_sessions",
			Help: "Sessions currently held in session.Manager (connected to Meta).",
		}),
		BridgeInFlight: f.NewGauge(prometheus.GaugeOpts{
			Name: "hermes_mbs_bridge_in_flight",
			Help: "Current count of in-flight BridgeLogin operations (semaphore value).",
		}),
		StateTransitions: f.NewCounterVec(
			prometheus.CounterOpts{
				Name: "hermes_mbs_session_state_transitions_total",
				Help: "Session lifecycle state transitions.",
			},
			[]string{"from", "to"},
		),
	}
}
