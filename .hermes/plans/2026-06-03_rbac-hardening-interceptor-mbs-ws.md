# RBAC Hardening — Interceptor/REST Unification, MBS Role Map, WS Scoping

**Date:** 2026-06-03
**Author:** Oracle
**Status:** PLAN — awaiting decision sign-off (D1–D5) before build
**Scope:** Three related authorization gaps in `hermes-gateway`:
1. RBAC interceptor is bypassed on the REST path (the path the SPA uses)
2. MBS RPCs are absent from the role map entirely
3. WS event fan-out ignores per-conversation ownership

---

## 0. Why these three are one workstream

All three are the same disease: **authorization intent that exists in prose/partial code but is not enforced on the path that matters.** Gaps 1 and 2 are tightly coupled (the fix for 1 is the delivery vehicle for 2). Gap 3 is the real-time mirror of the conversation-access RBAC already shipped in `c8f7ebd` — without it, a cs_agent who can't *fetch* another agent's conversation over REST still *receives* its messages live over WS.

---

## 1. GAP 1 — RBAC interceptor REST-bypass

### Current state (verified)

- `cmd/gateway/main.go:120-125` chains `AuthInterceptor` → `RBACInterceptor` on the **gRPC server only**.
- The REST adapter (`internal/gateway/rest/`) dispatches **in-process** to `a.gw.X()` / `a.mbs.X()` — it never traverses the gRPC interceptor chain.
- REST's `auth()` wrapper (`rest.go:305`) validates the JWT and injects claims — **authentication only, zero role-tier authorization.**
- The SPA talks **REST exclusively** (`web/src/api/client.ts`). So role-tier RBAC is effectively **not enforced in production.**
- Only **2** REST handlers have inline hand-coded role checks (`clearAllConversations`, `clearAllowlist`, `handlers.go:1007/1075`) — proof someone knew REST skips the interceptor and patched the two scariest by hand.

### Consequence

Over REST a `cs_agent` reaches role-restricted ops the gRPC map limits to `workspace_admin`/`tenant_admin`: `createUser`, `deleteContact`, `createCampaign`, `addProxies`, `createTemplate`, etc. Tenant/workspace scoping still constrains *which* rows they touch (in-handler), but the role gate is gone.

### What already holds on REST (do NOT regress)

- Tenant isolation: `forceTenantFromJWT` (MBS) + per-handler workspace checks run in-handler → both transports.
- Conversation-access ownership: shipped `c8f7ebd`, in-handler → both transports.

---

## 2. GAP 2 — MBS RPCs missing from `rpcRoles`

The 7 MBS proxy RPCs (`ListMbsSessions`, `GetMbsSessionStatus`, `ListSessionAssets`, `BurnMbsSession`, `RemoveMbsSession`, `ResolveMbsPhone`, `SendMbsMessage`) are:
- Served by the **HermesMbs** service, proxied through the gateway handler + REST — they are NOT HermesGateway gRPC methods, so the gRPC `RBACInterceptor` never sees them.
- Absent from the `rpcRoles` map → **no role-tier gate on either transport.**
- Tenant-scoped only (`forceTenantFromJWT` defense-in-depth).

### Frontend usage (informs role policy)

- `listMbsSessions` / `getMbsSessionStatus` / `listSessionAssets` — Dashboard tile, CampaignCreate sender picker, MbsSessions admin page.
- `resolveMbsPhone` / `sendMbsMessage` — inbox ColdComposeForm (cold outbound).
- `burnMbsSession` / `removeMbsSession` — MbsSessions admin page (destructive).

---

## 3. GAP 3 — WS event per-conversation scoping

### Current state (verified, `websocket/events.go` + `events_mbs.go` + `hub.go`)

- **Message events broadcast tenant-wide:** `new_message`, `message_status_updated`, `mbs_new_message`, `mbs_outbound_status` all call `Broadcast(tenantID, "", data)` → **every** client in the tenant, ignoring conversation ownership and even workspace.
- A `cs_agent` on WS receives **every** inbound message in the tenant live — including conversations assigned to other agents. This directly bypasses the REST ownership gate from `c8f7ebd`.
- **Subscription authorization missing:** `handleSubscribe` (`hub.go:388`) sets `c.subscriptions[id]=true` for **any** conversation id the client sends — no access check. `BroadcastToConversation` (typing indicators) then trusts it.
- **Root data problem:** the raw `WaInboundMessageEvent` / `MbsInboundMessageEvent` the gateway consumes carry **no `conversation_id`, `workspace_id`, or `assigned_to`** — the gateway cannot scope them without resolving the conversation. The publishers (wa/mbs services) don't own conversations (inbox does).

### Two pieces

- **3a — subscription authorization** (self-contained, no proto change): gate `handleSubscribe` by conversation access.
- **3b — message-event broadcast scoping** (needs conversation ownership data at broadcast time): the architectural fork — see **D4**.

---

## 4. DESIGN

### 4.1 GAP 1+2 — shared role map, enforced on both transports

**Single source of truth.** Promote the `rpcRoles` map to a shared, exported authorization function in `middleware`. Both the gRPC interceptor and a new REST `authz` wrapper consult it. Identical policy, identical default-deny, identical superadmin bypass — no drift possible.

**`internal/gateway/middleware/rbac.go`:**
```go
// AuthorizeMethod enforces role-tier RBAC for a logical method key.
// superadmin bypasses; unknown methods are denied (fail-closed).
// Shared by the gRPC interceptor and the REST authz wrapper so both
// transports enforce identical policy from one map.
func AuthorizeMethod(role, method string) error {
    if role == "" {
        return status.Error(codes.PermissionDenied, "no role in context")
    }
    if role == "superadmin" {
        return nil
    }
    allowed, ok := rpcRoles[method]
    if !ok {
        return status.Errorf(codes.PermissionDenied, "no RBAC rule for %s", method)
    }
    for _, r := range allowed {
        if r == role {
            return nil
        }
    }
    return status.Errorf(codes.PermissionDenied, "role %q not allowed for %s", role, method)
}
```
`RBACInterceptor` is refactored to call `AuthorizeMethod(RoleFromCtx(ctx), info.FullMethod)` (behavior-identical; keeps the Login/RefreshToken skip).

**`rpcRoles` additions (GAP 2 + REST-only routes):**
```go
// --- MBS (HermesMbs proxy; consumed by REST authz only) ---
"/hermes.v1.HermesMbs/ListSessions":      {…D1…},
"/hermes.v1.HermesMbs/GetSessionStatus":  {…D1…},
"/hermes.v1.HermesMbs/ListSessionAssets": {…D1…},
"/hermes.v1.HermesMbs/ResolvePhone":      {…D2…},
"/hermes.v1.HermesMbs/SendMessage":       {…D2…},
"/hermes.v1.HermesMbs/BurnSession":       {"superadmin","tenant_admin"},   // D3
"/hermes.v1.HermesMbs/RemoveSession":     {"superadmin","tenant_admin"},   // D3

// --- REST-only routes (no gRPC equivalent; synthetic keys) ---
"REST:POST /api/v1/mbs-sessions/{uid}/typing-or-na": …  // (none today)
"REST:DELETE /api/v1/conversations/clear":     {"superadmin","tenant_admin","workspace_admin"},
"REST:GET /api/v1/allowlist":                  {"superadmin","tenant_admin","workspace_admin"},
"REST:POST /api/v1/allowlist":                 {"superadmin","tenant_admin","workspace_admin"},
"REST:DELETE /api/v1/allowlist":               {"superadmin","tenant_admin","workspace_admin"},
"REST:DELETE /api/v1/allowlist/clear":         {"superadmin","tenant_admin","workspace_admin"},
"REST:POST /api/v1/wa-numbers/{id}/pair-phone":{"superadmin","tenant_admin"},
```

**REST `authz` wrapper (`rest.go`):**
```go
// authz wraps a handler with JWT auth + role-tier RBAC for the given
// logical method key, so REST enforces the SAME rpcRoles policy as the
// gRPC interceptor. Replaces a.auth for every authenticated route.
func (a *Adapter) authz(method string, next http.HandlerFunc) http.HandlerFunc {
    return a.auth(func(w http.ResponseWriter, r *http.Request) {
        role, _ := r.Context().Value(middleware.CtxRole).(string)
        if err := middleware.AuthorizeMethod(role, method); err != nil {
            a.grpcError(w, err) // → 403 via grpcToHTTP(PermissionDenied)
            return
        }
        next(w, r)
    })
}
```
Every `mux.HandleFunc(..., a.auth(a.X))` becomes `a.authz("<method-key>", a.X)`. The 2 inline hand-patched checks are removed (the wrapper covers them).

**Default-deny implication (critical):** routing REST through `AuthorizeMethod` means **every authenticated route must have a map key** or it 403s. Build step includes an exhaustive route→key audit (Section 7). `auth/login`, `auth/refresh` stay unauthenticated (`a.login` direct). `auth/logout`, `auth/me` map to existing `Logout`/`GetMe` keys.

### 4.2 GAP 3a — subscription authorization

Gate `handleSubscribe` by conversation access before granting. Requires the hub to resolve the conversation's `assigned_to`. The hub currently has no inbox client — inject a minimal resolver interface:
```go
// ConversationAuthorizer resolves whether a (role,userID) may access a
// conversation. Backed by the gateway handler's authorizeConversationAccess.
type ConversationAuthorizer interface {
    CanAccessConversation(ctx context.Context, role, userID, conversationID string) bool
}
```
`handleSubscribe` calls it; on deny → `sendError("FORBIDDEN", ...)` and does NOT add the subscription. Reuses the exact `canAccessConversation` predicate from `c8f7ebd` (privileged allow; cs → own/unassigned).

### 4.3 GAP 3b — message-event broadcast scoping (see D4)

The events lack ownership data. Two approaches:

**D4-A (gateway resolves, lighter):** the gateway WS message handlers resolve the conversation via the inbox client to get `assigned_to` + `workspace_id`, then fan out with a new scoped broadcast `BroadcastConversationScoped(tenantID, workspaceID, assignedTo, data)` that delivers to: admins in tenant + (assignedTo=="" ? all cs in workspace : that cs). Cost: one gateway→inbox lookup per message event. Race: the conversation may not exist yet when the gateway sees the raw event (inbox creates it from the same event async) → on NotFound, fall back to admin-only delivery + log (message still lands in the list on next REST poll/refresh).

**D4-B (inbox re-publishes enriched event, cleaner):** inbox — which owns conversations and already runs `FindOrCreateConversation` — publishes a new `hermes.inbox.message.new.<tenant>` event carrying `conversation_id, workspace_id, assigned_to, channel`. Gateway subscribes to THAT for WS fan-out (instead of/in addition to the raw wa/mbs inbound) and scopes by `assigned_to`. No race (published only after the row exists), data lives where it's owned. Cost: new event proto + inbox publisher + gateway subscription swap; larger surface.

**Recommendation: D4-B.** It's the correct ownership boundary and eliminates the race. D4-A is acceptable as a faster interim if you want WS scoping shipped this week and the enriched-event refactor later.

**Scoped delivery predicate (either approach):**
```
deliver to client c if:
  c.tenantID == ev.tenant AND
  ( c.role != "cs_agent"                              // admins: all
    OR ev.assignedTo == ""  && c.workspaceID == ev.ws // unassigned: all cs in ws
    OR ev.assignedTo == c.userID )                    // own
```

---

## 5. DECISIONS — LOCKED (2026-06-03)

- **D1 — MBS read RPCs** (`ListSessions`,`GetSessionStatus`,`ListSessionAssets`): **all 4 roles** incl. `cs_agent`. → `{"superadmin","tenant_admin","workspace_admin","cs_agent"}`
- **D2 — MBS send/resolve** (`SendMessage`,`ResolvePhone`): **all 4 roles** incl. `cs_agent`. → `{"superadmin","tenant_admin","workspace_admin","cs_agent"}`
- **D3 — MBS destructive** (`BurnSession`,`RemoveSession`): **superadmin, tenant_admin, workspace_admin** (cs_agent excluded). → `{"superadmin","tenant_admin","workspace_admin"}`
- **D4 — WS broadcast scoping:** **B — inbox publishes enriched event** (`hermes.inbox.message.new.<tenant>`).
- **D5 — WS scoping behavior:** CONFIRMED. cs_agent receives live message events only for own + unassigned (in their workspace); admins receive all in tenant.

---

## 6. CONTRACTS

### 6.1 No public gateway proto change (GAP 1+2)
RBAC is enforcement-layer; `rpcRoles` is a Go map. REST request/response shapes unchanged → **frontend untouched** for 1+2. The only behavioral change: a `cs_agent` now gets `403 PERMISSION_DENIED` on admin-only REST routes it could previously call. (Frontend already hides those controls by role; this closes the API-direct hole.)

### 6.2 Internal proto change — ONLY if D4-B chosen
`docs/contracts/proto/events.proto` (+ inline) — new message + subject:
```proto
// Published by inbox after a conversation is created/updated, carrying the
// ownership fields the gateway needs to scope WS fan-out.
message InboxMessageNewEvent {
  EventMeta meta = 1;            // tenant_id, timestamp
  string conversation_id = 2;
  string workspace_id = 3;
  string assigned_to = 4;        // "" = unassigned
  string channel = 5;            // "wa" | "mbs"
  // Display payload (mirrors current new_message / mbs_new_message fields):
  string contact_name = 6;
  string contact_phone = 7;
  string body = 8;
  string mid = 9;                // wa_message_id or mbs mid
  google.protobuf.Timestamp received_at = 10;
}
```
Subject `hermes.inbox.message.new.<tenant>`; new gateway durable `gateway-inbox-scoped`; stream wiring per existing `ensureStreams` pattern. Additive; no frontend WS frame change (gateway still emits `new_message`/`mbs_new_message` frames — only the *recipient set* narrows).

### 6.3 Middleware API (GAP 1+2)
- New exported `middleware.AuthorizeMethod(role, method string) error`.
- `rpcRoles` gains MBS + REST-only keys (Section 4.1).
- `RBACInterceptor` refactored to delegate to `AuthorizeMethod` (no behavior change on gRPC).

### 6.4 Hub API (GAP 3)
- `handleSubscribe` gains an access check via injected `ConversationAuthorizer`.
- New `Hub.BroadcastConversationScoped(tenantID, workspaceID, assignedTo string, data []byte)` (or the predicate inlined into the MBS/WA message handlers).
- `NewHub` / `NewEventSubscriber` gain the authorizer/inbox dependency (threaded from `cmd/gateway/main.go`, which already holds `inboxClient` + handler).

### 6.5 No change
- DB schema / migrations.
- `ClaimConversation`, conversation-access REST gates (already shipped).
- Public gateway proto, frontend code (unless a future toggle hides newly-403'd controls — they're already role-gated in UI).

---

## 7. BUILD ORDER

1. **Middleware:** extract `AuthorizeMethod`, refactor `RBACInterceptor` to use it, add MBS + REST-only keys. Unit tests: every key resolves; superadmin bypass; unknown → deny.
2. **REST authz:** add `authz` wrapper; convert all authenticated routes `auth → authz` with exhaustive method keys; delete the 2 inline checks. Test: route table audit — every authenticated route has a key (table test enumerating the mux).
3. **GAP 3a:** inject `ConversationAuthorizer` into hub; gate `handleSubscribe`; tests (own/unassigned/other × roles).
4. **GAP 3b (per D4):**
   - **If B:** events.proto `InboxMessageNewEvent` + buf regen; inbox publisher after `FindOrCreateConversation`; gateway subscribes + scopes; deprecate tenant-wide message fan-out.
   - **If A:** gateway message handlers resolve via inbox + `BroadcastConversationScoped`; NotFound → admin-only fallback.
   - Tests: scoping predicate matrix; race/fallback path.
5. `go build ./...`, `go vet`, `internal/gateway/...` + `internal/inbox/...` suites.
6. Build `hermes-gateway` (+ `hermes-inbox` if D4-B); deploy **serially** (cold dep cache — parallel builds saturate proxy.golang.org, see prod-iteration trap); recreate via `docker-compose.prod.yml --env-file .env.prod`.
7. Live verify (Section 8).

## 8. LIVE VERIFICATION

- **GAP 1+2:** as `cs@hermes.local` over REST: `POST /users` → 403; `POST /campaigns` → 403; `DELETE /mbs-sessions/{uid}` → 403; `GET /mbs-sessions` → per D1; `POST /mbs-sessions/{uid}/messages` → per D2; `GET /conversations` still 200. As admin: all the above still 200.
- **GAP 3a:** cs WS `subscribe_conversation` to another agent's conv → error frame, no events; to own/unassigned → ok.
- **GAP 3b:** assign a conv to cs1; send inbound to it; cs2 WS must NOT receive `new_message`; cs1 + admin must. Send inbound to an unassigned conv; all cs in workspace receive it.
- Restore any mutated test data.

## 9. RISKS / NOTES

- **Default-deny coverage (GAP 1):** the single biggest break risk — any authenticated REST route missing a map key returns 403. Mitigated by the Section-7.2 route audit test that fails the build if a route lacks a key.
- **WS scoping race (D4-A only):** raw event may precede conversation creation → admin-only fallback + list reconciles on next fetch. D4-B avoids this entirely.
- **Frontend 403 UX:** controls are already role-hidden, so direct-API 403s shouldn't surface to normal use. If any legitimate cs_agent flow breaks, the role map entry is the fix (one line), not a code change.
- Orthogonal follow-ups untouched: refresh-token rotation, WS workspace dimension for non-message events.

---

## 10. STATUS
Plan only. **Blocked on D1–D5.** On sign-off, build in the Section-7 order; spec + contracts above are implementation-ready.
