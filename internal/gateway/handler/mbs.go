package handler

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/hermes-waba/hermes/internal/gateway/middleware"
)

// ─────────────────────────────────────────────────────────────────────
// HermesMbs proxy methods
//
// Each method:
//
//   1. Reject if mbsClient is nil → codes.Unavailable. This matches
//      the existing waClient/proxyClient/etc. pattern: a missing
//      backend never panics, the route just reports 503.
//
//   2. Force tenant_id from the JWT claim (forceTenantFromJWT). For
//      requests with a tenant_id field that the caller already filled,
//      we cross-check and refuse on mismatch unless the caller is
//      superadmin. For uid-keyed requests (no tenant_id field) we
//      still verify the JWT carried a tenant claim — anonymous
//      requests fail closed here instead of leaking to mbs.
//
//   3. Forward unmodified to the mbs client and return the response.
//
// The cross-tenant guard is **defense in depth** alongside the
// chunk-4 server-side guard in hermes-mbs (SECURITY F1). Any drift
// between the gateway's JWT claim and the mbs row's tenant_id is
// caught HERE, before any decrypt happens.
//
// BridgeLogin (bidi stream) is intentionally NOT in this file. The
// chunk-2 WS bridge calls mbsClient.BridgeLogin directly and tunnels
// over WebSocket. A unary proxy method would either duplicate the
// bridge or force a synchronous reimplementation that defeats the
// stream semantics.
// ─────────────────────────────────────────────────────────────────────

// forceTenantFromJWT cross-checks a request's tenant_id against the JWT
// claim. Returns the effective tenant_id to thread back into the
// request before forwarding.
//
// Behavior:
//
//   - JWT carries no tenant → Unauthenticated (the auth interceptor
//     should have caught this; treat as defense-in-depth).
//   - Request's tenant is empty → fill from JWT.
//   - Request's tenant matches JWT → preserve.
//   - Request's tenant differs from JWT → PermissionDenied unless
//     the caller has the superadmin role.
//
// reqTenant is the value the gRPC request already carries (empty
// string when the request type has no tenant_id field).
func (h *Handler) forceTenantFromJWT(ctx context.Context, reqTenant string) (string, error) {
	caller := middleware.TenantIDFromCtx(ctx)
	if caller == "" {
		// Authenticated request must have a tenant in the JWT.
		// Missing claim means the auth interceptor didn't populate
		// (or wasn't run, e.g. unprotected route misconfig). Fail
		// closed.
		return "", status.Error(codes.Unauthenticated, "missing tenant claim")
	}
	if reqTenant == "" {
		return caller, nil
	}
	if reqTenant != caller && !isSuperadmin(ctx) {
		return "", status.Error(codes.PermissionDenied, "tenant_id does not match caller")
	}
	return reqTenant, nil
}

// isSuperadmin reports whether the caller's role is superadmin. Single
// source of truth so new privileged paths just call this helper rather
// than re-stringifying the enum.
func isSuperadmin(ctx context.Context) bool {
	return middleware.RoleFromCtx(ctx) == hermesv1.Role_ROLE_SUPERADMIN.String()
}

// ─── unary proxy methods ─────────────────────────────────────────────

func (h *Handler) ListMbsSessions(ctx context.Context, req *hermesv1.ListMbsSessionsRequest) (*hermesv1.ListMbsSessionsResponse, error) {
	if h.mbsClient == nil {
		return nil, status.Error(codes.Unavailable, "mbs service not available")
	}
	tenant, err := h.forceTenantFromJWT(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	req.TenantId = tenant
	return h.mbsClient.ListSessions(ctx, req)
}

func (h *Handler) GetMbsSessionStatus(ctx context.Context, req *hermesv1.GetMbsSessionStatusRequest) (*hermesv1.GetMbsSessionStatusResponse, error) {
	if h.mbsClient == nil {
		return nil, status.Error(codes.Unavailable, "mbs service not available")
	}
	// GetSessionStatus is uid-keyed; tenant scoping happens server-side
	// in mbs handler via GetSessionByTenant. We still verify the caller
	// has a tenant claim — anonymous callers fail here, not at mbs.
	if _, err := h.forceTenantFromJWT(ctx, ""); err != nil {
		return nil, err
	}
	return h.mbsClient.GetSessionStatus(ctx, req)
}

func (h *Handler) ListSessionAssets(ctx context.Context, req *hermesv1.ListSessionAssetsRequest) (*hermesv1.ListSessionAssetsResponse, error) {
	if h.mbsClient == nil {
		return nil, status.Error(codes.Unavailable, "mbs service not available")
	}
	if _, err := h.forceTenantFromJWT(ctx, ""); err != nil {
		return nil, err
	}
	return h.mbsClient.ListSessionAssets(ctx, req)
}

func (h *Handler) BurnMbsSession(ctx context.Context, req *hermesv1.BurnMbsSessionRequest) (*hermesv1.BurnMbsSessionResponse, error) {
	if h.mbsClient == nil {
		return nil, status.Error(codes.Unavailable, "mbs service not available")
	}
	if _, err := h.forceTenantFromJWT(ctx, ""); err != nil {
		return nil, err
	}
	return h.mbsClient.BurnSession(ctx, req)
}

func (h *Handler) ResolveMbsPhone(ctx context.Context, req *hermesv1.ResolvePhoneRequest) (*hermesv1.ResolvePhoneResponse, error) {
	if h.mbsClient == nil {
		return nil, status.Error(codes.Unavailable, "mbs service not available")
	}
	if _, err := h.forceTenantFromJWT(ctx, ""); err != nil {
		return nil, err
	}
	return h.mbsClient.ResolvePhone(ctx, req)
}

func (h *Handler) SendMbsMessage(ctx context.Context, req *hermesv1.MbsSendMessageRequest) (*hermesv1.MbsSendMessageResponse, error) {
	if h.mbsClient == nil {
		return nil, status.Error(codes.Unavailable, "mbs service not available")
	}
	if _, err := h.forceTenantFromJWT(ctx, ""); err != nil {
		return nil, err
	}
	return h.mbsClient.SendMessage(ctx, req)
}
