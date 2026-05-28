package handler

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"mbs-native/auth"
	"mbs-native/client"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/hermes-waba/hermes/internal/mbs/session"
	"github.com/hermes-waba/hermes/internal/mbs/store"
	"github.com/hermes-waba/hermes/internal/mbs/store/mock"
	"github.com/hermes-waba/hermes/pkg/crypto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// ─────────────────────────────────────────────────────────────────────
// fakeManager — scriptable session.Manager for send tests
// ─────────────────────────────────────────────────────────────────────

type fakeManager struct {
	sendCalls       atomic.Int64
	disconnectCalls atomic.Int64
	lastUID         atomic.Int64
	lastThreadID    atomic.Int64
	lastText        atomic.Value // string

	// Scriptable outcomes.
	sendResult *client.SendResult
	sendErr    error
}

func (m *fakeManager) GetOrConnect(context.Context, int64) (*session.Connected, error) {
	return nil, errors.New("fakeManager: GetOrConnect not used by send path")
}
func (m *fakeManager) Disconnect(int64) error {
	m.disconnectCalls.Add(1)
	return nil
}
func (m *fakeManager) Subscribe(int64) (<-chan *session.InboundDelta, func()) {
	ch := make(chan *session.InboundDelta)
	close(ch)
	return ch, func() {}
}
func (m *fakeManager) Send(_ context.Context, uid, threadID int64, text string) (*client.SendResult, error) {
	m.sendCalls.Add(1)
	m.lastUID.Store(uid)
	m.lastThreadID.Store(threadID)
	m.lastText.Store(text)
	if m.sendErr != nil {
		return nil, m.sendErr
	}
	return m.sendResult, nil
}
func (m *fakeManager) Drain(context.Context) error    { return nil }
func (m *fakeManager) Shutdown(context.Context) error { return nil }

// ─────────────────────────────────────────────────────────────────────
// Test harness
// ─────────────────────────────────────────────────────────────────────

type sendFixture struct {
	h        *Handler
	st       *mock.Store
	pub      *recordingPublisher
	mgr      *fakeManager
	resolver *fakePhoneResolver
}

func newSendFixture(t *testing.T, mgr *fakeManager, resolver *fakePhoneResolver) sendFixture {
	t.Helper()
	st := mock.NewStore()
	pub := &recordingPublisher{}
	dek := newTestDEK(t)
	factory := PhoneResolverFactory(func(*auth.Creds) (PhoneResolver, error) {
		return resolver, nil
	})
	h, err := NewHandler(Options{
		Store:           st,
		Manager:         mgr,
		Publisher:       pub,
		DriverFactory:   DriverFactory(func(DriverOptions) Driver { return nil }),
		ResolverFactory: factory,
		DEK:             dek,
		PodID:           "pod-test",
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return sendFixture{h: h, st: st, pub: pub, mgr: mgr, resolver: resolver}
}

func okResult(mid string) *client.SendResult {
	return &client.SendResult{MID: mid, OTID: "otid-1", DurationMs: 42}
}

func threadRecipient(s string) *hermesv1.MbsSendMessageRequest_ThreadId {
	return &hermesv1.MbsSendMessageRequest_ThreadId{ThreadId: s}
}
func phoneRecipient(s string) *hermesv1.MbsSendMessageRequest_Phone {
	return &hermesv1.MbsSendMessageRequest_Phone{Phone: s}
}

// ─────────────────────────────────────────────────────────────────────
// Happy paths
// ─────────────────────────────────────────────────────────────────────

func TestSend_ThreadID_HappyPath(t *testing.T) {
	mgr := &fakeManager{sendResult: okResult("mid.$A")}
	fx := newSendFixture(t, mgr, &fakePhoneResolver{})
	seedEncryptedSession(t, fx.st, fx.h.dek, 100, "tenant-A")

	resp, err := fx.h.SendMessage(ctxWith("tenant-A"), &hermesv1.MbsSendMessageRequest{
		Uid:       100,
		Recipient: threadRecipient("987654321012345"),
		Text:      "hello",
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if resp.Mid != "mid.$A" || resp.ThreadId != "987654321012345" {
		t.Errorf("response: %+v", resp)
	}
	if mgr.sendCalls.Load() != 1 {
		t.Errorf("manager.Send calls: got %d want 1", mgr.sendCalls.Load())
	}
	if mgr.lastThreadID.Load() != 987654321012345 {
		t.Errorf("threadID propagation: got %d want 987654321012345", mgr.lastThreadID.Load())
	}
	if got, _ := mgr.lastText.Load().(string); got != "hello" {
		t.Errorf("text: got %q want hello", got)
	}

	// Outbound event published with ok=true.
	if len(fx.pub.outbound) != 1 {
		t.Fatalf("outbound events: got %d want 1", len(fx.pub.outbound))
	}
	ev := fx.pub.outbound[0]
	if !ev.ok || ev.mid != "mid.$A" || ev.tenantID != "tenant-A" {
		t.Errorf("outbound event: %+v", ev)
	}
}

func TestSend_Phone_CacheHit(t *testing.T) {
	mgr := &fakeManager{sendResult: okResult("mid.$B")}
	resolver := &fakePhoneResolver{thread: "should-not-see"}
	fx := newSendFixture(t, mgr, resolver)
	seedEncryptedSession(t, fx.st, fx.h.dek, 100, "tenant-A")

	// Pre-seed cache.
	_ = fx.st.UpsertPhoneThread(context.Background(), &store.PhoneThreadRow{
		UID: 100, PageID: "page-PRIMARY", Phone: "6281234567890",
		ThreadID: "111222333444555", WecMailboxID: "mbox-1",
	})

	resp, err := fx.h.SendMessage(ctxWith("tenant-A"), &hermesv1.MbsSendMessageRequest{
		Uid:       100,
		Recipient: phoneRecipient("0812-3456-7890"),
		Text:      "from-phone",
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if resp.ThreadId != "111222333444555" {
		t.Errorf("thread_id from cache: got %q", resp.ThreadId)
	}
	if resolver.mu.Load() != 0 {
		t.Errorf("cache hit must skip live resolve, calls=%d", resolver.mu.Load())
	}
	if mgr.lastThreadID.Load() != 111222333444555 {
		t.Errorf("manager saw threadID=%d want cached 111222333444555", mgr.lastThreadID.Load())
	}
}

func TestSend_Phone_CacheMiss_LiveResolveAndSend(t *testing.T) {
	mgr := &fakeManager{sendResult: okResult("mid.$C")}
	resolver := &fakePhoneResolver{thread: "888777666555444", mailbox: "mbox-live"}
	fx := newSendFixture(t, mgr, resolver)
	seedEncryptedSession(t, fx.st, fx.h.dek, 100, "tenant-A")

	resp, err := fx.h.SendMessage(ctxWith("tenant-A"), &hermesv1.MbsSendMessageRequest{
		Uid:       100,
		Recipient: phoneRecipient("+62 812-3456-7890"),
		Text:      "live-resolved",
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if resp.ThreadId != "888777666555444" {
		t.Errorf("thread_id: got %q want 888777666555444", resp.ThreadId)
	}
	if resolver.mu.Load() != 1 {
		t.Errorf("live resolve should fire once, got %d", resolver.mu.Load())
	}
	// Cache should be written back.
	cached, err := fx.st.GetPhoneThread(context.Background(), 100, "page-PRIMARY", "6281234567890")
	if err != nil || cached.ThreadID != "888777666555444" {
		t.Errorf("cache writeback: row=%+v err=%v", cached, err)
	}
}

// ─────────────────────────────────────────────────────────────────────
// Dedupe
// ─────────────────────────────────────────────────────────────────────

func TestSend_DedupeHit_ShortCircuits(t *testing.T) {
	mgr := &fakeManager{sendResult: okResult("mid.$first")}
	fx := newSendFixture(t, mgr, &fakePhoneResolver{})
	seedEncryptedSession(t, fx.st, fx.h.dek, 100, "tenant-A")

	dedupeID := []byte("idempotent-key-1")
	req := &hermesv1.MbsSendMessageRequest{
		Uid:            100,
		Recipient:      threadRecipient("987654321012345"),
		Text:           "first",
		ClientDedupeId: dedupeID,
	}

	first, err := fx.h.SendMessage(ctxWith("tenant-A"), req)
	if err != nil {
		t.Fatalf("first send: %v", err)
	}
	if mgr.sendCalls.Load() != 1 {
		t.Fatalf("first send should hit manager, got %d", mgr.sendCalls.Load())
	}

	// Second call with same dedupe id → cache hit, NO manager call.
	// Use proto.Clone to avoid copying the protoimpl.MessageState lock.
	req2 := proto.Clone(req).(*hermesv1.MbsSendMessageRequest)
	req2.Text = "second-should-be-ignored"
	second, err := fx.h.SendMessage(ctxWith("tenant-A"), req2)
	if err != nil {
		t.Fatalf("second send: %v", err)
	}
	if mgr.sendCalls.Load() != 1 {
		t.Errorf("dedupe MUST short-circuit, manager calls=%d want 1", mgr.sendCalls.Load())
	}
	if second.Mid != first.Mid {
		t.Errorf("dedupe returned different response: %q vs %q", second.Mid, first.Mid)
	}
	// Outbound published only ONCE (from the actual send, not dedupe hit).
	if len(fx.pub.outbound) != 1 {
		t.Errorf("outbound events: got %d want 1 (no publish on dedupe hit)", len(fx.pub.outbound))
	}
}

func TestSend_EmptyDedupeKey_DoesNotCache(t *testing.T) {
	mgr := &fakeManager{sendResult: okResult("mid.$X")}
	fx := newSendFixture(t, mgr, &fakePhoneResolver{})
	seedEncryptedSession(t, fx.st, fx.h.dek, 100, "tenant-A")

	req := &hermesv1.MbsSendMessageRequest{
		Uid:       100,
		Recipient: threadRecipient("987654321012345"),
		Text:      "no-dedupe",
		// ClientDedupeId not set
	}
	_, _ = fx.h.SendMessage(ctxWith("tenant-A"), req)
	_, _ = fx.h.SendMessage(ctxWith("tenant-A"), req)
	if mgr.sendCalls.Load() != 2 {
		t.Errorf("empty dedupe should NOT cache, calls=%d want 2", mgr.sendCalls.Load())
	}
}

// ─────────────────────────────────────────────────────────────────────
// Validation / error paths
// ─────────────────────────────────────────────────────────────────────

func TestSend_MissingTenant(t *testing.T) {
	fx := newSendFixture(t, &fakeManager{}, &fakePhoneResolver{})
	_, err := fx.h.SendMessage(context.Background(),
		&hermesv1.MbsSendMessageRequest{Uid: 100, Recipient: threadRecipient("1"), Text: "x"})
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("expected Unauthenticated, got %v", err)
	}
}

func TestSend_MissingUID(t *testing.T) {
	fx := newSendFixture(t, &fakeManager{}, &fakePhoneResolver{})
	_, err := fx.h.SendMessage(ctxWith("tenant-A"),
		&hermesv1.MbsSendMessageRequest{Recipient: threadRecipient("1"), Text: "x"})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", err)
	}
}

func TestSend_MissingText(t *testing.T) {
	fx := newSendFixture(t, &fakeManager{}, &fakePhoneResolver{})
	_, err := fx.h.SendMessage(ctxWith("tenant-A"),
		&hermesv1.MbsSendMessageRequest{Uid: 100, Recipient: threadRecipient("1")})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", err)
	}
}

func TestSend_MissingRecipient(t *testing.T) {
	fx := newSendFixture(t, &fakeManager{}, &fakePhoneResolver{})
	seedEncryptedSession(t, fx.st, fx.h.dek, 100, "tenant-A")

	_, err := fx.h.SendMessage(ctxWith("tenant-A"),
		&hermesv1.MbsSendMessageRequest{Uid: 100, Text: "x"})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", err)
	}
}

func TestSend_TenantMismatch(t *testing.T) {
	mgr := &fakeManager{sendResult: okResult("mid.$X")}
	fx := newSendFixture(t, mgr, &fakePhoneResolver{})
	seedEncryptedSession(t, fx.st, fx.h.dek, 100, "tenant-A")

	_, err := fx.h.SendMessage(ctxWith("tenant-B"), &hermesv1.MbsSendMessageRequest{
		Uid: 100, Recipient: threadRecipient("123"), Text: "x",
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Errorf("expected PermissionDenied, got %v", err)
	}
	if mgr.sendCalls.Load() != 0 {
		t.Errorf("tenant mismatch must NOT invoke manager")
	}
	if len(fx.pub.outbound) != 0 {
		t.Errorf("no outbound event expected on tenant mismatch")
	}
}

func TestSend_NonNumericThreadID(t *testing.T) {
	fx := newSendFixture(t, &fakeManager{sendResult: okResult("x")}, &fakePhoneResolver{})
	seedEncryptedSession(t, fx.st, fx.h.dek, 100, "tenant-A")

	_, err := fx.h.SendMessage(ctxWith("tenant-A"), &hermesv1.MbsSendMessageRequest{
		Uid: 100, Recipient: threadRecipient("not-a-number"), Text: "x",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument on non-numeric thread_id, got %v", err)
	}
}

func TestSend_EmptyThreadID(t *testing.T) {
	fx := newSendFixture(t, &fakeManager{sendResult: okResult("x")}, &fakePhoneResolver{})
	seedEncryptedSession(t, fx.st, fx.h.dek, 100, "tenant-A")

	_, err := fx.h.SendMessage(ctxWith("tenant-A"), &hermesv1.MbsSendMessageRequest{
		Uid: 100, Recipient: threadRecipient(""), Text: "x",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument on empty thread_id, got %v", err)
	}
}

func TestSend_PhoneInvalid(t *testing.T) {
	fx := newSendFixture(t, &fakeManager{sendResult: okResult("x")}, &fakePhoneResolver{})
	seedEncryptedSession(t, fx.st, fx.h.dek, 100, "tenant-A")

	_, err := fx.h.SendMessage(ctxWith("tenant-A"), &hermesv1.MbsSendMessageRequest{
		Uid: 100, Recipient: phoneRecipient("abc"), Text: "x",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────
// Send-layer failures
// ─────────────────────────────────────────────────────────────────────

func TestSend_ManagerSendErr_PublishesOutboundFalse(t *testing.T) {
	mgr := &fakeManager{sendErr: errors.New("network: connection reset")}
	fx := newSendFixture(t, mgr, &fakePhoneResolver{})
	seedEncryptedSession(t, fx.st, fx.h.dek, 100, "tenant-A")

	_, err := fx.h.SendMessage(ctxWith("tenant-A"), &hermesv1.MbsSendMessageRequest{
		Uid: 100, Recipient: threadRecipient("123"), Text: "boom",
	})
	if status.Code(err) != codes.Internal {
		t.Errorf("expected Internal for plain network err, got %v", err)
	}
	if len(fx.pub.outbound) != 1 {
		t.Fatalf("outbound event MUST publish on failure, got %d", len(fx.pub.outbound))
	}
	ev := fx.pub.outbound[0]
	if ev.ok {
		t.Errorf("outbound.ok should be false on send failure")
	}
	if ev.errMsg == "" {
		t.Errorf("outbound.errMsg should carry the error text")
	}
}

func TestSend_ClaimConflict_FailedPrecondition(t *testing.T) {
	mgr := &fakeManager{sendErr: &session.ErrClaimConflict{UID: 100, OwnerPodID: "pod-99"}}
	fx := newSendFixture(t, mgr, &fakePhoneResolver{})
	seedEncryptedSession(t, fx.st, fx.h.dek, 100, "tenant-A")

	_, err := fx.h.SendMessage(ctxWith("tenant-A"), &hermesv1.MbsSendMessageRequest{
		Uid: 100, Recipient: threadRecipient("123"), Text: "x",
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("ClaimConflict should map to FailedPrecondition, got %v", err)
	}
	// Outbound still publishes the failure.
	if len(fx.pub.outbound) != 1 || fx.pub.outbound[0].ok {
		t.Errorf("outbound publish on claim conflict: %+v", fx.pub.outbound)
	}
}

func TestSend_DrainedDuringSend_Unavailable(t *testing.T) {
	mgr := &fakeManager{sendErr: session.ErrDrained}
	fx := newSendFixture(t, mgr, &fakePhoneResolver{})
	seedEncryptedSession(t, fx.st, fx.h.dek, 100, "tenant-A")

	_, err := fx.h.SendMessage(ctxWith("tenant-A"), &hermesv1.MbsSendMessageRequest{
		Uid: 100, Recipient: threadRecipient("123"), Text: "x",
	})
	if status.Code(err) != codes.Unavailable {
		t.Errorf("ErrDrained should map to Unavailable, got %v", err)
	}
}

func TestSend_DecryptFailDuringPhoneResolve_Unauthenticated(t *testing.T) {
	mgr := &fakeManager{sendResult: okResult("x")}
	fx := newSendFixture(t, mgr, &fakePhoneResolver{thread: "888"})

	// Seed with a DIFFERENT DEK so the inline phone resolve hits a
	// decrypt failure during cache miss.
	wrongDEK := newTestDEK(t)
	seedEncryptedSession(t, fx.st, wrongDEK, 100, "tenant-A")

	_, err := fx.h.SendMessage(ctxWith("tenant-A"), &hermesv1.MbsSendMessageRequest{
		Uid: 100, Recipient: phoneRecipient("6281234567890"), Text: "x",
	})
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("decrypt fail during phone resolve should be Unauthenticated, got %v", err)
	}
	if mgr.sendCalls.Load() != 0 {
		t.Errorf("decrypt fail must NOT reach manager.Send")
	}
}

func TestSend_NilSendResult_Internal(t *testing.T) {
	// Manager returns (nil, nil) — invariant violation. Handler must
	// surface this as Internal (not panic).
	mgr := &fakeManager{} // both sendResult and sendErr nil
	fx := newSendFixture(t, mgr, &fakePhoneResolver{})
	seedEncryptedSession(t, fx.st, fx.h.dek, 100, "tenant-A")

	_, err := fx.h.SendMessage(ctxWith("tenant-A"), &hermesv1.MbsSendMessageRequest{
		Uid: 100, Recipient: threadRecipient("123"), Text: "x",
	})
	if status.Code(err) != codes.Internal {
		t.Errorf("nil-result-no-err should map to Internal, got %v", err)
	}
	if len(fx.pub.outbound) != 1 || fx.pub.outbound[0].ok {
		t.Errorf("nil-result still publishes outbound(ok=false): %+v", fx.pub.outbound)
	}
}

// ─────────────────────────────────────────────────────────────────────
// Send response shape
// ─────────────────────────────────────────────────────────────────────

func TestSend_ResponseEchoesRecipient(t *testing.T) {
	// Confirms thread_id field on response carries the resolved form,
	// not the input — for phone-recipient sends, this matters.
	mgr := &fakeManager{sendResult: okResult("mid.$Z")}
	resolver := &fakePhoneResolver{thread: "999888777666555", mailbox: "m"}
	fx := newSendFixture(t, mgr, resolver)
	seedEncryptedSession(t, fx.st, fx.h.dek, 100, "tenant-A")

	resp, _ := fx.h.SendMessage(ctxWith("tenant-A"), &hermesv1.MbsSendMessageRequest{
		Uid: 100, Recipient: phoneRecipient("6281234567890"), Text: "x",
	})
	if resp.ThreadId != "999888777666555" {
		t.Errorf("resp.thread_id should echo resolved value, got %q", resp.ThreadId)
	}
	if resp.SentAt == nil {
		t.Error("resp.sent_at should be set")
	}
	if resp.LatencyMs < 0 {
		t.Errorf("latency_ms negative: %d", resp.LatencyMs)
	}
}

// Sanity: ensure errorsIsAny + sessionSentinelErrs wire-up works.
func TestSessionClassifyHelpers(t *testing.T) {
	if !errorsIsAny(session.ErrShutdown, sessionSentinelErrs()...) {
		t.Error("ErrShutdown should match")
	}
	wrappedDecrypt := errors.New("wrapper") // Plain error — should NOT match
	if errorsIsAny(wrappedDecrypt, sessionSentinelErrs()...) {
		t.Error("plain error should not match sentinels")
	}
	if !errorsIsAny(crypto.ErrDecryptFailed, sessionSentinelErrs()...) {
		t.Error("crypto.ErrDecryptFailed should match")
	}
}
