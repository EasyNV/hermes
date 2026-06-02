package rest

import (
	"encoding/json"
	"net/http"
	"strconv"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
)

// ─────────────────────────────────────────────────────────────────────
// Helpers (kept local to MBS handlers — they're trivial and don't
// belong in the broader REST helpers file)
// ─────────────────────────────────────────────────────────────────────

// parseStateFilter converts a query-string MBS state into the proto enum.
// Empty or unknown → UNSPECIFIED, which the backend treats as "no filter".
// Only accepts the protojson string form (matches what the chunk-4
// frontend client serializes).
func parseStateFilter(s string) hermesv1.MbsSessionState {
	switch s {
	case "MBS_SESSION_STATE_ACTIVE":
		return hermesv1.MbsSessionState_MBS_SESSION_STATE_ACTIVE
	case "MBS_SESSION_STATE_SUSPENDED":
		return hermesv1.MbsSessionState_MBS_SESSION_STATE_SUSPENDED
	case "MBS_SESSION_STATE_BURNED":
		return hermesv1.MbsSessionState_MBS_SESSION_STATE_BURNED
	case "MBS_SESSION_STATE_BRIDGING":
		return hermesv1.MbsSessionState_MBS_SESSION_STATE_BRIDGING
	}
	return hermesv1.MbsSessionState_MBS_SESSION_STATE_UNSPECIFIED
}

// parseUIDPath extracts the {uid} path parameter as int64. On any
// failure, writes a 400 INVALID_UID error directly and returns ok=false
// so the caller short-circuits.
//
// Uid 0 is treated as invalid — backend treats it as "no session" and
// the wire shape always carries a positive int64.
func parseUIDPath(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := r.PathValue("uid")
	if raw == "" {
		writeMbsError(w, http.StatusBadRequest, "INVALID_UID", "missing uid path parameter")
		return 0, false
	}
	uid, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || uid <= 0 {
		writeMbsError(w, http.StatusBadRequest, "INVALID_UID", "uid must be a positive int64")
		return 0, false
	}
	return uid, true
}

// writeMbsError is a tiny free-function variant of (*Adapter).writeError
// so the parse helpers don't need an Adapter receiver. The shape
// matches the canonical error format `{"code","message"}`.
//
// Renamed from `writeError` to avoid collision with the canonical
// Adapter.writeError method (which has the same name but is a method).
func writeMbsError(w http.ResponseWriter, statusCode int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": message})
}

// ─────────────────────────────────────────────────────────────────────
// REST handlers
// ─────────────────────────────────────────────────────────────────────

// listMbsSessions
//
//	GET /api/v1/mbs-sessions?stateFilter=...&page=1&pageSize=25&tenantId=...
//
// tenantId is optional from the client — chunk-1's forceTenantFromJWT
// fills it from the JWT when empty and rejects mismatches.
func (a *Adapter) listMbsSessions(w http.ResponseWriter, r *http.Request) {
	// Accept both `state` (what the chunk-4 frontend client serializes via
	// listMbsSessions params) and `stateFilter` (the original contract name).
	// They were out of sync: the client sent `state`, the handler only read
	// `stateFilter`, so the server-side filter was silently dropped and the
	// campaign picker received burned/suspended sessions. `state` wins when
	// both are present; empty/unknown → UNSPECIFIED → "no filter".
	rawState := r.URL.Query().Get("state")
	if rawState == "" {
		rawState = r.URL.Query().Get("stateFilter")
	}
	req := &hermesv1.ListMbsSessionsRequest{
		TenantId:    r.URL.Query().Get("tenantId"),
		StateFilter: parseStateFilter(rawState),
		Page:        pagination(r),
	}
	resp, err := a.mbs.ListMbsSessions(r.Context(), req)
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

// getMbsSession
//
//	GET /api/v1/mbs-sessions/{uid}
func (a *Adapter) getMbsSession(w http.ResponseWriter, r *http.Request) {
	uid, ok := parseUIDPath(w, r)
	if !ok {
		return
	}
	resp, err := a.mbs.GetMbsSessionStatus(r.Context(), &hermesv1.GetMbsSessionStatusRequest{Uid: uid})
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

// listMbsSessionAssets
//
//	GET /api/v1/mbs-sessions/{uid}/assets
func (a *Adapter) listMbsSessionAssets(w http.ResponseWriter, r *http.Request) {
	uid, ok := parseUIDPath(w, r)
	if !ok {
		return
	}
	resp, err := a.mbs.ListSessionAssets(r.Context(), &hermesv1.ListSessionAssetsRequest{Uid: uid})
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

// burnMbsSession
//
//	POST /api/v1/mbs-sessions/{uid}/burn
//	body: {"reason":"..."}  — optional
func (a *Adapter) burnMbsSession(w http.ResponseWriter, r *http.Request) {
	uid, ok := parseUIDPath(w, r)
	if !ok {
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	// Empty body is OK; reason is informational. Ignore unmarshal
	// errors on empty bodies — readJSON tolerates zero-length input.
	_ = readJSON(r, &body)
	resp, err := a.mbs.BurnMbsSession(r.Context(), &hermesv1.BurnMbsSessionRequest{
		Uid:    uid,
		Reason: body.Reason,
	})
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

// resolveMbsPhone
//
//	POST /api/v1/mbs-sessions/{uid}/resolve-phone
//	body: {phone, pageIdOverride?, bypassCache?}
//
// The {uid} path param is the source of truth; if the body sets uid
// we overwrite it after readProto to defend against a client that
// mismatches path and body.
func (a *Adapter) resolveMbsPhone(w http.ResponseWriter, r *http.Request) {
	uid, ok := parseUIDPath(w, r)
	if !ok {
		return
	}
	req := &hermesv1.ResolvePhoneRequest{}
	if err := readProto(r, req); err != nil {
		writeMbsError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}
	req.Uid = uid // path wins, defend against body-supplied mismatch
	resp, err := a.mbs.ResolveMbsPhone(r.Context(), req)
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

// sendMbsMessage
//
//	POST /api/v1/mbs-sessions/{uid}/messages
//	body: MbsSendMessageRequest (oneof recipient + text)
//
// {uid} path wins over any body-supplied uid (same rationale as
// resolveMbsPhone).
func (a *Adapter) sendMbsMessage(w http.ResponseWriter, r *http.Request) {
	uid, ok := parseUIDPath(w, r)
	if !ok {
		return
	}
	req := &hermesv1.MbsSendMessageRequest{}
	if err := readProto(r, req); err != nil {
		writeMbsError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}
	req.Uid = uid
	resp, err := a.mbs.SendMbsMessage(r.Context(), req)
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}
