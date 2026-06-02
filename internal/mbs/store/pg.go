package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PgStore implements Store using a pgxpool connection pool.
//
// Chunk 2 implements only the methods needed by main.go startup,
// observability /readyz, and the session manager's first-use path.
// Remaining methods return ErrNotImplemented and will be filled by
// chunks 3-5 as their first callers land.
type PgStore struct {
	pool *pgxpool.Pool
}

// NewPgStore wraps a pgxpool.Pool. Caller owns the pool lifecycle.
func NewPgStore(pool *pgxpool.Pool) *PgStore { return &PgStore{pool: pool} }

// ─────────────────────────────────────────────────────────────────────
// Implemented in chunk 2 (8 methods)
// ─────────────────────────────────────────────────────────────────────

// Ping verifies the connection pool can reach Postgres. Used by /readyz.
// A 2-second deadline is reasonable; longer means the DB is sick.
func (s *PgStore) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

// ExistsSession returns true iff a row exists for uid.
func (s *PgStore) ExistsSession(ctx context.Context, uid int64) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM mbs_sessions WHERE uid = $1)`, uid,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("exists session: %w", err)
	}
	return exists, nil
}

const sessionCols = `
    uid, tenant_id, display_name, login_email, state, pod_id,
    access_token, secret, session_key, cookies, totp_secret_enc,
    machine_id, device_id, family_device_id,
    app_version, build_number, device_model, android_ver,
    manufacturer, locale, density, screen_width, screen_height,
    abi, version_id, mqtt_capabilities,
    bridge_envelope,
    last_refreshed_at, last_validated_at, last_connack_rc, last_connack_at,
    burned_at, burned_reason,
    created_at, updated_at`

// CreateSession inserts a new mbs_sessions row. The caller has already
// encrypted the secret blobs with column-bound AAD.
func (s *PgStore) CreateSession(ctx context.Context, r *SessionRow) error {
	_, err := s.pool.Exec(ctx, `
        INSERT INTO mbs_sessions (
            uid, tenant_id, display_name, login_email, state, pod_id,
            access_token, secret, session_key, cookies, totp_secret_enc,
            machine_id, device_id, family_device_id,
            app_version, build_number, device_model, android_ver,
            manufacturer, locale, density, screen_width, screen_height,
            abi, version_id, mqtt_capabilities,
            bridge_envelope,
            last_refreshed_at, last_validated_at, last_connack_rc, last_connack_at,
            burned_at, burned_reason
        ) VALUES (
            $1, $2, $3, $4, $5,
            $6, $7, $8, $9, $10,
            $11, $12, $13, $14,
            $15, $16, $17, $18,
            $19, $20, $21, $22, $23,
            $24, $25, $26,
            $27,
            $28, $29, $30, $31,
            $32, $33
        )`,
		r.UID, r.TenantID, r.DisplayName, r.LoginEmail, r.State, r.PodID,
		r.EncryptedAccessToken, r.EncryptedSecret, r.EncryptedSessionKey,
		r.EncryptedCookies, r.EncryptedTOTPSecret,
		r.MachineID, r.DeviceID, r.FamilyDeviceID,
		r.AppVersion, r.BuildNumber, r.DeviceModel, r.AndroidVer,
		r.Manufacturer, r.Locale, r.Density, r.ScreenWidth, r.ScreenHeight,
		r.ABI, r.VersionID, r.MQTTCapabilities,
		r.BridgeEnvelope,
		r.LastRefreshedAt, r.LastValidatedAt, r.LastConnackRC, r.LastConnackAt,
		r.BurnedAt, r.BurnedReason,
	)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

// GetSession returns the session row for uid, or ErrNotFound.
func (s *PgStore) GetSession(ctx context.Context, uid int64) (*SessionRow, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+sessionCols+` FROM mbs_sessions WHERE uid = $1`, uid)
	r, err := scanSession(row)
	if err != nil {
		return nil, err
	}
	return r, nil
}

// ClaimSession atomically claims pod_id ownership for uid. The CTE
// approach makes both the "claimed" and "held by other" cases return
// a row, so callers can distinguish without a second round-trip.
//
// SQL semantics:
//
//	WITH upd AS (
//	    UPDATE mbs_sessions
//	       SET pod_id = $1, updated_at = NOW()
//	     WHERE uid = $2 AND (pod_id = '' OR pod_id = $1)
//	 RETURNING pod_id
//	)
//	SELECT pod_id FROM upd                                       -- claim succeeded
//	UNION ALL
//	SELECT pod_id FROM mbs_sessions
//	 WHERE uid = $2 AND NOT EXISTS (SELECT 1 FROM upd)            -- held by other
//	LIMIT 1;
//
// Result interpretations:
//
//	scanned pod_id == $1                  → claimed=true,  owner=$1
//	scanned pod_id == "" or other-non-$1  → claimed=false, owner=that
//	pgx.ErrNoRows                          → ErrNotFound (uid doesn't exist)
func (s *PgStore) ClaimSession(ctx context.Context, uid int64, podID string) (bool, string, error) {
	const q = `
        WITH upd AS (
            UPDATE mbs_sessions
               SET pod_id = $1, updated_at = NOW()
             WHERE uid = $2 AND (pod_id = '' OR pod_id = $1)
         RETURNING pod_id
        )
        SELECT pod_id FROM upd
        UNION ALL
        SELECT pod_id FROM mbs_sessions
         WHERE uid = $2 AND NOT EXISTS (SELECT 1 FROM upd)
        LIMIT 1`
	var ownerPodID string
	err := s.pool.QueryRow(ctx, q, podID, uid).Scan(&ownerPodID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, "", ErrNotFound
	}
	if err != nil {
		return false, "", fmt.Errorf("claim session: %w", err)
	}
	claimed := ownerPodID == podID
	return claimed, ownerPodID, nil
}

// ReleaseSession clears pod_id ownership IF currently held by podID.
// Wrong-pod release is a no-op (no error) so shutdown handlers can be
// idempotent.
func (s *PgStore) ReleaseSession(ctx context.Context, uid int64, podID string) error {
	_, err := s.pool.Exec(ctx, `
        UPDATE mbs_sessions
           SET pod_id = '', updated_at = NOW()
         WHERE uid = $1 AND pod_id = $2`, uid, podID)
	if err != nil {
		return fmt.Errorf("release session: %w", err)
	}
	return nil
}

// ListSessionsByPod returns all sessions currently owned by podID,
// optionally filtered by state.
func (s *PgStore) ListSessionsByPod(ctx context.Context, podID, stateFilter string) ([]*SessionRow, error) {
	q := `SELECT ` + sessionCols + ` FROM mbs_sessions WHERE pod_id = $1`
	args := []any{podID}
	if stateFilter != "" {
		q += ` AND state = $2`
		args = append(args, stateFilter)
	}
	q += ` ORDER BY uid`
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list sessions by pod: %w", err)
	}
	defer rows.Close()
	return scanSessions(rows)
}

// ListReconnectableSessions returns active sessions this pod may reclaim on
// startup: those it already owns (pod_id = podID) PLUS orphans whose pod_id was
// released to '' on a prior graceful shutdown. Without the orphan clause, a pod
// can never re-adopt its own sessions after a restart (graceful shutdown sets
// pod_id='' via ReleaseSession), so the listener never resumes and inbound
// polling stops — the exact bug behind "reply never reached the inbox".
//
// Safe in multi-pod: ClaimSession's CTE refuses to steal a session owned by a
// different LIVE pod (claims only when pod_id='' OR pod_id=self). A session
// another pod grabbed first will fail the claim with ErrClaimConflict and be
// skipped. We only widen the *candidate* set here; the atomic claim is still
// the authority.
func (s *PgStore) ListReconnectableSessions(ctx context.Context, podID string) ([]*SessionRow, error) {
	q := `SELECT ` + sessionCols + ` FROM mbs_sessions
	      WHERE state = 'active' AND (pod_id = '' OR pod_id = $1)
	      ORDER BY uid`
	rows, err := s.pool.Query(ctx, q, podID)
	if err != nil {
		return nil, fmt.Errorf("list reconnectable sessions: %w", err)
	}
	defer rows.Close()
	return scanSessions(rows)
}

// ListSessionsNeedingRefresh returns active sessions owned by podID
// with stale LastRefreshedAt (or NULL). Limit caps the batch size so
// the refresh ticker stays bounded.
func (s *PgStore) ListSessionsNeedingRefresh(ctx context.Context, before time.Time, podID string, limit int) ([]*SessionRow, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
        SELECT `+sessionCols+`
          FROM mbs_sessions
         WHERE state = 'active'
           AND pod_id = $1
           AND (last_refreshed_at IS NULL OR last_refreshed_at < $2)
         ORDER BY last_refreshed_at NULLS FIRST
         LIMIT $3`, podID, before, limit)
	if err != nil {
		return nil, fmt.Errorf("list sessions needing refresh: %w", err)
	}
	defer rows.Close()
	return scanSessions(rows)
}

// ─────────────────────────────────────────────────────────────────────
// Stubbed in chunk 2; filled by first caller in chunks 3-5
// ─────────────────────────────────────────────────────────────────────

func (s *PgStore) GetSessionByTenant(ctx context.Context, tenantID string, uid int64) (*SessionRow, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+sessionCols+` FROM mbs_sessions WHERE uid = $1`, uid)
	r, err := scanSession(row)
	if err != nil {
		return nil, err
	}
	if r.TenantID != tenantID {
		// Distinct error so handler can map to PermissionDenied vs NotFound.
		return nil, ErrTenantMismatch
	}
	return r, nil
}

// ListSessions returns a paginated, optionally state-filtered slice of
// sessions for tenantID plus the total matching-row count. Total is
// computed via a separate COUNT(*) under the same WHERE filter so the
// UI can render "showing 1-50 of 327" without a second round-trip.
//
// limit ≤ 0 means "use 50 (default)"; capped at 200 to bound payload.
// offset < 0 means 0.
func (s *PgStore) ListSessions(ctx context.Context, tenantID string, stateFilter string, limit, offset int) ([]*SessionRow, int, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}

	// COUNT(*) first. Both queries share the same predicate set so a
	// drift here would silently produce a wrong total — keep them
	// trivially audit-able.
	countQ := `SELECT COUNT(*) FROM mbs_sessions WHERE tenant_id = $1`
	listQ := `SELECT ` + sessionCols + ` FROM mbs_sessions WHERE tenant_id = $1`
	args := []any{tenantID}
	if stateFilter != "" {
		countQ += ` AND state = $2`
		listQ += ` AND state = $2`
		args = append(args, stateFilter)
	}
	var total int
	if err := s.pool.QueryRow(ctx, countQ, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("list sessions count: %w", err)
	}
	if total == 0 {
		return []*SessionRow{}, 0, nil
	}
	listQ += ` ORDER BY updated_at DESC, uid LIMIT $` +
		fmt.Sprintf("%d", len(args)+1) + ` OFFSET $` + fmt.Sprintf("%d", len(args)+2)
	args = append(args, limit, offset)

	rows, err := s.pool.Query(ctx, listQ, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()
	out, err := scanSessions(rows)
	if err != nil {
		return nil, 0, err
	}
	if out == nil {
		out = []*SessionRow{}
	}
	return out, total, nil
}

// UpdateSessionState updates the session's state and optionally records
// the most recent CONNACK return code (last_connack_rc + last_connack_at).
// Used by the session manager when CONNECT succeeds/fails and by the
// refresh ticker when it observes a state-affecting condition.
func (s *PgStore) UpdateSessionState(ctx context.Context, uid int64, state string, connackRC *int16) error {
	var tag pgconn.CommandTag
	var err error
	if connackRC != nil {
		tag, err = s.pool.Exec(ctx, `
            UPDATE mbs_sessions
               SET state = $1,
                   last_connack_rc = $2,
                   last_connack_at = NOW(),
                   updated_at = NOW()
             WHERE uid = $3`, state, *connackRC, uid)
	} else {
		tag, err = s.pool.Exec(ctx, `
            UPDATE mbs_sessions
               SET state = $1, updated_at = NOW()
             WHERE uid = $2`, state, uid)
	}
	if err != nil {
		return fmt.Errorf("update session state: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateSessionCookies rewrites the encrypted_cookies blob and the
// two cookie-freshness timestamps for uid. Used by:
//
//   - importer --force path (importer.go:286) after replacing the
//     access-token triple, to install the legacy archive's cookies.
//   - refresh ticker (refresh/attempt.go:171,224) to persist merged
//     cookies post-rotation, or to bump LastValidatedAt on a no-cookie-
//     change validation hit.
//
// Single statement, no transaction needed — concurrent writers race
// last-writer-wins on the cookies/timestamps columns and that's the
// correct semantics for both call sites (importer wins over a stale
// refresh; refresh wins over a stale importer; either way the row
// stays internally consistent because all three columns move together).
//
// Empty encryptedCookies and zero-value timestamps are permitted —
// caller's responsibility to pass meaningful values. Returns ErrNotFound
// if uid has no row (importer should not call this on a path where the
// CreateSession/UpdateSessionTokens predecessor failed; refresh ticker's
// row was loaded via ListSessions so it exists).
func (s *PgStore) UpdateSessionCookies(ctx context.Context, uid int64, encryptedCookies []byte, lastRefreshedAt, lastValidatedAt time.Time) error {
	tag, err := s.pool.Exec(ctx, `
        UPDATE mbs_sessions
           SET cookies           = $1,
               last_refreshed_at = $2,
               last_validated_at = $3,
               updated_at        = NOW()
         WHERE uid = $4`,
		encryptedCookies, lastRefreshedAt, lastValidatedAt, uid)
	if err != nil {
		return fmt.Errorf("update session cookies: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateSessionTokens rewrites the encrypted access-token triple
// (access_token, secret, session_key) for uid. Used by the importer's
// --force path (importer.go:280) when re-importing a legacy archive
// onto an existing row.
//
// Does NOT touch cookies, state, pod_id, or any identity column.
// Caller composes with UpdateSessionCookies + UpdateSessionState as
// needed (importer's --force flow runs all three back-to-back).
//
// All three byte slices are passed through verbatim — empty/nil is
// allowed (legacy archives occasionally have a missing field; the
// encryption layer encodes "field absent" as sealed-empty, which is
// distinct from nil). No length validation here — that belongs to the
// AAD-bound encrypt path which produced these bytes.
//
// Single statement, no transaction. Returns ErrNotFound if uid has no
// row (importer should fall through to CreateSession in that case;
// it currently branches on ExistsSession before calling us).
func (s *PgStore) UpdateSessionTokens(ctx context.Context, uid int64, encAccessToken, encSecret, encSessionKey []byte) error {
	tag, err := s.pool.Exec(ctx, `
        UPDATE mbs_sessions
           SET access_token = $1,
               secret       = $2,
               session_key  = $3,
               updated_at   = NOW()
         WHERE uid = $4`,
		encAccessToken, encSecret, encSessionKey, uid)
	if err != nil {
		return fmt.Errorf("update session tokens: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// BurnSession marks the session as burned, records the reason, and
// stamps burned_at. Idempotent — re-burning an already-burned session
// updates the reason and timestamp.
func (s *PgStore) BurnSession(ctx context.Context, uid int64, reason string) error {
	tag, err := s.pool.Exec(ctx, `
        UPDATE mbs_sessions
           SET state = 'burned',
               burned_at = NOW(),
               burned_reason = $1,
               pod_id = '',
               updated_at = NOW()
         WHERE uid = $2`, reason, uid)
	if err != nil {
		return fmt.Errorf("burn session: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteSession removes the mbs_sessions row for uid. Cascade FKs
// (mbs_session_assets ON DELETE CASCADE) clear dependent rows; callers
// who care about cascading children that DON'T have ON DELETE CASCADE
// (mbs_phone_threads — verify via \d before relying on this) must
// delete those first.
//
// Returns ErrNotFound if uid has no row. Burn-and-keep flow uses
// UpdateSessionState→BURNED instead; DeleteSession is reserved for
// operator-initiated removal (GDPR / wrong-tenant cleanup / test
// teardown).
func (s *PgStore) DeleteSession(ctx context.Context, uid int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM mbs_sessions WHERE uid = $1`, uid)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// UpsertAssets persists a session's business asset map (one row per
// page_id) in mbs_session_assets. Semantics:
//
//   - Per-row upsert by natural key (uid, page_id). Existing rows are
//     UPDATEd in place; new rows INSERTed.
//   - discovered_at is PRESERVED on update — first-discovery timestamp
//     matters for ops. The SQL uses ON CONFLICT DO UPDATE without
//     touching discovered_at.
//   - is_primary is overwritten by caller's value. The DB enforces the
//     at-most-one-primary invariant via the partial unique index
//     uniq_mbs_session_assets_one_primary; concurrent writers that
//     both submit IsPrimary=true for the same uid will have the second
//     commit fail with 23505 (wrapped as ErrConflict here).
//   - assets NOT present in the input are NOT deleted. UpsertAssets is
//     additive/refresh. Removal is via DeleteSession (cascade) or a
//     future TrimAssets path.
//   - Empty assets slice is a no-op — returns nil without opening a
//     transaction.
//   - All upserts run in a single transaction. Any error rolls back.
//
// Foreign-key contract: caller MUST ensure mbs_sessions(uid) exists
// before calling. FK violation surfaces as a wrapped error containing
// the pgconn code "23503" so callers may classify if needed.
func (s *PgStore) UpsertAssets(ctx context.Context, uid int64, assets []*AssetRow) error {
	if len(assets) == 0 {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("upsert assets: begin: %w", err)
	}
	// Rollback is a no-op after Commit; deferring is the standard pgx pattern.
	defer func() { _ = tx.Rollback(ctx) }()

	const stmt = `
        INSERT INTO mbs_session_assets (
            uid, page_id, page_name, business_presence_node_id,
            business_id, business_name,
            waba_id, wec_mailbox_id, wec_phone_number,
            ig_account_id, is_primary, wec_account_registered,
            discovered_at
        ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12,
                  COALESCE($13, now()))
        ON CONFLICT (uid, page_id) DO UPDATE SET
            page_name                 = EXCLUDED.page_name,
            business_presence_node_id = EXCLUDED.business_presence_node_id,
            business_id               = EXCLUDED.business_id,
            business_name             = EXCLUDED.business_name,
            waba_id                   = EXCLUDED.waba_id,
            wec_mailbox_id            = EXCLUDED.wec_mailbox_id,
            wec_phone_number          = EXCLUDED.wec_phone_number,
            ig_account_id             = EXCLUDED.ig_account_id,
            is_primary                = EXCLUDED.is_primary,
            wec_account_registered    = EXCLUDED.wec_account_registered`

	for _, a := range assets {
		if a == nil {
			continue
		}
		// nil discovered_at → COALESCE($13, now()) on the SQL side.
		var discoveredAt any
		if !a.DiscoveredAt.IsZero() {
			discoveredAt = a.DiscoveredAt
		}
		if _, err := tx.Exec(ctx, stmt,
			uid, a.PageID, a.PageName, a.BusinessPresenceNodeID,
			a.BusinessID, a.BusinessName,
			a.WabaID, a.WecMailboxID, a.WecPhoneNumber,
			a.IgAccountID, a.IsPrimary, a.WECAccountRegistered,
			discoveredAt,
		); err != nil {
			// Classify pgconn errors for caller observability without
			// breaking errors.Is on sentinels.
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) {
				switch pgErr.Code {
				case "23505": // unique_violation — primary partial index race
					return fmt.Errorf("upsert assets: primary conflict for uid=%d page=%s: %w",
						uid, a.PageID, ErrConflict)
				case "23503": // foreign_key_violation — uid not in mbs_sessions
					return fmt.Errorf("upsert assets: FK violation for uid=%d (session row missing?): %w", uid, err)
				}
			}
			return fmt.Errorf("upsert assets: exec uid=%d page=%s: %w", uid, a.PageID, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("upsert assets: commit: %w", err)
	}
	return nil
}

// SetPrimaryAsset atomically flips primary to (uid, pageID), clearing
// any other primary on the same uid. Two-statement transaction:
//
//  1. Clear any current primary on uid except pageID.
//  2. Set is_primary=true on (uid, pageID).
//
// Returns ErrNotFound if (uid, pageID) has no row. Returns ErrConflict
// if a concurrent SetPrimaryAsset races and trips the partial unique
// index — callers MAY retry once after refetching the assets list.
func (s *PgStore) SetPrimaryAsset(ctx context.Context, uid int64, pageID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("set primary asset: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
        UPDATE mbs_session_assets
           SET is_primary = false
         WHERE uid = $1 AND is_primary = true AND page_id <> $2`,
		uid, pageID); err != nil {
		return fmt.Errorf("set primary asset: clear: %w", err)
	}

	tag, err := tx.Exec(ctx, `
        UPDATE mbs_session_assets
           SET is_primary = true
         WHERE uid = $1 AND page_id = $2`,
		uid, pageID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return fmt.Errorf("set primary asset: race for uid=%d page=%s: %w", uid, pageID, ErrConflict)
		}
		return fmt.Errorf("set primary asset: set: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("set primary asset: commit: %w", err)
	}
	return nil
}

// ListAssets returns all rows from mbs_session_assets for uid, ordered
// primary-first then by page_id. Returns an empty slice (not nil) when
// the session has no enriched assets yet.
func (s *PgStore) ListAssets(ctx context.Context, uid int64) ([]*AssetRow, error) {
	rows, err := s.pool.Query(ctx, `
        SELECT uid, page_id, page_name, business_presence_node_id,
               business_id, business_name,
               waba_id, wec_mailbox_id, wec_phone_number,
               ig_account_id, is_primary, wec_account_registered,
               discovered_at
          FROM mbs_session_assets
         WHERE uid = $1
         ORDER BY is_primary DESC, page_id`, uid)
	if err != nil {
		return nil, fmt.Errorf("list assets: %w", err)
	}
	defer rows.Close()

	out := make([]*AssetRow, 0)
	for rows.Next() {
		a := &AssetRow{}
		var businessID, businessName, wabaID, wecMailbox, wecPhone, igAccount *string
		if err := rows.Scan(
			&a.UID, &a.PageID, &a.PageName, &a.BusinessPresenceNodeID,
			&businessID, &businessName,
			&wabaID, &wecMailbox, &wecPhone,
			&igAccount, &a.IsPrimary, &a.WECAccountRegistered,
			&a.DiscoveredAt,
		); err != nil {
			return nil, fmt.Errorf("scan asset row: %w", err)
		}
		// Nullable text columns → empty string when absent.
		if businessID != nil {
			a.BusinessID = *businessID
		}
		if businessName != nil {
			a.BusinessName = *businessName
		}
		if wabaID != nil {
			a.WabaID = *wabaID
		}
		if wecMailbox != nil {
			a.WecMailboxID = *wecMailbox
		}
		if wecPhone != nil {
			a.WecPhoneNumber = *wecPhone
		}
		if igAccount != nil {
			a.IgAccountID = *igAccount
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// GetPhoneThread returns the cached (uid, page_id, phone) → thread_id
// mapping. Returns ErrNotFound on miss; caller should fall through
// to a live BizInboxWhatsAppCustomerMutation.
func (s *PgStore) GetPhoneThread(ctx context.Context, uid int64, pageID, phoneE164 string) (*PhoneThreadRow, error) {
	row := s.pool.QueryRow(ctx, `
        SELECT uid, page_id, phone, thread_id, wec_mailbox_id, last_send_at, created_at
          FROM mbs_phone_threads
         WHERE uid = $1 AND page_id = $2 AND phone = $3`,
		uid, pageID, phoneE164)
	r := &PhoneThreadRow{}
	err := row.Scan(&r.UID, &r.PageID, &r.Phone, &r.ThreadID, &r.WecMailboxID, &r.LastSendAt, &r.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get phone thread: %w", err)
	}
	return r, nil
}

// UpsertPhoneThread writes-or-updates the cache row. On conflict
// (same uid+page+phone), refreshes thread_id and mailbox in case
// the mailbox migrated between pages — and bumps last_send_at when
// the caller set it (sends update; resolver-only callers leave it
// nil and only the row's existence is the cache marker).
func (s *PgStore) UpsertPhoneThread(ctx context.Context, row *PhoneThreadRow) error {
	if row == nil {
		return errors.New("upsert phone thread: nil row")
	}
	_, err := s.pool.Exec(ctx, `
        INSERT INTO mbs_phone_threads (uid, page_id, phone, thread_id, wec_mailbox_id, last_send_at)
        VALUES ($1, $2, $3, $4, $5, $6)
        ON CONFLICT (uid, page_id, phone) DO UPDATE
           SET thread_id      = EXCLUDED.thread_id,
               wec_mailbox_id = EXCLUDED.wec_mailbox_id,
               last_send_at   = COALESCE(EXCLUDED.last_send_at, mbs_phone_threads.last_send_at)`,
		row.UID, row.PageID, row.Phone, row.ThreadID, row.WecMailboxID, row.LastSendAt)
	if err != nil {
		return fmt.Errorf("upsert phone thread: %w", err)
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────
// scanners
// ─────────────────────────────────────────────────────────────────────

// scanSession reads one row in the canonical sessionCols order.
// Centralized so a column-order tweak only requires editing here +
// the const sessionCols.
func scanSession(row pgx.Row) (*SessionRow, error) {
	r := &SessionRow{}
	err := row.Scan(
		&r.UID, &r.TenantID, &r.DisplayName, &r.LoginEmail, &r.State, &r.PodID,
		&r.EncryptedAccessToken, &r.EncryptedSecret, &r.EncryptedSessionKey,
		&r.EncryptedCookies, &r.EncryptedTOTPSecret,
		&r.MachineID, &r.DeviceID, &r.FamilyDeviceID,
		&r.AppVersion, &r.BuildNumber, &r.DeviceModel, &r.AndroidVer,
		&r.Manufacturer, &r.Locale, &r.Density, &r.ScreenWidth, &r.ScreenHeight,
		&r.ABI, &r.VersionID, &r.MQTTCapabilities,
		&r.BridgeEnvelope,
		&r.LastRefreshedAt, &r.LastValidatedAt, &r.LastConnackRC, &r.LastConnackAt,
		&r.BurnedAt, &r.BurnedReason,
		&r.CreatedAt, &r.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan session: %w", err)
	}
	return r, nil
}

func scanSessions(rows pgx.Rows) ([]*SessionRow, error) {
	var out []*SessionRow
	for rows.Next() {
		r := &SessionRow{}
		err := rows.Scan(
			&r.UID, &r.TenantID, &r.DisplayName, &r.LoginEmail, &r.State, &r.PodID,
			&r.EncryptedAccessToken, &r.EncryptedSecret, &r.EncryptedSessionKey,
			&r.EncryptedCookies, &r.EncryptedTOTPSecret,
			&r.MachineID, &r.DeviceID, &r.FamilyDeviceID,
			&r.AppVersion, &r.BuildNumber, &r.DeviceModel, &r.AndroidVer,
			&r.Manufacturer, &r.Locale, &r.Density, &r.ScreenWidth, &r.ScreenHeight,
			&r.ABI, &r.VersionID, &r.MQTTCapabilities,
			&r.BridgeEnvelope,
			&r.LastRefreshedAt, &r.LastValidatedAt, &r.LastConnackRC, &r.LastConnackAt,
			&r.BurnedAt, &r.BurnedReason,
			&r.CreatedAt, &r.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan session row: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
