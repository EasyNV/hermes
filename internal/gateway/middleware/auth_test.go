package middleware

import (
	"context"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// ---------------------------------------------------------------------------
// TestAuthInterceptor
// ---------------------------------------------------------------------------

func TestAuthInterceptor(t *testing.T) {
	testSecret := []byte("test-secret")
	interceptor := AuthInterceptor(testSecret)

	// Generate a valid JWT for the "accepts valid token" test case.
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
		UserID:      "u1",
		TenantID:    "t1",
		WorkspaceID: "w1",
		Role:        "workspace_admin",
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(testSecret)
	if err != nil {
		t.Fatalf("failed to sign test token: %v", err)
	}

	tests := []struct {
		name       string
		fullMethod string
		ctx        context.Context
		wantCode   codes.Code
		checkCtx   func(t *testing.T, result any)
	}{
		{
			name:       "skips auth for Login",
			fullMethod: "/hermes.v1.HermesGateway/Login",
			ctx:        context.Background(),
		},
		{
			name:       "skips auth for RefreshToken",
			fullMethod: "/hermes.v1.HermesGateway/RefreshToken",
			ctx:        context.Background(),
		},
		{
			name:       "rejects missing token",
			fullMethod: "/hermes.v1.HermesGateway/GetMe",
			ctx:        context.Background(),
			wantCode:   codes.Unauthenticated,
		},
		{
			name:       "rejects invalid token",
			fullMethod: "/hermes.v1.HermesGateway/GetMe",
			ctx: metadata.NewIncomingContext(
				context.Background(),
				metadata.Pairs("authorization", "Bearer garbage"),
			),
			wantCode: codes.Unauthenticated,
		},
		{
			name:       "accepts valid token",
			fullMethod: "/hermes.v1.HermesGateway/GetMe",
			ctx: metadata.NewIncomingContext(
				context.Background(),
				metadata.Pairs("authorization", "Bearer "+signed),
			),
			checkCtx: func(t *testing.T, result any) {
				uid, ok := result.(string)
				if !ok || uid != "u1" {
					t.Errorf("expected UserID=u1, got %v", result)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := &grpc.UnaryServerInfo{FullMethod: tt.fullMethod}

			fakeHandler := func(ctx context.Context, req any) (any, error) {
				// Return the user ID from context so the test can verify claims extraction.
				return UserIDFromCtx(ctx), nil
			}

			result, err := interceptor(tt.ctx, nil, info, fakeHandler)

			if tt.wantCode != codes.OK {
				if err == nil {
					t.Fatalf("expected error code %v, got nil", tt.wantCode)
				}
				if got := status.Code(err); got != tt.wantCode {
					t.Fatalf("expected code %v, got %v: %v", tt.wantCode, got, err)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.checkCtx != nil {
				tt.checkCtx(t, result)
			}
		})
	}
}
