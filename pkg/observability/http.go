// Package observability provides a reusable diagnostic HTTP server
// (/livez, /readyz, /metrics, optional /debug/pprof) shared by every
// Hermes service.
//
// Pattern (per cmd/<svc>/main.go):
//
//  1. config + log + DB + NATS connect
//  2. pre-bind diagListener on cfg.MetricsPort
//  3. construct diagSrv with a ReadinessFn that probes its deps
//  4. start diagSrv goroutine (now /livez returns 200, /readyz returns 503)
//  5. start NATS consumers / service-specific work
//  6. bind gRPC listener
//  7. diagSrv.SetReady(true)
//  8. grpcSrv.Serve(lis)
//
// On shutdown, the reverse: SetReady(false) → grpc health
// NOT_SERVING → drain consumers → gRPC GracefulStop → close DB/NATS →
// diagSrv shutdown LAST. The 503-before-NOT_SERVING ordering matters:
// it gives load balancers and `depends_on: service_healthy` graphs
// time to drain in-flight traffic before connections start refusing.
//
// Stage F chunk 4 extracted this package from
// internal/mbs/observability/http.go to share the shape across all
// services. Mbs-specific metric structs (HandlerMetrics,
// SessionMetrics) stay in their internal packages — only the HTTP
// probe surface is shared.

package observability

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// readinessTimeout caps how long /readyz waits for the underlying probe
// (typically a DB Ping). A K8s readiness probe with periodSeconds=5
// can't tolerate longer than this anyway.
const readinessTimeout = 2 * time.Second

// shutdownTimeout caps graceful HTTP server shutdown.
const shutdownTimeout = 5 * time.Second

// ReadinessFunc is called by /readyz to probe downstream dependencies
// (DB, NATS). Return nil for ready, non-nil with a brief reason for
// not-ready. Implementations MUST respect ctx (the server applies a
// readinessTimeout deadline).
type ReadinessFunc func(ctx context.Context) error

// HTTPServer hosts /healthz, /readyz, /metrics, and optional /debug/pprof
// on a dedicated diagnostic port (default :9092). Wraps an http.Server
// and exposes a ready flag toggled by SetReady — main.go flips it
// after its dependencies are connected, and back off during graceful
// shutdown so probes see the transition.
type HTTPServer struct {
	addr        string
	srv         *http.Server
	ready       atomic.Bool
	readinessFn ReadinessFunc
}

// Options bundles HTTPServer constructor args.
type Options struct {
	Addr        string             // e.g. ":9092"
	Registerer  prometheus.Gatherer // typically prometheus.DefaultGatherer
	ReadinessFn ReadinessFunc       // probe; may be nil (then /readyz only checks the ready flag)
	EnablePprof bool                // mount /debug/pprof/* when true
}

// NewHTTPServer constructs (but does not start) the diagnostic server.
// Initial readiness is false — main.go must call SetReady(true) after
// dependencies are up. /healthz returns 200 unconditionally.
func NewHTTPServer(opts Options) *HTTPServer {
	h := &HTTPServer{
		addr:        opts.Addr,
		readinessFn: opts.ReadinessFn,
	}

	mux := http.NewServeMux()
	// /livez is the K8s-aligned liveness probe name. /healthz is the
	// historical Hermes name preserved for chunk-1 dev compose
	// compatibility — both routes return identical 200 ok\n.
	mux.HandleFunc("/livez", h.handleHealthz)
	mux.HandleFunc("/healthz", h.handleHealthz)
	mux.HandleFunc("/readyz", h.handleReadyz)
	mux.Handle("/metrics", promhttp.HandlerFor(opts.Registerer, promhttp.HandlerOpts{
		// Avoid panic-on-failure; production exposition should never crash the server.
		ErrorHandling: promhttp.ContinueOnError,
	}))

	if opts.EnablePprof {
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	}

	h.srv = &http.Server{
		Addr:              opts.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second, // mitigate slowloris on diag port
	}
	return h
}

// SetReady toggles the ready flag. main.go calls SetReady(true) once
// its dependencies are connected, SetReady(false) at shutdown.
func (h *HTTPServer) SetReady(ready bool) { h.ready.Store(ready) }

// Start runs the HTTP server until ctx is canceled. Returns nil on
// graceful shutdown, error otherwise. Safe to call once.
//
// Prefer Serve(ctx, lis) when callers want to pre-bind the listener
// so port-collision errors surface synchronously at boot rather than
// on the goroutine that runs Start.
func (h *HTTPServer) Start(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		if err := h.srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		// Graceful shutdown with our own bounded deadline.
		shutCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		// Mark not-ready so probes flip before listener closes.
		h.ready.Store(false)
		_ = h.srv.Shutdown(shutCtx)
		return <-errCh
	case err := <-errCh:
		return err
	}
}

// Serve drives the HTTP server on a pre-bound listener. Use this when
// the caller wants port-bind errors to surface synchronously at boot
// (the recommended pattern — see package doc). The listener is closed
// by the server's Shutdown call on graceful exit; callers should NOT
// double-close.
//
// Returns nil on graceful shutdown via ctx, the http.Serve error
// otherwise (excluding http.ErrServerClosed which is normalised to nil).
func (h *HTTPServer) Serve(ctx context.Context, lis net.Listener) error {
	errCh := make(chan error, 1)
	go func() {
		if err := h.srv.Serve(lis); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()
	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		_ = h.srv.Shutdown(shutCtx)
		return <-errCh
	case err := <-errCh:
		return err
	}
}

// Addr returns the configured listen address. Useful for tests that
// hijack the listener.
func (h *HTTPServer) Addr() string { return h.addr }

// Handler returns the underlying http.Handler. Used by tests that
// drive requests via httptest without binding a real port.
func (h *HTTPServer) Handler() http.Handler { return h.srv.Handler }

// ─────────────────────────────────────────────────────────────────────
// handlers
// ─────────────────────────────────────────────────────────────────────

func (h *HTTPServer) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func (h *HTTPServer) handleReadyz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")

	if !h.ready.Load() {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = fmt.Fprintln(w, "not ready: service is starting or shutting down")
		return
	}

	if h.readinessFn != nil {
		ctx, cancel := context.WithTimeout(r.Context(), readinessTimeout)
		defer cancel()
		if err := h.readinessFn(ctx); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			// Surface a short reason; full err detail goes to logs upstream.
			_, _ = fmt.Fprintf(w, "not ready: %s\n", err.Error())
			return
		}
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ready\n"))
}
