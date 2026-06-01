package engine

import (
	"context"
	"testing"
	"time"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/hermes-waba/hermes/internal/campaign/handler"
)

func resultCampaign() *handler.CampaignRow {
	return &handler.CampaignRow{
		ID:          "camp-123",
		WorkspaceID: "ws-1",
		Status:      "running",
	}
}

func mkResult(ok bool, dedupe string, uid int64, errMsg string) *hermesv1.MbsOutboundEvent {
	return &hermesv1.MbsOutboundEvent{
		Meta:           &hermesv1.EventMeta{TenantId: "tenant-A"},
		Uid:            uid,
		Ok:             ok,
		Error:          errMsg,
		ClientDedupeId: []byte(dedupe),
	}
}

// S6: positive result transitions queued->sent, bumps counters, and (when the
// campaign has fully drained) marks it completed.
func TestHandleMbsResult_SuccessMarksSentAndCompletes(t *testing.T) {
	store := &fakeMbsStore{
		campaign:        resultCampaign(),
		inflightPending: 0,
		inflightQueued:  0, // fully drained after this result
	}
	eng := newTestEngine(store)

	ack := eng.HandleMbsResult(context.Background(), mkResult(true, "camp-123:c-1", 61590752691262, ""))
	if !ack {
		t.Fatal("expected Ack (true)")
	}
	if len(store.sentFromResultLog) != 1 || store.sentFromResultLog[0] != "c-1" {
		t.Errorf("expected sent write-back for c-1, got %v", store.sentFromResultLog)
	}
	if store.sentCount != 1 {
		t.Errorf("expected sent_count bump, got %d", store.sentCount)
	}
	if len(store.mbsIncLog) != 1 || store.mbsIncLog[0].UID != 61590752691262 {
		t.Errorf("expected session sent-count bump for uid, got %v", store.mbsIncLog)
	}
	// Drained -> completed.
	completed := false
	for _, s := range store.statusLog {
		if s == "completed" {
			completed = true
		}
	}
	if !completed {
		t.Errorf("expected campaign completed when drained; statusLog=%v", store.statusLog)
	}
}

// S6: negative result transitions queued->failed, bumps failed_count.
func TestHandleMbsResult_FailureMarksFailed(t *testing.T) {
	store := &fakeMbsStore{
		campaign:        resultCampaign(),
		inflightPending: 0,
		inflightQueued:  1, // still one queued -> NOT complete yet
	}
	eng := newTestEngine(store)

	eng.HandleMbsResult(context.Background(), mkResult(false, "camp-123:c-2", 999, "OAuthException 190/464"))

	if len(store.failedFromResultLog) != 1 || store.failedFromResultLog[0].ContactID != "c-2" {
		t.Errorf("expected failed write-back for c-2, got %v", store.failedFromResultLog)
	}
	if store.failedFromResultLog[0].Err != "OAuthException 190/464" {
		t.Errorf("error string not propagated: %q", store.failedFromResultLog[0].Err)
	}
	if store.failedCount != 1 {
		t.Errorf("expected failed_count bump, got %d", store.failedCount)
	}
	// Not drained -> not completed.
	for _, s := range store.statusLog {
		if s == "completed" {
			t.Errorf("must NOT complete while contacts still queued; statusLog=%v", store.statusLog)
		}
	}
}

// S6: a duplicate/redelivered result (write-back returns 0 rows affected) must
// NOT double-count.
func TestHandleMbsResult_DuplicateNoDoubleCount(t *testing.T) {
	store := &fakeMbsStore{campaign: resultCampaign(), inflightQueued: 1}
	// Override to simulate "already transitioned" — 0 rows affected.
	store.sentFromResultZeroRows = true
	eng := newTestEngine(store)

	eng.HandleMbsResult(context.Background(), mkResult(true, "camp-123:c-1", 100, ""))

	if store.sentCount != 0 {
		t.Errorf("duplicate result must not bump sent_count; got %d", store.sentCount)
	}
	if len(store.mbsIncLog) != 0 {
		t.Errorf("duplicate result must not bump session count; got %d", len(store.mbsIncLog))
	}
}

// S6: a non-campaign dedupe id (manual send / malformed) is ignored + Ack'd.
func TestHandleMbsResult_NonCampaignIgnored(t *testing.T) {
	store := &fakeMbsStore{campaign: resultCampaign()}
	eng := newTestEngine(store)

	cases := [][]byte{nil, []byte(""), []byte("nocolon"), []byte("a:b:c"), []byte(":c-1"), []byte("camp:")}
	for _, dedupe := range cases {
		ev := &hermesv1.MbsOutboundEvent{Meta: &hermesv1.EventMeta{TenantId: "t"}, Ok: true, ClientDedupeId: dedupe}
		if ack := eng.HandleMbsResult(context.Background(), ev); !ack {
			t.Errorf("dedupe %q: expected Ack", dedupe)
		}
	}
	if len(store.sentFromResultLog) != 0 || len(store.failedFromResultLog) != 0 {
		t.Errorf("non-campaign events must not write back")
	}
}

// S7: the reaper times out stuck queued contacts -> failed + failed_count, and
// re-checks completion.
func TestReapStuckQueued_TimesOutAndCompletes(t *testing.T) {
	store := &fakeMbsStore{
		campaign: resultCampaign(),
		reapReturn: []handler.ReapedContact{
			{CampaignID: "camp-123", ContactID: "c-1"},
			{CampaignID: "camp-123", ContactID: "c-2"},
		},
		inflightPending: 0,
		inflightQueued:  0, // drained after reap
	}
	eng := newTestEngine(store)

	eng.ReapStuckQueued(context.Background(), 5*time.Minute)

	if store.failedCount != 2 {
		t.Errorf("expected 2 failed_count bumps (one per reaped contact), got %d", store.failedCount)
	}
	completed := false
	for _, s := range store.statusLog {
		if s == "completed" {
			completed = true
		}
	}
	if !completed {
		t.Errorf("expected completion after reap drains campaign; statusLog=%v", store.statusLog)
	}
}

// S7: no stuck contacts -> reaper is a no-op (no counter bumps, no status writes).
func TestReapStuckQueued_NoopWhenNothingStuck(t *testing.T) {
	store := &fakeMbsStore{campaign: resultCampaign(), reapReturn: nil}
	eng := newTestEngine(store)

	eng.ReapStuckQueued(context.Background(), 5*time.Minute)

	if store.failedCount != 0 || len(store.statusLog) != 0 {
		t.Errorf("reaper must be a no-op when nothing stuck; failed=%d status=%v", store.failedCount, store.statusLog)
	}
}
