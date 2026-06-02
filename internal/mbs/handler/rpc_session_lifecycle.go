package handler

import (
	"context"
	"errors"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/hermes-waba/hermes/internal/mbs/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Lifecycle RPCs: ListSessions, GetSessionStatus, ListSessionAssets,
// BurnSession. All four share the same shape:
//
//  1. requireTenant(ctx) — Unauthenticated if missing
//  2. Verify session.TenantID matches ctx tenant (cross-check)
//  3. Do the work, map errors via mapStoreErr
//
// Defense in depth: even if the gateway forwarded a spoofed
// x-tenant-id, the cross-check stops the request at step 2.

// ─────────────────────────────────────────────────────────────────────
// ListSessions
// ─────────────────────────────────────────────────────────────────────

// paginate converts a PageRequest to (limit, offset) using:
//   - 1-based page → 0-based offset
//   - PageSize default 50, clamped [1,200]
//   - Page default 1
func paginate(p *hermesv1.PageRequest) (limit, offset int) {
	page := int32(1)
	pageSize := int32(50)
	if p != nil {
		if p.Page > 0 {
			page = p.Page
		}
		if p.PageSize > 0 {
			pageSize = p.PageSize
		}
	}
	if pageSize < 1 {
		pageSize = 1
	}
	if pageSize > 200 {
		pageSize = 200
	}
	return int(pageSize), int(page-1) * int(pageSize)
}

// requestedPage returns the effective (page, pageSize) for echoing
// back in the PageResponse — must match what paginate clamped.
func requestedPage(p *hermesv1.PageRequest) (page, pageSize int32) {
	page = int32(1)
	pageSize = int32(50)
	if p != nil {
		if p.Page > 0 {
			page = p.Page
		}
		if p.PageSize > 0 {
			pageSize = p.PageSize
		}
	}
	if pageSize < 1 {
		pageSize = 1
	}
	if pageSize > 200 {
		pageSize = 200
	}
	return page, pageSize
}

// totalPages = ceil(total / pageSize). Zero total → zero pages.
func totalPages(total int64, pageSize int32) int32 {
	if total <= 0 || pageSize <= 0 {
		return 0
	}
	return int32((total + int64(pageSize) - 1) / int64(pageSize))
}

// ListSessions returns paginated tenant-scoped sessions with their
// primary asset enriched (best-effort lookup).
func (h *Handler) ListSessions(ctx context.Context, req *hermesv1.ListMbsSessionsRequest) (*hermesv1.ListMbsSessionsResponse, error) {
	tenantID, err := requireTenant(ctx)
	if err != nil {
		return nil, err
	}
	// If the request body explicitly sets a tenant_id, it MUST match
	// the metadata tenant. This catches "client thinks they're filtering
	// by tenant-B while their JWT says tenant-A" — fail closed.
	if req.TenantId != "" && req.TenantId != tenantID {
		return nil, status.Error(codes.PermissionDenied, "tenant_id in request does not match caller tenant")
	}

	stateFilter := protoStateToDB(req.StateFilter)
	limit, offset := paginate(req.Page)

	rows, total, err := h.store.ListSessions(ctx, tenantID, stateFilter, limit, offset)
	if err != nil {
		return nil, mapStoreErr(err)
	}

	// Enrich each row's primary asset (best-effort — empty if no
	// assets discovered yet, or if a lookup error occurs).
	sessions := make([]*hermesv1.MbsSessionInfo, 0, len(rows))
	for _, r := range rows {
		primary := h.lookupPrimaryAsset(ctx, r.UID)
		sessions = append(sessions, sessionRowToProto(r, primary))
	}

	page, pageSize := requestedPage(req.Page)
	return &hermesv1.ListMbsSessionsResponse{
		Sessions: sessions,
		Page: &hermesv1.PageResponse{
			Total:      int64(total),
			Page:       page,
			PageSize:   pageSize,
			TotalPages: totalPages(int64(total), pageSize),
		},
	}, nil
}

// ─────────────────────────────────────────────────────────────────────
// GetSessionStatus
// ─────────────────────────────────────────────────────────────────────

func (h *Handler) GetSessionStatus(ctx context.Context, req *hermesv1.GetMbsSessionStatusRequest) (*hermesv1.GetMbsSessionStatusResponse, error) {
	tenantID, err := requireTenant(ctx)
	if err != nil {
		return nil, err
	}
	if req.Uid == 0 {
		return nil, status.Error(codes.InvalidArgument, "uid is required")
	}

	row, err := h.store.GetSessionByTenant(ctx, tenantID, req.Uid)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	primary := h.lookupPrimaryAsset(ctx, row.UID)
	return &hermesv1.GetMbsSessionStatusResponse{
		Session: sessionRowToProto(row, primary),
	}, nil
}

// ─────────────────────────────────────────────────────────────────────
// ListSessionAssets
// ─────────────────────────────────────────────────────────────────────

func (h *Handler) ListSessionAssets(ctx context.Context, req *hermesv1.ListSessionAssetsRequest) (*hermesv1.ListSessionAssetsResponse, error) {
	tenantID, err := requireTenant(ctx)
	if err != nil {
		return nil, err
	}
	if req.Uid == 0 {
		return nil, status.Error(codes.InvalidArgument, "uid is required")
	}

	// Tenant cross-check first — caller must be entitled to this uid
	// before we hand them the asset list.
	if _, err := h.store.GetSessionByTenant(ctx, tenantID, req.Uid); err != nil {
		return nil, mapStoreErr(err)
	}

	assets, err := h.store.ListAssets(ctx, req.Uid)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	return &hermesv1.ListSessionAssetsResponse{
		Assets: assetRowsToProto(assets),
	}, nil
}

// ─────────────────────────────────────────────────────────────────────
// BurnSession
// ─────────────────────────────────────────────────────────────────────

func (h *Handler) BurnSession(ctx context.Context, req *hermesv1.BurnMbsSessionRequest) (*hermesv1.BurnMbsSessionResponse, error) {
	tenantID, err := requireTenant(ctx)
	if err != nil {
		return nil, err
	}
	if req.Uid == 0 {
		return nil, status.Error(codes.InvalidArgument, "uid is required")
	}

	// 1. Verify tenant ownership + capture prior state (for lifecycle event).
	row, err := h.store.GetSessionByTenant(ctx, tenantID, req.Uid)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	prevState := dbStateToProto(row.State)

	// 2. Tear down any in-memory connection (best-effort — Disconnect
	// is idempotent and no-ops if uid isn't currently connected).
	// Errors here are non-fatal; the burn still proceeds.
	if h.manager != nil {
		_ = h.manager.Disconnect(req.Uid)
	}

	// 3. Mark burned. This also releases pod_id (PgStore does both
	// in one UPDATE).
	if err := h.store.BurnSession(ctx, req.Uid, req.Reason); err != nil {
		return nil, mapStoreErr(err)
	}

	// 4. Re-read to return the updated row + emit lifecycle event.
	updated, err := h.store.GetSession(ctx, req.Uid)
	if err != nil {
		// Burn succeeded but follow-up read failed — that's odd but
		// not the caller's problem. Construct a best-effort response
		// from the pre-burn row with state flipped.
		row.State = "burned"
		updated = row
	}
	h.publisher.PublishSessionLifecycle(
		req.Uid, tenantID,
		prevState, hermesv1.MbsSessionState_MBS_SESSION_STATE_BURNED,
		"burned", 0, h.podID,
	)

	primary := h.lookupPrimaryAsset(ctx, req.Uid)
	return &hermesv1.BurnMbsSessionResponse{
		Session: sessionRowToProto(updated, primary),
	}, nil
}

// ─────────────────────────────────────────────────────────────────────
// RemoveSession
// ─────────────────────────────────────────────────────────────────────

// RemoveSession permanently deletes a session row (and cascades its
// assets + phone-thread cache via ON DELETE CASCADE FKs). Unlike
// BurnSession, no row survives — this is operator-initiated cleanup, not
// a soft-disable. Tears down any live connection first so the in-memory
// manager state doesn't outlive the row.
func (h *Handler) RemoveSession(ctx context.Context, req *hermesv1.RemoveMbsSessionRequest) (*hermesv1.RemoveMbsSessionResponse, error) {
	tenantID, err := requireTenant(ctx)
	if err != nil {
		return nil, err
	}
	if req.Uid == 0 {
		return nil, status.Error(codes.InvalidArgument, "uid is required")
	}

	// 1. Verify tenant ownership + capture prior state (for lifecycle event).
	row, err := h.store.GetSessionByTenant(ctx, tenantID, req.Uid)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	prevState := dbStateToProto(row.State)

	// 2. Tear down any in-memory connection (best-effort — Disconnect is
	// idempotent and no-ops if uid isn't currently connected). Must run
	// BEFORE the delete so the manager releases its pod claim and listener
	// rather than racing a re-claim against a row that's about to vanish.
	if h.manager != nil {
		_ = h.manager.Disconnect(req.Uid)
	}

	// 3. Hard-delete. Cascade FKs clear mbs_session_assets +
	// mbs_phone_threads in the same transaction.
	if err := h.store.DeleteSession(ctx, req.Uid); err != nil {
		return nil, mapStoreErr(err)
	}

	// 4. Emit a lifecycle event. There is no post-state row, so we signal
	// removal as a transition to UNSPECIFIED with reason "removed" — inbox
	// /UI consumers treat UNSPECIFIED-after-existing as "gone".
	h.publisher.PublishSessionLifecycle(
		req.Uid, tenantID,
		prevState, hermesv1.MbsSessionState_MBS_SESSION_STATE_UNSPECIFIED,
		"removed", 0, h.podID,
	)

	return &hermesv1.RemoveMbsSessionResponse{Uid: req.Uid}, nil
}

// ─────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────

// lookupPrimaryAsset fetches a session's primary asset. Best-effort —
// returns nil on any error (handler tests assert this is graceful).
// Errors during enrichment must NOT fail the parent RPC; the asset is
// presentation-layer data, the session row is the source of truth.
func (h *Handler) lookupPrimaryAsset(ctx context.Context, uid int64) *store.AssetRow {
	assets, err := h.store.ListAssets(ctx, uid)
	if err != nil {
		// Specifically silence ErrNotImplemented during chunk-dev so
		// tests with limited mocks don't flake; surface other errors
		// via debug log only.
		if !errors.Is(err, store.ErrNotImplemented) {
			h.log.Debug().Err(err).Int64("uid", uid).
				Msg("lookupPrimaryAsset: list failed (non-fatal)")
		}
		return nil
	}
	return findPrimaryAsset(assets)
}
