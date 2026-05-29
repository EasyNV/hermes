package main

import (
	"errors"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
)

// jetStreamRecorder is a tiny fake that records AddStream calls so
// tests can assert shape without spinning up a real NATS server.
// Mirrors the pattern used by chunk-4's handler/events_test.go.
type jetStreamRecorder struct {
	calls   []*natsgo.StreamConfig
	failOn  string // when non-empty, AddStream returns errInject for any stream with this name
	errInject error
}

func (r *jetStreamRecorder) AddStream(cfg *natsgo.StreamConfig, _ ...natsgo.JSOpt) (*natsgo.StreamInfo, error) {
	r.calls = append(r.calls, cfg)
	if r.failOn != "" && cfg.Name == r.failOn {
		return nil, r.errInject
	}
	return &natsgo.StreamInfo{Config: *cfg}, nil
}

func TestEnsureStreams_CreatesBothStreams(t *testing.T) {
	rec := &jetStreamRecorder{}
	if err := ensureStreams(rec, 1); err != nil {
		t.Fatalf("ensureStreams: %v", err)
	}
	if len(rec.calls) != 2 {
		t.Fatalf("expected 2 AddStream calls, got %d", len(rec.calls))
	}

	// Stream #1 — HERMES_MBS (events).
	got := rec.calls[0]
	if got.Name != "HERMES_MBS" {
		t.Errorf("stream[0].Name: got %q want HERMES_MBS", got.Name)
	}
	if got.Retention != natsgo.LimitsPolicy {
		t.Errorf("stream[0].Retention: got %v want LimitsPolicy", got.Retention)
	}
	if got.MaxAge != 7*24*time.Hour {
		t.Errorf("stream[0].MaxAge: got %v want 7d", got.MaxAge)
	}
	if got.Replicas != 1 {
		t.Errorf("stream[0].Replicas: got %d want 1", got.Replicas)
	}
	if got.Duplicates != 60*time.Second {
		t.Errorf("stream[0].Duplicates: got %v want 60s", got.Duplicates)
	}
	if want := []string{"hermes.mbs.message.>", "hermes.mbs.session.>"}; !subjectsEqual(got.Subjects, want) {
		t.Errorf("stream[0].Subjects: got %v want %v", got.Subjects, want)
	}

	// Stream #2 — HERMES_MBS_SEND (work queue).
	got = rec.calls[1]
	if got.Name != "HERMES_MBS_SEND" {
		t.Errorf("stream[1].Name: got %q want HERMES_MBS_SEND", got.Name)
	}
	if got.Retention != natsgo.WorkQueuePolicy {
		t.Errorf("stream[1].Retention: got %v want WorkQueuePolicy", got.Retention)
	}
	if got.MaxAge != 24*time.Hour {
		t.Errorf("stream[1].MaxAge: got %v want 24h", got.MaxAge)
	}
	if want := []string{"hermes.mbs.send.>"}; !subjectsEqual(got.Subjects, want) {
		t.Errorf("stream[1].Subjects: got %v want %v", got.Subjects, want)
	}
}

func TestEnsureStreams_NormalizesZeroReplicas(t *testing.T) {
	for _, in := range []int{0, -1, -42} {
		rec := &jetStreamRecorder{}
		if err := ensureStreams(rec, in); err != nil {
			t.Fatalf("replicas=%d: %v", in, err)
		}
		if rec.calls[0].Replicas != 1 {
			t.Errorf("replicas=%d normalized to %d want 1", in, rec.calls[0].Replicas)
		}
	}
}

func TestEnsureStreams_PassesThroughHigherReplicas(t *testing.T) {
	rec := &jetStreamRecorder{}
	if err := ensureStreams(rec, 3); err != nil {
		t.Fatalf("replicas=3: %v", err)
	}
	for i, c := range rec.calls {
		if c.Replicas != 3 {
			t.Errorf("stream[%d].Replicas: got %d want 3", i, c.Replicas)
		}
	}
}

func TestEnsureStreams_PropagatesAddStreamError(t *testing.T) {
	injected := errors.New("simulated NATS failure")
	rec := &jetStreamRecorder{failOn: "HERMES_MBS_SEND", errInject: injected}
	err := ensureStreams(rec, 1)
	if err == nil {
		t.Fatal("expected error from failed AddStream")
	}
	if !errors.Is(err, injected) {
		t.Errorf("err should wrap injected: got %v", err)
	}
}

// subjectsEqual is a small order-sensitive comparator. NATS treats
// subject order as significant when matching reused stream configs,
// so we mirror that strictness in the test.
func subjectsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
