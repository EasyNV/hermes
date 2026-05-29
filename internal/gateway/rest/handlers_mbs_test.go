package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/rs/zerolog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/hermes-waba/hermes/internal/gateway/handler"
	"github.com/hermes-waba/hermes/internal/gateway/middleware"
)

// ─────────────────────────────────────────────────────────────────────
// Test scaffolding
// ─────────────────────────────────────────────────────────────────────

const testJWTSecret = "test-secret-do-not-use-in-prod"

// mintTestJWT produces a signed access token with the standard Hermes
// claim shape. The auth middleware ParseJWT consumes it identically
// to a production token.
func mintTestJWT(t *testing.T, userID, tenantID, workspaceID, role string) string {
	t.Helper()
	claims := middleware.Claims{
		UserID:      userID,
		TenantID:    tenantID,
		WorkspaceID: workspaceID,
		Role:        role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString([]byte(testJWTSecret))
	if err != nil {
		t.Fatalf("mintTestJWT: %v", err)
	}
	return signed
}

// fakeMbsRouter satisfies rest.MbsRouter for handler tests. Records
// the last request per method and returns the configured response or
// error.
type fakeMbsRouter struct {
	lastListReq    *hermesv1.ListMbsSessionsRequest
	lastStatusReq  *hermesv1.GetMbsSessionStatusRequest
	lastAssetsReq  *hermesv1.ListSessionAssetsRequest
	lastBurnReq    *hermesv1.BurnMbsSessionRequest
	lastResolveReq *hermesv1.ResolvePhoneRequest
	lastSendReq    *hermesv1.MbsSendMessageRequest

	listResp    *hermesv1.ListMbsSessionsResponse
	statusResp  *hermesv1.GetMbsSessionStatusResponse
	assetsResp  *hermesv1.ListSessionAssetsResponse
	burnResp    *hermesv1.BurnMbsSessionResponse
	resolveResp *hermesv1.ResolvePhoneResponse
	sendResp    *hermesv1.MbsSendMessageResponse

	err error
}

func (f *fakeMbsRouter) ListMbsSessions(ctx context.Context, req *hermesv1.ListMbsSessionsRequest) (*hermesv1.ListMbsSessionsResponse, error) {
	f.lastListReq = req
	if f.err != nil {
		return nil, f.err
	}
	if f.listResp == nil {
		return &hermesv1.ListMbsSessionsResponse{}, nil
	}
	return f.listResp, nil
}

func (f *fakeMbsRouter) GetMbsSessionStatus(ctx context.Context, req *hermesv1.GetMbsSessionStatusRequest) (*hermesv1.GetMbsSessionStatusResponse, error) {
	f.lastStatusReq = req
	if f.err != nil {
		return nil, f.err
	}
	if f.statusResp == nil {
		return &hermesv1.GetMbsSessionStatusResponse{}, nil
	}
	return f.statusResp, nil
}

func (f *fakeMbsRouter) ListSessionAssets(ctx context.Context, req *hermesv1.ListSessionAssetsRequest) (*hermesv1.ListSessionAssetsResponse, error) {
	f.lastAssetsReq = req
	if f.err != nil {
		return nil, f.err
	}
	if f.assetsResp == nil {
		return &hermesv1.ListSessionAssetsResponse{}, nil
	}
	return f.assetsResp, nil
}

func (f *fakeMbsRouter) BurnMbsSession(ctx context.Context, req *hermesv1.BurnMbsSessionRequest) (*hermesv1.BurnMbsSessionResponse, error) {
	f.lastBurnReq = req
	if f.err != nil {
		return nil, f.err
	}
	if f.burnResp == nil {
		return &hermesv1.BurnMbsSessionResponse{}, nil
	}
	return f.burnResp, nil
}

func (f *fakeMbsRouter) ResolveMbsPhone(ctx context.Context, req *hermesv1.ResolvePhoneRequest) (*hermesv1.ResolvePhoneResponse, error) {
	f.lastResolveReq = req
	if f.err != nil {
		return nil, f.err
	}
	if f.resolveResp == nil {
		return &hermesv1.ResolvePhoneResponse{}, nil
	}
	return f.resolveResp, nil
}

func (f *fakeMbsRouter) SendMbsMessage(ctx context.Context, req *hermesv1.MbsSendMessageRequest) (*hermesv1.MbsSendMessageResponse, error) {
	f.lastSendReq = req
	if f.err != nil {
		return nil, f.err
	}
	if f.sendResp == nil {
		return &hermesv1.MbsSendMessageResponse{}, nil
	}
	return f.sendResp, nil
}

// fakeGatewayServer satisfies hermesv1.HermesGatewayServer for tests.
// We only need a zero implementation; the MBS routes call via mbs,
// not gw.
type fakeGatewayServer struct {
	hermesv1.UnimplementedHermesGatewayServer
}

// fakeGatewayStore satisfies rest.GatewayStore for tests. All methods
// return zero values; MBS tests don't exercise the store.
type fakeGatewayStore struct{}

func (fakeGatewayStore) ClearAllConversations(ctx context.Context, ws string) (int64, error) {
	return 0, nil
}
func (fakeGatewayStore) AddToAllowlist(ctx context.Context, ws, phone, source string) error {
	return nil
}
func (fakeGatewayStore) RemoveFromAllowlist(ctx context.Context, ws, phone string) error {
	return nil
}
func (fakeGatewayStore) ClearAllowlist(ctx context.Context, ws string) (int64, error) {
	return 0, nil
}
func (fakeGatewayStore) ListAllowlist(ctx context.Context, ws string, page, pageSize int32) ([]handler.AllowlistRow, int64, error) {
	return nil, 0, nil
}

// newTestRESTServer wires an httptest.Server around a fresh Adapter
// with the supplied MbsRouter. mbsClient is always nil — the chunk-2
// REST handler tests don't exercise the WS bridge.
func newTestRESTServer(t *testing.T, mbs *fakeMbsRouter) *httptest.Server {
	t.Helper()
	a := New(&fakeGatewayServer{}, mbs, fakeGatewayStore{}, nil,
		[]byte(testJWTSecret), zerolog.Nop(), "")
	mux := http.NewServeMux()
	a.Register(mux)
	srv := httptest.NewServer(a.CORS(mux))
	t.Cleanup(srv.Close)
	return srv
}

// doJSON performs an authenticated request and returns the response
// body. Caller asserts status code separately.
func doJSON(t *testing.T, srv *httptest.Server, method, path string, body any, token string) (*http.Response, []byte) {
	t.Helper()
	var bodyReader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		bodyReader = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, srv.URL+path, bodyReader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp, raw
}

// ─────────────────────────────────────────────────────────────────────
// Tests
// ─────────────────────────────────────────────────────────────────────

func TestREST_ListMbsSessions(t *testing.T) {
	mbs := &fakeMbsRouter{
		listResp: &hermesv1.ListMbsSessionsResponse{},
	}
	srv := newTestRESTServer(t, mbs)
	token := mintTestJWT(t, "u1", "tenant-A", "ws1", "tenant_admin")

	resp, _ := doJSON(t, srv, "GET",
		"/api/v1/mbs-sessions?stateFilter=MBS_SESSION_STATE_ACTIVE&page=2&pageSize=50",
		nil, token)
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if mbs.lastListReq == nil {
		t.Fatal("backend not called")
	}
	if mbs.lastListReq.StateFilter != hermesv1.MbsSessionState_MBS_SESSION_STATE_ACTIVE {
		t.Errorf("stateFilter not parsed: %v", mbs.lastListReq.StateFilter)
	}
	if mbs.lastListReq.GetPage().GetPage() != 2 || mbs.lastListReq.GetPage().GetPageSize() != 50 {
		t.Errorf("pagination not parsed: %+v", mbs.lastListReq.Page)
	}
}

func TestREST_GetMbsSession(t *testing.T) {
	mbs := &fakeMbsRouter{}
	srv := newTestRESTServer(t, mbs)
	token := mintTestJWT(t, "u1", "tenant-A", "ws1", "tenant_admin")

	resp, _ := doJSON(t, srv, "GET", "/api/v1/mbs-sessions/1674772559", nil, token)
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if mbs.lastStatusReq == nil || mbs.lastStatusReq.Uid != 1674772559 {
		t.Errorf("uid not parsed: %+v", mbs.lastStatusReq)
	}
}

func TestREST_ListMbsSessionAssets(t *testing.T) {
	mbs := &fakeMbsRouter{}
	srv := newTestRESTServer(t, mbs)
	token := mintTestJWT(t, "u1", "tenant-A", "ws1", "tenant_admin")

	resp, _ := doJSON(t, srv, "GET", "/api/v1/mbs-sessions/42/assets", nil, token)
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if mbs.lastAssetsReq == nil || mbs.lastAssetsReq.Uid != 42 {
		t.Errorf("uid not parsed: %+v", mbs.lastAssetsReq)
	}
}

func TestREST_BurnMbsSession(t *testing.T) {
	mbs := &fakeMbsRouter{}
	srv := newTestRESTServer(t, mbs)
	token := mintTestJWT(t, "u1", "tenant-A", "ws1", "tenant_admin")

	body := map[string]string{"reason": "operator_burn"}
	resp, _ := doJSON(t, srv, "POST", "/api/v1/mbs-sessions/100/burn", body, token)
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if mbs.lastBurnReq == nil {
		t.Fatal("backend not called")
	}
	if mbs.lastBurnReq.Uid != 100 || mbs.lastBurnReq.Reason != "operator_burn" {
		t.Errorf("burn req wrong: %+v", mbs.lastBurnReq)
	}
}

func TestREST_BurnMbsSession_EmptyBodyOK(t *testing.T) {
	mbs := &fakeMbsRouter{}
	srv := newTestRESTServer(t, mbs)
	token := mintTestJWT(t, "u1", "tenant-A", "ws1", "tenant_admin")

	resp, _ := doJSON(t, srv, "POST", "/api/v1/mbs-sessions/100/burn", nil, token)
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if mbs.lastBurnReq.Uid != 100 || mbs.lastBurnReq.Reason != "" {
		t.Errorf("burn req with empty body: %+v", mbs.lastBurnReq)
	}
}

func TestREST_ResolveMbsPhone_PathWinsOverBody(t *testing.T) {
	mbs := &fakeMbsRouter{
		resolveResp: &hermesv1.ResolvePhoneResponse{ThreadId: "t1"},
	}
	srv := newTestRESTServer(t, mbs)
	token := mintTestJWT(t, "u1", "tenant-A", "ws1", "tenant_admin")

	// Body says uid=999, path says uid=42. Path must win.
	body := map[string]any{
		"uid":   999,
		"phone": "62812345",
	}
	resp, _ := doJSON(t, srv, "POST", "/api/v1/mbs-sessions/42/resolve-phone", body, token)
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if mbs.lastResolveReq.Uid != 42 {
		t.Errorf("uid path override not applied: %d", mbs.lastResolveReq.Uid)
	}
	if mbs.lastResolveReq.Phone != "62812345" {
		t.Errorf("phone not forwarded: %q", mbs.lastResolveReq.Phone)
	}
}

func TestREST_SendMbsMessage_PathWinsOverBody(t *testing.T) {
	mbs := &fakeMbsRouter{
		sendResp: &hermesv1.MbsSendMessageResponse{Mid: "mid.xyz"},
	}
	srv := newTestRESTServer(t, mbs)
	token := mintTestJWT(t, "u1", "tenant-A", "ws1", "tenant_admin")

	body := map[string]any{
		"uid":      999,
		"phone":    "62812345",
		"text":     "hello",
	}
	resp, _ := doJSON(t, srv, "POST", "/api/v1/mbs-sessions/42/messages", body, token)
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, "")
	}
	if mbs.lastSendReq.Uid != 42 || mbs.lastSendReq.Text != "hello" {
		t.Errorf("send req wrong: %+v", mbs.lastSendReq)
	}
}

func TestREST_InvalidUID_400(t *testing.T) {
	mbs := &fakeMbsRouter{}
	srv := newTestRESTServer(t, mbs)
	token := mintTestJWT(t, "u1", "tenant-A", "ws1", "tenant_admin")

	cases := []struct{ name, path string }{
		{"non-numeric", "/api/v1/mbs-sessions/abc"},
		{"zero uid", "/api/v1/mbs-sessions/0"},
		{"negative uid", "/api/v1/mbs-sessions/-5"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, raw := doJSON(t, srv, "GET", c.path, nil, token)
			if resp.StatusCode != 400 {
				t.Fatalf("status=%d body=%s", resp.StatusCode, raw)
			}
			if !strings.Contains(string(raw), "INVALID_UID") {
				t.Errorf("body does not carry INVALID_UID code: %s", raw)
			}
		})
	}
	if mbs.lastStatusReq != nil {
		t.Error("backend invoked despite invalid uid")
	}
}

func TestREST_BackendNotFound_Maps404(t *testing.T) {
	mbs := &fakeMbsRouter{err: status.Error(codes.NotFound, "session uid 999 not found")}
	srv := newTestRESTServer(t, mbs)
	token := mintTestJWT(t, "u1", "tenant-A", "ws1", "tenant_admin")

	resp, _ := doJSON(t, srv, "GET", "/api/v1/mbs-sessions/999", nil, token)
	if resp.StatusCode != 404 {
		t.Errorf("status=%d, want 404", resp.StatusCode)
	}
}

func TestREST_MissingToken_401(t *testing.T) {
	srv := newTestRESTServer(t, &fakeMbsRouter{})
	resp, _ := doJSON(t, srv, "GET", "/api/v1/mbs-sessions", nil, "")
	if resp.StatusCode != 401 {
		t.Errorf("status=%d, want 401", resp.StatusCode)
	}
}

func TestREST_ParseStateFilter(t *testing.T) {
	cases := []struct {
		in   string
		want hermesv1.MbsSessionState
	}{
		{"", hermesv1.MbsSessionState_MBS_SESSION_STATE_UNSPECIFIED},
		{"MBS_SESSION_STATE_ACTIVE", hermesv1.MbsSessionState_MBS_SESSION_STATE_ACTIVE},
		{"MBS_SESSION_STATE_SUSPENDED", hermesv1.MbsSessionState_MBS_SESSION_STATE_SUSPENDED},
		{"MBS_SESSION_STATE_BURNED", hermesv1.MbsSessionState_MBS_SESSION_STATE_BURNED},
		{"MBS_SESSION_STATE_BRIDGING", hermesv1.MbsSessionState_MBS_SESSION_STATE_BRIDGING},
		{"random-garbage", hermesv1.MbsSessionState_MBS_SESSION_STATE_UNSPECIFIED},
	}
	for _, c := range cases {
		if got := parseStateFilter(c.in); got != c.want {
			t.Errorf("parseStateFilter(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
