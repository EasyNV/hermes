// Package store is the persistence layer for hermes-mbs. It exposes a
// Store interface implemented by two backends:
//
//   - *PgStore  — production, pgxpool-backed. Defined in pg.go. Chunk 2
//                 implements 8 methods (the minimum to wire main.go and
//                 the observability /readyz probe); remaining methods
//                 are filled by chunks 3-5 as their first callers land.
//   - *mock.Store — in-memory, in internal/mbs/store/mock. Used by
//                 handler/manager/refresh tests so they don't need a
//                 live Postgres.
//
// Encryption is NOT the store's concern beyond providing AAD helpers
// (BuildAAD). Callers encrypt before write and decrypt after read,
// keeping the DEK on the application side.
package store

import (
	"context"
	"errors"
	"time"
)

// Sentinel errors. Callers MUST use errors.Is to compare — wrapped
// returns from pgx layers stay informative without breaking matching.
var (
	// ErrNotFound is returned when a uid lookup yields zero rows.
	ErrNotFound = errors.New("store: not found")

	// ErrClaimConflict is reserved for future call paths that surface
	// claim conflicts as errors instead of as (claimed=false, owner) —
	// e.g. for atomic-claim helpers introduced in chunk 3+.
	ErrClaimConflict = errors.New("store: session owned by another pod")

	// ErrTenantMismatch is returned by GetSessionByTenant when the
	// uid exists but belongs to a different tenant. Maps to gRPC
	// codes.PermissionDenied at the handler layer.
	ErrTenantMismatch = errors.New("store: tenant_id does not match session")

	// ErrNotImplemented marks methods stubbed in chunk 2 that will be
	// filled in by their first caller. Callers SHOULD NOT swallow this —
	// it signals "this code path is not wired yet, finish the impl".
	ErrNotImplemented = errors.New("store: method not yet implemented (see plan chunk 3-5)")
)

// Store is the data-access surface for hermes-mbs. All methods take a
// context for cancellation propagation. Concrete implementations are
// expected to map context cancellation to the relevant transport error
// (pgx cancels SQL, mock returns ctx.Err()).
type Store interface {
	// ─── Session lifecycle ──────────────────────────────────────────
	CreateSession(ctx context.Context, s *SessionRow) error
	GetSession(ctx context.Context, uid int64) (*SessionRow, error)
	GetSessionByTenant(ctx context.Context, tenantID string, uid int64) (*SessionRow, error)
	ListSessions(ctx context.Context, tenantID string, stateFilter string, limit, offset int) ([]*SessionRow, int, error)
	UpdateSessionState(ctx context.Context, uid int64, state string, connackRC *int16) error
	UpdateSessionCookies(ctx context.Context, uid int64, encryptedCookies []byte, lastRefreshedAt, lastValidatedAt time.Time) error
	UpdateSessionTokens(ctx context.Context, uid int64, encAccessToken, encSecret, encSessionKey []byte) error
	BurnSession(ctx context.Context, uid int64, reason string) error
	DeleteSession(ctx context.Context, uid int64) error

	// ─── pod_id ownership (CAS-style; replaces advisory locks) ──────
	//
	// ClaimSession attempts to set pod_id=podID on the session row,
	// succeeding iff the column is currently empty OR already equals
	// podID. Returns:
	//
	//   claimed=true,  owner=podID   → we own it (was unclaimed or re-claim)
	//   claimed=false, owner=other   → another pod owns it; handler routes there
	//   err=ErrNotFound              → uid does not exist
	//
	// Single-statement CAS — safe under pgbouncer in any pooling mode.
	ClaimSession(ctx context.Context, uid int64, podID string) (claimed bool, ownerPodID string, err error)
	ReleaseSession(ctx context.Context, uid int64, podID string) error
	ListSessionsByPod(ctx context.Context, podID, stateFilter string) ([]*SessionRow, error)

	// ─── Assets ─────────────────────────────────────────────────────
	UpsertAssets(ctx context.Context, uid int64, assets []*AssetRow) error
	ListAssets(ctx context.Context, uid int64) ([]*AssetRow, error)
	SetPrimaryAsset(ctx context.Context, uid int64, pageID string) error

	// ─── Phone resolver cache (Path C) ──────────────────────────────
	GetPhoneThread(ctx context.Context, uid int64, pageID, phoneE164 string) (*PhoneThreadRow, error)
	UpsertPhoneThread(ctx context.Context, row *PhoneThreadRow) error

	// ─── Refresh ticker support ─────────────────────────────────────
	//
	// ListSessionsNeedingRefresh returns up to `limit` active sessions
	// owned by podID whose LastRefreshedAt is older than `before`
	// (or NULL). Used by the cookie-refresh cron.
	ListSessionsNeedingRefresh(ctx context.Context, before time.Time, podID string, limit int) ([]*SessionRow, error)

	// ─── Importer / general ─────────────────────────────────────────
	ExistsSession(ctx context.Context, uid int64) (bool, error)

	// ─── Health (for observability /readyz) ─────────────────────────
	Ping(ctx context.Context) error
}
