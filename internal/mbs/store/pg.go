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
    uid, tenant_id, display_name, state, pod_id,
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
            uid, tenant_id, display_name, state, pod_id,
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
            $11, $12, $13,
            $14, $15, $16, $17,
            $18, $19, $20, $21, $22,
            $23, $24, $25,
            $26,
            $27, $28, $29, $30,
            $31, $32
        )`,
		r.UID, r.TenantID, r.DisplayName, r.State, r.PodID,
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

func (s *PgStore) UpdateSessionCookies(ctx context.Context, uid int64, encryptedCookies []byte, lastRefreshedAt, lastValidatedAt time.Time) error {
	return ErrNotImplemented
}

func (s *PgStore) UpdateSessionTokens(ctx context.Context, uid int64, encAccessToken, encSecret, encSessionKey []byte) error {
	return ErrNotImplemented
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

func (s *PgStore) DeleteSession(ctx context.Context, uid int64) error {
	return ErrNotImplemented
}

func (s *PgStore) UpsertAssets(ctx context.Context, uid int64, assets []*AssetRow) error {
	return ErrNotImplemented
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

func (s *PgStore) SetPrimaryAsset(ctx context.Context, uid int64, pageID string) error {
	return ErrNotImplemented
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
		&r.UID, &r.TenantID, &r.DisplayName, &r.State, &r.PodID,
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
			&r.UID, &r.TenantID, &r.DisplayName, &r.State, &r.PodID,
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
