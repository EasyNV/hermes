// Package mock provides an in-memory implementation of store.Store for
// tests that don't need (and don't want) a live Postgres. Handler,
// session-manager, refresh-ticker tests all compose against this mock
// instead of standing up a database.
//
// ClaimSession semantics mirror PgStore exactly so the two backends are
// behaviorally interchangeable for tests. The package-level test in
// mock_test.go asserts this.
package mock

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/hermes-waba/hermes/internal/mbs/store"
)

// Store is the in-memory implementation. Safe for concurrent use via
// a single mutex (perf is not the goal — tests are).
type Store struct {
	mu sync.Mutex

	sessions     map[int64]*store.SessionRow
	assets       map[int64][]*store.AssetRow
	phoneThreads map[phoneKey]*store.PhoneThreadRow

	// Injectable health failure for /readyz tests.
	pingErr error
}

type phoneKey struct{ uid int64; pageID, phone string }

// NewStore constructs an empty Mock store.
func NewStore() *Store {
	return &Store{
		sessions:     make(map[int64]*store.SessionRow),
		assets:       make(map[int64][]*store.AssetRow),
		phoneThreads: make(map[phoneKey]*store.PhoneThreadRow),
	}
}

// SetPingError makes Ping return err. Used by /readyz tests. Set nil
// to restore healthy state.
func (s *Store) SetPingError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pingErr = err
}

// ─────────────────────────────────────────────────────────────────────
// store.Store implementation
// ─────────────────────────────────────────────────────────────────────

func (s *Store) Ping(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pingErr
}

func (s *Store) ExistsSession(ctx context.Context, uid int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.sessions[uid]
	return ok, nil
}

func (s *Store) CreateSession(ctx context.Context, r *store.SessionRow) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.sessions[r.UID]; exists {
		return errors.New("mock: session already exists for uid")
	}
	cp := *r
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = time.Now()
	}
	cp.UpdatedAt = time.Now()
	s.sessions[r.UID] = &cp
	return nil
}

func (s *Store) GetSession(ctx context.Context, uid int64) (*store.SessionRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.sessions[uid]
	if !ok {
		return nil, store.ErrNotFound
	}
	cp := *r
	return &cp, nil
}

// ClaimSession matches PgStore's CAS semantics:
//
//	pod_id == ""      → claim, return (true, podID, nil)
//	pod_id == podID   → re-claim no-op, return (true, podID, nil)
//	pod_id == other   → reject, return (false, other, nil)
//	not in map        → return ErrNotFound
func (s *Store) ClaimSession(ctx context.Context, uid int64, podID string) (bool, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.sessions[uid]
	if !ok {
		return false, "", store.ErrNotFound
	}
	if r.PodID == "" || r.PodID == podID {
		r.PodID = podID
		r.UpdatedAt = time.Now()
		return true, podID, nil
	}
	return false, r.PodID, nil
}

func (s *Store) ReleaseSession(ctx context.Context, uid int64, podID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.sessions[uid]
	if !ok {
		return nil // idempotent: no-op when row absent
	}
	if r.PodID == podID {
		r.PodID = ""
		r.UpdatedAt = time.Now()
	}
	return nil
}

func (s *Store) ListSessionsByPod(ctx context.Context, podID, stateFilter string) ([]*store.SessionRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*store.SessionRow
	for _, r := range s.sessions {
		if r.PodID != podID {
			continue
		}
		if stateFilter != "" && r.State != stateFilter {
			continue
		}
		cp := *r
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UID < out[j].UID })
	return out, nil
}

// ListReconnectableSessions mirrors PgStore: active sessions this pod owns OR
// orphans (pod_id="").
func (s *Store) ListReconnectableSessions(ctx context.Context, podID string) ([]*store.SessionRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*store.SessionRow
	for _, r := range s.sessions {
		if r.State != "active" {
			continue
		}
		if r.PodID != "" && r.PodID != podID {
			continue
		}
		cp := *r
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UID < out[j].UID })
	return out, nil
}

func (s *Store) ListSessionsNeedingRefresh(ctx context.Context, before time.Time, podID string, limit int) ([]*store.SessionRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*store.SessionRow
	for _, r := range s.sessions {
		if r.PodID != podID || r.State != "active" {
			continue
		}
		// NULL last_refreshed_at always qualifies. A non-NULL value
		// qualifies if before.
		if r.LastRefreshedAt != nil && !r.LastRefreshedAt.Before(before) {
			continue
		}
		cp := *r
		out = append(out, &cp)
	}
	// Match PgStore order: NULLS FIRST then ascending.
	sort.Slice(out, func(i, j int) bool {
		ai, bi := out[i].LastRefreshedAt, out[j].LastRefreshedAt
		switch {
		case ai == nil && bi != nil:
			return true
		case ai != nil && bi == nil:
			return false
		case ai == nil && bi == nil:
			return out[i].UID < out[j].UID
		default:
			if ai.Equal(*bi) {
				return out[i].UID < out[j].UID
			}
			return ai.Before(*bi)
		}
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ─────────────────────────────────────────────────────────────────────
// Stubbed methods (mirror PgStore's chunk-2 layout)
// ─────────────────────────────────────────────────────────────────────

func (s *Store) GetSessionByTenant(ctx context.Context, tenantID string, uid int64) (*store.SessionRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.sessions[uid]
	if !ok {
		return nil, store.ErrNotFound
	}
	if r.TenantID != tenantID {
		return nil, store.ErrTenantMismatch
	}
	cp := *r
	return &cp, nil
}

// ListSessions returns sessions filtered by tenant + optional state,
// ordered by updated_at DESC then uid. Mirrors PgStore's pagination
// + bounds (default 50, max 200; offset clamped to 0).
func (s *Store) ListSessions(ctx context.Context, tenantID string, stateFilter string, limit, offset int) ([]*store.SessionRow, int, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	matches := make([]*store.SessionRow, 0)
	for _, r := range s.sessions {
		if r.TenantID != tenantID {
			continue
		}
		if stateFilter != "" && r.State != stateFilter {
			continue
		}
		cp := *r
		matches = append(matches, &cp)
	}
	total := len(matches)
	if total == 0 {
		return []*store.SessionRow{}, 0, nil
	}
	sort.Slice(matches, func(i, j int) bool {
		// updated_at DESC, then uid ascending for stable order.
		if !matches[i].UpdatedAt.Equal(matches[j].UpdatedAt) {
			return matches[i].UpdatedAt.After(matches[j].UpdatedAt)
		}
		return matches[i].UID < matches[j].UID
	})
	// Paginate.
	if offset >= total {
		return []*store.SessionRow{}, total, nil
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return matches[offset:end], total, nil
}

// UpdateSessionState mirrors PgStore: sets state, optionally records
// last_connack_rc + last_connack_at.
func (s *Store) UpdateSessionState(ctx context.Context, uid int64, state string, connackRC *int16) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.sessions[uid]
	if !ok {
		return store.ErrNotFound
	}
	r.State = state
	if connackRC != nil {
		r.LastConnackRC = connackRC
		now := time.Now()
		r.LastConnackAt = &now
	}
	r.UpdatedAt = time.Now()
	return nil
}

func (s *Store) UpdateSessionCookies(ctx context.Context, uid int64, encryptedCookies []byte, lastRefreshedAt, lastValidatedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.sessions[uid]
	if !ok {
		return store.ErrNotFound
	}
	r.EncryptedCookies = encryptedCookies
	r.LastRefreshedAt = &lastRefreshedAt
	r.LastValidatedAt = &lastValidatedAt
	r.UpdatedAt = time.Now()
	return nil
}

func (s *Store) UpdateSessionTokens(ctx context.Context, uid int64, encAccessToken, encSecret, encSessionKey []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.sessions[uid]
	if !ok {
		return store.ErrNotFound
	}
	r.EncryptedAccessToken = encAccessToken
	r.EncryptedSecret = encSecret
	r.EncryptedSessionKey = encSessionKey
	r.UpdatedAt = time.Now()
	return nil
}

// BurnSession mirrors PgStore: state=burned, burned_reason, burned_at,
// releases pod_id.
func (s *Store) BurnSession(ctx context.Context, uid int64, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.sessions[uid]
	if !ok {
		return store.ErrNotFound
	}
	r.State = "burned"
	r.BurnedReason = reason
	now := time.Now()
	r.BurnedAt = &now
	r.PodID = ""
	r.UpdatedAt = now
	return nil
}

func (s *Store) DeleteSession(ctx context.Context, uid int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[uid]; !ok {
		return store.ErrNotFound
	}
	delete(s.sessions, uid)
	// Mirror the ON DELETE CASCADE FKs: assets + phone-thread cache for
	// this uid go with the row.
	delete(s.assets, uid)
	for k := range s.phoneThreads {
		if k.uid == uid {
			delete(s.phoneThreads, k)
		}
	}
	return nil
}

func (s *Store) UpsertAssets(ctx context.Context, uid int64, assets []*store.AssetRow) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Test convenience: full replace. Real PgStore impl will be smarter
	// (preserve discovered_at, atomic delete-then-insert).
	cps := make([]*store.AssetRow, len(assets))
	for i, a := range assets {
		cp := *a
		if cp.DiscoveredAt.IsZero() {
			cp.DiscoveredAt = time.Now()
		}
		cps[i] = &cp
	}
	s.assets[uid] = cps
	return nil
}

// ListAssets returns assets ordered primary-first then page_id.
func (s *Store) ListAssets(ctx context.Context, uid int64) ([]*store.AssetRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	src := s.assets[uid]
	out := make([]*store.AssetRow, 0, len(src))
	for _, a := range src {
		cp := *a
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsPrimary != out[j].IsPrimary {
			return out[i].IsPrimary // true sorts before false
		}
		return out[i].PageID < out[j].PageID
	})
	return out, nil
}

func (s *Store) SetPrimaryAsset(ctx context.Context, uid int64, pageID string) error {
	return store.ErrNotImplemented
}

// GetPhoneThread returns the cached row or ErrNotFound.
func (s *Store) GetPhoneThread(ctx context.Context, uid int64, pageID, phoneE164 string) (*store.PhoneThreadRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.phoneThreads[phoneKey{uid: uid, pageID: pageID, phone: phoneE164}]
	if !ok {
		return nil, store.ErrNotFound
	}
	cp := *r
	return &cp, nil
}

// UpsertPhoneThread writes the cache row. ON CONFLICT semantics mirror
// PgStore: existing rows have thread_id and wec_mailbox_id refreshed;
// last_send_at is updated only when the new row sets it (nil = keep).
func (s *Store) UpsertPhoneThread(ctx context.Context, row *store.PhoneThreadRow) error {
	if row == nil {
		return errors.New("mock: nil phone thread row")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := phoneKey{uid: row.UID, pageID: row.PageID, phone: row.Phone}
	cp := *row
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = time.Now()
	}
	if existing, ok := s.phoneThreads[k]; ok {
		// Preserve created_at + roll over last_send_at if new is nil.
		cp.CreatedAt = existing.CreatedAt
		if cp.LastSendAt == nil {
			cp.LastSendAt = existing.LastSendAt
		}
	}
	s.phoneThreads[k] = &cp
	return nil
}

// Compile-time assertion that Store implements store.Store. If a chunk
// adds a new method to the interface, the mock either implements it or
// fails to compile.
var _ store.Store = (*Store)(nil)
