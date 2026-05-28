package handler

import (
	"crypto/rand"
	"strings"
	"testing"

	"github.com/hermes-waba/hermes/internal/mbs/session"
	"github.com/hermes-waba/hermes/internal/mbs/store/mock"
	"github.com/hermes-waba/hermes/pkg/crypto"
	"github.com/rs/zerolog"
)

func newTestDEK(t *testing.T) crypto.DataEncryptionKey {
	t.Helper()
	var dek crypto.DataEncryptionKey
	if _, err := rand.Read(dek[:]); err != nil {
		t.Fatalf("rand DEK: %v", err)
	}
	return dek
}

func newTestManager(t *testing.T, st *mock.Store, dek crypto.DataEncryptionKey) session.Manager {
	t.Helper()
	return session.NewManager(session.Opts{
		Store:  st,
		DEK:    dek,
		PodID:  "hermes-mbs-test",
		Logger: zerolog.Nop(),
	})
}

func TestNewHandler_RejectsMissingRequired(t *testing.T) {
	dek := newTestDEK(t)
	st := mock.NewStore()
	mgr := newTestManager(t, st, dek)
	pub := NopPublisher{}
	driver := DriverFactory(func(DriverOptions) Driver { return nil })

	full := Options{
		Store: st, Manager: mgr, Publisher: pub,
		DriverFactory: driver, DEK: dek, PodID: "pod-1",
	}

	// Each required field flipped to zero/nil in turn.
	cases := map[string]Options{
		"missing Store":         {Manager: mgr, Publisher: pub, DriverFactory: driver, DEK: dek, PodID: "pod-1"},
		"missing Manager":       {Store: st, Publisher: pub, DriverFactory: driver, DEK: dek, PodID: "pod-1"},
		"missing Publisher":     {Store: st, Manager: mgr, DriverFactory: driver, DEK: dek, PodID: "pod-1"},
		"missing DriverFactory": {Store: st, Manager: mgr, Publisher: pub, DEK: dek, PodID: "pod-1"},
		"missing DEK":           {Store: st, Manager: mgr, Publisher: pub, DriverFactory: driver, PodID: "pod-1"},
		"missing PodID":         {Store: st, Manager: mgr, Publisher: pub, DriverFactory: driver, DEK: dek},
	}
	for name, opts := range cases {
		if _, err := NewHandler(opts); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}

	// Full opts should succeed.
	if _, err := NewHandler(full); err != nil {
		t.Errorf("full opts should construct: %v", err)
	}
}

func TestNewHandler_AppliesDefaults(t *testing.T) {
	dek := newTestDEK(t)
	st := mock.NewStore()
	mgr := newTestManager(t, st, dek)

	h, err := NewHandler(Options{
		Store: st, Manager: mgr, Publisher: NopPublisher{},
		DriverFactory: DriverFactory(func(DriverOptions) Driver { return nil }),
		DEK:           dek, PodID: "pod-1",
		// All optional fields left zero
	})
	if err != nil {
		t.Fatalf("construct: %v", err)
	}

	if cap(h.bridgeSem) != 4 {
		t.Errorf("MaxConcurrentBridgeLogins default: got %d want 4", cap(h.bridgeSem))
	}
	if h.dedupe.cap != 1024 {
		t.Errorf("DedupeCacheCap default: got %d want 1024", h.dedupe.cap)
	}
	if h.dedupe.ttl.Minutes() != 5 {
		t.Errorf("DedupeTTL default: got %v want 5m", h.dedupe.ttl)
	}
	if h.bridgeAcquireTimeout.Milliseconds() != 100 {
		t.Errorf("BridgeAcquireTimeout default: got %v want 100ms", h.bridgeAcquireTimeout)
	}
	if h.resolverFactory == nil {
		t.Error("resolverFactory should default to graphql adapter")
	}
}

func TestNewHandler_HonorsCustomLimits(t *testing.T) {
	dek := newTestDEK(t)
	st := mock.NewStore()
	mgr := newTestManager(t, st, dek)

	h, err := NewHandler(Options{
		Store: st, Manager: mgr, Publisher: NopPublisher{},
		DriverFactory: DriverFactory(func(DriverOptions) Driver { return nil }),
		DEK:           dek, PodID: "pod-1",

		MaxConcurrentBridgeLogins: 7,
		DedupeCacheCap:            42,
	})
	if err != nil {
		t.Fatalf("construct: %v", err)
	}

	if cap(h.bridgeSem) != 7 {
		t.Errorf("MaxConcurrentBridgeLogins: got %d want 7", cap(h.bridgeSem))
	}
	if h.dedupe.cap != 42 {
		t.Errorf("DedupeCacheCap: got %d want 42", h.dedupe.cap)
	}
}

func TestFirstNonEmpty(t *testing.T) {
	got := firstNonEmpty("", "", "x", "y")
	if got != "x" {
		t.Errorf("got %q want x", got)
	}
	if firstNonEmpty("", "") != "" {
		t.Errorf("all-empty should return empty")
	}
	if firstNonEmpty() != "" {
		t.Errorf("no args should return empty")
	}
}

// Compile-time guard: NopPublisher satisfies EventPublisher.
var _ EventPublisher = NopPublisher{}

// Smoke test that the package's exported error mappers don't panic on
// nil; the more interesting cases are in errors_test.go.
func TestMappers_NilSafe(t *testing.T) {
	if mapStoreErr(nil) != nil || mapSessionErr(nil) != nil || mapClientErr(nil) != nil {
		t.Error("nil err should map to nil")
	}
}

// Compile-time guard: ensure DriverFactory + DriverUpdate signatures
// align with what handler RPCs expect.
func TestBridgeUpdateShape(t *testing.T) {
	u := DriverUpdate{Kind: UpdateKindProgress, Progress: &DriverProgress{Detail: "x"}}
	if u.Kind != UpdateKindProgress || u.Progress.Detail != "x" {
		t.Errorf("Update shape wrong: %+v", u)
	}
	// Ensure the kind constants are non-zero (zero would clash with
	// the "no kind set" / "default zero value" semantics).
	for _, k := range []DriverUpdateKind{UpdateKindProgress, UpdateKindPrompt, UpdateKindSuccess, UpdateKindFailure} {
		if k == 0 {
			t.Errorf("DriverUpdateKind constant unexpectedly 0: %v", k)
		}
	}
	// And distinct.
	seen := map[DriverUpdateKind]bool{}
	for _, k := range []DriverUpdateKind{UpdateKindProgress, UpdateKindPrompt, UpdateKindSuccess, UpdateKindFailure} {
		if seen[k] {
			t.Errorf("duplicate kind value: %v", k)
		}
		seen[k] = true
	}
}

// Sanity: NewHandler errors are descriptive (no opaque "invalid input"
// from somewhere upstream).
func TestNewHandler_ErrorMessages(t *testing.T) {
	_, err := NewHandler(Options{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "Store") {
		t.Errorf("first missing-field err should mention Store, got %q", err.Error())
	}
}
