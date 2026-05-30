package observability

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// freshRegistry returns a brand-new Prometheus registry so each test
// is isolated from package-level metric registration.
func freshRegistry(t *testing.T) *prometheus.Registry {
	t.Helper()
	return prometheus.NewRegistry()
}

func newServer(t *testing.T, readinessFn ReadinessFunc, enablePprof bool) *HTTPServer {
	t.Helper()
	reg := freshRegistry(t)
	// Register a single metric so /metrics has something to emit.
	// We register a no-op counter inline rather than depend on any
	// service-specific metric struct (pkg/observability is shared).
	reg.MustRegister(prometheus.NewCounter(prometheus.CounterOpts{
		Name: "hermes_obs_test_counter_total",
		Help: "Pkg observability test no-op counter.",
	}))
	return NewHTTPServer(Options{
		Addr:        ":0",
		Registerer:  reg,
		ReadinessFn: readinessFn,
		EnablePprof: enablePprof,
	})
}

func get(t *testing.T, h *HTTPServer, path string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	h.Handler().ServeHTTP(rec, req)
	body, _ := io.ReadAll(rec.Body)
	return rec.Code, string(body)
}

// ─────────────────────────────────────────────────────────────────────
// /healthz
// ─────────────────────────────────────────────────────────────────────

func TestHealthz_AlwaysOK(t *testing.T) {
	// Even if readinessFn errs, healthz is liveness — must stay 200.
	h := newServer(t, func(ctx context.Context) error {
		return errors.New("DB is on fire")
	}, false)
	code, body := get(t, h, "/healthz")
	if code != http.StatusOK {
		t.Errorf("expected 200, got %d (body=%q)", code, body)
	}
	if !strings.Contains(body, "ok") {
		t.Errorf("expected body to contain 'ok', got %q", body)
	}
}

// ─────────────────────────────────────────────────────────────────────
// /readyz
// ─────────────────────────────────────────────────────────────────────

func TestReadyz_NotReadyByDefault(t *testing.T) {
	// SetReady has not been called → 503.
	h := newServer(t, nil, false)
	code, body := get(t, h, "/readyz")
	if code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 before SetReady, got %d (body=%q)", code, body)
	}
}

func TestReadyz_ReadyToggleReflected(t *testing.T) {
	h := newServer(t, nil, false)
	h.SetReady(true)
	code, body := get(t, h, "/readyz")
	if code != http.StatusOK {
		t.Errorf("expected 200 after SetReady(true), got %d (body=%q)", code, body)
	}
	if !strings.Contains(body, "ready") {
		t.Errorf("expected 'ready' in body, got %q", body)
	}

	h.SetReady(false)
	code, _ = get(t, h, "/readyz")
	if code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 after SetReady(false), got %d", code)
	}
}

func TestReadyz_ReadinessFnError(t *testing.T) {
	h := newServer(t, func(ctx context.Context) error {
		return errors.New("postgres down")
	}, false)
	h.SetReady(true) // flag says ready, but probe still runs

	code, body := get(t, h, "/readyz")
	if code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when readiness fn errors, got %d", code)
	}
	if !strings.Contains(body, "postgres down") {
		t.Errorf("expected error reason in body, got %q", body)
	}
}

func TestReadyz_ReadinessFnTimeout(t *testing.T) {
	// Block longer than readinessTimeout to ensure the deadline is honored.
	h := newServer(t, func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
			return nil
		}
	}, false)
	h.SetReady(true)

	start := time.Now()
	code, body := get(t, h, "/readyz")
	elapsed := time.Since(start)

	if code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 on timeout, got %d (body=%q)", code, body)
	}
	if elapsed >= 5*time.Second {
		t.Errorf("readiness fn deadline not honored; elapsed=%v", elapsed)
	}
	// 2s readiness timeout + small handler overhead — give ourselves headroom.
	if elapsed > 3*time.Second {
		t.Errorf("readiness fn took unexpectedly long; elapsed=%v", elapsed)
	}
}

// ─────────────────────────────────────────────────────────────────────
// /metrics
// ─────────────────────────────────────────────────────────────────────

func TestMetrics_PrometheusFormat(t *testing.T) {
	h := newServer(t, nil, false)
	code, body := get(t, h, "/metrics")
	if code != http.StatusOK {
		t.Errorf("expected 200, got %d", code)
	}
	// Spot-check: a plain Counter emits its zero state at registration
	// time, so it must appear in the exposition. (HistogramVec/CounterVec
	// only emit after first observation — they're not useful spot-checks
	// in a steady-state test.)
	if !strings.Contains(body, "hermes_obs_test_counter_total") {
		t.Errorf("expected hermes_obs_test_counter_total in /metrics, got:\n%s", body)
	}
	// Sanity check the content type indicates Prometheus text format.
	// httptest doesn't expose headers via our helper; do an inline check.
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	h.Handler().ServeHTTP(rec, req)
	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/plain") && !strings.Contains(ct, "openmetrics") {
		t.Errorf("expected Prometheus text format Content-Type, got %q", ct)
	}
}

// ─────────────────────────────────────────────────────────────────────
// /debug/pprof gating
// ─────────────────────────────────────────────────────────────────────

func TestPprof_DisabledByDefault(t *testing.T) {
	h := newServer(t, nil, false) // EnablePprof=false
	code, _ := get(t, h, "/debug/pprof/cmdline")
	if code != http.StatusNotFound {
		t.Errorf("expected 404 when pprof disabled, got %d", code)
	}
}

func TestPprof_EnabledByFlag(t *testing.T) {
	h := newServer(t, nil, true) // EnablePprof=true
	code, body := get(t, h, "/debug/pprof/cmdline")
	if code != http.StatusOK {
		t.Errorf("expected 200 when pprof enabled, got %d (body=%q)", code, body)
	}
}
