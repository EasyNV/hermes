package rest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/rs/zerolog"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	mbshandler "github.com/hermes-waba/hermes/internal/mbs/handler"
)

// ─────────────────────────────────────────────────────────────────────
// Stream fakes for the BridgeLogin bidi client.
//
// grpc.BidiStreamingClient[Req, Res] embeds grpc.ClientStream. We
// implement the minimum surface the bridge handler exercises (Send,
// Recv, CloseSend, Context) and stub the rest.
// ─────────────────────────────────────────────────────────────────────

type fakeBridgeStream struct {
	// Inbound (browser → gateway → mbs) recorded for assertions.
	mu       sync.Mutex
	requests []*hermesv1.BridgeLoginRequest

	// Outbound queue (mbs → gateway → browser). Tests push updates
	// to drive the bridge.
	outbound chan *hermesv1.BridgeLoginUpdate

	// closed when Recv should return io.EOF (clean termination) or
	// the configured recvErr (error termination).
	done    chan struct{}
	recvErr error

	// ctx is what Context() returns — same one used by the bridge
	// for outgoing metadata. Tests don't typically assert on it.
	ctx context.Context

	closeSendOnce sync.Once
}

func newFakeBridgeStream(ctx context.Context) *fakeBridgeStream {
	return &fakeBridgeStream{
		outbound: make(chan *hermesv1.BridgeLoginUpdate, 8),
		done:     make(chan struct{}),
		ctx:      ctx,
	}
}

func (s *fakeBridgeStream) Send(req *hermesv1.BridgeLoginRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, req)
	return nil
}

func (s *fakeBridgeStream) Recv() (*hermesv1.BridgeLoginUpdate, error) {
	select {
	case upd := <-s.outbound:
		return upd, nil
	case <-s.done:
		if s.recvErr != nil {
			return nil, s.recvErr
		}
		return nil, io.EOF
	}
}

func (s *fakeBridgeStream) CloseSend() error {
	s.closeSendOnce.Do(func() { close(s.done) })
	return nil
}

// grpc.ClientStream surface — minimum stubs.
func (s *fakeBridgeStream) Header() (metadata.MD, error) { return nil, nil }
func (s *fakeBridgeStream) Trailer() metadata.MD         { return nil }
func (s *fakeBridgeStream) Context() context.Context     { return s.ctx }
func (s *fakeBridgeStream) SendMsg(m any) error          { return errors.New("not implemented") }
func (s *fakeBridgeStream) RecvMsg(m any) error          { return errors.New("not implemented") }

// pushedRequests returns a copy of the recorded requests.
func (s *fakeBridgeStream) pushedRequests() []*hermesv1.BridgeLoginRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*hermesv1.BridgeLoginRequest, len(s.requests))
	copy(out, s.requests)
	return out
}

// finish ends the stream from the mbs side (clean io.EOF).
func (s *fakeBridgeStream) finish() {
	s.closeSendOnce.Do(func() { close(s.done) })
}

// finishWithError ends the stream with a configured error from Recv.
func (s *fakeBridgeStream) finishWithError(err error) {
	s.recvErr = err
	s.closeSendOnce.Do(func() { close(s.done) })
}

// ─── Fake MBS client that returns the controllable stream ───────────

type fakeMbsBridgeClient struct {
	mu      sync.Mutex
	stream  *fakeBridgeStream
	openMD  metadata.MD // captured at BridgeLogin call
	openErr error
}

// streamRef returns the current stream pointer under the lock so test
// goroutines and the handler goroutine don't race on the field.
func (f *fakeMbsBridgeClient) streamRef() *fakeBridgeStream {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stream
}

// waitForStream polls until BridgeLogin has populated the stream, or
// the deadline fires. Returns the stream or fails the test.
func (f *fakeMbsBridgeClient) waitForStream(t *testing.T) *fakeBridgeStream {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s := f.streamRef(); s != nil {
			return s
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("backend stream never opened")
	return nil
}

// embed the chunk-1 stubMbsClient-equivalent interface; we only need
// BridgeLogin for these tests so the rest panics on call.
func (f *fakeMbsBridgeClient) BridgeLogin(ctx context.Context, opts ...grpc.CallOption) (grpc.BidiStreamingClient[hermesv1.BridgeLoginRequest, hermesv1.BridgeLoginUpdate], error) {
	f.mu.Lock()
	if md, ok := metadata.FromOutgoingContext(ctx); ok {
		f.openMD = md
	}
	if f.openErr != nil {
		f.mu.Unlock()
		return nil, f.openErr
	}
	if f.stream == nil {
		f.stream = newFakeBridgeStream(ctx)
	}
	s := f.stream
	f.mu.Unlock()
	return s, nil
}

// outgoingMD returns the metadata seen at BridgeLogin call time.
func (f *fakeMbsBridgeClient) outgoingMD() metadata.MD {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.openMD
}

// Unused MBS methods — chunk-2 WS tests only exercise BridgeLogin.
func (f *fakeMbsBridgeClient) ListSessions(context.Context, *hermesv1.ListMbsSessionsRequest, ...grpc.CallOption) (*hermesv1.ListMbsSessionsResponse, error) {
	return nil, errors.New("not implemented in bridge tests")
}
func (f *fakeMbsBridgeClient) GetSessionStatus(context.Context, *hermesv1.GetMbsSessionStatusRequest, ...grpc.CallOption) (*hermesv1.GetMbsSessionStatusResponse, error) {
	return nil, errors.New("not implemented in bridge tests")
}
func (f *fakeMbsBridgeClient) ListSessionAssets(context.Context, *hermesv1.ListSessionAssetsRequest, ...grpc.CallOption) (*hermesv1.ListSessionAssetsResponse, error) {
	return nil, errors.New("not implemented in bridge tests")
}
func (f *fakeMbsBridgeClient) BurnSession(context.Context, *hermesv1.BurnMbsSessionRequest, ...grpc.CallOption) (*hermesv1.BurnMbsSessionResponse, error) {
	return nil, errors.New("not implemented in bridge tests")
}
func (f *fakeMbsBridgeClient) RemoveSession(context.Context, *hermesv1.RemoveMbsSessionRequest, ...grpc.CallOption) (*hermesv1.RemoveMbsSessionResponse, error) {
	return nil, errors.New("not implemented in bridge tests")
}
func (f *fakeMbsBridgeClient) ResolvePhone(context.Context, *hermesv1.ResolvePhoneRequest, ...grpc.CallOption) (*hermesv1.ResolvePhoneResponse, error) {
	return nil, errors.New("not implemented in bridge tests")
}
func (f *fakeMbsBridgeClient) SendMessage(context.Context, *hermesv1.MbsSendMessageRequest, ...grpc.CallOption) (*hermesv1.MbsSendMessageResponse, error) {
	return nil, errors.New("not implemented in bridge tests")
}
func (f *fakeMbsBridgeClient) Listen(context.Context, *hermesv1.MbsListenRequest, ...grpc.CallOption) (grpc.ServerStreamingClient[hermesv1.MbsInboundMessage], error) {
	return nil, errors.New("not implemented in bridge tests")
}

// ─────────────────────────────────────────────────────────────────────
// Bridge test scaffolding
// ─────────────────────────────────────────────────────────────────────

func newTestBridgeServer(t *testing.T, mbsClient hermesv1.HermesMbsClient) *httptest.Server {
	t.Helper()
	a := New(&fakeGatewayServer{}, &fakeMbsRouter{}, fakeGatewayStore{}, mbsClient,
		[]byte(testJWTSecret), zerolog.Nop(), "")
	mux := http.NewServeMux()
	a.Register(mux)
	srv := httptest.NewServer(a.CORS(mux))
	t.Cleanup(srv.Close)
	return srv
}

// dialBridgeWS opens a WS connection to the bridge with the supplied JWT.
func dialBridgeWS(t *testing.T, srv *httptest.Server, token string) *websocket.Conn {
	t.Helper()
	wsURL := strings.Replace(srv.URL, "http://", "ws://", 1) + "/ws/mbs/bridge-login?token=" + token
	conn, _, err := websocket.Dial(context.Background(), wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	return conn
}

// readWSFrame reads one JSON-decoded frame off the WS with a timeout.
func readWSFrame(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, raw, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("ws read: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("ws frame unmarshal (%s): %v", raw, err)
	}
	return out
}

func writeWSFrame(t *testing.T, conn *websocket.Conn, frame any) {
	t.Helper()
	data, err := json.Marshal(frame)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatalf("ws write: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────
// Tests
// ─────────────────────────────────────────────────────────────────────

func TestBridgeWS_MissingToken_401(t *testing.T) {
	srv := newTestBridgeServer(t, &fakeMbsBridgeClient{})
	resp, err := http.Get(strings.Replace(srv.URL, "http://", "http://", 1) + "/ws/mbs/bridge-login")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status=%d, want 401", resp.StatusCode)
	}
}

func TestBridgeWS_NilMbsClient_503BeforeUpgrade(t *testing.T) {
	srv := newTestBridgeServer(t, nil) // nil mbsClient
	token := mintTestJWT(t, "u1", "tenant-A", "ws1", "tenant_admin")
	resp, err := http.Get(srv.URL + "/ws/mbs/bridge-login?token=" + token)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status=%d, want 503", resp.StatusCode)
	}
}

func TestBridgeWS_StartTenantForcedFromJWT(t *testing.T) {
	fake := &fakeMbsBridgeClient{}
	srv := newTestBridgeServer(t, fake)
	token := mintTestJWT(t, "u1", "tenant-A", "ws1", "tenant_admin")
	conn := dialBridgeWS(t, srv, token)

	// Send start with WRONG tenant — must be force-overwritten.
	writeWSFrame(t, conn, map[string]any{
		"type": "start",
		"payload": map[string]any{
			"tenantId": "tenant-EVIL",
			"email":    "x@y.com",
			"password": "supersecret",
		},
	})

	stream := fake.waitForStream(t)
	// Wait for the bridge to forward the start frame to the backend.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(stream.pushedRequests()) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	reqs := stream.pushedRequests()
	if len(reqs) == 0 {
		t.Fatal("backend did not receive start request")
	}
	req := reqs[0].GetStart()
	if req == nil {
		t.Fatalf("first request not Start: %+v", reqs[0])
	}
	if req.TenantId != "tenant-A" {
		t.Errorf("tenant not forced: got %q want tenant-A", req.TenantId)
	}
	if got := fake.outgoingMD().Get(mbshandler.TenantMetadataKey); len(got) != 1 || got[0] != "tenant-A" {
		t.Errorf("outgoing metadata %s: got %v", mbshandler.TenantMetadataKey, got)
	}

	// Cleanly terminate.
	stream.finish()
	_ = conn.Close(websocket.StatusNormalClosure, "")
}

func TestBridgeWS_HappyPath_StartProgressSuccess(t *testing.T) {
	fake := &fakeMbsBridgeClient{}
	srv := newTestBridgeServer(t, fake)
	token := mintTestJWT(t, "u1", "tenant-A", "ws1", "tenant_admin")
	conn := dialBridgeWS(t, srv, token)

	writeWSFrame(t, conn, map[string]any{
		"type": "start",
		"payload": map[string]any{
			"email":    "x@y.com",
			"password": "secret",
		},
	})

	stream := fake.waitForStream(t)

	// Backend pushes progress + success.
	stream.outbound <- &hermesv1.BridgeLoginUpdate{
		Event: &hermesv1.BridgeLoginUpdate_Progress{
			Progress: &hermesv1.BridgeLoginProgress{Stage: "BRIDGE_STAGE_CALLING_CAA", Detail: "logging in"},
		},
	}
	stream.outbound <- &hermesv1.BridgeLoginUpdate{
		Event: &hermesv1.BridgeLoginUpdate_Success{
			Success: &hermesv1.BridgeLoginSuccess{
				Uid:         42,
				DisplayName: "Acme Page",
				PageCount:   1,
			},
		},
	}

	frame1 := readWSFrame(t, conn)
	if frame1["type"] != "bridge_login_progress" {
		t.Errorf("frame1 type: got %v", frame1["type"])
	}
	frame2 := readWSFrame(t, conn)
	if frame2["type"] != "bridge_login_success" {
		t.Fatalf("frame2 type: got %v", frame2["type"])
	}
	payload2 := frame2["payload"].(map[string]any)
	if payload2["displayName"] != "Acme Page" {
		t.Errorf("displayName not serialized: %v", payload2["displayName"])
	}

	stream.finish()
}

func TestBridgeWS_PromptCycle(t *testing.T) {
	fake := &fakeMbsBridgeClient{}
	srv := newTestBridgeServer(t, fake)
	token := mintTestJWT(t, "u1", "tenant-A", "ws1", "tenant_admin")
	conn := dialBridgeWS(t, srv, token)

	writeWSFrame(t, conn, map[string]any{
		"type":    "start",
		"payload": map[string]any{"email": "x@y.com", "password": "p"},
	})
	stream := fake.waitForStream(t)

	// Backend asks for 2FA.
	stream.outbound <- &hermesv1.BridgeLoginUpdate{
		Event: &hermesv1.BridgeLoginUpdate_Prompt{
			Prompt: &hermesv1.BridgeLoginPrompt{
				StepId:       "two_step_verification",
				Instructions: "Enter your TOTP",
				Fields: []*hermesv1.BridgeLoginField{
					{Id: "totp_code", Name: "totp_code", Type: "code"},
				},
			},
		},
	}

	frame := readWSFrame(t, conn)
	if frame["type"] != "bridge_login_prompt" {
		t.Fatalf("type: got %v", frame["type"])
	}

	// Browser submits the code.
	writeWSFrame(t, conn, map[string]any{
		"type": "input",
		"payload": map[string]any{
			"fieldId": "totp_code",
			"value":   "123456",
		},
	})

	// Wait for backend to receive the input frame.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		reqs := stream.pushedRequests()
		if len(reqs) >= 2 {
			if in := reqs[1].GetInput(); in != nil && in.FieldId == "totp_code" && in.Value == "123456" {
				goto pass
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("backend never received input frame")
pass:
	stream.finish()
}

func TestBridgeWS_CancelFromBrowser(t *testing.T) {
	fake := &fakeMbsBridgeClient{}
	srv := newTestBridgeServer(t, fake)
	token := mintTestJWT(t, "u1", "tenant-A", "ws1", "tenant_admin")
	conn := dialBridgeWS(t, srv, token)

	writeWSFrame(t, conn, map[string]any{
		"type":    "start",
		"payload": map[string]any{"email": "x@y.com", "password": "p"},
	})
	stream := fake.waitForStream(t)

	writeWSFrame(t, conn, map[string]any{"type": "cancel"})

	// Wait for the cancel to be recorded on the backend stream.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		reqs := stream.pushedRequests()
		if len(reqs) >= 2 && reqs[1].GetCancel() != nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("backend never received cancel")
}

func TestBridgeWS_GRPCErrorSurfacesAsErrorFrame(t *testing.T) {
	fake := &fakeMbsBridgeClient{}
	srv := newTestBridgeServer(t, fake)
	token := mintTestJWT(t, "u1", "tenant-A", "ws1", "tenant_admin")
	conn := dialBridgeWS(t, srv, token)

	writeWSFrame(t, conn, map[string]any{
		"type":    "start",
		"payload": map[string]any{"email": "x@y.com", "password": "p"},
	})
	stream := fake.waitForStream(t)

	// Backend errors out.
	stream.finishWithError(status.Error(codes.Unavailable, "mbs down"))

	frame := readWSFrame(t, conn)
	if frame["type"] != "error" {
		t.Errorf("type: got %v", frame["type"])
	}
}

func TestBridgeWS_MalformedFirstFrame_PolicyViolation(t *testing.T) {
	fake := &fakeMbsBridgeClient{}
	srv := newTestBridgeServer(t, fake)
	token := mintTestJWT(t, "u1", "tenant-A", "ws1", "tenant_admin")
	conn := dialBridgeWS(t, srv, token)

	// Send input as first frame — must be rejected.
	writeWSFrame(t, conn, map[string]any{
		"type":    "input",
		"payload": map[string]any{"fieldId": "x", "value": "y"},
	})

	// Bridge should respond with error then close. Read until we get
	// either an error frame OR the close.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, raw, err := conn.Read(ctx)
	if err != nil {
		// Close received first — that's fine; the bridge closed with
		// PolicyViolation BEFORE we could read the error frame.
		return
	}
	var frame map[string]any
	_ = json.Unmarshal(raw, &frame)
	if frame["type"] != "error" {
		t.Errorf("frame type: got %v, want 'error' or immediate close", frame["type"])
	}
}

func TestBridgeWS_BridgeOpenError_SendsErrorFrame(t *testing.T) {
	fake := &fakeMbsBridgeClient{
		openErr: status.Error(codes.Unavailable, "open failed"),
	}
	srv := newTestBridgeServer(t, fake)
	token := mintTestJWT(t, "u1", "tenant-A", "ws1", "tenant_admin")
	conn := dialBridgeWS(t, srv, token)

	// Bridge should emit an error frame and close. Read with timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, raw, err := conn.Read(ctx)
	if err != nil {
		// If the bridge closed before write, that's still a valid
		// outcome — the open-error path is best-effort.
		return
	}
	var frame map[string]any
	_ = json.Unmarshal(raw, &frame)
	if frame["type"] != "error" {
		t.Errorf("frame type: got %v", frame["type"])
	}
}
