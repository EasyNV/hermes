package handler

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/hermes-waba/hermes/internal/gateway/middleware"
)

// ─────────────────────────────────────────────────────────────────────
// stubMbsClient — minimal HermesMbsClient implementation for tests.
// Records the request each method received plus the outgoing gRPC
// metadata (so we verify chunk-2's tenant-metadata propagation fix).
// Streams (BridgeLogin, Listen) are unimplemented; chunk 1/2 tests
// never exercise them.
// ─────────────────────────────────────────────────────────────────────

type stubMbsClient struct {
	// recorded last-call requests, per RPC
	lastListReq    *hermesv1.ListMbsSessionsRequest
	lastStatusReq  *hermesv1.GetMbsSessionStatusRequest
	lastAssetsReq  *hermesv1.ListSessionAssetsRequest
	lastBurnReq    *hermesv1.BurnMbsSessionRequest
	lastResolveReq *hermesv1.ResolvePhoneRequest
	lastSendReq    *hermesv1.MbsSendMessageRequest

	// last outgoing gRPC metadata observed on the most recent call.
	// Tests assert `lastMD.Get("tenant-id")` matches the JWT tenant.
	lastMD metadata.MD

	// configurable responses
	listResp    *hermesv1.ListMbsSessionsResponse
	statusResp  *hermesv1.GetMbsSessionStatusResponse
	assetsResp  *hermesv1.ListSessionAssetsResponse
	burnResp    *hermesv1.BurnMbsSessionResponse
	resolveResp *hermesv1.ResolvePhoneResponse
	sendResp    *hermesv1.MbsSendMessageResponse

	// optional error injection
	err error
}

// captureMD reads outgoing metadata off ctx. Called at the top of each
// stub RPC. Safe when no metadata is set — returns the zero MD which
// MD.Get returns nil for.
func (s *stubMbsClient) captureMD(ctx context.Context) {
	if md, ok := metadata.FromOutgoingContext(ctx); ok {
		s.lastMD = md
	} else {
		s.lastMD = metadata.MD{}
	}
}

func (s *stubMbsClient) ListSessions(ctx context.Context, in *hermesv1.ListMbsSessionsRequest, opts ...grpc.CallOption) (*hermesv1.ListMbsSessionsResponse, error) {
	s.captureMD(ctx)
	s.lastListReq = in
	if s.err != nil {
		return nil, s.err
	}
	if s.listResp == nil {
		return &hermesv1.ListMbsSessionsResponse{}, nil
	}
	return s.listResp, nil
}

func (s *stubMbsClient) GetSessionStatus(ctx context.Context, in *hermesv1.GetMbsSessionStatusRequest, opts ...grpc.CallOption) (*hermesv1.GetMbsSessionStatusResponse, error) {
	s.captureMD(ctx)
	s.lastStatusReq = in
	if s.err != nil {
		return nil, s.err
	}
	if s.statusResp == nil {
		return &hermesv1.GetMbsSessionStatusResponse{}, nil
	}
	return s.statusResp, nil
}

func (s *stubMbsClient) ListSessionAssets(ctx context.Context, in *hermesv1.ListSessionAssetsRequest, opts ...grpc.CallOption) (*hermesv1.ListSessionAssetsResponse, error) {
	s.captureMD(ctx)
	s.lastAssetsReq = in
	if s.err != nil {
		return nil, s.err
	}
	if s.assetsResp == nil {
		return &hermesv1.ListSessionAssetsResponse{}, nil
	}
	return s.assetsResp, nil
}

func (s *stubMbsClient) BurnSession(ctx context.Context, in *hermesv1.BurnMbsSessionRequest, opts ...grpc.CallOption) (*hermesv1.BurnMbsSessionResponse, error) {
	s.captureMD(ctx)
	s.lastBurnReq = in
	if s.err != nil {
		return nil, s.err
	}
	if s.burnResp == nil {
		return &hermesv1.BurnMbsSessionResponse{}, nil
	}
	return s.burnResp, nil
}

func (s *stubMbsClient) ResolvePhone(ctx context.Context, in *hermesv1.ResolvePhoneRequest, opts ...grpc.CallOption) (*hermesv1.ResolvePhoneResponse, error) {
	s.captureMD(ctx)
	s.lastResolveReq = in
	if s.err != nil {
		return nil, s.err
	}
	if s.resolveResp == nil {
		return &hermesv1.ResolvePhoneResponse{}, nil
	}
	return s.resolveResp, nil
}

func (s *stubMbsClient) SendMessage(ctx context.Context, in *hermesv1.MbsSendMessageRequest, opts ...grpc.CallOption) (*hermesv1.MbsSendMessageResponse, error) {
	s.captureMD(ctx)
	s.lastSendReq = in
	if s.err != nil {
		return nil, s.err
	}
	if s.sendResp == nil {
		return &hermesv1.MbsSendMessageResponse{}, nil
	}
	return s.sendResp, nil
}

// BridgeLogin is not implemented for chunk-1 unit tests — chunk-2's WS
// bridge tests construct their own stream stub. Returning an error
// keeps the type satisfied without inviting accidental calls here.
func (s *stubMbsClient) BridgeLogin(ctx context.Context, opts ...grpc.CallOption) (grpc.BidiStreamingClient[hermesv1.BridgeLoginRequest, hermesv1.BridgeLoginUpdate], error) {
	return nil, errors.New("stubMbsClient: BridgeLogin not implemented in chunk-1 tests")
}

// Listen is not implemented for chunk-1 unit tests — gateway never
// proxies Listen directly; it consumes via NATS in chunk-3.
func (s *stubMbsClient) Listen(ctx context.Context, in *hermesv1.MbsListenRequest, opts ...grpc.CallOption) (grpc.ServerStreamingClient[hermesv1.MbsInboundMessage], error) {
	return nil, errors.New("stubMbsClient: Listen not implemented in chunk-1 tests")
}

// anyCall reports whether any RPC on the stub was invoked. Tests use
// this to assert that the backend was NOT touched when the gateway
// rejected at the boundary.
func (s *stubMbsClient) anyCall() bool {
	return s.lastListReq != nil ||
		s.lastStatusReq != nil ||
		s.lastAssetsReq != nil ||
		s.lastBurnReq != nil ||
		s.lastResolveReq != nil ||
		s.lastSendReq != nil
}

// ─────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────

// withTenantClaim returns a context populated as the gateway's auth
// interceptor would populate it — tenant_id + role. Tests pass this
// straight into the proxy method.
func withTenantClaim(t *testing.T, tenantID, role string) context.Context {
	t.Helper()
	ctx := context.Background()
	ctx = context.WithValue(ctx, middleware.CtxTenantID, tenantID)
	ctx = context.WithValue(ctx, middleware.CtxRole, role)
	return ctx
}

// ─────────────────────────────────────────────────────────────────────
// Tests
// ─────────────────────────────────────────────────────────────────────

func TestHandler_ListMbsSessions_ForcesTenantFromJWT(t *testing.T) {
	stub := &stubMbsClient{}
	h := newTestHandlerWithMbs(nil, stub)

	ctx := withTenantClaim(t, "tenant-A", hermesv1.Role_ROLE_TENANT_ADMIN.String())
	req := &hermesv1.ListMbsSessionsRequest{TenantId: ""} // caller did not fill
	if _, err := h.ListMbsSessions(ctx, req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stub.lastListReq == nil {
		t.Fatal("backend was not called")
	}
	if stub.lastListReq.TenantId != "tenant-A" {
		t.Errorf("tenant not forced from JWT: got %q want tenant-A", stub.lastListReq.TenantId)
	}
}

func TestHandler_ListMbsSessions_RejectsTenantMismatch(t *testing.T) {
	stub := &stubMbsClient{}
	h := newTestHandlerWithMbs(nil, stub)

	ctx := withTenantClaim(t, "tenant-A", hermesv1.Role_ROLE_TENANT_ADMIN.String())
	req := &hermesv1.ListMbsSessionsRequest{TenantId: "tenant-B"} // attempted attack
	_, err := h.ListMbsSessions(ctx, req)
	if status.Code(err) != codes.PermissionDenied {
		t.Errorf("want PermissionDenied, got %v", err)
	}
	if stub.anyCall() {
		t.Error("backend was invoked despite tenant mismatch")
	}
}

func TestHandler_ListMbsSessions_SuperadminBypassesTenantCheck(t *testing.T) {
	stub := &stubMbsClient{}
	h := newTestHandlerWithMbs(nil, stub)

	ctx := withTenantClaim(t, "tenant-A", hermesv1.Role_ROLE_SUPERADMIN.String())
	req := &hermesv1.ListMbsSessionsRequest{TenantId: "tenant-X"} // any tenant
	if _, err := h.ListMbsSessions(ctx, req); err != nil {
		t.Fatalf("superadmin should bypass: %v", err)
	}
	if stub.lastListReq == nil {
		t.Fatal("backend was not called")
	}
	if stub.lastListReq.TenantId != "tenant-X" {
		t.Errorf("superadmin tenant override not preserved: got %q want tenant-X",
			stub.lastListReq.TenantId)
	}
}

func TestHandler_MbsMethods_RejectMissingTenantClaim(t *testing.T) {
	// Context with NO tenant claim — the auth interceptor failed to
	// populate, or this is an unprotected route misconfig. Every
	// proxy method MUST fail closed without touching the backend.
	stub := &stubMbsClient{}
	h := newTestHandlerWithMbs(nil, stub)
	ctx := context.Background()

	calls := []struct {
		name string
		fn   func() error
	}{
		{"ListMbsSessions", func() error {
			_, err := h.ListMbsSessions(ctx, &hermesv1.ListMbsSessionsRequest{})
			return err
		}},
		{"GetMbsSessionStatus", func() error {
			_, err := h.GetMbsSessionStatus(ctx, &hermesv1.GetMbsSessionStatusRequest{})
			return err
		}},
		{"ListSessionAssets", func() error {
			_, err := h.ListSessionAssets(ctx, &hermesv1.ListSessionAssetsRequest{})
			return err
		}},
		{"BurnMbsSession", func() error {
			_, err := h.BurnMbsSession(ctx, &hermesv1.BurnMbsSessionRequest{})
			return err
		}},
		{"ResolveMbsPhone", func() error {
			_, err := h.ResolveMbsPhone(ctx, &hermesv1.ResolvePhoneRequest{})
			return err
		}},
		{"SendMbsMessage", func() error {
			_, err := h.SendMbsMessage(ctx, &hermesv1.MbsSendMessageRequest{})
			return err
		}},
	}
	for _, c := range calls {
		t.Run(c.name, func(t *testing.T) {
			err := c.fn()
			if status.Code(err) != codes.Unauthenticated {
				t.Errorf("%s: want Unauthenticated, got %v", c.name, err)
			}
		})
	}
	if stub.anyCall() {
		t.Error("backend was invoked despite missing tenant claim")
	}
}

func TestHandler_MbsMethods_NilClientReturnsUnavailable(t *testing.T) {
	h := newTestHandlerWithMbs(nil, nil) // nil mbsClient simulates mbs-down
	ctx := withTenantClaim(t, "tenant-A", hermesv1.Role_ROLE_TENANT_ADMIN.String())

	calls := []struct {
		name string
		fn   func() error
	}{
		{"ListMbsSessions", func() error {
			_, err := h.ListMbsSessions(ctx, &hermesv1.ListMbsSessionsRequest{})
			return err
		}},
		{"GetMbsSessionStatus", func() error {
			_, err := h.GetMbsSessionStatus(ctx, &hermesv1.GetMbsSessionStatusRequest{})
			return err
		}},
		{"ListSessionAssets", func() error {
			_, err := h.ListSessionAssets(ctx, &hermesv1.ListSessionAssetsRequest{})
			return err
		}},
		{"BurnMbsSession", func() error {
			_, err := h.BurnMbsSession(ctx, &hermesv1.BurnMbsSessionRequest{})
			return err
		}},
		{"ResolveMbsPhone", func() error {
			_, err := h.ResolveMbsPhone(ctx, &hermesv1.ResolvePhoneRequest{})
			return err
		}},
		{"SendMbsMessage", func() error {
			_, err := h.SendMbsMessage(ctx, &hermesv1.MbsSendMessageRequest{})
			return err
		}},
	}
	for _, c := range calls {
		t.Run(c.name, func(t *testing.T) {
			if status.Code(c.fn()) != codes.Unavailable {
				t.Errorf("%s: want Unavailable, got %v", c.name, c.fn())
			}
		})
	}
}

func TestHandler_GetMbsSessionStatus_ForwardsUid(t *testing.T) {
	// GetSessionStatus has no tenant_id field — verify the wrapper
	// doesn't try to set one but DOES still require a tenant claim
	// and forwards the uid unchanged.
	stub := &stubMbsClient{}
	h := newTestHandlerWithMbs(nil, stub)
	ctx := withTenantClaim(t, "tenant-A", hermesv1.Role_ROLE_TENANT_ADMIN.String())

	if _, err := h.GetMbsSessionStatus(ctx, &hermesv1.GetMbsSessionStatusRequest{Uid: 42}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if stub.lastStatusReq == nil {
		t.Fatal("backend was not called")
	}
	if stub.lastStatusReq.Uid != 42 {
		t.Errorf("uid not forwarded: got %d want 42", stub.lastStatusReq.Uid)
	}
}

func TestHandler_MbsMethods_PropagateBackendError(t *testing.T) {
	// When the backend returns an error, the gateway should pass it
	// through unmodified. This is the contract for codes.NotFound
	// (uid doesn't exist), codes.PermissionDenied (tenant mismatch at
	// backend's defense-in-depth layer), etc.
	backendErr := status.Error(codes.NotFound, "session uid 999 not found")
	stub := &stubMbsClient{err: backendErr}
	h := newTestHandlerWithMbs(nil, stub)
	ctx := withTenantClaim(t, "tenant-A", hermesv1.Role_ROLE_TENANT_ADMIN.String())

	_, err := h.GetMbsSessionStatus(ctx, &hermesv1.GetMbsSessionStatusRequest{Uid: 999})
	if status.Code(err) != codes.NotFound {
		t.Errorf("want NotFound propagated, got %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────
// Chunk-2 closes C1-G1: every MBS proxy method MUST inject outgoing
// gRPC metadata so mbs's server-side interceptor sees the right
// tenant for uid-keyed RPCs. The keys MUST be `tenant-id` and
// `user-id` (lowercase, hyphen) so they match what mbs reads.
// ─────────────────────────────────────────────────────────────────────

func TestHandler_MbsMethods_PropagateTenantMetadata(t *testing.T) {
	ctx := context.Background()
	ctx = context.WithValue(ctx, middleware.CtxTenantID, "tenant-A")
	ctx = context.WithValue(ctx, middleware.CtxRole, hermesv1.Role_ROLE_TENANT_ADMIN.String())
	ctx = context.WithValue(ctx, middleware.CtxUserID, "user-007")

	calls := []struct {
		name string
		fn   func(*Handler) error
	}{
		{"ListMbsSessions", func(h *Handler) error {
			_, err := h.ListMbsSessions(ctx, &hermesv1.ListMbsSessionsRequest{})
			return err
		}},
		{"GetMbsSessionStatus", func(h *Handler) error {
			_, err := h.GetMbsSessionStatus(ctx, &hermesv1.GetMbsSessionStatusRequest{Uid: 1})
			return err
		}},
		{"ListSessionAssets", func(h *Handler) error {
			_, err := h.ListSessionAssets(ctx, &hermesv1.ListSessionAssetsRequest{Uid: 1})
			return err
		}},
		{"BurnMbsSession", func(h *Handler) error {
			_, err := h.BurnMbsSession(ctx, &hermesv1.BurnMbsSessionRequest{Uid: 1})
			return err
		}},
		{"ResolveMbsPhone", func(h *Handler) error {
			_, err := h.ResolveMbsPhone(ctx, &hermesv1.ResolvePhoneRequest{Uid: 1, Phone: "62812"})
			return err
		}},
		{"SendMbsMessage", func(h *Handler) error {
			_, err := h.SendMbsMessage(ctx, &hermesv1.MbsSendMessageRequest{Uid: 1, Text: "hi"})
			return err
		}},
	}

	for _, c := range calls {
		t.Run(c.name, func(t *testing.T) {
			stub := &stubMbsClient{}
			h := newTestHandlerWithMbs(nil, stub)
			if err := c.fn(h); err != nil {
				t.Fatalf("%s: unexpected: %v", c.name, err)
			}
			if got := stub.lastMD.Get("tenant-id"); len(got) != 1 || got[0] != "tenant-A" {
				t.Errorf("%s: tenant-id metadata: got %v, want [tenant-A]", c.name, got)
			}
			if got := stub.lastMD.Get("user-id"); len(got) != 1 || got[0] != "user-007" {
				t.Errorf("%s: user-id metadata: got %v, want [user-007]", c.name, got)
			}
		})
	}
}

// Superadmin acting on a non-own tenant must inject THAT tenant into
// outgoing metadata, not the superadmin's own JWT tenant. Otherwise
// the cross-tenant operation never reaches the right mbs session row.
func TestHandler_MbsMethods_SuperadminMetadataUsesRequestTenant(t *testing.T) {
	ctx := context.Background()
	ctx = context.WithValue(ctx, middleware.CtxTenantID, "tenant-A")
	ctx = context.WithValue(ctx, middleware.CtxRole, hermesv1.Role_ROLE_SUPERADMIN.String())

	stub := &stubMbsClient{}
	h := newTestHandlerWithMbs(nil, stub)
	req := &hermesv1.ListMbsSessionsRequest{TenantId: "tenant-X"}
	if _, err := h.ListMbsSessions(ctx, req); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if got := stub.lastMD.Get("tenant-id"); len(got) != 1 || got[0] != "tenant-X" {
		t.Errorf("superadmin: tenant-id metadata: got %v, want [tenant-X]", got)
	}
}
