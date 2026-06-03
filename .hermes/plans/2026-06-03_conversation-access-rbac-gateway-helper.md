# Conversation-Access RBAC — Gateway Helper (Approach A)

**Date:** 2026-06-03
**Author:** Oracle
**Status:** APPROVED — build + integrate
**Scope:** Enforce per-conversation read/write ownership for `cs_agent` at the gateway handler layer.

---

## 1. Problem

The system promises (docstrings/comments) a per-conversation ownership model for CS agents,
but the code enforces it at **1 of 8** conversation-scoped endpoints. The intent lives in
prose, not in code — same class of defect as the RBAC REST-bypass.

### Ownership model (authoritative, from operator)

- **Unassigned conversation** (`assigned_to IS NULL`) → **any** `cs_agent` may read + claim.
- **Assigned to a cs_agent** → **only that agent + higher roles**
  (`workspace_admin`, `tenant_admin`, `superadmin`) may read or act on it.

### Current enforcement audit

| Endpoint | Keyed by | Current | Verdict |
|---|---|---|---|
| `ClaimConversation` | id | `UPDATE ... WHERE id=$1 AND status='unassigned'` → `ErrAlreadyAssigned` | ✅ correct, leave |
| `ListConversations` | workspace | `WHERE assigned_to = $self` (literal eq) — drops unassigned | ❌ broken (empty inbox) |
| `GetConversation` | id | none | ❌ any cs reads any |
| `ListMessages` | conversation_id | none | ❌ any cs reads any history |
| `SearchMessages` | workspace | no scope | ❌ leaks other agents' bodies |
| `SendMessage` | conversation_id | none | ❌ any cs sends into any |
| `TransferConversation` | id | `UPDATE ... WHERE id=$1` (ignores FromUserId) | ❌ any cs reassigns any |
| `CloseConversation` | id | `UPDATE ... WHERE id=$1` | ❌ any cs closes any |

### Live DB ground truth (prod stack, 2026-06-03)

3 conversations, all `status=unassigned`, `assigned_to=NULL`. With the broken
`WHERE assigned_to=$self` filter, `cs@hermes.local` matches 0 rows → empty inbox.

---

## 2. Approach — A (Gateway Helper)

Enforce inside the **gateway handler methods** (`internal/gateway/handler/handler.go`),
NOT in a gRPC interceptor.

### Why this is correct (key insight)

The React SPA talks **REST only** (`web/src/api/client.ts` → `fetch('/api/v1/*')`).
REST handlers (`internal/gateway/rest/handlers.go`) dispatch **in-process** to the same
`a.gw.<Method>(...)` gateway handlers — they do NOT traverse the gRPC interceptor chain.

Therefore:
- The RBAC **interceptor** (`middleware/rbac.go`) is bypassed by REST. (Separate gap;
  see the RBAC-unification follow-up.)
- Any check placed **inside the gateway handler method** runs on **both** transports
  (gRPC and REST), exactly like the existing `requireTenant` (MBS) and the current
  cs_agent `ListConversations` injection.

So `canAccess` placed in the handler methods closes the hole for the production REST
frontend with zero interceptor dependency. This is the decisive reason A is chosen over
a pure-interceptor design.

### A vs B recap

- **A (chosen):** gateway helper for the 5 id-keyed endpoints (resolve conversation,
  apply predicate). List + Search get a minimal **internal** (gateway↔inbox) request-field
  addition because they are not id-keyed and cannot be correctly post-filtered at the
  gateway without breaking pagination. No **public** gateway proto change → frontend
  untouched.
- **B (rejected for now):** thread caller identity + privileged flag through all 6 inbox
  RPCs and enforce in every SQL query. Larger surface, 6 proto messages, more buf churn.
  Can migrate to B later if round-trip cost ever matters (irrelevant at ~20–30 users).

---

## 3. Spec

### 3.1 Pure predicate (single source of truth)

```go
// canAccessConversation reports whether a caller with the given role and user ID
// may read/act on a conversation with the given assigned_to value.
//
//   privileged (role != cs_agent)        -> true   (admins see everything)
//   cs_agent, assignedTo == ""           -> true   (unassigned: open to all CS)
//   cs_agent, assignedTo == callerUserID -> true   (own)
//   cs_agent, otherwise                  -> false  (someone else's)
func canAccessConversation(role, callerUserID, assignedTo string) bool {
    if role != "cs_agent" {
        return true
    }
    return assignedTo == "" || assignedTo == callerUserID
}
```

### 3.2 Resolve-and-authorize wrapper (id-keyed endpoints)

```go
// authorizeConversationAccess resolves the conversation's assigned_to via the inbox
// service and enforces canAccessConversation. Privileged roles skip the round trip.
// Returns:
//   nil                       -> allowed
//   codes.PermissionDenied    -> cs_agent lacks access
//   propagated inbox error    -> NotFound / Unavailable / Internal
func (h *Handler) authorizeConversationAccess(ctx context.Context, convID string) error {
    role := middleware.RoleFromCtx(ctx)
    if role != "cs_agent" {
        return nil // privileged: full access, no extra fetch
    }
    if h.inboxClient == nil {
        return status.Error(codes.Unavailable, "inbox service not available")
    }
    resp, err := h.inboxClient.GetConversation(ctx, &hermesv1.InboxGetConversationRequest{Id: convID})
    if err != nil {
        return err // propagate NotFound etc. unchanged
    }
    userID := middleware.UserIDFromCtx(ctx)
    if !canAccessConversation(role, userID, resp.GetConversation().GetAssignedTo()) {
        return status.Error(codes.PermissionDenied, "conversation assigned to another agent")
    }
    return nil
}
```

### 3.3 Per-endpoint integration

**`GetConversation`** — already fetches the conversation from inbox. Apply predicate
inline on the fetched row (no extra round trip):
```go
role := middleware.RoleFromCtx(ctx); userID := middleware.UserIDFromCtx(ctx)
if !canAccessConversation(role, userID, resp.GetConversation().GetAssignedTo()) {
    return nil, status.Error(codes.PermissionDenied, "conversation assigned to another agent")
}
```

**`ListMessages`** — `authorizeConversationAccess(ctx, req.GetConversationId())` before forwarding.

**`SendMessage`** — `authorizeConversationAccess(ctx, req.GetConversationId())` before forwarding.

**`TransferConversation`** — `authorizeConversationAccess(ctx, req.GetId())` before forwarding.
(Read access to a conversation implies the right to transfer/close it under this model:
own or unassigned. A cs cannot touch another agent's conversation.)

**`CloseConversation`** — `authorizeConversationAccess(ctx, req.GetId())` before forwarding.

**`ListConversations`** — replace the literal-equality injection. For `cs_agent`:
set `assigned_to = self` AND new `include_unassigned = true`. Store applies an OR.

**`SearchMessages`** — for `cs_agent`: pass new `requester_user_id = self` AND
`include_unassigned = true`. Store joins `conversations` and filters scope.

### 3.4 Claim — unchanged

`ClaimConversation` already enforces "claim only unassigned" atomically
(`WHERE id=$1 AND status='unassigned'` → `ErrAlreadyAssigned`). Matches the model. Leave it.

---

## 4. Contracts

### 4.1 Internal proto additions (gateway↔inbox only; NO public gateway change)

`docs/contracts/proto/inbox.proto` (and synced inline `proto/hermes/v1/inbox.proto`):

```proto
message InboxListConversationsRequest {
  string workspace_id = 1;
  ConversationStatus status = 2;
  string assigned_to = 3;
  string wa_number_id = 4;
  string search = 5;
  ConversationSortOrder sort_order = 6;
  PageRequest pagination = 7;
  InboxChannel channel = 8;
  // RBAC scope: when true AND assigned_to is set, the store returns conversations
  // assigned to `assigned_to` OR unassigned (assigned_to IS NULL). Used by the
  // gateway for cs_agent callers (own + unassigned). Admins leave this false so an
  // explicit assigned_to filter remains an exact match.
  bool include_unassigned = 9;
}

message InboxSearchMessagesRequest {
  string workspace_id = 1;
  string query = 2;
  string conversation_id = 3;
  google.protobuf.Timestamp from_date = 4;
  google.protobuf.Timestamp to_date = 5;
  PageRequest pagination = 6;
  // RBAC scope: when set, restrict hits to conversations assigned to this user
  // (cs_agent's own). Empty = no assignee restriction (admin).
  string requester_user_id = 7;
  // When true AND requester_user_id is set, also include unassigned conversations.
  bool include_unassigned = 8;
}
```

Field numbers 9 (list) and 7/8 (search) are new, additive, backward-compatible.
`gen/` is gitignored → regenerate via buf with the PATH workaround; commit proto source only.

### 4.2 Inbox store signature changes

`internal/inbox/handler/store.go` + `Store` interface + mocks:

```go
// ListConversations: add includeUnassigned bool
func (s *PgStore) ListConversations(ctx, workspaceID, statusFilter, assignedTo,
    waNumberID, search, channel string, sortOrder int32,
    includeUnassigned bool, page, pageSize int32) ([]*ConversationRow, int64, error)
```
SQL (the assigned_to branch):
```go
if assignedTo != "" {
    if includeUnassigned {
        where += fmt.Sprintf(" AND (c.assigned_to = $%d OR c.assigned_to IS NULL)", idx)
    } else {
        where += fmt.Sprintf(" AND c.assigned_to = $%d", idx)
    }
    args = append(args, assignedTo); idx++
}
```

```go
// SearchMessages: add requesterUserID string, includeUnassigned bool
func (s *PgStore) SearchMessages(ctx, workspaceID, query, conversationID string,
    requesterUserID string, includeUnassigned bool,
    fromDate, toDate *time.Time, page, pageSize int32) ([]*SearchHitRow, int64, error)
```
SQL (added to WHERE; `c` is the joined conversations alias already present):
```go
if requesterUserID != "" {
    if includeUnassigned {
        where += fmt.Sprintf(" AND (c.assigned_to = $%d OR c.assigned_to IS NULL)", idx)
    } else {
        where += fmt.Sprintf(" AND c.assigned_to = $%d", idx)
    }
    args = append(args, requesterUserID); idx++
}
```

Callers to update: inbox `handler.go` (`ListConversations`, `SearchMessages` RPCs),
`mockStore` in `internal/inbox/handler/handler_test.go`, any `cmd/inbox` fake.

### 4.3 Gateway handler changes

`internal/gateway/handler/handler.go`:
- New `canAccessConversation(role, callerUserID, assignedTo string) bool`.
- New `(h *Handler) authorizeConversationAccess(ctx, convID string) error`.
- `ListConversations`: cs_agent branch sets `inboxReq.IncludeUnassigned = true`
  (keep `AssignedTo = userID`). Drop the now-false "backend interprets" comment;
  keep the explicit-other-agent guard (block cs requesting another agent's id).
- `GetConversation`: inline predicate on fetched row.
- `ListMessages`, `SendMessage`, `TransferConversation`, `CloseConversation`:
  `authorizeConversationAccess` guard before forwarding.
- `SearchMessages`: cs_agent branch sets `RequesterUserId = userID`,
  `IncludeUnassigned = true`.

### 4.4 No changes

- Public gateway proto (`gateway.proto`) — unchanged → **frontend untouched**.
- `ClaimConversation` — unchanged.
- DB schema / migrations — unchanged (uses existing `conversations.assigned_to`).
- RBAC interceptor map — unchanged (this is orthogonal; the separate REST-bypass
  unification still stands as its own task).

---

## 5. Error semantics

| Condition | Code |
|---|---|
| cs_agent reads/acts on another agent's conversation | `PermissionDenied` |
| conversation id not found (during authorize fetch) | `NotFound` (propagated) |
| inbox unavailable during authorize | `Unavailable` |
| cs_agent reads/acts on own or unassigned | OK |
| privileged role any conversation | OK (no extra fetch) |

Ordering: validate args → authorize → forward. Authorize before side effects.

---

## 6. Test matrix (`internal/gateway/handler/handler_test.go`)

Per gated endpoint, table-driven over (role × assigned_to):

| role | conv.assigned_to | expect |
|---|---|---|
| cs_agent | "" (unassigned) | OK |
| cs_agent | self | OK |
| cs_agent | other-agent | PermissionDenied |
| workspace_admin | other-agent | OK |
| tenant_admin | other-agent | OK |
| superadmin | other-agent | OK (no inbox fetch — assert mock not called) |

Plus:
- `ListConversations`: assert cs sets `IncludeUnassigned=true` + `AssignedTo=self`;
  admin no-filter sets neither; admin explicit `assigned_to=X` → `IncludeUnassigned=false`.
- `SearchMessages`: assert cs sets `RequesterUserId=self` + `IncludeUnassigned=true`;
  admin sets neither.
- Inbox store tests: `ListConversations`/`SearchMessages` OR-branch returns
  own+unassigned, exact branch returns only the named assignee.

Harness: extend `mockInboxClient` with `getConversationFn`, `listMessagesFn`,
`sendMessageFn`, `transferConversationFn`, `closeConversationFn`, `searchMessagesFn`
(pattern already established by `listConversationsFn`).

---

## 7. Build order

1. Proto: add fields to `docs/contracts/proto/inbox.proto`, sync inline `proto/`, buf regen
   (`PATH="$HOME/go/bin:$HOME/.hermes/profiles/oracle/home/go/bin:$PATH" buf generate`).
2. Inbox store: signatures + SQL OR-branches; update `Store` interface.
3. Inbox handler RPCs: pass new fields through.
4. Inbox mocks/fakes: update signatures.
5. Gateway: `canAccessConversation` + `authorizeConversationAccess`; wire 7 endpoints.
6. Tests: gateway access matrix + inbox store scope tests.
7. `go build ./...` + `go vet`; run `internal/gateway/...` + `internal/inbox/...` suites.
8. Build images `hermes-gateway`, `hermes-inbox`; deploy to prod stack.
9. Live verify with `cs@hermes.local`: empty→sees 3 unassigned; claim one;
   second (simulated) cs cannot read it; admin reads everything.

---

## 8. Verification (live)

```
# cs token
CS=$(curl -s .../auth/login -d '{"email":"cs@hermes.local","password":"cs123"}' | jq -r .accessToken)
# 1) cs now sees the 3 unassigned
curl -s .../conversations?workspaceId=...0010 -H "Authorization: Bearer $CS" | jq '.conversations|length'  # expect 3
# 2) cs claims one
curl -s -X POST .../conversations/<id>/claim -H "Authorization: Bearer $CS"
# 3) cs can read its messages
curl -s .../conversations/<id>/messages -H "Authorization: Bearer $CS"   # 200
# 4) admin still sees all
curl -s .../conversations?workspaceId=...0010 -H "Authorization: Bearer $ADMIN" | jq '.conversations|length'  # 3
```
(Single live cs user; "other agent" denial proven by unit tests + a transient second user if needed.)

---

## 9. Out of scope (tracked separately)

- RBAC interceptor REST-bypass unification (role-tier gate for REST) — prior approved task.
- MBS RPCs absent from `rpcRoles` map.
- WS event fan-out per-conversation scoping (cs receiving `new_message` WS for
  conversations they can't read) — note for follow-up; this plan covers request/response RPCs.
```
