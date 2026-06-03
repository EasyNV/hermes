package websocket

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
)

// ─────────────────────────────────────────────────────────────────────
// Test scaffolding
// ─────────────────────────────────────────────────────────────────────

// recordedBroadcast captures one Broadcast call's args.
type recordedBroadcast struct {
	tenantID, workspaceID string
	data                  []byte
}

// recordedScoped captures one BroadcastConversationScoped call's args.
type recordedScoped struct {
	tenantID, workspaceID, assignedTo string
	data                              []byte
}

// recordingBroadcaster satisfies the Broadcaster interface and records
// every broadcast for assertion. Thread-safe so tests run cleanly
// under -race.
type recordingBroadcaster struct {
	mu         sync.Mutex
	broadcasts []recordedBroadcast
	users      []recordedBroadcast
	convos     []recordedBroadcast
	scoped     []recordedScoped
}

func (r *recordingBroadcaster) Broadcast(tenantID, workspaceID string, data []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.broadcasts = append(r.broadcasts, recordedBroadcast{tenantID, workspaceID, data})
}

func (r *recordingBroadcaster) BroadcastToUser(userID string, data []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.users = append(r.users, recordedBroadcast{userID, "", data})
}

func (r *recordingBroadcaster) BroadcastToConversation(conversationID string, data []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.convos = append(r.convos, recordedBroadcast{conversationID, "", data})
}

func (r *recordingBroadcaster) BroadcastConversationScoped(tenantID, workspaceID, assignedTo string, data []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scoped = append(r.scoped, recordedScoped{tenantID, workspaceID, assignedTo, data})
}

// snapshot returns a copy of recorded broadcasts so tests don't race
// the handler goroutine (when any).
func (r *recordingBroadcaster) snapshot() []recordedBroadcast {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]recordedBroadcast, len(r.broadcasts))
	copy(out, r.broadcasts)
	return out
}

// scopedSnapshot returns a copy of recorded conversation-scoped broadcasts.
func (r *recordingBroadcaster) scopedSnapshot() []recordedScoped {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]recordedScoped, len(r.scoped))
	copy(out, r.scoped)
	return out
}

// newTestSubscriber builds an EventSubscriber backed by a recording
// broadcaster. The js field is nil — handler unit tests never touch
// it (they call handlers directly, not through Start/Subscribe).
func newTestSubscriber(t *testing.T) (*EventSubscriber, *recordingBroadcaster) {
	t.Helper()
	rec := &recordingBroadcaster{}
	sub := &EventSubscriber{
		hub: rec,
		js:  nil,
		log: zerolog.Nop(),
	}
	return sub, rec
}

// natsMsg constructs an in-memory natsgo.Msg with the supplied subject
// and data. Note: msg.Ack() on a synthetic message is a no-op error —
// handlers ignore that error via `_ = msg.Ack()`.
func natsMsg(subject string, data []byte) *natsgo.Msg {
	return &natsgo.Msg{
		Subject: subject,
		Data:    data,
	}
}

// parseFrame decodes the WS envelope produced by marshalWSEvent.
func parseFrame(t *testing.T, data []byte) (frameType string, payload map[string]any) {
	t.Helper()
	var envelope wsMessage
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatalf("envelope unmarshal: %v", err)
	}
	if envelope.Payload != nil {
		if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
			t.Fatalf("payload unmarshal: %v", err)
		}
	}
	return envelope.Type, payload
}

// ─────────────────────────────────────────────────────────────────────
// Tests
// ─────────────────────────────────────────────────────────────────────

func TestMbsInbound_RawHandlerNoLongerBroadcasts(t *testing.T) {
	// GAP 3b: the raw hermes.mbs.message.inbound.* handler is neutralized.
	// The "mbs_new_message" frame is now emitted by handleInboxMessageNew
	// (enriched event carrying conversation ownership) so the fan-out can be
	// scoped per the conversation ownership model. The raw handler must drain
	// the stream (Ack) but emit zero broadcasts.
	sub, rec := newTestSubscriber(t)
	receivedAt := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	ev := &hermesv1.MbsInboundMessageEvent{
		Meta:          &hermesv1.EventMeta{TenantId: "tenant-A", Timestamp: timestamppb.Now()},
		Uid:           1674772559,
		PageId:        "page-123",
		WecMailboxId:  "mailbox-456",
		ThreadId:      "thread-789",
		Mid:           "mid.$cAAAA_test",
		SenderPhone:   "62812345",
		Text:          "hello world",
		MetaTimestamp: timestamppb.New(receivedAt),
	}
	data, err := proto.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	sub.handleMbsInboundMessage(natsMsg("hermes.mbs.message.inbound.tenant-A", data))

	if got := len(rec.snapshot()); got != 0 {
		t.Fatalf("raw mbs inbound handler must not broadcast, got %d", got)
	}
	if got := len(rec.scopedSnapshot()); got != 0 {
		t.Fatalf("raw mbs inbound handler must not scoped-broadcast, got %d", got)
	}
}

func TestInboxMessageNew_Mbs_BuildsScopedFrame(t *testing.T) {
	// handleInboxMessageNew is the sole emitter of "mbs_new_message". It scopes
	// the fan-out via BroadcastConversationScoped(tenant, workspace, assignedTo).
	sub, rec := newTestSubscriber(t)
	receivedAt := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	ev := &hermesv1.InboxMessageNewEvent{
		Meta:           &hermesv1.EventMeta{TenantId: "tenant-A", Timestamp: timestamppb.Now()},
		ConversationId: "conv-uuid-1",
		WorkspaceId:    "ws-1",
		AssignedTo:     "agent-7",
		Channel:        "mbs",
		ContactPhone:   "62812345",
		Body:           "hello world",
		MessageId:      "mid.$cAAAA_test",
		ReceivedAt:     timestamppb.New(receivedAt),
		Uid:            1674772559,
		ThreadId:       "thread-789",
	}
	data, err := proto.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	sub.handleInboxMessageNew(natsMsg("hermes.inbox.message.new.tenant-A", data))

	sc := rec.scopedSnapshot()
	if len(sc) != 1 {
		t.Fatalf("want 1 scoped broadcast, got %d", len(sc))
	}
	if sc[0].tenantID != "tenant-A" {
		t.Errorf("tenantID: got %q", sc[0].tenantID)
	}
	if sc[0].workspaceID != "ws-1" {
		t.Errorf("workspaceID: got %q want ws-1", sc[0].workspaceID)
	}
	if sc[0].assignedTo != "agent-7" {
		t.Errorf("assignedTo: got %q want agent-7", sc[0].assignedTo)
	}

	frameType, payload := parseFrame(t, sc[0].data)
	if frameType != "mbs_new_message" {
		t.Errorf("frame type: got %q want mbs_new_message", frameType)
	}
	if payload["uid"] != "1674772559" {
		t.Errorf("uid: got %v (want string '1674772559')", payload["uid"])
	}
	if payload["threadId"] != "thread-789" {
		t.Errorf("threadId: got %v", payload["threadId"])
	}
	if payload["mid"] != "mid.$cAAAA_test" {
		t.Errorf("mid: got %v", payload["mid"])
	}
	if payload["text"] != "hello world" {
		t.Errorf("text: got %v", payload["text"])
	}
	if !strings.HasPrefix(payload["receivedAt"].(string), "2026-05-29T12:00:00") {
		t.Errorf("receivedAt: got %v", payload["receivedAt"])
	}
}

func TestInboxMessageNew_Wa_BuildsScopedFrame(t *testing.T) {
	// WA channel emits the "new_message" frame, also scoped.
	sub, rec := newTestSubscriber(t)
	receivedAt := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	ev := &hermesv1.InboxMessageNewEvent{
		Meta:           &hermesv1.EventMeta{TenantId: "tenant-A", Timestamp: timestamppb.Now()},
		ConversationId: "conv-uuid-2",
		WorkspaceId:    "ws-2",
		AssignedTo:     "", // unassigned
		Channel:        "wa",
		ContactName:    "Jane",
		ContactPhone:   "628199",
		Body:           "hi there",
		MessageId:      "wamid.ABC",
		ReceivedAt:     timestamppb.New(receivedAt),
	}
	data, _ := proto.Marshal(ev)
	sub.handleInboxMessageNew(natsMsg("hermes.inbox.message.new.tenant-A", data))

	sc := rec.scopedSnapshot()
	if len(sc) != 1 {
		t.Fatalf("want 1 scoped broadcast, got %d", len(sc))
	}
	if sc[0].assignedTo != "" {
		t.Errorf("assignedTo: got %q want empty (unassigned)", sc[0].assignedTo)
	}
	frameType, payload := parseFrame(t, sc[0].data)
	if frameType != "new_message" {
		t.Errorf("frame type: got %q want new_message", frameType)
	}
	if payload["conversation_id"] != "conv-uuid-2" {
		t.Errorf("conversation_id: got %v", payload["conversation_id"])
	}
	if payload["wa_message_id"] != "wamid.ABC" {
		t.Errorf("wa_message_id: got %v", payload["wa_message_id"])
	}
	if payload["contact_name"] != "Jane" {
		t.Errorf("contact_name: got %v", payload["contact_name"])
	}
	if payload["body"] != "hi there" {
		t.Errorf("body: got %v", payload["body"])
	}
}

func TestMbsOutbound_BuildsFrame(t *testing.T) {
	sub, rec := newTestSubscriber(t)
	sentAt := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	ev := &hermesv1.MbsOutboundEvent{
		Meta:      &hermesv1.EventMeta{TenantId: "tenant-A"},
		Uid:       42,
		ThreadId:  "t-1",
		Mid:       "mid.outbound",
		Otid:      "otid-123",
		LatencyMs: 423,
		Ok:        true,
		Error:     "",
		SentAt:    timestamppb.New(sentAt),
	}
	data, _ := proto.Marshal(ev)
	sub.handleMbsOutboundStatus(natsMsg("hermes.mbs.message.outbound.tenant-A", data))

	br := rec.snapshot()
	if len(br) != 1 {
		t.Fatalf("want 1 broadcast, got %d", len(br))
	}
	if br[0].tenantID != "tenant-A" {
		t.Errorf("tenantID: got %q", br[0].tenantID)
	}

	frameType, payload := parseFrame(t, br[0].data)
	if frameType != "mbs_outbound_status" {
		t.Errorf("frame type: got %q", frameType)
	}
	if payload["uid"] != "42" {
		t.Errorf("uid: got %v", payload["uid"])
	}
	// JSON numbers come out as float64; assert that way.
	if got, _ := payload["latencyMs"].(float64); got != 423 {
		t.Errorf("latencyMs: got %v", payload["latencyMs"])
	}
	if got, _ := payload["ok"].(bool); !got {
		t.Errorf("ok: got %v", payload["ok"])
	}
	if payload["error"] != "" {
		t.Errorf("error: got %q", payload["error"])
	}
	if !strings.HasPrefix(payload["sentAt"].(string), "2026-05-29T12:00:00") {
		t.Errorf("sentAt: got %v", payload["sentAt"])
	}
}

func TestMbsOutbound_FailedSend_CarriesErrorField(t *testing.T) {
	sub, rec := newTestSubscriber(t)
	ev := &hermesv1.MbsOutboundEvent{
		Meta:      &hermesv1.EventMeta{TenantId: "tenant-A"},
		Uid:       42,
		Mid:       "mid.failed",
		Otid:      "otid-x",
		Ok:        false,
		Error:     "thread not found",
		LatencyMs: 1200,
	}
	data, _ := proto.Marshal(ev)
	sub.handleMbsOutboundStatus(natsMsg("hermes.mbs.message.outbound.tenant-A", data))

	br := rec.snapshot()
	_, payload := parseFrame(t, br[0].data)
	if payload["ok"].(bool) {
		t.Error("ok: want false")
	}
	if payload["error"] != "thread not found" {
		t.Errorf("error: got %q", payload["error"])
	}
}

func TestMbsLifecycle_BuildsFrame(t *testing.T) {
	sub, rec := newTestSubscriber(t)
	ev := &hermesv1.MbsSessionLifecycleEvent{
		Meta: &hermesv1.EventMeta{
			TenantId:  "tenant-A",
			Timestamp: timestamppb.New(time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)),
		},
		Uid:           42,
		PreviousState: hermesv1.MbsSessionState_MBS_SESSION_STATE_ACTIVE,
		NewState:      hermesv1.MbsSessionState_MBS_SESSION_STATE_BURNED,
		Reason:        "checkpoint_required",
		LastConnackRc: 4,
		PodId:         "hermes-mbs-0",
	}
	data, _ := proto.Marshal(ev)
	sub.handleMbsSessionLifecycle(natsMsg("hermes.mbs.session.burned.tenant-A", data))

	br := rec.snapshot()
	if len(br) != 1 {
		t.Fatalf("want 1 broadcast, got %d", len(br))
	}
	if br[0].tenantID != "tenant-A" {
		t.Errorf("tenantID: got %q", br[0].tenantID)
	}

	frameType, payload := parseFrame(t, br[0].data)
	if frameType != "mbs_session_lifecycle" {
		t.Errorf("frame type: got %q", frameType)
	}
	if payload["uid"] != "42" {
		t.Errorf("uid: got %v", payload["uid"])
	}
	if payload["previousState"] != "MBS_SESSION_STATE_ACTIVE" {
		t.Errorf("previousState: got %v", payload["previousState"])
	}
	if payload["newState"] != "MBS_SESSION_STATE_BURNED" {
		t.Errorf("newState: got %v", payload["newState"])
	}
	if payload["reason"] != "checkpoint_required" {
		t.Errorf("reason: got %v", payload["reason"])
	}
	if got, _ := payload["lastConnackRc"].(float64); got != 4 {
		t.Errorf("lastConnackRc: got %v", payload["lastConnackRc"])
	}
	if payload["podId"] != "hermes-mbs-0" {
		t.Errorf("podId: got %v", payload["podId"])
	}
}

func TestMbsHandlers_MalformedProto_AckAndDrop(t *testing.T) {
	// Each handler must NOT panic on garbage proto bytes and must NOT
	// emit a broadcast. Ack happens via the deferred `_ = msg.Ack()`
	// which is a no-op on synthetic test messages.
	cases := []struct {
		name string
		fn   func(*EventSubscriber, *natsgo.Msg)
	}{
		{"inbound", (*EventSubscriber).handleMbsInboundMessage},
		{"outbound", (*EventSubscriber).handleMbsOutboundStatus},
		{"lifecycle", (*EventSubscriber).handleMbsSessionLifecycle},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sub, rec := newTestSubscriber(t)
			c.fn(sub, natsMsg("hermes.mbs.fake."+c.name, []byte("garbage proto bytes")))
			if got := len(rec.snapshot()); got != 0 {
				t.Errorf("malformed proto should not broadcast, got %d", got)
			}
		})
	}
}

func TestMbsHandlers_TenantScoped_NoWorkspaceID(t *testing.T) {
	// The outbound + lifecycle MBS event families are tenant-wide (no workspace
	// dimension — MBS sessions belong to a tenant). Workspace ID must always be
	// empty in the broadcast call. NOTE: inbound is excluded here — it's
	// neutralized (GAP 3b); the inbound frame now flows through
	// handleInboxMessageNew with conversation scoping (see
	// TestInboxMessageNew_*_BuildsScopedFrame).
	sub, rec := newTestSubscriber(t)
	cases := []struct {
		name string
		ev   proto.Message
		fn   func(*EventSubscriber, *natsgo.Msg)
		subj string
	}{
		{"outbound", &hermesv1.MbsOutboundEvent{
			Meta: &hermesv1.EventMeta{TenantId: "tenant-X"}, Uid: 1,
		}, (*EventSubscriber).handleMbsOutboundStatus, "hermes.mbs.message.outbound.tenant-X"},
		{"lifecycle", &hermesv1.MbsSessionLifecycleEvent{
			Meta: &hermesv1.EventMeta{TenantId: "tenant-X"}, Uid: 1,
			NewState: hermesv1.MbsSessionState_MBS_SESSION_STATE_ACTIVE,
		}, (*EventSubscriber).handleMbsSessionLifecycle, "hermes.mbs.session.created.tenant-X"},
	}
	for _, c := range cases {
		data, _ := proto.Marshal(c.ev)
		c.fn(sub, natsMsg(c.subj, data))
	}
	br := rec.snapshot()
	if len(br) != 2 {
		t.Fatalf("want 2 broadcasts, got %d", len(br))
	}
	for i, b := range br {
		if b.tenantID != "tenant-X" {
			t.Errorf("[%d] tenantID: %q", i, b.tenantID)
		}
		if b.workspaceID != "" {
			t.Errorf("[%d] workspaceID should be empty: %q", i, b.workspaceID)
		}
	}
}

func TestInboxMessageNew_FallsBackToMetaTimestamp(t *testing.T) {
	// When received_at is nil, the frame's receivedAt falls back to the
	// EventMeta.timestamp. This documents the cascade (moved from the raw
	// inbound handler to the enriched-event handler).
	sub, rec := newTestSubscriber(t)
	metaTs := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ev := &hermesv1.InboxMessageNewEvent{
		Meta: &hermesv1.EventMeta{
			TenantId:  "tenant-A",
			Timestamp: timestamppb.New(metaTs),
		},
		ConversationId: "conv-1",
		Channel:        "mbs",
		Uid:            1,
		MessageId:      "mid.x",
		// ReceivedAt deliberately nil
	}
	data, _ := proto.Marshal(ev)
	sub.handleInboxMessageNew(natsMsg("hermes.inbox.message.new.tenant-A", data))

	sc := rec.scopedSnapshot()
	if len(sc) != 1 {
		t.Fatalf("want 1 scoped broadcast, got %d", len(sc))
	}
	_, payload := parseFrame(t, sc[0].data)
	if !strings.HasPrefix(payload["receivedAt"].(string), "2026-01-01T00:00:00") {
		t.Errorf("receivedAt did not fall back to meta.timestamp: %v", payload["receivedAt"])
	}
}

func TestProtoToISO(t *testing.T) {
	// Direct tests for the timestamp helper. Used by every handler.
	now := time.Date(2026, 6, 1, 9, 30, 0, 0, time.UTC)
	got := protoToISO(timestamppb.New(now), "fallback")
	if got != "2026-06-01T09:30:00Z" {
		t.Errorf("non-nil ts: got %q", got)
	}
	got = protoToISO(nil, "fallback")
	if got != "fallback" {
		t.Errorf("nil ts: got %q want fallback", got)
	}
}
