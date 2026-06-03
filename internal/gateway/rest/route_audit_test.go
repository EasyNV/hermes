package rest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"

	"github.com/hermes-waba/hermes/internal/gateway/middleware"
)

// TestRouteAudit_EveryAuthzKeyHasRule is the mandatory safety net for the
// REST↔RBAC unification. The REST authz wrapper is default-deny: if a route's
// method key is absent from middleware.rpcRoles, that route silently 403s in
// production. This test parses rest.go's AST, extracts the first argument of
// every a.authz(...) call in Register, and asserts each key has an RBAC rule.
//
// If you add a new authenticated REST route, add its method key to rpcRoles —
// otherwise this test fails the build instead of letting the route 403 silently.
func TestRouteAudit_EveryAuthzKeyHasRule(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "rest.go", nil, 0)
	if err != nil {
		t.Fatalf("parse rest.go: %v", err)
	}

	var keys []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "authz" {
			return true
		}
		if len(call.Args) == 0 {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			t.Errorf("a.authz first arg is not a string literal at %s — "+
				"route-audit can't verify it statically", fset.Position(call.Pos()))
			return true
		}
		key, err := strconv.Unquote(lit.Value)
		if err != nil {
			t.Errorf("unquote %q: %v", lit.Value, err)
			return true
		}
		keys = append(keys, key)
		return true
	})

	if len(keys) == 0 {
		t.Fatal("no a.authz(...) calls found in rest.go — parser broke or routes were refactored")
	}

	for _, key := range keys {
		if !middleware.HasRule(key) {
			t.Errorf("route key %q has NO RBAC rule in rpcRoles — it will 403 under default-deny. "+
				"Add it to internal/gateway/middleware/rbac.go", key)
		}
	}
	t.Logf("audited %d authenticated REST routes — all have RBAC rules", len(keys))
}

// TestRouteAudit_NoBareAuthOnApiRoutes guards against regressing a route back to
// the bare a.auth(...) wrapper (JWT-only, no role tier). The only legitimate
// bare-auth mounts are the unauthenticated login/refresh handlers and the
// bridge-login WS upgrade (which validates JWT inline). Every /api/v1 route that
// takes a handler must go through authz.
func TestRouteAudit_NoBareAuthHandleFunc(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "rest.go", nil, 0)
	if err != nil {
		t.Fatalf("parse rest.go: %v", err)
	}

	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "HandleFunc" || len(call.Args) < 2 {
			return true
		}
		// First arg is the route pattern string.
		patLit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || patLit.Kind != token.STRING {
			return true
		}
		pattern, _ := strconv.Unquote(patLit.Value)
		if !strings.Contains(pattern, "/api/v1/") {
			return true // non-api (e.g. /ws/...) handled separately
		}
		// The handler arg must be a call to a.authz(...). Allow the two
		// unauthenticated auth endpoints.
		if pattern == "POST /api/v1/auth/login" || pattern == "POST /api/v1/auth/refresh" {
			return true
		}
		handlerCall, ok := call.Args[1].(*ast.CallExpr)
		if !ok {
			t.Errorf("route %q handler is not wrapped (expected a.authz) at %s",
				pattern, fset.Position(call.Pos()))
			return true
		}
		hSel, ok := handlerCall.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if hSel.Sel.Name == "auth" {
			t.Errorf("route %q uses bare a.auth (JWT-only, NO role tier) — use a.authz", pattern)
		}
		if hSel.Sel.Name != "authz" {
			t.Errorf("route %q handler wrapper is %q, expected authz", pattern, hSel.Sel.Name)
		}
		return true
	})
}
