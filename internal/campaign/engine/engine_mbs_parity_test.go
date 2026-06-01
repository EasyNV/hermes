package engine

import (
	"context"
	"testing"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
)

// ─────────────────────────────────────────────────────────────────────
// G3: completion is idempotent — a redelivered/raced terminal result that
// re-enters maybeCompleteCampaign after the campaign is already completed must
// NOT publish a second completion event.
// ─────────────────────────────────────────────────────────────────────

func TestMaybeComplete_Idempotent_NoDoublePublish(t *testing.T) {
	store := &fakeMbsStore{
		campaign:        resultCampaign(),
		inflightPending: 0,
		inflightQueued:  0, // fully drained on every check
	}
	eng := newTestEngine(store)

	// First terminal result: drains -> completes -> transitions (publish once).
	eng.HandleMbsResult(context.Background(), mkResult(true, "camp-123:c-1", 61590752691262, ""))
	// Second (redelivered) terminal result for a different contact, still drained.
	eng.HandleMbsResult(context.Background(), mkResult(true, "camp-123:c-2", 61590752691262, ""))

	// CompleteCampaignIfRunning should have been CALLED twice (both drained)...
	if store.completeCalls != 2 {
		t.Errorf("expected 2 completion checks, got %d", store.completeCalls)
	}
	// ...but only ONE actual 'completed' transition recorded (the guard makes the
	// second a no-op). statusLog only appends 'completed' on a real transition.
	completedCount := 0
	for _, s := range store.statusLog {
		if s == "completed" {
			completedCount++
		}
	}
	if completedCount != 1 {
		t.Errorf("completion must transition exactly once; got %d 'completed' entries in %v",
			completedCount, store.statusLog)
	}
}

// G3: while contacts remain in flight, completion must not even be attempted.
func TestMaybeComplete_NotDrained_NoTransition(t *testing.T) {
	store := &fakeMbsStore{
		campaign:        resultCampaign(),
		inflightPending: 0,
		inflightQueued:  1, // one still queued
	}
	eng := newTestEngine(store)

	eng.HandleMbsResult(context.Background(), mkResult(true, "camp-123:c-1", 61590752691262, ""))

	if store.completeCalls != 0 {
		t.Errorf("must not attempt completion while queued>0, got %d calls", store.completeCalls)
	}
}

// ─────────────────────────────────────────────────────────────────────
// G2: a burned-session event disables the uid as a sender across campaigns.
// ─────────────────────────────────────────────────────────────────────

func mkBurned(uid int64, reason string) *hermesv1.MbsSessionLifecycleEvent {
	return &hermesv1.MbsSessionLifecycleEvent{
		Meta:         &hermesv1.EventMeta{TenantId: "tenant-A"},
		Uid:          uid,
		NewState:     hermesv1.MbsSessionState_MBS_SESSION_STATE_BURNED,
		Reason:       reason,
		LastConnackRc: 19,
	}
}

func TestHandleSessionBurned_DisablesSender(t *testing.T) {
	store := &fakeMbsStore{
		campaign:     resultCampaign(),
		burnedReturn: 2, // pretend 2 campaign_senders rows were active
	}
	eng := newTestEngine(store)

	ack := eng.HandleSessionBurned(context.Background(), mkBurned(61590134170831, "connack-rc-19"))
	if !ack {
		t.Fatal("expected Ack (true)")
	}
	if len(store.burnedUIDs) != 1 || store.burnedUIDs[0] != 61590134170831 {
		t.Errorf("expected MarkMbsSenderBurned for the uid, got %v", store.burnedUIDs)
	}
}

// G2: a zero-uid event is a poison drop (Ack, no store write).
func TestHandleSessionBurned_ZeroUidDropped(t *testing.T) {
	store := &fakeMbsStore{campaign: resultCampaign()}
	eng := newTestEngine(store)

	ack := eng.HandleSessionBurned(context.Background(), mkBurned(0, "x"))
	if !ack {
		t.Fatal("expected Ack (true) for poison drop")
	}
	if len(store.burnedUIDs) != 0 {
		t.Errorf("zero-uid event must not touch the store, got %v", store.burnedUIDs)
	}
}

// G2: a nil event is tolerated (Ack, no panic).
func TestHandleSessionBurned_NilEvent(t *testing.T) {
	store := &fakeMbsStore{campaign: resultCampaign()}
	eng := newTestEngine(store)
	var ev *hermesv1.MbsSessionLifecycleEvent
	if !eng.HandleSessionBurned(context.Background(), ev) {
		t.Fatal("expected Ack (true) for nil event")
	}
}
