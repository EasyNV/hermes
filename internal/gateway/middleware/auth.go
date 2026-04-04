package middleware

import (
	"context"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// ctxKey is used for storing values in context to avoid collisions.
type ctxKey string

const (
	CtxUserID      ctxKey = "user_id"
	CtxTenantID    ctxKey = "tenant_id"
	CtxWorkspaceID ctxKey = "workspace_id"
	CtxRole        ctxKey = "role"
	CtxRefreshID   ctxKey = "refresh_id"
)

// Claims represents the JWT claims embedded in access tokens.
// JSON tags MUST match handler.Claims which generates the tokens: uid, tid, wid, role.
type Claims struct {
	UserID      string `json:"uid"`
	TenantID    string `json:"tid"`
	WorkspaceID string `json:"wid"`
	Role        string `json:"role"`
	jwt.RegisteredClaims
}

// UserIDFromCtx extracts the user ID from the context.
func UserIDFromCtx(ctx context.Context) string {
	v, _ := ctx.Value(CtxUserID).(string)
	return v
}

// TenantIDFromCtx extracts the tenant ID from the context.
func TenantIDFromCtx(ctx context.Context) string {
	v, _ := ctx.Value(CtxTenantID).(string)
	return v
}

// WorkspaceIDFromCtx extracts the workspace ID from the context.
func WorkspaceIDFromCtx(ctx context.Context) string {
	v, _ := ctx.Value(CtxWorkspaceID).(string)
	return v
}

// RoleFromCtx extracts the role from the context.
func RoleFromCtx(ctx context.Context) string {
	v, _ := ctx.Value(CtxRole).(string)
	return v
}

// ParseJWT validates and parses a JWT token string, returning the claims.
// Exported for reuse by the REST adapter.
func ParseJWT(tokenStr string, jwtSecret []byte) (*Claims, error) {
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrSignatureInvalid
		}
		return jwtSecret, nil
	})
	if err != nil || !token.Valid {
		return nil, err
	}
	return claims, nil
}

// AuthInterceptor returns a gRPC unary server interceptor that validates JWT
// tokens from the "authorization" metadata header and injects claims into the
// request context. Login and RefreshToken RPCs are skipped.
func AuthInterceptor(jwtSecret []byte) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		// Skip authentication for public endpoints.
		if strings.Contains(info.FullMethod, "/Login") ||
			strings.Contains(info.FullMethod, "/RefreshToken") {
			return handler(ctx, req)
		}

		// Extract the "authorization" header from gRPC metadata.
		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "missing metadata")
		}

		vals := md.Get("authorization")
		if len(vals) == 0 {
			return nil, status.Error(codes.Unauthenticated, "missing authorization header")
		}

		authHeader := vals[0]
		if !strings.HasPrefix(authHeader, "Bearer ") {
			return nil, status.Error(codes.Unauthenticated, "invalid authorization format")
		}
		tokenStr := strings.TrimPrefix(authHeader, "Bearer ")

		// Parse and validate the JWT.
		claims := &Claims{}
		token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (any, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, status.Errorf(codes.Unauthenticated, "unexpected signing method: %v", t.Header["alg"])
			}
			return jwtSecret, nil
		})
		if err != nil || !token.Valid {
			return nil, status.Error(codes.Unauthenticated, "invalid or expired token")
		}

		// Inject claims into the context.
		ctx = context.WithValue(ctx, CtxUserID, claims.UserID)
		ctx = context.WithValue(ctx, CtxTenantID, claims.TenantID)
		ctx = context.WithValue(ctx, CtxWorkspaceID, claims.WorkspaceID)
		ctx = context.WithValue(ctx, CtxRole, claims.Role)
		if claims.ID != "" {
			ctx = context.WithValue(ctx, CtxRefreshID, claims.ID)
		}

		return handler(ctx, req)
	}
}
