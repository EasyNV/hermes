// Package refresh drives periodic cookie refresh for live MBS sessions.
//
// Meta's web session cookies have a 30-day inactivity expiry. To keep
// sessions alive between user actions, the ticker periodically pings
// /latest/inbox/ for every session owned by this pod whose
// LastRefreshedAt exceeds RefreshThreshold. The Ping response usually
// includes Set-Cookie headers that bump the expiry; we merge those
// back into the encrypted cookie column.
//
// Refresh outcomes:
//
//   - Cookies changed -> re-encrypt + UpdateSessionCookies (sets
//     LastRefreshedAt = response time)
//   - No cookie change -> UpdateSessionCookies with lastRefreshedAt
//     unchanged but lastValidatedAt = now (session still alive)
//   - Stage-D sentinel (5 cases) -> burn or suspend, emit lifecycle
//     event, release pod_id claim
//   - Transient (network, ctx) -> log + metric, retry next tick
//
// Threading: one ticker per process, single goroutine drives the
// tick loop, fans out attempts under a Concurrency-sized semaphore.
// Pod-startup jitter (0-5min) spreads fleet-bounce load.
package refresh

import "github.com/prometheus/client_golang/prometheus"

// Metrics is the Prometheus surface for the refresh ticker. All
// fields are nil-safe (methods are no-ops when receiver is nil) so
// callers can wire `Metrics: nil` in tests without nil-checks at
// every metric site.
type Metrics struct {
	// TickDuration histograms per-tick wall-clock latency. Useful
	// for confirming Interval is bigger than tick duration (we'd
	// fall behind otherwise).
	TickDuration prometheus.Histogram

	// Attempts counts every per-uid refresh attempt regardless of
	// outcome.
	Attempts prometheus.Counter

	// Successes counts attempts that completed without burn or
	// transient error. Includes "no cookie change" (session healthy).
	Successes prometheus.Counter

	// Burns counts permanent burns labeled by reason
	// ("token_invalidated", "account_suspended").
	Burns *prometheus.CounterVec

	// Suspends counts recoverable suspends labeled by reason
	// ("checkpoint_required", "challenge_required", "consent_required").
	Suspends *prometheus.CounterVec

	// TransientErrors counts network / ctx / 5xx outcomes that
	// don't change session state. High rate signals upstream pain.
	TransientErrors prometheus.Counter

	// DecryptFailures counts rows that couldn't be decrypted (wrong
	// DEK or row corruption). We do NOT auto-burn on decrypt failure
	// because that would destroy good sessions if the operator
	// mounted the wrong DEK file. Operators alert on a non-zero
	// rate.
	DecryptFailures prometheus.Counter

	// StaleSessionsGauge is set each tick to the number of rows
	// ListSessionsNeedingRefresh returned (bounded by the batch
	// limit). Growing trend = falling behind; flat = healthy.
	StaleSessionsGauge prometheus.Gauge
}

// NewMetrics registers all refresh-ticker metrics on the supplied
// registerer. Returns nil if reg is nil — callers should treat the
// returned *Metrics as nilable.
//
// All metric names are namespaced under `hermes_mbs_refresh_` to keep
// them sortable in Grafana and explicit about which service owns
// them.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	if reg == nil {
		return nil
	}
	m := &Metrics{
		TickDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "hermes_mbs_refresh_tick_duration_seconds",
			Help:    "Wall-clock duration of one refresh tick (list + fan-out).",
			Buckets: prometheus.DefBuckets,
		}),
		Attempts: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "hermes_mbs_refresh_attempts_total",
			Help: "Total per-uid refresh attempts (any outcome).",
		}),
		Successes: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "hermes_mbs_refresh_successes_total",
			Help: "Attempts that completed without burn/suspend/transient.",
		}),
		Burns: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "hermes_mbs_refresh_burns_total",
			Help: "Permanent burns triggered by refresh (Stage-D sentinel).",
		}, []string{"reason"}),
		Suspends: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "hermes_mbs_refresh_suspends_total",
			Help: "Recoverable suspends triggered by refresh (Stage-D sentinel).",
		}, []string{"reason"}),
		TransientErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "hermes_mbs_refresh_transient_errors_total",
			Help: "Network / ctx / 5xx outcomes that didn't change state.",
		}),
		DecryptFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "hermes_mbs_refresh_decrypt_failures_total",
			Help: "Rows where DecryptCreds failed (DEK drift or corruption).",
		}),
		StaleSessionsGauge: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "hermes_mbs_refresh_stale_sessions",
			Help: "Sessions returned by the last ListSessionsNeedingRefresh.",
		}),
	}
	reg.MustRegister(
		m.TickDuration,
		m.Attempts,
		m.Successes,
		m.Burns,
		m.Suspends,
		m.TransientErrors,
		m.DecryptFailures,
		m.StaleSessionsGauge,
	)
	return m
}

// ─────────────────────────────────────────────────────────────────────
// Nil-safe wrappers — call sites use these so they don't have to
// nil-check on every metric increment.
// ─────────────────────────────────────────────────────────────────────

func (m *Metrics) observeTickDuration(seconds float64) {
	if m == nil {
		return
	}
	m.TickDuration.Observe(seconds)
}

func (m *Metrics) incAttempts() {
	if m == nil {
		return
	}
	m.Attempts.Inc()
}

func (m *Metrics) incSuccesses() {
	if m == nil {
		return
	}
	m.Successes.Inc()
}

func (m *Metrics) incBurns(reason string) {
	if m == nil {
		return
	}
	m.Burns.WithLabelValues(reason).Inc()
}

func (m *Metrics) incSuspends(reason string) {
	if m == nil {
		return
	}
	m.Suspends.WithLabelValues(reason).Inc()
}

func (m *Metrics) incTransient() {
	if m == nil {
		return
	}
	m.TransientErrors.Inc()
}

func (m *Metrics) incDecryptFailures() {
	if m == nil {
		return
	}
	m.DecryptFailures.Inc()
}

func (m *Metrics) setStale(n int) {
	if m == nil {
		return
	}
	m.StaleSessionsGauge.Set(float64(n))
}
