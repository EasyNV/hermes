package handler

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mbs-native/client"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/hermes-waba/hermes/internal/mbs/session"
	"github.com/hermes-waba/hermes/internal/mbs/store/mock"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ─────────────────────────────────────────────────────────────────────
// fakeListenStream — satisfies grpc.ServerStreamingServer[MbsInboundMessage]
// ─────────────────────────────────────────────────────────────────────

type fakeListenStream struct {
	grpc.ServerStream // unused embedding

	ctx context.Context

	mu      sync.Mutex
	sent    []*hermesv1.MbsInboundMessage
	sendErr error
}

func newFakeListenStream(ctx context.Context) *fakeListenStream {
	return &fakeListenStream{ctx: ctx}
}

func (s *fakeListenStream) Context() context.Context { return s.ctx }
func (s *fakeListenStream) Send(msg *hermesv1.MbsInboundMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sendErr != nil {
		return s.sendErr
	}
	s.sent = append(s.sent, msg)
	return nil
}
func (s *fakeListenStream) SetHeader(metadata.MD) error  { return nil }
func (s *fakeListenStream) SendHeader(metadata.MD) error { return nil }
func (s *fakeListenStream) SetTrailer(metadata.MD)       {}
func (s *fakeListenStream) RecvMsg(any) error            { return nil }
func (s *fakeListenStream) SendMsg(any) error            { return nil }

func (s *fakeListenStream) sentCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sent)
}
func (s *fakeListenStream) snapshot() []*hermesv1.MbsInboundMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]*hermesv1.MbsInboundMessage, len(s.sent))
	copy(cp, s.sent)
	return cp
}
func (s *fakeListenStream) setSendErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sendErr = err
}

// ─────────────────────────────────────────────────────────────────────
// listenManager — session.Manager with a controllable Subscribe channel
// ─────────────────────────────────────────────────────────────────────

type listenManager struct {
	getOrConnectCalls atomic.Int64
	subscribeCalls    atomic.Int64
	unsubCalls        atomic.Int64

	deltaCh chan *session.InboundDelta

	getOrConnectErr error
}

func newListenManager() *listenManager {
	return &listenManager{
		deltaCh: make(chan *session.InboundDelta, 16),
	}
}

func (m *listenManager) GetOrConnect(context.Context, int64) (*session.Connected, error) {
	m.getOrConnectCalls.Add(1)
	if m.getOrConnectErr != nil {
		return nil, m.getOrConnectErr
	}
	return &session.Connected{UID: 100}, nil
}
func (m *listenManager) Disconnect(int64) error { return nil }
func (m *listenManager) Subscribe(int64) (<-chan *session.InboundDelta, func()) {
	m.subscribeCalls.Add(1)
	return m.deltaCh, func() { m.unsubCalls.Add(1) }
}
func (m *listenManager) Send(context.Context, int64, int64, string) (*client.SendResult, error) {
	return nil, errors.New("listenManager: Send not used")
}
func (m *listenManager) Drain(context.Context) error    { return nil }
func (m *listenManager) Shutdown(context.Context) error { return nil }

// ─────────────────────────────────────────────────────────────────────
// Test harness
// ─────────────────────────────────────────────────────────────────────

func newListenHandler(t *testing.T, mgr *listenManager) (*Handler, *mock.Store) {
	t.Helper()
	st := mock.NewStore()
	pub := &recordingPublisher{}
	dek := newTestDEK(t)
	h, err := NewHandler(Options{
		Store:         st,
		Manager:       mgr,
		Publisher:     pub,
		DriverFactory: DriverFactory(func(DriverOptions) Driver { return nil }),
		DEK:           dek,
		PodID:         "pod-test",
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return h, st
}

// runListen invokes h.Listen on a background goroutine; returns the
// stream + a cancel func for the ctx + a done channel that closes
// when Listen returns. The returned error from Listen is stored in
// the done's payload (we send it on the channel).
func runListen(t *testing.T, h *Handler, req *hermesv1.MbsListenRequest, tenant string) (*fakeListenStream, context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(ctxWith(tenant))
	stream := newFakeListenStream(ctx)
	done := make(chan error, 1)
	go func() {
		done <- h.Listen(req, stream)
	}()
	return stream, cancel, done
}

// waitForCount busy-waits (with timeout) until stream.sentCount() ≥ n.
// Returns false if timeout fires first.
func waitForCount(s *fakeListenStream, n int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if s.sentCount() >= n {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

func mkDelta(uid int64, mid, text string) *session.InboundDelta {
	return &session.InboundDelta{
		UID:        uid,
		TenantID:   "tenant-A",
		ThreadID:   "thread-1",
		MID:        mid,
		Text:       text,
		ReceivedAt: time.Now(),
	}
}

// ─────────────────────────────────────────────────────────────────────
// Tests
// ─────────────────────────────────────────────────────────────────────

func TestListen_HappyPath_StreamsDeltas(t *testing.T) {
	mgr := newListenManager()
	h, st := newListenHandler(t, mgr)
	seedActiveSession(t, st, 100, "tenant-A")

	stream, cancel, done := runListen(t, h, &hermesv1.MbsListenRequest{Uid: 100}, "tenant-A")
	defer cancel()

	// Push 3 deltas.
	mgr.deltaCh <- mkDelta(100, "mid.$A", "first")
	mgr.deltaCh <- mkDelta(100, "mid.$B", "second")
	mgr.deltaCh <- mkDelta(100, "mid.$C", "third")

	if !waitForCount(stream, 3, time.Second) {
		t.Fatalf("expected 3 streamed messages, got %d", stream.sentCount())
	}

	got := stream.snapshot()
	wantMIDs := []string{"mid.$A", "mid.$B", "mid.$C"}
	for i, want := range wantMIDs {
		if got[i].Mid != want {
			t.Errorf("delta %d: got mid=%q want %q", i, got[i].Mid, want)
		}
		if got[i].ThreadId != "thread-1" {
			t.Errorf("delta %d: thread_id=%q", i, got[i].ThreadId)
		}
		if got[i].ReceivedAt == nil {
			t.Errorf("delta %d: received_at unset", i)
		}
	}

	// Side effects: GetOrConnect + Subscribe each called once.
	if mgr.getOrConnectCalls.Load() != 1 {
		t.Errorf("GetOrConnect: got %d want 1", mgr.getOrConnectCalls.Load())
	}
	if mgr.subscribeCalls.Load() != 1 {
		t.Errorf("Subscribe: got %d want 1", mgr.subscribeCalls.Load())
	}

	// Cancel + verify Listen returns ctx.Canceled + unsub called.
	cancel()
	select {
	case err := <-done:
		if status.Code(err) != codes.Canceled && !errors.Is(err, context.Canceled) {
			t.Errorf("expected ctx.Canceled, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Listen did not return after cancel")
	}
	if mgr.unsubCalls.Load() != 1 {
		t.Errorf("unsub: got %d want 1 (defer should fire)", mgr.unsubCalls.Load())
	}
}

func TestListen_SinceFiltersOldDeltas(t *testing.T) {
	mgr := newListenManager()
	h, st := newListenHandler(t, mgr)
	seedActiveSession(t, st, 100, "tenant-A")

	cutoff := time.Now()
	stream, cancel, done := runListen(t, h, &hermesv1.MbsListenRequest{
		Uid:   100,
		Since: timestamppb.New(cutoff),
	}, "tenant-A")
	defer cancel()

	// Push 2 OLD deltas (before cutoff) + 1 NEW (after).
	oldDelta := &session.InboundDelta{
		UID: 100, TenantID: "tenant-A",
		ThreadID: "t", MID: "mid.OLD-1", Text: "old1",
		ReceivedAt: cutoff.Add(-time.Hour),
	}
	oldDelta2 := *oldDelta
	oldDelta2.MID = "mid.OLD-2"
	newDelta := *oldDelta
	newDelta.MID = "mid.NEW-1"
	newDelta.ReceivedAt = cutoff.Add(time.Second)

	mgr.deltaCh <- oldDelta
	mgr.deltaCh <- &oldDelta2
	mgr.deltaCh <- &newDelta

	if !waitForCount(stream, 1, time.Second) {
		t.Fatalf("expected 1 streamed message (only new), got %d", stream.sentCount())
	}
	// Brief pause to confirm old ones don't sneak in later.
	time.Sleep(50 * time.Millisecond)
	if c := stream.sentCount(); c != 1 {
		t.Errorf("since filter leaked: got %d streamed (want 1)", c)
	}
	if got := stream.snapshot()[0].Mid; got != "mid.NEW-1" {
		t.Errorf("got streamed mid=%q want mid.NEW-1", got)
	}

	cancel()
	<-done
}

func TestListen_ClientCancel_ReturnsCanceled(t *testing.T) {
	mgr := newListenManager()
	h, st := newListenHandler(t, mgr)
	seedActiveSession(t, st, 100, "tenant-A")

	_, cancel, done := runListen(t, h, &hermesv1.MbsListenRequest{Uid: 100}, "tenant-A")

	// Cancel immediately, before any delta.
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) && status.Code(err) != codes.Canceled {
			t.Errorf("expected ctx.Canceled or Canceled code, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Listen did not return after immediate cancel")
	}
}

func TestListen_SubscriptionClosed_Unavailable(t *testing.T) {
	mgr := newListenManager()
	h, st := newListenHandler(t, mgr)
	seedActiveSession(t, st, 100, "tenant-A")

	_, cancel, done := runListen(t, h, &hermesv1.MbsListenRequest{Uid: 100}, "tenant-A")
	defer cancel()

	// Server-side disconnect (manager.Disconnect closes the channel).
	mgr.closeDelta()
	select {
	case err := <-done:
		if status.Code(err) != codes.Unavailable {
			t.Errorf("closed subscription should map to Unavailable, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Listen did not return after subscription close")
	}
}

func (m *listenManager) closeDelta() { close(m.deltaCh) }

func TestListen_MissingTenant(t *testing.T) {
	mgr := newListenManager()
	h, _ := newListenHandler(t, mgr)

	stream := newFakeListenStream(context.Background())
	err := h.Listen(&hermesv1.MbsListenRequest{Uid: 100}, stream)
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("expected Unauthenticated, got %v", err)
	}
	if mgr.getOrConnectCalls.Load() != 0 {
		t.Errorf("missing tenant must not call GetOrConnect")
	}
}

func TestListen_MissingUID(t *testing.T) {
	mgr := newListenManager()
	h, _ := newListenHandler(t, mgr)

	stream := newFakeListenStream(ctxWith("tenant-A"))
	err := h.Listen(&hermesv1.MbsListenRequest{}, stream)
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", err)
	}
}

func TestListen_TenantMismatch(t *testing.T) {
	mgr := newListenManager()
	h, st := newListenHandler(t, mgr)
	seedActiveSession(t, st, 100, "tenant-A")

	stream := newFakeListenStream(ctxWith("tenant-B"))
	err := h.Listen(&hermesv1.MbsListenRequest{Uid: 100}, stream)
	if status.Code(err) != codes.PermissionDenied {
		t.Errorf("expected PermissionDenied, got %v", err)
	}
	if mgr.getOrConnectCalls.Load() != 0 {
		t.Errorf("tenant mismatch must NOT reach GetOrConnect")
	}
	if mgr.subscribeCalls.Load() != 0 {
		t.Errorf("tenant mismatch must NOT subscribe")
	}
}

func TestListen_NotFound(t *testing.T) {
	mgr := newListenManager()
	h, _ := newListenHandler(t, mgr)

	stream := newFakeListenStream(ctxWith("tenant-A"))
	err := h.Listen(&hermesv1.MbsListenRequest{Uid: 999}, stream)
	if status.Code(err) != codes.NotFound {
		t.Errorf("expected NotFound, got %v", err)
	}
}

func TestListen_GetOrConnectFails(t *testing.T) {
	mgr := newListenManager()
	mgr.getOrConnectErr = session.ErrDrained
	h, st := newListenHandler(t, mgr)
	seedActiveSession(t, st, 100, "tenant-A")

	stream := newFakeListenStream(ctxWith("tenant-A"))
	err := h.Listen(&hermesv1.MbsListenRequest{Uid: 100}, stream)
	if status.Code(err) != codes.Unavailable {
		t.Errorf("ErrDrained should map to Unavailable, got %v", err)
	}
	if mgr.subscribeCalls.Load() != 0 {
		t.Errorf("failed GetOrConnect must NOT subscribe")
	}
}

func TestListen_StreamSendFailure_ReturnsErr(t *testing.T) {
	// Simulates client-side disconnect mid-stream — stream.Send returns
	// an error, Listen should propagate it (so the deferred unsub fires).
	mgr := newListenManager()
	h, st := newListenHandler(t, mgr)
	seedActiveSession(t, st, 100, "tenant-A")

	stream, cancel, done := runListen(t, h, &hermesv1.MbsListenRequest{Uid: 100}, "tenant-A")
	defer cancel()

	// Inject Send error AFTER subscribe has wired up.
	time.Sleep(20 * time.Millisecond) // let Listen reach the select loop
	stream.setSendErr(errors.New("stream broken (client gone)"))

	mgr.deltaCh <- mkDelta(100, "mid.$X", "boom")

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error propagation")
		}
		if err.Error() == "" {
			t.Errorf("error should carry the Send failure text")
		}
	case <-time.After(time.Second):
		t.Fatal("Listen did not return after Send failure")
	}
	if mgr.unsubCalls.Load() != 1 {
		t.Errorf("unsub must fire even on Send failure, got %d", mgr.unsubCalls.Load())
	}
}

func TestListen_NilDelta_SkipsNoError(t *testing.T) {
	mgr := newListenManager()
	h, st := newListenHandler(t, mgr)
	seedActiveSession(t, st, 100, "tenant-A")

	stream, cancel, done := runListen(t, h, &hermesv1.MbsListenRequest{Uid: 100}, "tenant-A")
	defer cancel()

	// Push nil + real delta.
	mgr.deltaCh <- nil
	mgr.deltaCh <- mkDelta(100, "mid.$only", "real")

	if !waitForCount(stream, 1, time.Second) {
		t.Fatalf("expected 1 streamed message, got %d", stream.sentCount())
	}
	if got := stream.snapshot()[0].Mid; got != "mid.$only" {
		t.Errorf("got %q want mid.$only", got)
	}

	cancel()
	<-done
}

func TestDeltaToInboundMessage_FieldMapping(t *testing.T) {
	now := time.Now()
	d := &session.InboundDelta{
		UID: 100, TenantID: "tenant-A",
		ThreadID:    "thr-1",
		MID:         "mid.$x",
		SenderPhone: "62812",
		Text:        "hello",
		ReceivedAt:  now,
	}
	msg := deltaToInboundMessage(d)
	if msg.ThreadId != "thr-1" || msg.Mid != "mid.$x" || msg.Text != "hello" {
		t.Errorf("fields lost: %+v", msg)
	}
	if msg.SenderPhone != "62812" {
		t.Errorf("sender_phone: got %q", msg.SenderPhone)
	}
	if msg.SenderUid != 0 {
		t.Errorf("sender_uid should be 0 in chunk 4 (fb.ExtractMessages doesn't surface it)")
	}
	if msg.ReceivedAt == nil || !msg.ReceivedAt.AsTime().Equal(now) {
		t.Errorf("received_at: got %v want %v", msg.ReceivedAt, now)
	}
	if msg.RawDelta != "" {
		t.Errorf("raw_delta should be empty in chunk 4 (include_payload TBD)")
	}
}
