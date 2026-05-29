package handler

import (
	"context"
	"errors"
	"sync"
	"testing"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	natsgo "github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// ---------------------------------------------------------------------------
// recordingJS — captures publish calls without round-tripping NATS.
// Implements just enough of natsgo.JetStreamContext for tests to compile;
// only Publish is called in the test paths.
// ---------------------------------------------------------------------------

type recordedPublish struct {
	subject string
	data    []byte
	eventID string
}

type recordingJS struct {
	mu       sync.Mutex
	calls    []recordedPublish
	failNext bool
}

func (r *recordingJS) Publish(subj string, data []byte, opts ...natsgo.PubOpt) (*natsgo.PubAck, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failNext {
		r.failNext = false
		return nil, errors.New("simulated NATS publish failure")
	}
	// Best-effort extract MsgId from opts. Since natsgo.MsgId returns an
	// unexported impl, we just record the subject/data — tests assert on those.
	r.calls = append(r.calls, recordedPublish{subject: subj, data: data})
	return &natsgo.PubAck{}, nil
}

// Stub the rest of natsgo.JetStreamContext to nil-returns. None are called.
// We need to embed an interface to fill the rest without copying 30 method sigs.
type stubJS struct{ *recordingJS }

// Below: satisfy natsgo.JetStreamContext by composition. Recording handles Publish;
// the rest is filled by an unsatisfied nil that compiles but panics if called.
// Simpler path: shadow only what's needed using a tiny wrapper that exposes Publish.

// fakeJS implements just the surface our handler uses: Publish.
// We expose it via a type assertion in the handler — but the handler takes
// natsgo.JetStreamContext (a real interface). Easiest: wrap recordingJS in a
// thin shim with the full interface, embedding the real type for unused methods.

type fullJS struct {
	natsgo.JetStreamContext
	rec *recordingJS
}

func (f *fullJS) Publish(subj string, data []byte, opts ...natsgo.PubOpt) (*natsgo.PubAck, error) {
	return f.rec.Publish(subj, data, opts...)
}

// ---------------------------------------------------------------------------
// Test fixtures
// ---------------------------------------------------------------------------

func newTestHandlerMbs(t *testing.T, store Store) (*Handler, *recordingJS) {
	t.Helper()
	rec := &recordingJS{}
	js := &fullJS{rec: rec}
	h := New(store, js, zerolog.Nop())
	return h, rec
}

func mkMbsReq(convID, body string) *hermesv1.InboxSendMessageRequest {
	return &hermesv1.InboxSendMessageRequest{
		ConversationId: convID,
		SenderUserId:   "agent-1",
		ContentType:    hermesv1.ContentType_CONTENT_TYPE_TEXT,
		Body:           body,
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// Happy MBS path: publishes to hermes.mbs.send.manual.<tenant> with the right
// fields wired through.
func TestSendMessage_MbsChannel_PublishesToMbsManualSubject(t *testing.T) {
	ms := &mockStore{
		getConversationFn: func(_ context.Context, id string) (*ConversationRow, error) {
			return &ConversationRow{
				ID:            id,
				ContactID:     "contact-1",
				Channel:       "mbs",
				MbsSessionUID: "1674772559",
				MbsThreadID:   "thread-A",
				MbsPageID:     "page-A",
				Status:        "unassigned",
			}, nil
		},
		getWorkspaceIDForMbsUidFn: func(_ context.Context, uid int64) (string, string, error) {
			if uid != 1674772559 {
				t.Errorf("uid: got %d, want 1674772559", uid)
			}
			return "ws-1", "tenant-1", nil
		},
		createMbsMessageFn: func(_ context.Context, convID, direction, body, mid string) (*MessageRow, error) {
			if direction != "outbound" {
				t.Errorf("direction: got %q, want outbound", direction)
			}
			if mid != "" {
				t.Errorf("mid should be empty on outbound create; got %q", mid)
			}
			return &MessageRow{ID: "msg-uuid-1", ConversationID: convID, Direction: direction, MbsMID: ""}, nil
		},
	}

	h, rec := newTestHandlerMbs(t, ms)
	resp, err := h.SendMessage(context.Background(), mkMbsReq("conv-mbs-1", "hello"))
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if resp.Message.Id != "msg-uuid-1" {
		t.Errorf("response message id: got %q", resp.Message.Id)
	}
	if len(rec.calls) != 1 {
		t.Fatalf("expected 1 NATS publish, got %d", len(rec.calls))
	}
	call := rec.calls[0]
	wantSubject := "hermes.mbs.send.manual.tenant-1"
	if call.subject != wantSubject {
		t.Errorf("subject: got %q, want %q", call.subject, wantSubject)
	}
	var task hermesv1.MbsCampaignSendTask
	if err := proto.Unmarshal(call.data, &task); err != nil {
		t.Fatalf("unmarshal task: %v", err)
	}
	if task.CampaignId != "" {
		t.Errorf("campaign_id should be empty for manual sends; got %q", task.CampaignId)
	}
	if task.Uid != 1674772559 {
		t.Errorf("uid: got %d", task.Uid)
	}
	if task.ThreadId != "thread-A" {
		t.Errorf("thread_id: got %q", task.ThreadId)
	}
	if task.PageIdOverride != "page-A" {
		t.Errorf("page_id_override: got %q", task.PageIdOverride)
	}
	if task.ResolvedBody != "hello" {
		t.Errorf("resolved_body: got %q", task.ResolvedBody)
	}
	if task.IdempotencyKey != "msg-uuid-1" {
		t.Errorf("idempotency_key: got %q, want msg-uuid-1 (so outbound event can correlate)", task.IdempotencyKey)
	}
	if task.Meta.GetTenantId() != "tenant-1" {
		t.Errorf("meta.tenant_id: got %q", task.Meta.GetTenantId())
	}
}

// WA regression: default WA channel still publishes to hermes.wa.send.manual.<tenant>.
func TestSendMessage_WaChannel_RegressionStillUsesWaSubject(t *testing.T) {
	ms := &mockStore{
		getConversationFn: func(_ context.Context, id string) (*ConversationRow, error) {
			return &ConversationRow{
				ID:         id,
				ContactID:  "contact-1",
				WaNumberID: "wanum-1",
				Channel:    "wa",
				Status:     "unassigned",
			}, nil
		},
		getConversationWaNumberFn: func(_ context.Context, _ string) (*WaNumberRow, error) {
			return &WaNumberRow{ID: "wanum-1", TenantID: "tenant-1"}, nil
		},
		getConversationContactFn: func(_ context.Context, _ string) (*ContactRow, error) {
			return &ContactRow{ID: "contact-1", Phone: "+628000"}, nil
		},
		createMessageFn: func(_ context.Context, convID, direction, ct string, body, mediaURL *string, waID string) (*MessageRow, error) {
			return &MessageRow{ID: "msg-wa-1", ConversationID: convID, Direction: direction, ContentType: ct}, nil
		},
	}

	h, rec := newTestHandlerMbs(t, ms)
	req := &hermesv1.InboxSendMessageRequest{
		ConversationId: "conv-wa-1",
		SenderUserId:   "agent-1",
		ContentType:    hermesv1.ContentType_CONTENT_TYPE_TEXT,
		Body:           "hi",
	}
	if _, err := h.SendMessage(context.Background(), req); err != nil {
		t.Fatalf("WA SendMessage: %v", err)
	}
	if len(rec.calls) != 1 {
		t.Fatalf("expected 1 NATS publish, got %d", len(rec.calls))
	}
	if rec.calls[0].subject != "hermes.wa.send.manual.tenant-1" {
		t.Errorf("subject: got %q, want hermes.wa.send.manual.tenant-1", rec.calls[0].subject)
	}
}

// Empty channel string defaults to wa (CHECK constraint guarantees it's wa or mbs,
// COALESCE on read returns ""; handler must treat as wa).
func TestSendMessage_EmptyChannel_DefaultsToWa(t *testing.T) {
	ms := &mockStore{
		getConversationFn: func(_ context.Context, id string) (*ConversationRow, error) {
			return &ConversationRow{
				ID: id, ContactID: "c-1", WaNumberID: "wanum-1",
				Channel: "", // default
				Status:  "unassigned",
			}, nil
		},
		getConversationWaNumberFn: func(_ context.Context, _ string) (*WaNumberRow, error) {
			return &WaNumberRow{ID: "wanum-1", TenantID: "tenant-1"}, nil
		},
		getConversationContactFn: func(_ context.Context, _ string) (*ContactRow, error) {
			return &ContactRow{ID: "c-1", Phone: "+628"}, nil
		},
		createMessageFn: func(_ context.Context, convID, direction, ct string, _, _ *string, _ string) (*MessageRow, error) {
			return &MessageRow{ID: "m-1", ConversationID: convID, Direction: direction, ContentType: ct}, nil
		},
	}
	h, rec := newTestHandlerMbs(t, ms)
	if _, err := h.SendMessage(context.Background(), &hermesv1.InboxSendMessageRequest{
		ConversationId: "c-1",
		SenderUserId:   "agent",
		ContentType:    hermesv1.ContentType_CONTENT_TYPE_TEXT,
		Body:           "hi",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if len(rec.calls) == 0 || rec.calls[0].subject != "hermes.wa.send.manual.tenant-1" {
		t.Errorf("empty channel must route as WA; got calls=%+v", rec.calls)
	}
}

// Unknown channel → Internal error, no DB write.
func TestSendMessage_UnknownChannel_Internal(t *testing.T) {
	ms := &mockStore{
		getConversationFn: func(_ context.Context, _ string) (*ConversationRow, error) {
			return &ConversationRow{ID: "c", Channel: "weirdchan", Status: "unassigned"}, nil
		},
	}
	h, _ := newTestHandlerMbs(t, ms)
	_, err := h.SendMessage(context.Background(), &hermesv1.InboxSendMessageRequest{
		ConversationId: "c", SenderUserId: "u",
		ContentType: hermesv1.ContentType_CONTENT_TYPE_TEXT, Body: "x",
	})
	if status.Code(err) != codes.Internal {
		t.Errorf("expected Internal, got %v", err)
	}
}

// MBS conv with missing session → FailedPrecondition.
func TestSendMessage_MbsMissingSession_FailedPrecondition(t *testing.T) {
	ms := &mockStore{
		getConversationFn: func(_ context.Context, id string) (*ConversationRow, error) {
			return &ConversationRow{
				ID: id, ContactID: "c", Channel: "mbs",
				MbsSessionUID: "999", MbsThreadID: "thread-X",
				Status: "unassigned",
			}, nil
		},
		getWorkspaceIDForMbsUidFn: func(_ context.Context, _ int64) (string, string, error) {
			return "", "", ErrNotFound
		},
	}
	h, _ := newTestHandlerMbs(t, ms)
	_, err := h.SendMessage(context.Background(), mkMbsReq("c", "hello"))
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("expected FailedPrecondition, got %v", err)
	}
}

// MBS conv with non-numeric session_uid → Internal.
func TestSendMessage_MbsNonNumericUID_Internal(t *testing.T) {
	ms := &mockStore{
		getConversationFn: func(_ context.Context, id string) (*ConversationRow, error) {
			return &ConversationRow{
				ID: id, ContactID: "c", Channel: "mbs",
				MbsSessionUID: "not-a-number", MbsThreadID: "t",
				Status: "unassigned",
			}, nil
		},
	}
	h, _ := newTestHandlerMbs(t, ms)
	_, err := h.SendMessage(context.Background(), mkMbsReq("c", "hello"))
	if status.Code(err) != codes.Internal {
		t.Errorf("expected Internal, got %v", err)
	}
}

// MBS + non-text content type → Unimplemented, no DB write.
func TestSendMessage_MbsNonText_Unimplemented(t *testing.T) {
	ms := &mockStore{
		getConversationFn: func(_ context.Context, id string) (*ConversationRow, error) {
			return &ConversationRow{
				ID: id, ContactID: "c", Channel: "mbs",
				MbsSessionUID: "1", MbsThreadID: "t", Status: "unassigned",
			}, nil
		},
	}
	h, _ := newTestHandlerMbs(t, ms)
	req := &hermesv1.InboxSendMessageRequest{
		ConversationId: "c", SenderUserId: "u",
		ContentType: hermesv1.ContentType_CONTENT_TYPE_IMAGE,
		Body:        "ignored", MediaUrl: "http://x",
	}
	_, err := h.SendMessage(context.Background(), req)
	if status.Code(err) != codes.Unimplemented {
		t.Errorf("expected Unimplemented, got %v", err)
	}
}

// MBS + empty body → InvalidArgument.
func TestSendMessage_MbsEmptyBody_InvalidArgument(t *testing.T) {
	ms := &mockStore{
		getConversationFn: func(_ context.Context, id string) (*ConversationRow, error) {
			return &ConversationRow{
				ID: id, ContactID: "c", Channel: "mbs",
				MbsSessionUID: "1", MbsThreadID: "t", Status: "unassigned",
			}, nil
		},
	}
	h, _ := newTestHandlerMbs(t, ms)
	_, err := h.SendMessage(context.Background(), mkMbsReq("c", ""))
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", err)
	}
}

// Silence unused warnings on the embed helper.
var _ = stubJS{}
