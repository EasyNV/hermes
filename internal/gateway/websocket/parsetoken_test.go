package websocket

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/rs/zerolog"
)

// TestParseToken_PopulatesIdentity is a regression guard for the wsClaims JSON
// tag bug: tokens carry uid/tid/wid/role, but wsClaims once declared
// user_id/tenant_id/workspace_id, so every WS client parsed with empty identity
// — silently breaking ALL tenant/workspace/conversation scoping. This asserts
// the hub extracts identity from a token signed exactly like the gateway's.
func TestParseToken_PopulatesIdentity(t *testing.T) {
	secret := []byte("test-secret")
	h := NewHub(secret, nil, zerolog.Nop())

	// Build a token with the canonical claim names (uid/tid/wid/role), matching
	// middleware.Claims / handler.Claims.
	claims := wsClaims{
		UserID:      "user-123",
		TenantID:    "tenant-abc",
		WorkspaceID: "ws-xyz",
		Role:        "cs_agent",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(secret)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	got, err := h.parseToken(signed)
	if err != nil {
		t.Fatalf("parseToken: %v", err)
	}
	if got.UserID != "user-123" {
		t.Errorf("UserID empty/wrong: %q — JSON tag must be `uid`", got.UserID)
	}
	if got.TenantID != "tenant-abc" {
		t.Errorf("TenantID empty/wrong: %q — JSON tag must be `tid`", got.TenantID)
	}
	if got.WorkspaceID != "ws-xyz" {
		t.Errorf("WorkspaceID empty/wrong: %q — JSON tag must be `wid`", got.WorkspaceID)
	}
	if got.Role != "cs_agent" {
		t.Errorf("Role empty/wrong: %q", got.Role)
	}
}
