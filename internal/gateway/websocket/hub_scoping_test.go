package websocket

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/rs/zerolog"
)

// stubAuthorizer is a ConversationAuthorizer that returns a fixed verdict,
// recording the last call for assertion.
type stubAuthorizer struct {
	allow      bool
	lastRole   string
	lastUser   string
	lastConvID string
	calls      int
}

func (s *stubAuthorizer) CanAccessConversation(_ context.Context, role, userID, convID string) bool {
	s.calls++
	s.lastRole, s.lastUser, s.lastConvID = role, userID, convID
	return s.allow
}

// newTestClient builds a Client wired to the hub with a buffered send channel
// so sendMsg never blocks and tests can drain frames.
func newTestClient(h *Hub, userID, tenantID, workspaceID, role string) *Client {
	return &Client{
		hub:           h,
		userID:        userID,
		tenantID:      tenantID,
		workspaceID:   workspaceID,
		role:          role,
		subscriptions: make(map[string]bool),
		send:          make(chan []byte, 16),
		done:          make(chan struct{}),
	}
}

func drainFrameTypes(c *Client) []string {
	var types []string
	for {
		select {
		case data := <-c.send:
			var m wsMessage
			if err := json.Unmarshal(data, &m); err == nil {
				types = append(types, m.Type)
			}
		default:
			return types
		}
	}
}

// ─────────────────────────────────────────────────────────────────────
// GAP 3a: subscription authorization
// ─────────────────────────────────────────────────────────────────────

func TestHandleSubscribe_CsAgentDeniedForNonOwned(t *testing.T) {
	authz := &stubAuthorizer{allow: false}
	h := NewHub([]byte("secret"), authz, zerolog.Nop())
	c := newTestClient(h, "agent-1", "tenant-A", "ws-1", "cs_agent")

	c.handleSubscribe(json.RawMessage(`{"conversation_id":"conv-x"}`))

	if c.subscriptions["conv-x"] {
		t.Error("subscription should NOT be granted when authorizer denies")
	}
	if authz.calls != 1 {
		t.Fatalf("authorizer should be consulted once, got %d", authz.calls)
	}
	if authz.lastRole != "cs_agent" || authz.lastUser != "agent-1" || authz.lastConvID != "conv-x" {
		t.Errorf("authorizer args: role=%q user=%q conv=%q", authz.lastRole, authz.lastUser, authz.lastConvID)
	}
	types := drainFrameTypes(c)
	if len(types) != 1 || types[0] != "error" {
		t.Errorf("expected a single error frame, got %v", types)
	}
}

func TestHandleSubscribe_CsAgentAllowedForOwned(t *testing.T) {
	authz := &stubAuthorizer{allow: true}
	h := NewHub([]byte("secret"), authz, zerolog.Nop())
	c := newTestClient(h, "agent-1", "tenant-A", "ws-1", "cs_agent")

	c.handleSubscribe(json.RawMessage(`{"conversation_id":"conv-own"}`))

	if !c.subscriptions["conv-own"] {
		t.Error("subscription should be granted when authorizer allows")
	}
	types := drainFrameTypes(c)
	if len(types) != 1 || types[0] != "subscribed" {
		t.Errorf("expected a single subscribed frame, got %v", types)
	}
}

func TestHandleSubscribe_NilAuthorizerSkipsCheck(t *testing.T) {
	// Legacy/test wiring: nil authorizer means no per-conversation gate.
	h := NewHub([]byte("secret"), nil, zerolog.Nop())
	c := newTestClient(h, "agent-1", "tenant-A", "ws-1", "cs_agent")

	c.handleSubscribe(json.RawMessage(`{"conversation_id":"conv-x"}`))
	if !c.subscriptions["conv-x"] {
		t.Error("nil authorizer should skip the check and allow subscribe")
	}
}

func TestHandleSubscribe_MissingConversationID(t *testing.T) {
	h := NewHub([]byte("secret"), &stubAuthorizer{allow: true}, zerolog.Nop())
	c := newTestClient(h, "agent-1", "tenant-A", "ws-1", "cs_agent")

	c.handleSubscribe(json.RawMessage(`{}`))
	if len(c.subscriptions) != 0 {
		t.Error("empty conversation_id must not create a subscription")
	}
	types := drainFrameTypes(c)
	if len(types) != 1 || types[0] != "error" {
		t.Errorf("expected error frame for missing conversation_id, got %v", types)
	}
}

// ─────────────────────────────────────────────────────────────────────
// GAP 3b: BroadcastConversationScoped fan-out matrix
// ─────────────────────────────────────────────────────────────────────

func TestBroadcastConversationScoped_OwnershipMatrix(t *testing.T) {
	h := NewHub([]byte("secret"), nil, zerolog.Nop())

	// Clients across roles, workspaces, tenants.
	admin := newTestClient(h, "admin-1", "tenant-A", "ws-1", "workspace_admin")
	tadmin := newTestClient(h, "tadmin-1", "tenant-A", "", "tenant_admin")
	owner := newTestClient(h, "agent-owner", "tenant-A", "ws-1", "cs_agent")
	otherSameWs := newTestClient(h, "agent-other", "tenant-A", "ws-1", "cs_agent")
	otherDiffWs := newTestClient(h, "agent-diffws", "tenant-A", "ws-2", "cs_agent")
	diffTenant := newTestClient(h, "agent-difft", "tenant-B", "ws-1", "cs_agent")

	for _, c := range []*Client{admin, tadmin, owner, otherSameWs, otherDiffWs, diffTenant} {
		h.register(c)
	}

	// Case 1: assigned to owner. Only owner + privileged in tenant get it.
	h.BroadcastConversationScoped("tenant-A", "ws-1", "agent-owner", []byte(`{"type":"new_message"}`))

	assertGot := func(c *Client, want bool, label string) {
		t.Helper()
		got := len(drainFrameTypes(c)) > 0
		if got != want {
			t.Errorf("%s: got delivered=%v want %v", label, got, want)
		}
	}
	assertGot(admin, true, "assigned: workspace_admin (privileged)")
	assertGot(tadmin, true, "assigned: tenant_admin (privileged)")
	assertGot(owner, true, "assigned: owner cs_agent")
	assertGot(otherSameWs, false, "assigned: other cs_agent same ws")
	assertGot(otherDiffWs, false, "assigned: other cs_agent diff ws")
	assertGot(diffTenant, false, "assigned: cs_agent different tenant")

	// Case 2: unassigned. Every cs_agent in the conversation's workspace gets
	// it, plus privileged roles; cs_agents in other workspaces do not.
	h.BroadcastConversationScoped("tenant-A", "ws-1", "", []byte(`{"type":"new_message"}`))
	assertGot(admin, true, "unassigned: workspace_admin")
	assertGot(tadmin, true, "unassigned: tenant_admin")
	assertGot(owner, true, "unassigned: cs_agent in ws-1")
	assertGot(otherSameWs, true, "unassigned: other cs_agent in ws-1")
	assertGot(otherDiffWs, false, "unassigned: cs_agent in ws-2")
	assertGot(diffTenant, false, "unassigned: cs_agent different tenant")
}
