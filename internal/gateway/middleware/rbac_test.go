package middleware

import (
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestAuthorizeMethod_Matrix exercises the role-tier RBAC decision across the
// representative method tiers, the default-deny path, the superadmin bypass,
// and the empty-role guard. This is the single source of truth shared by the
// gRPC interceptor and the REST authz wrapper, so its behavior is load-bearing
// for both transports.
func TestAuthorizeMethod_Matrix(t *testing.T) {
	const (
		superadmin = "superadmin"
		tenant     = "tenant_admin"
		ws         = "workspace_admin"
		cs         = "cs_agent"
	)

	cases := []struct {
		name    string
		role    string
		method  string
		allowed bool
	}{
		// superadmin bypass: allowed even for unmapped methods.
		{"superadmin bypass mapped", superadmin, "/hermes.v1.HermesGateway/CreateTenant", true},
		{"superadmin bypass unmapped", superadmin, "/hermes.v1.HermesGateway/TotallyUnknown", true},

		// empty role always denied.
		{"empty role denied", "", "/hermes.v1.HermesGateway/GetMe", false},

		// default-deny: a method with no rule denies every non-superadmin role.
		{"unmapped method tenant_admin denied", tenant, "/hermes.v1.HermesGateway/NoSuchRpc", false},
		{"unmapped method cs_agent denied", cs, "/hermes.v1.HermesGateway/NoSuchRpc", false},

		// any-authenticated tier.
		{"GetMe cs_agent", cs, "/hermes.v1.HermesGateway/GetMe", true},

		// superadmin-only tier.
		{"CreateTenant tenant_admin denied", tenant, "/hermes.v1.HermesGateway/CreateTenant", false},
		{"CreateTenant cs_agent denied", cs, "/hermes.v1.HermesGateway/CreateTenant", false},

		// superadmin+tenant_admin tier.
		{"AddProxies tenant_admin", tenant, "/hermes.v1.HermesGateway/AddProxies", true},
		{"AddProxies workspace_admin denied", ws, "/hermes.v1.HermesGateway/AddProxies", false},

		// admin tier (no cs_agent).
		{"CreateUser workspace_admin", ws, "/hermes.v1.HermesGateway/CreateUser", true},
		{"CreateUser cs_agent denied", cs, "/hermes.v1.HermesGateway/CreateUser", false},
		{"CreateCampaign cs_agent denied", cs, "/hermes.v1.HermesGateway/CreateCampaign", false},

		// conversation handling tier (workspace_admin + cs_agent only).
		{"SendMessage cs_agent", cs, "/hermes.v1.HermesGateway/SendMessage", true},
		{"SendMessage tenant_admin denied", tenant, "/hermes.v1.HermesGateway/SendMessage", false},
		{"ClaimConversation cs_agent", cs, "/hermes.v1.HermesGateway/ClaimConversation", true},

		// MBS read (D1): all roles incl. cs_agent.
		{"MBS ListSessions cs_agent", cs, "/hermes.v1.HermesMbs/ListSessions", true},
		{"MBS GetSessionStatus cs_agent", cs, "/hermes.v1.HermesMbs/GetSessionStatus", true},
		{"MBS ListSessionAssets cs_agent", cs, "/hermes.v1.HermesMbs/ListSessionAssets", true},

		// MBS send/resolve (D2): all roles incl. cs_agent.
		{"MBS SendMessage cs_agent", cs, "/hermes.v1.HermesMbs/SendMessage", true},
		{"MBS ResolvePhone cs_agent", cs, "/hermes.v1.HermesMbs/ResolvePhone", true},

		// MBS destructive (D3): superadmin + tenant_admin + workspace_admin; cs_agent excluded.
		{"MBS BurnSession workspace_admin", ws, "/hermes.v1.HermesMbs/BurnSession", true},
		{"MBS BurnSession tenant_admin", tenant, "/hermes.v1.HermesMbs/BurnSession", true},
		{"MBS BurnSession cs_agent denied", cs, "/hermes.v1.HermesMbs/BurnSession", false},
		{"MBS RemoveSession workspace_admin", ws, "/hermes.v1.HermesMbs/RemoveSession", true},
		{"MBS RemoveSession cs_agent denied", cs, "/hermes.v1.HermesMbs/RemoveSession", false},

		// REST-only synthetic keys.
		{"REST allowlist GET cs_agent", cs, "REST:GET /api/v1/allowlist", true},
		{"REST allowlist POST cs_agent denied", cs, "REST:POST /api/v1/allowlist", false},
		{"REST conversations clear cs_agent denied", cs, "REST:DELETE /api/v1/conversations/clear", false},
		{"REST conversations clear workspace_admin", ws, "REST:DELETE /api/v1/conversations/clear", true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := AuthorizeMethod(c.role, c.method)
			if c.allowed && err != nil {
				t.Fatalf("AuthorizeMethod(%q, %q) = %v, want nil (allowed)", c.role, c.method, err)
			}
			if !c.allowed {
				if err == nil {
					t.Fatalf("AuthorizeMethod(%q, %q) = nil, want PermissionDenied", c.role, c.method)
				}
				if status.Code(err) != codes.PermissionDenied {
					t.Fatalf("AuthorizeMethod(%q, %q) code = %v, want PermissionDenied", c.role, c.method, status.Code(err))
				}
			}
		})
	}
}

// TestHasRule verifies the route-audit helper recognizes mapped vs unmapped keys.
func TestHasRule(t *testing.T) {
	if !HasRule("/hermes.v1.HermesGateway/CreateUser") {
		t.Error("CreateUser should have a rule")
	}
	if !HasRule("/hermes.v1.HermesMbs/RemoveSession") {
		t.Error("MBS RemoveSession should have a rule")
	}
	if !HasRule("REST:DELETE /api/v1/conversations/clear") {
		t.Error("REST conversations/clear should have a rule")
	}
	if HasRule("/hermes.v1.HermesGateway/NoSuchMethod") {
		t.Error("unknown method should not have a rule")
	}
}
