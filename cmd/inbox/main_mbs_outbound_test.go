package main

import (
	"context"
	"errors"
	"testing"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/hermes-waba/hermes/internal/inbox/handler"
)

// fakeStoreOutbound captures calls for processMbsOutbound's three paths.
// Reuses the fakeStore from main_mbs_test.go by extending its hooks via
// the existing function fields.

type outboundCaptures struct {
	setMID struct {
		called    bool
		messageID string
		mbsMID    string
	}
	updateStatus struct {
		called bool
		mid    string
		status string
	}
	markFailed struct {
		called bool
		id     string
	}
	getByMID struct {
		called bool
		mid    string
	}
}

// Use fakeStore from main_mbs_test.go but override SetMbsMID etc by
// instance methods. Go doesn't support method overrides on struct
// instances, so we add the hooks to fakeStore upstream OR build a
// dedicated test struct. We choose the latter for isolation.

type fakeOutStore struct {
	*fakeStore // satisfies handler.Store via embedding
	caps       *outboundCaptures
	// Method overrides
	setMbsMIDFn        func(ctx context.Context, messageID, mbsMID string) error
	updateStatusFn     func(ctx context.Context, mid, status string) error
	markFailedByIDFn   func(ctx context.Context, id string) error
	getMessageByMIDFn  func(ctx context.Context, mid string) (*handler.MessageRow, error)
}

// Override the relevant methods on the embedded fakeStore.
func (f *fakeOutStore) SetMbsMID(ctx context.Context, messageID, mbsMID string) error {
	f.caps.setMID.called = true
	f.caps.setMID.messageID = messageID
	f.caps.setMID.mbsMID = mbsMID
	if f.setMbsMIDFn != nil {
		return f.setMbsMIDFn(ctx, messageID, mbsMID)
	}
	return nil
}

func (f *fakeOutStore) UpdateMbsMessageStatus(ctx context.Context, mid, statusStr string) error {
	f.caps.updateStatus.called = true
	f.caps.updateStatus.mid = mid
	f.caps.updateStatus.status = statusStr
	if f.updateStatusFn != nil {
		return f.updateStatusFn(ctx, mid, statusStr)
	}
	return nil
}

func (f *fakeOutStore) MarkOutboundFailedByID(ctx context.Context, id string) error {
	f.caps.markFailed.called = true
	f.caps.markFailed.id = id
	if f.markFailedByIDFn != nil {
		return f.markFailedByIDFn(ctx, id)
	}
	return nil
}

func (f *fakeOutStore) GetMessageByMbsMID(ctx context.Context, mid string) (*handler.MessageRow, error) {
	f.caps.getByMID.called = true
	f.caps.getByMID.mid = mid
	if f.getMessageByMIDFn != nil {
		return f.getMessageByMIDFn(ctx, mid)
	}
	// Default: row exists, status=pending so forward-transitions succeed.
	return &handler.MessageRow{ID: "msg-1", Direction: "outbound", Status: "pending", MbsMID: mid}, nil
}

func mkOutStore() *fakeOutStore {
	return &fakeOutStore{
		fakeStore: &fakeStore{},
		caps:      &outboundCaptures{},
	}
}

func mkOutboundEvent(otid, mid string, ok bool, errMsg string) *hermesv1.MbsOutboundEvent {
	ev := &hermesv1.MbsOutboundEvent{
		Meta: &hermesv1.EventMeta{
			EventId:  "evt-out",
			TenantId: "tenant-1",
			Source:   "hermes-mbs",
		},
		Uid:            42,
		ThreadId:       "thread-A",
		Mid:            mid,
		Otid:           "client-otid-from-meta",
		LatencyMs:      100,
		Ok:             ok,
		Error:          errMsg,
		ClientDedupeId: []byte(otid),
	}
	return ev
}

// 1. Happy path: success event with mid → SetMbsMID + UpdateMbsMessageStatus(sent).
func TestProcessMbsOutbound_Success_StampsAndMarksSent(t *testing.T) {
	fs := mkOutStore()
	ev := mkOutboundEvent("msg-uuid-1", "mid.$AAA", true, "")

	if ok := processMbsOutbound(context.Background(), fs, silentLogger(), ev); !ok {
		t.Fatal("expected ack")
	}
	if !fs.caps.setMID.called {
		t.Error("expected SetMbsMID called")
	}
	if fs.caps.setMID.messageID != "msg-uuid-1" {
		t.Errorf("SetMbsMID messageID: got %q", fs.caps.setMID.messageID)
	}
	if fs.caps.setMID.mbsMID != "mid.$AAA" {
		t.Errorf("SetMbsMID mbsMID: got %q", fs.caps.setMID.mbsMID)
	}
	if !fs.caps.updateStatus.called {
		t.Error("expected UpdateMbsMessageStatus called")
	}
	if fs.caps.updateStatus.status != "sent" {
		t.Errorf("status: got %q, want sent", fs.caps.updateStatus.status)
	}
	if fs.caps.markFailed.called {
		t.Error("MarkOutboundFailedByID should NOT be called on success")
	}
}

// 2. Failure before mid (ok=false, mid="") → MarkOutboundFailedByID(otid).
func TestProcessMbsOutbound_FailureBeforeMid_MarksFailedByOtid(t *testing.T) {
	fs := mkOutStore()
	ev := mkOutboundEvent("msg-uuid-2", "", false, "MQTToT publish blocked")

	if ok := processMbsOutbound(context.Background(), fs, silentLogger(), ev); !ok {
		t.Fatal("expected ack")
	}
	if !fs.caps.markFailed.called {
		t.Error("expected MarkOutboundFailedByID called")
	}
	if fs.caps.markFailed.id != "msg-uuid-2" {
		t.Errorf("markFailed id: got %q", fs.caps.markFailed.id)
	}
	if fs.caps.setMID.called {
		t.Error("SetMbsMID should NOT be called when mid empty")
	}
	if fs.caps.updateStatus.called {
		t.Error("UpdateMbsMessageStatus should NOT be called when mid empty")
	}
}

// 3. Failure WITH mid → SetMbsMID + UpdateMbsMessageStatus(failed).
func TestProcessMbsOutbound_FailureWithMid_StampsAndMarksFailed(t *testing.T) {
	fs := mkOutStore()
	// Override default existing status so forward-transition allows pending→failed.
	fs.getMessageByMIDFn = func(_ context.Context, mid string) (*handler.MessageRow, error) {
		return &handler.MessageRow{ID: "msg-3", Direction: "outbound", Status: "sent", MbsMID: mid}, nil
	}
	ev := mkOutboundEvent("msg-uuid-3", "mid.$XYZ", false, "delivery refused")

	if ok := processMbsOutbound(context.Background(), fs, silentLogger(), ev); !ok {
		t.Fatal("expected ack")
	}
	if !fs.caps.setMID.called {
		t.Error("expected SetMbsMID called even on failure-with-mid")
	}
	if !fs.caps.updateStatus.called {
		t.Error("expected UpdateMbsMessageStatus called")
	}
	if fs.caps.updateStatus.status != "failed" {
		t.Errorf("status: got %q, want failed", fs.caps.updateStatus.status)
	}
}

// 4. Duplicate delivery (SetMbsMID returns ErrNotFound because mid already
//    stamped → still proceeds to status check). Test the no-op path.
func TestProcessMbsOutbound_DuplicateDelivery_NoOp(t *testing.T) {
	fs := mkOutStore()
	fs.setMbsMIDFn = func(_ context.Context, _, _ string) error {
		return handler.ErrNotFound // already stamped
	}
	fs.getMessageByMIDFn = func(_ context.Context, mid string) (*handler.MessageRow, error) {
		// Row in 'sent' state (already processed); forward-transition skip.
		return &handler.MessageRow{ID: "msg-4", Direction: "outbound", Status: "sent", MbsMID: mid}, nil
	}
	ev := mkOutboundEvent("msg-uuid-4", "mid.$AAA", true, "")

	if ok := processMbsOutbound(context.Background(), fs, silentLogger(), ev); !ok {
		t.Fatal("expected ack on duplicate")
	}
	// Should not double-write status.
	if fs.caps.updateStatus.called {
		t.Error("UpdateMbsMessageStatus should NOT be called when transition is non-forward")
	}
}

// 5. Empty otid + empty mid → ack with warning, no DB writes.
func TestProcessMbsOutbound_EmptyOtidAndMid_Drops(t *testing.T) {
	fs := mkOutStore()
	ev := mkOutboundEvent("", "", true, "")

	if ok := processMbsOutbound(context.Background(), fs, silentLogger(), ev); !ok {
		t.Fatal("expected ack on empty event")
	}
	if fs.caps.setMID.called || fs.caps.updateStatus.called || fs.caps.markFailed.called {
		t.Error("expected NO DB writes on empty correlation event")
	}
}

// 6. UpdateMbsMessageStatus transient error → nak.
func TestProcessMbsOutbound_StatusUpdateError_Nak(t *testing.T) {
	fs := mkOutStore()
	fs.updateStatusFn = func(_ context.Context, _, _ string) error {
		return errors.New("simulated DB outage")
	}
	ev := mkOutboundEvent("msg-uuid-5", "mid.$AAA", true, "")

	if ok := processMbsOutbound(context.Background(), fs, silentLogger(), ev); ok {
		t.Fatal("expected nak on UpdateMbsMessageStatus error")
	}
}

// 7. Failure with empty otid AND empty mid (both correlation keys absent) → ack drop.
func TestProcessMbsOutbound_FailureNoCorrelation_Drops(t *testing.T) {
	fs := mkOutStore()
	ev := mkOutboundEvent("", "", false, "weird")

	if ok := processMbsOutbound(context.Background(), fs, silentLogger(), ev); !ok {
		t.Fatal("expected ack")
	}
	if fs.caps.markFailed.called {
		t.Error("MarkOutboundFailedByID should NOT be called without otid")
	}
}
