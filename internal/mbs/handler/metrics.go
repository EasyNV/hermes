package handler

import (
	"github.com/prometheus/client_golang/prometheus"
)

// HandlerMetrics is the Prometheus metric set for the gRPC handler.
// All names are prefixed `hermes_mbs_handler_*` to keep them distinct
// from the session/store/refresh metrics.
//
// A nil *HandlerMetrics is safe — every RPC checks h.metrics != nil
// before incrementing. This lets tests pass nil for brevity.
type HandlerMetrics struct {
	// RPCDuration is the per-method histogram. Labels: method, code.
	RPCDuration *prometheus.HistogramVec

	// BridgeLogins counts BridgeLogin terminations by outcome.
	// Labels: outcome (success|failure_<code>|cancel).
	BridgeLogins *prometheus.CounterVec

	// SendLatency is the SendMessage end-to-end latency histogram.
	// Labels: outcome (ok|err).
	SendLatency *prometheus.HistogramVec

	// SendTotal counts SendMessage outcomes including dedupe hits.
	// Labels: outcome (ok|err|dedupe_hit).
	SendTotal *prometheus.CounterVec

	// ResolveTotal counts ResolvePhone backends. Labels: source (cache|live).
	ResolveTotal *prometheus.CounterVec

	// InboundCount is the per-delta counter (post-Listen-stream).
	// Used for dashboarding "how chatty is each session".
	InboundCount prometheus.Counter

	// SubscribersGauge is the live count of Listen subscriptions.
	SubscribersGauge prometheus.Gauge

	// DriverSemaphoreFull increments when BridgeLogin returns
	// ResourceExhausted because the bridge semaphore is full.
	DriverSemaphoreFull prometheus.Counter
}

// NewHandlerMetrics registers all handler metrics on reg. Returns
// nil-safe metrics; callers should check h.metrics != nil before use.
func NewHandlerMetrics(reg prometheus.Registerer) *HandlerMetrics {
	if reg == nil {
		return nil
	}
	m := &HandlerMetrics{
		RPCDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "hermes_mbs_handler_rpc_duration_seconds",
			Help:    "Per-RPC handler latency.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "code"}),

		BridgeLogins: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "hermes_mbs_handler_bridge_logins_total",
			Help: "BridgeLogin terminations by outcome.",
		}, []string{"outcome"}),

		SendLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "hermes_mbs_handler_send_latency_seconds",
			Help:    "End-to-end SendMessage latency.",
			Buckets: prometheus.DefBuckets,
		}, []string{"outcome"}),

		SendTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "hermes_mbs_handler_send_total",
			Help: "SendMessage outcomes (ok|err|dedupe_hit).",
		}, []string{"outcome"}),

		ResolveTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "hermes_mbs_handler_resolve_total",
			Help: "ResolvePhone backend distribution (cache|live).",
		}, []string{"source"}),

		InboundCount: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "hermes_mbs_handler_inbound_total",
			Help: "Total inbound deltas surfaced through Listen.",
		}),

		SubscribersGauge: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "hermes_mbs_handler_listen_subscribers",
			Help: "Active Listen subscriptions.",
		}),

		DriverSemaphoreFull: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "hermes_mbs_handler_bridge_semaphore_full_total",
			Help: "Count of BridgeLogin rejections due to full semaphore.",
		}),
	}

	reg.MustRegister(
		m.RPCDuration, m.BridgeLogins, m.SendLatency, m.SendTotal,
		m.ResolveTotal, m.InboundCount, m.SubscribersGauge,
		m.DriverSemaphoreFull,
	)
	return m
}
