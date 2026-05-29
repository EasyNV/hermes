package handler

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// TenantMetadataKey is the gRPC metadata header the gateway forwards to
// every hermes-mbs call. The gateway has already validated the JWT and
// resolved the tenant; we trust the header AND cross-check session
// ownership inside each RPC.
const TenantMetadataKey = "x-tenant-id"

// tenantCtxKey is a private type+value so we can't collide with any
// other context-key in the import graph. The gateway's
// middleware.CtxTenantID is a different key — keep these separate.
type tenantCtxKey struct{}

// TenantUnaryInterceptor reads x-tenant-id from incoming metadata and
// stores it in context. Returns Unauthenticated if missing/empty.
//
// Skip list: none. Every HermesMbs RPC requires a tenant. If a future
// RPC needs to be public (health probe, etc.), match on
// info.FullMethod here.
func TenantUnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		ctx, err := injectTenant(ctx)
		if err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

// TenantStreamInterceptor mirrors the unary version for streaming
// RPCs (BridgeLogin, Listen).
func TenantStreamInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx, err := injectTenant(ss.Context())
		if err != nil {
			return err
		}
		// Wrap the ServerStream so handlers reading from Context() get
		// the augmented context. grpc's ServerStream interface doesn't
		// allow context replacement directly — use a wrapper.
		return handler(srv, &tenantServerStream{ServerStream: ss, ctx: ctx})
	}
}

// tenantServerStream wraps a ServerStream to override Context(). This
// is the canonical pattern for stream interceptors that need to inject
// values into the per-stream context.
type tenantServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *tenantServerStream) Context() context.Context { return w.ctx }

func injectTenant(ctx context.Context) (context.Context, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ctx, status.Error(codes.Unauthenticated, "missing gRPC metadata")
	}
	vals := md.Get(TenantMetadataKey)
	if len(vals) == 0 || vals[0] == "" {
		return ctx, status.Errorf(codes.Unauthenticated, "missing %s metadata header", TenantMetadataKey)
	}
	return context.WithValue(ctx, tenantCtxKey{}, vals[0]), nil
}

// tenantFromCtx extracts the tenant_id stored by injectTenant. Returns
// "" if absent (test code that doesn't run the interceptor; the RPC
// validates via requireTenant).
func tenantFromCtx(ctx context.Context) string {
	v, _ := ctx.Value(tenantCtxKey{}).(string)
	return v
}

// requireTenant returns the tenant_id stored on ctx, or a gRPC
// Unauthenticated error if missing. Convenience for RPC entry points
// (one-line guard that produces a properly-coded error).
func requireTenant(ctx context.Context) (string, error) {
	t := tenantFromCtx(ctx)
	if t == "" {
		return "", status.Errorf(codes.Unauthenticated, "missing %s metadata header", TenantMetadataKey)
	}
	return t, nil
}

// withTenantForTest injects a tenant directly into ctx, bypassing the
// metadata layer. Useful for unit tests that don't run gRPC interceptors.
// NOT exported — tests in the same package access via tenantCtxKey.
func withTenantForTest(ctx context.Context, tenantID string) context.Context {
	return context.WithValue(ctx, tenantCtxKey{}, tenantID)
}

// WithTenantForTest is the cross-package test helper variant of
// withTenantForTest. Integration tests outside the handler package
// (e.g. internal/mbs/bridge_test) need to inject a tenant context
// without running the gRPC metadata interceptor. The name carries
// "ForTest" to discourage non-test use; the godoc warns explicitly.
//
// NEVER call this from production code. Use TenantUnaryInterceptor
// or TenantStreamInterceptor instead — those read x-tenant-id from
// real gRPC metadata, which is the only trustworthy source.
func WithTenantForTest(ctx context.Context, tenantID string) context.Context {
	return withTenantForTest(ctx, tenantID)
}
