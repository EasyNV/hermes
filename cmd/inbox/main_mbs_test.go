package main

import (
	"context"
	"errors"
	"testing"
	"time"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/hermes-waba/hermes/internal/inbox/handler"
	"github.com/rs/zerolog"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ---------------------------------------------------------------------------
// fakeStore — implements handler.Store with per-method function hooks.
// Only the methods exercised by processMbsInbound need hooks; the rest
// return zero/ErrNotFound so unexpected calls surface clearly.
// ---------------------------------------------------------------------------

type fakeStore struct {
	getWorkspaceIDForMbsUid func(ctx context.Context, uid int64) (string, string, error)
	getPhoneByMbsThread     func(ctx context.Context, uid int64, threadID string) (string, error)
	findContactByPhone      func(ctx context.Context, phone string) (*handler.ContactRow, string, error)
	autoCreateContact       func(ctx context.Context, tenantID, phone, name string) (*handler.ContactRow, error)
	findOrCreateMbsConv     func(ctx context.Context, ws, contact, sessUID, threadID, pageID string) (*handler.ConversationRow, bool, error)
	createMbsMessage        func(ctx context.Context, convID, direction, body, mid string) (*handler.MessageRow, bool, error)
	reopenConversation      func(ctx context.Context, id string) error
	updateLastMessage       func(ctx context.Context, convID, preview string) error

	// Captures
	autoCreatedTenant string
	autoCreatedPhone  string
	autoCreatedName   string
	convArgs          struct {
		workspaceID, contactID, sessionUID, threadID, pageID string
	}
	createdMsg struct {
		conversationID, direction, body, mid string
	}
	reopenedConvID   string
	lastMessageConv  string
	lastMessageText  string
}

// ---- Methods used by processMbsInbound ---------------------------------------

func (f *fakeStore) GetWorkspaceIDForMbsUid(ctx context.Context, uid int64) (string, string, error) {
	if f.getWorkspaceIDForMbsUid != nil {
		return f.getWorkspaceIDForMbsUid(ctx, uid)
	}
	return "", "", handler.ErrNotFound
}

func (f *fakeStore) GetPhoneByMbsThread(ctx context.Context, uid int64, threadID string) (string, error) {
	if f.getPhoneByMbsThread != nil {
		return f.getPhoneByMbsThread(ctx, uid, threadID)
	}
	return "", handler.ErrNotFound
}

func (f *fakeStore) FindContactByPhone(ctx context.Context, phone string) (*handler.ContactRow, string, error) {
	if f.findContactByPhone != nil {
		return f.findContactByPhone(ctx, phone)
	}
	return nil, "", handler.ErrNotFound
}

func (f *fakeStore) AutoCreateContact(ctx context.Context, tenantID, phone, name string) (*handler.ContactRow, error) {
	f.autoCreatedTenant = tenantID
	f.autoCreatedPhone = phone
	f.autoCreatedName = name
	if f.autoCreateContact != nil {
		return f.autoCreateContact(ctx, tenantID, phone, name)
	}
	return &handler.ContactRow{ID: "contact-auto", Phone: phone, Name: name}, nil
}

func (f *fakeStore) FindOrCreateMbsConversation(ctx context.Context, ws, contact, sessUID, threadID, pageID string) (*handler.ConversationRow, bool, error) {
	f.convArgs.workspaceID = ws
	f.convArgs.contactID = contact
	f.convArgs.sessionUID = sessUID
	f.convArgs.threadID = threadID
	f.convArgs.pageID = pageID
	if f.findOrCreateMbsConv != nil {
		return f.findOrCreateMbsConv(ctx, ws, contact, sessUID, threadID, pageID)
	}
	return &handler.ConversationRow{ID: "conv-1", Status: "unassigned"}, true, nil
}

func (f *fakeStore) CreateMbsMessage(ctx context.Context, convID, direction, body, mid string) (*handler.MessageRow, bool, error) {
	f.createdMsg.conversationID = convID
	f.createdMsg.direction = direction
	f.createdMsg.body = body
	f.createdMsg.mid = mid
	if f.createMbsMessage != nil {
		return f.createMbsMessage(ctx, convID, direction, body, mid)
	}
	return &handler.MessageRow{ID: "msg-1", ConversationID: convID, Direction: direction, MbsMID: mid}, true, nil
}

func (f *fakeStore) ReopenConversation(ctx context.Context, id string) error {
	f.reopenedConvID = id
	if f.reopenConversation != nil {
		return f.reopenConversation(ctx, id)
	}
	return nil
}

func (f *fakeStore) UpdateLastMessage(ctx context.Context, convID, preview string) error {
	f.lastMessageConv = convID
	f.lastMessageText = preview
	if f.updateLastMessage != nil {
		return f.updateLastMessage(ctx, convID, preview)
	}
	return nil
}

// ---- Stub methods (unused by processMbsInbound) ------------------------------

func (f *fakeStore) ListConversations(ctx context.Context, _, _, _, _, _, _ string, _ int32, _ bool, _, _ int32) ([]*handler.ConversationRow, int64, error) {
	return nil, 0, errors.New("not implemented in fakeStore")
}
func (f *fakeStore) GetConversation(ctx context.Context, _ string) (*handler.ConversationRow, error) {
	return nil, handler.ErrNotFound
}
func (f *fakeStore) GetConversationContact(ctx context.Context, _ string) (*handler.ContactRow, error) {
	return nil, handler.ErrNotFound
}
func (f *fakeStore) GetConversationWaNumber(ctx context.Context, _ string) (*handler.WaNumberRow, error) {
	return nil, handler.ErrNotFound
}
func (f *fakeStore) ClaimConversation(ctx context.Context, _, _ string) (*handler.ConversationRow, error) {
	return nil, handler.ErrNotFound
}
func (f *fakeStore) TransferConversation(ctx context.Context, _, _ string) (*handler.ConversationRow, error) {
	return nil, handler.ErrNotFound
}
func (f *fakeStore) CloseConversation(ctx context.Context, _ string) (*handler.ConversationRow, error) {
	return nil, handler.ErrNotFound
}
func (f *fakeStore) ListMessages(ctx context.Context, _, _ string, _, _ int32) ([]*handler.MessageRow, bool, int64, error) {
	return nil, false, 0, nil
}
func (f *fakeStore) CreateMessage(ctx context.Context, _, _, _ string, _, _ *string, _ string) (*handler.MessageRow, error) {
	return nil, errors.New("WA path not used by MBS consumer")
}
func (f *fakeStore) SearchMessages(ctx context.Context, _, _, _, _ string, _ bool, _, _ *time.Time, _, _ int32) ([]*handler.SearchHitRow, int64, error) {
	return nil, 0, nil
}
func (f *fakeStore) UpdateMessageStatus(ctx context.Context, _, _ string) error { return nil }
func (f *fakeStore) GetMessageByWaMessageID(ctx context.Context, _ string) (*handler.MessageRow, error) {
	return nil, handler.ErrNotFound
}
func (f *fakeStore) FindOrCreateConversation(ctx context.Context, _, _, _ string, _ *string) (*handler.ConversationRow, bool, error) {
	return nil, false, errors.New("WA path not used by MBS consumer")
}
func (f *fakeStore) SetFirstResponseTime(ctx context.Context, _ string, _ int32) error { return nil }
func (f *fakeStore) GetMessageByMbsMID(ctx context.Context, _ string) (*handler.MessageRow, error) {
	return nil, handler.ErrNotFound
}
func (f *fakeStore) UpdateMbsMessageStatus(ctx context.Context, _, _ string) error { return nil }
func (f *fakeStore) SetMbsMID(ctx context.Context, _, _ string) error              { return nil }
func (f *fakeStore) MarkOutboundFailedByID(ctx context.Context, _ string) error    { return nil }
func (f *fakeStore) GetWorkspaceIDForWaNumber(ctx context.Context, _ string) (string, string, error) {
	return "", "", handler.ErrNotFound
}
func (f *fakeStore) ClearAllConversations(ctx context.Context, _ string) (int64, error) { return 0, nil }
func (f *fakeStore) IsPhoneAllowlisted(ctx context.Context, _, _ string) (bool, error)  { return true, nil }
func (f *fakeStore) AddToAllowlist(ctx context.Context, _, _, _, _ string) error        { return nil }
func (f *fakeStore) BulkAddToAllowlist(ctx context.Context, _ string, _ []string, _, _ string) (int64, error) {
	return 0, nil
}
func (f *fakeStore) RemoveFromAllowlist(ctx context.Context, _, _ string) error { return nil }
func (f *fakeStore) ListAllowlist(ctx context.Context, _ string, _, _ int32) ([]handler.AllowlistEntry, int64, error) {
	return nil, 0, nil
}
func (f *fakeStore) CreateCannedResponse(ctx context.Context, _, _, _ string, _ *string) (*handler.CannedResponseRow, error) {
	return nil, handler.ErrNotFound
}
func (f *fakeStore) GetCannedResponse(ctx context.Context, _ string) (*handler.CannedResponseRow, error) {
	return nil, handler.ErrNotFound
}
func (f *fakeStore) ListCannedResponses(ctx context.Context, _, _ string, _, _ int32) ([]*handler.CannedResponseRow, int64, error) {
	return nil, 0, nil
}
func (f *fakeStore) UpdateCannedResponse(ctx context.Context, _, _, _ string) (*handler.CannedResponseRow, error) {
	return nil, handler.ErrNotFound
}
func (f *fakeStore) DeleteCannedResponse(ctx context.Context, _ string) error { return nil }
func (f *fakeStore) GetContactCampaignHistory(ctx context.Context, _ string, _, _ int32) ([]*handler.CampaignHistoryRow, int64, error) {
	return nil, 0, nil
}
func (f *fakeStore) GetAgentPerformance(ctx context.Context, _, _ string, _, _ *time.Time) ([]*handler.AgentPerfRow, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// Test fixtures
// ---------------------------------------------------------------------------

func mkInboundEvent(uid int64, tenant, threadID, mid, phone, text string) *hermesv1.MbsInboundMessageEvent {
	return &hermesv1.MbsInboundMessageEvent{
		Meta: &hermesv1.EventMeta{
			EventId:   "evt-1",
			TenantId:  tenant,
			Timestamp: timestamppb.Now(),
			Source:    "hermes-mbs",
		},
		Uid:           uid,
		PageId:        "page-A",
		WecMailboxId:  "mailbox-A",
		ThreadId:      threadID,
		Mid:           mid,
		SenderPhone:   phone,
		Text:          text,
		MetaTimestamp: timestamppb.Now(),
	}
}

func silentLogger() zerolog.Logger {
	return zerolog.Nop()
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// Happy path: real phone, contact missing → auto-created, conversation
// created, message stored, ack.
func TestProcessMbsInbound_HappyPath_RealPhone(t *testing.T) {
	fs := &fakeStore{
		getWorkspaceIDForMbsUid: func(_ context.Context, uid int64) (string, string, error) {
			if uid != 999 {
				t.Errorf("uid: got %d, want 999", uid)
			}
			return "ws-1", "tenant-1", nil
		},
		// findContactByPhone left default → returns ErrNotFound → triggers AutoCreate
	}
	ev := mkInboundEvent(999, "tenant-1", "thread-A", "mid.AAA", "6281234567890", "hello")

	if ok := processMbsInbound(context.Background(), fs, nil, silentLogger(), ev); !ok {
		t.Fatal("expected ack, got nak")
	}

	if fs.autoCreatedTenant != "tenant-1" {
		t.Errorf("autoCreate tenant: got %q, want %q", fs.autoCreatedTenant, "tenant-1")
	}
	if fs.autoCreatedPhone != "6281234567890" {
		t.Errorf("autoCreate phone: got %q, want %q", fs.autoCreatedPhone, "6281234567890")
	}
	if fs.autoCreatedName != "" {
		t.Errorf("autoCreate name: got %q, want empty (real phone)", fs.autoCreatedName)
	}
	if fs.convArgs.sessionUID != "999" {
		t.Errorf("conv session_uid: got %q, want %q", fs.convArgs.sessionUID, "999")
	}
	if fs.convArgs.threadID != "thread-A" {
		t.Errorf("conv thread_id: got %q, want %q", fs.convArgs.threadID, "thread-A")
	}
	if fs.convArgs.pageID != "page-A" {
		t.Errorf("conv page_id: got %q, want %q", fs.convArgs.pageID, "page-A")
	}
	if fs.createdMsg.mid != "mid.AAA" {
		t.Errorf("message mid: got %q, want %q", fs.createdMsg.mid, "mid.AAA")
	}
	if fs.createdMsg.direction != "inbound" {
		t.Errorf("message direction: got %q, want %q", fs.createdMsg.direction, "inbound")
	}
	if fs.lastMessageText != "hello" {
		t.Errorf("preview: got %q, want %q", fs.lastMessageText, "hello")
	}
}

// Re-poll idempotency: the db130 snapshot is re-read every 10s, so the same
// MID re-surfaces constantly. CreateMbsMessage reports wasInserted=false on
// the conflict; the handler must ACK but SKIP UpdateLastMessage (which sets
// last_message_at=now()) and the notification. Without this guard every poll
// re-stamps the card to "now", collapsing all conversations to the same
// timestamp. Regression for the "all cards show deploy-time" bug.
func TestProcessMbsInbound_RePoll_SkipsSideEffects(t *testing.T) {
	fs := &fakeStore{
		getWorkspaceIDForMbsUid: func(_ context.Context, _ int64) (string, string, error) {
			return "ws-1", "tenant-1", nil
		},
		// Simulate the message already existing: wasInserted=false.
		createMbsMessage: func(_ context.Context, convID, direction, _, mid string) (*handler.MessageRow, bool, error) {
			return &handler.MessageRow{ID: "msg-1", ConversationID: convID, Direction: direction, MbsMID: mid}, false, nil
		},
	}
	ev := mkInboundEvent(999, "tenant-1", "thread-A", "mid.AAA", "6281234567890", "hello")

	if ok := processMbsInbound(context.Background(), fs, nil, silentLogger(), ev); !ok {
		t.Fatal("expected ack on re-poll, got nak")
	}
	if fs.lastMessageConv != "" {
		t.Errorf("UpdateLastMessage must NOT be called on re-poll; got conv %q", fs.lastMessageConv)
	}
	if fs.lastMessageText != "" {
		t.Errorf("UpdateLastMessage must NOT be called on re-poll; got preview %q", fs.lastMessageText)
	}
}

// Enrichment: inbound with EMPTY sender_phone but a known thread_id should
// reverse-resolve the real customer phone from mbs_phone_threads, so the
// contact uses the real phone (unifying with outbound) instead of the
// synthetic mbs:thread:<id> slug.
func TestProcessMbsInbound_EnrichesPhoneFromThread(t *testing.T) {
	fs := &fakeStore{
		getWorkspaceIDForMbsUid: func(_ context.Context, _ int64) (string, string, error) {
			return "ws-1", "tenant-1", nil
		},
		getPhoneByMbsThread: func(_ context.Context, uid int64, threadID string) (string, error) {
			if uid != 999 || threadID != "1127921160404565" {
				t.Errorf("reverse lookup args: uid=%d thread=%q", uid, threadID)
			}
			return "6281290928464", nil
		},
		// findContactByPhone default → ErrNotFound → AutoCreate captures phone.
	}
	// Empty sender_phone — the mbs publisher never fills it for inbound.
	ev := mkInboundEvent(999, "tenant-1", "1127921160404565", "mid.ENR", "", "halo gan")

	if ok := processMbsInbound(context.Background(), fs, nil, silentLogger(), ev); !ok {
		t.Fatal("expected ack, got nak")
	}

	if fs.autoCreatedPhone != "6281290928464" {
		t.Errorf("autoCreate phone: got %q, want real phone 6281290928464 (not slug)", fs.autoCreatedPhone)
	}
	if fs.autoCreatedName != "" {
		t.Errorf("autoCreate name: got %q, want empty (real phone path)", fs.autoCreatedName)
	}
	if fs.convArgs.threadID != "1127921160404565" {
		t.Errorf("conv thread_id: got %q, want customer_id", fs.convArgs.threadID)
	}
}

// Enrichment miss: empty sender_phone AND no mbs_phone_threads row → fall
// back to the synthetic slug (customer-first thread; phone fills later).
func TestProcessMbsInbound_NoThreadMapping_FallsBackToSlug(t *testing.T) {
	fs := &fakeStore{
		getWorkspaceIDForMbsUid: func(_ context.Context, _ int64) (string, string, error) {
			return "ws-1", "tenant-1", nil
		},
		// getPhoneByMbsThread default → ErrNotFound → slug fallback.
	}
	ev := mkInboundEvent(999, "tenant-1", "7467234173890026394", "mid.SLG", "", "hi")

	if ok := processMbsInbound(context.Background(), fs, nil, silentLogger(), ev); !ok {
		t.Fatal("expected ack, got nak")
	}

	if fs.autoCreatedPhone != "mbs:thread:7467234173890026394" {
		t.Errorf("autoCreate lookup key: got %q, want synthetic slug", fs.autoCreatedPhone)
	}
	if fs.autoCreatedName == "" {
		t.Error("expected a synthetic display name for slug contact")
	}
}

// Workspace lookup miss → ack with no DB writes downstream.
func TestProcessMbsInbound_WorkspaceMiss_Drops(t *testing.T) {
	fs := &fakeStore{
		getWorkspaceIDForMbsUid: func(_ context.Context, _ int64) (string, string, error) {
			return "", "", handler.ErrNotFound
		},
	}
	ev := mkInboundEvent(42, "tenant-1", "thread-Z", "mid.ZZZ", "6280000", "ignored")

	if ok := processMbsInbound(context.Background(), fs, nil, silentLogger(), ev); !ok {
		t.Fatal("expected ack on workspace miss, got nak")
	}

	if fs.convArgs.threadID != "" {
		t.Errorf("conversation should NOT be created on workspace miss; thread_id=%q", fs.convArgs.threadID)
	}
	if fs.createdMsg.mid != "" {
		t.Errorf("message should NOT be created on workspace miss; mid=%q", fs.createdMsg.mid)
	}
}

// Empty sender phone → synthetic mbs:thread:<id> slug + name "MBS thread <tail>".
func TestProcessMbsInbound_EmptyPhone_SyntheticSlug(t *testing.T) {
	fs := &fakeStore{
		getWorkspaceIDForMbsUid: func(_ context.Context, _ int64) (string, string, error) {
			return "ws-1", "tenant-1", nil
		},
	}
	ev := mkInboundEvent(1, "tenant-1", "1234567890123456", "mid.SYN", "", "anonymous")

	if ok := processMbsInbound(context.Background(), fs, nil, silentLogger(), ev); !ok {
		t.Fatal("expected ack, got nak")
	}

	wantPhone := "mbs:thread:1234567890123456"
	if fs.autoCreatedPhone != wantPhone {
		t.Errorf("synthetic phone: got %q, want %q", fs.autoCreatedPhone, wantPhone)
	}
	wantName := "MBS thread 90123456"
	if fs.autoCreatedName != wantName {
		t.Errorf("synthetic name: got %q, want %q", fs.autoCreatedName, wantName)
	}
}

// Closed conversation reopens on inbound.
func TestProcessMbsInbound_ClosedReopens(t *testing.T) {
	reopened := false
	fs := &fakeStore{
		getWorkspaceIDForMbsUid: func(_ context.Context, _ int64) (string, string, error) {
			return "ws-1", "tenant-1", nil
		},
		findOrCreateMbsConv: func(_ context.Context, _, _, _, _, _ string) (*handler.ConversationRow, bool, error) {
			return &handler.ConversationRow{ID: "conv-closed", Status: "closed"}, false, nil
		},
		reopenConversation: func(_ context.Context, id string) error {
			reopened = (id == "conv-closed")
			return nil
		},
	}
	ev := mkInboundEvent(7, "tenant-1", "thread-R", "mid.R1", "62800", "wake up")

	if ok := processMbsInbound(context.Background(), fs, nil, silentLogger(), ev); !ok {
		t.Fatal("expected ack, got nak")
	}
	if !reopened {
		t.Error("expected ReopenConversation to be called for closed conv")
	}
}

// CreateMbsMessage transient error → nak.
func TestProcessMbsInbound_CreateMessageError_Nak(t *testing.T) {
	fs := &fakeStore{
		getWorkspaceIDForMbsUid: func(_ context.Context, _ int64) (string, string, error) {
			return "ws-1", "tenant-1", nil
		},
		createMbsMessage: func(_ context.Context, _, _, _, _ string) (*handler.MessageRow, bool, error) {
			return nil, false, errors.New("simulated DB outage")
		},
	}
	ev := mkInboundEvent(5, "tenant-1", "thread-E", "mid.E1", "62800", "boom")

	if ok := processMbsInbound(context.Background(), fs, nil, silentLogger(), ev); ok {
		t.Fatal("expected nak on CreateMbsMessage error, got ack")
	}
}

// AutoCreateContact failure → nak. Verifies the transient-vs-terminal split:
// failed contact create is treated as transient (NATS retry will likely retry
// once Postgres recovers).
func TestProcessMbsInbound_AutoCreateContactError_Nak(t *testing.T) {
	fs := &fakeStore{
		getWorkspaceIDForMbsUid: func(_ context.Context, _ int64) (string, string, error) {
			return "ws-1", "tenant-1", nil
		},
		autoCreateContact: func(_ context.Context, _, _, _ string) (*handler.ContactRow, error) {
			return nil, errors.New("simulated contacts DB outage")
		},
	}
	ev := mkInboundEvent(3, "tenant-1", "thread-C", "mid.C1", "62811112222", "msg")

	if ok := processMbsInbound(context.Background(), fs, nil, silentLogger(), ev); ok {
		t.Fatal("expected nak on AutoCreateContact error, got ack")
	}
}

// Missing tenant_id in event meta → ack (terminal drop, publisher bug).
func TestProcessMbsInbound_MissingTenant_Drops(t *testing.T) {
	fs := &fakeStore{}
	ev := mkInboundEvent(1, "", "thread-x", "mid.x", "62800", "x")

	if ok := processMbsInbound(context.Background(), fs, nil, silentLogger(), ev); !ok {
		t.Fatal("expected ack on missing tenant_id, got nak")
	}
	// Workspace lookup must not be attempted.
	if fs.convArgs.threadID != "" {
		t.Error("expected no DB writes on missing tenant_id")
	}
}

// Existing contact found by phone (no AutoCreate path).
func TestProcessMbsInbound_ExistingContact_NoAutoCreate(t *testing.T) {
	autoCreated := false
	fs := &fakeStore{
		getWorkspaceIDForMbsUid: func(_ context.Context, _ int64) (string, string, error) {
			return "ws-1", "tenant-1", nil
		},
		findContactByPhone: func(_ context.Context, phone string) (*handler.ContactRow, string, error) {
			if phone == "62811112222" {
				return &handler.ContactRow{ID: "existing-contact", Phone: phone, Name: "Sam"}, "tenant-1", nil
			}
			return nil, "", handler.ErrNotFound
		},
		autoCreateContact: func(_ context.Context, _, _, _ string) (*handler.ContactRow, error) {
			autoCreated = true
			return nil, errors.New("should not be called")
		},
	}
	ev := mkInboundEvent(8, "tenant-1", "thread-X", "mid.X1", "62811112222", "hi")

	if ok := processMbsInbound(context.Background(), fs, nil, silentLogger(), ev); !ok {
		t.Fatal("expected ack, got nak")
	}
	if autoCreated {
		t.Error("AutoCreateContact should not be called when contact exists")
	}
	if fs.convArgs.contactID != "existing-contact" {
		t.Errorf("conv contact_id: got %q, want %q", fs.convArgs.contactID, "existing-contact")
	}
}
