package handler

import (
	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/hermes-waba/hermes/internal/mbs/store"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// dbStateToProto maps the canonical mbs_sessions.state string column
// to the proto enum. "bridging" is not persisted in chunk 4 (it's a
// transient in-process state during BridgeLogin) but the mapping is
// listed for symmetry / future use.
func dbStateToProto(s string) hermesv1.MbsSessionState {
	switch s {
	case "active":
		return hermesv1.MbsSessionState_MBS_SESSION_STATE_ACTIVE
	case "suspended":
		return hermesv1.MbsSessionState_MBS_SESSION_STATE_SUSPENDED
	case "burned":
		return hermesv1.MbsSessionState_MBS_SESSION_STATE_BURNED
	case "bridging":
		return hermesv1.MbsSessionState_MBS_SESSION_STATE_BRIDGING
	default:
		return hermesv1.MbsSessionState_MBS_SESSION_STATE_UNSPECIFIED
	}
}

// protoStateToDB is the reverse mapping. UNSPECIFIED → "" (no filter).
// BRIDGING is included for symmetry but should never be passed by the
// gateway — it's a transient state owned by the handler itself.
func protoStateToDB(s hermesv1.MbsSessionState) string {
	switch s {
	case hermesv1.MbsSessionState_MBS_SESSION_STATE_ACTIVE:
		return "active"
	case hermesv1.MbsSessionState_MBS_SESSION_STATE_SUSPENDED:
		return "suspended"
	case hermesv1.MbsSessionState_MBS_SESSION_STATE_BURNED:
		return "burned"
	case hermesv1.MbsSessionState_MBS_SESSION_STATE_BRIDGING:
		return "bridging"
	default:
		return ""
	}
}

// stateToSubjectFragment derives the {state} component of NATS
// subjects like hermes.mbs.session.{state}.{tenant_id}. Returns "" if
// the state shouldn't emit a lifecycle event (e.g. UNSPECIFIED, or
// the transient BRIDGING state which is in-process only).
func stateToSubjectFragment(s hermesv1.MbsSessionState) string {
	switch s {
	case hermesv1.MbsSessionState_MBS_SESSION_STATE_ACTIVE:
		// "active" lifecycle transitions are surfaced as "connected"
		// (or "refreshed" when triggered by the refresh ticker).
		// Lifecycle publishers always pass the explicit fragment via
		// PublishSessionLifecycle's reason hint; this function is the
		// fallback when reason doesn't override.
		return "connected"
	case hermesv1.MbsSessionState_MBS_SESSION_STATE_SUSPENDED:
		return "disconnected"
	case hermesv1.MbsSessionState_MBS_SESSION_STATE_BURNED:
		return "burned"
	default:
		return ""
	}
}

// sessionRowToProto converts a store.SessionRow + optional primary
// AssetRow to the proto MbsSessionInfo. primary may be nil.
//
// Timestamp policy: nullable *time.Time → nil-safe Timestamp (omit if
// stored value is nil). Plain time.Time → always emit (zero value is
// 1970 which is fine for diagnostics).
func sessionRowToProto(row *store.SessionRow, primary *store.AssetRow) *hermesv1.MbsSessionInfo {
	if row == nil {
		return nil
	}
	out := &hermesv1.MbsSessionInfo{
		Uid:         row.UID,
		TenantId:    row.TenantID,
		DisplayName: row.DisplayName,
		LoginEmail:  row.LoginEmail,
		State:       dbStateToProto(row.State),
		DeviceId:    row.DeviceID,
		AppVersion:  row.AppVersion,
		CreatedAt:   timestamppb.New(row.CreatedAt),
		UpdatedAt:   timestamppb.New(row.UpdatedAt),
	}
	if row.LastConnackRC != nil {
		out.LastConnackRc = int32(*row.LastConnackRC)
	}
	if row.LastConnackAt != nil {
		out.LastConnackAt = timestamppb.New(*row.LastConnackAt)
	}
	// proxy_id is stored directly on the session row (sticky pin). The
	// display-only proxy_label + proxy_status require a proxy-service
	// lookup and are enriched separately (enrichProxyDisplay) on the RPCs
	// that have a proxy client + ctx — keeping this converter pure.
	if row.ProxyID != nil {
		out.ProxyId = *row.ProxyID
	}
	if primary != nil {
		out.PrimaryAsset = assetRowToProto(primary)
	}
	return out
}

// assetRowToProto converts a store.AssetRow to MbsAsset. Returns nil
// on nil input.
func assetRowToProto(r *store.AssetRow) *hermesv1.MbsAsset {
	if r == nil {
		return nil
	}
	return &hermesv1.MbsAsset{
		PageId:                 r.PageID,
		PageName:               r.PageName,
		WabaId:                 r.WabaID,
		WecMailboxId:           r.WecMailboxID,
		WecPhoneNumber:         r.WecPhoneNumber,
		BusinessPresenceNodeId: r.BusinessPresenceNodeID,
		IgAccountId:            r.IgAccountID,
		HasWaba:                r.WabaID != "" || r.WecMailboxID != "",
		// ── Stage F follow-up chunk 4 (2026-05-30) ──
		// Previously dropped on the wire even though store.AssetRow
		// already carried them. UI needs all four to render the
		// asset card properly (PRIMARY badge, business parent,
		// WEC registration status).
		BusinessId:           r.BusinessID,
		BusinessName:         r.BusinessName,
		IsPrimary:            r.IsPrimary,
		WecAccountRegistered: r.WECAccountRegistered,
	}
}

// assetRowsToProto converts a slice of store.AssetRow to the proto
// slice, preserving order and skipping nils.
func assetRowsToProto(rows []*store.AssetRow) []*hermesv1.MbsAsset {
	out := make([]*hermesv1.MbsAsset, 0, len(rows))
	for _, r := range rows {
		if r == nil {
			continue
		}
		out = append(out, assetRowToProto(r))
	}
	return out
}

// findPrimaryAsset returns the asset row with IsPrimary=true, or the
// first row if no row is flagged primary. Returns nil if rows is empty.
// Mirrors session.pickPrimary semantics; duplicated here to avoid
// pulling the session package into proto_conv.
func findPrimaryAsset(rows []*store.AssetRow) *store.AssetRow {
	if len(rows) == 0 {
		return nil
	}
	for _, r := range rows {
		if r != nil && r.IsPrimary {
			return r
		}
	}
	return rows[0]
}
