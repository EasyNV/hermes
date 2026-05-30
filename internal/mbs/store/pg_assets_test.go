package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ─────────────────────────────────────────────────────────────────────
// PgStore integration tests for the asset surface (UpsertAssets,
// SetPrimaryAsset, DeleteSession + cascade).
//
// Gated on env MBS_PGTEST_DSN. When unset, the test is SKIPPED with a
// hint so CI doesn't break. To run locally against the live stack:
//
//   # Bridge in-network postgres to the host (one shot, runs as long
//   # as you need the tests):
//   docker run --rm --network hermes_hermes-net -d --name pg-host-bridge \
//       -p 15432:5432 alpine/socat \
//       tcp-listen:5432,fork,reuseaddr tcp-connect:postgres:5432
//
//   PGPW=$(cat deploy/secrets/prod/postgres-password)
//   MBS_PGTEST_DSN="postgres://hermes:${PGPW}@127.0.0.1:15432/hermes?sslmode=disable" \
//       go test ./internal/mbs/store -run TestPgStore_Assets -count=1 -v
//
// Each test allocates a UNIQUE synthetic uid and tenant_id via the
// test-name hash so parallel runs don't collide; teardown removes
// the test row (cascade clears mbs_session_assets).
// ─────────────────────────────────────────────────────────────────────

const (
	pgtestDSNEnv = "MBS_PGTEST_DSN"
	// Test tenant. mbs_sessions has a FK to tenants(id), so we use the
	// well-known Default Tenant that ships with every deployment's seed
	// (id=00000000-...-0001, name="Default Tenant"). Synthetic UIDs are
	// far above any real Meta user_id so we don't collide with real
	// rows under this tenant.
	testTenantID = "00000000-0000-0000-0000-000000000001"
)

// dialOrSkip returns a pgxpool.Pool or skips the test if the DSN env
// is unset / connection fails.
func dialOrSkip(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv(pgtestDSNEnv)
	if dsn == "" {
		t.Skipf("set %s to run PgStore asset integration tests", pgtestDSNEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("pgxpool.Ping: %v", err)
	}
	return pool
}

// seedSession inserts a minimal mbs_sessions row so asset FK is
// satisfied. Returns a cleanup func that deletes the session (cascade
// clears asset rows). Uses raw bytea zero-blobs for the encrypted
// fields — these tests don't exercise crypto.
func seedSession(t *testing.T, pool *pgxpool.Pool, uid int64) func() {
	t.Helper()
	ctx := context.Background()
	// Use the same column set as CreateSession but with placeholder
	// bytes for the encrypted fields. This is a TEST fixture — don't
	// reach for it in prod code paths.
	_, err := pool.Exec(ctx, `
        INSERT INTO mbs_sessions (
            uid, tenant_id, display_name, state, pod_id,
            access_token, secret, session_key, cookies,
            machine_id, device_id, family_device_id,
            app_version, build_number, device_model, android_ver,
            manufacturer, locale, density, screen_width, screen_height,
            abi, version_id, mqtt_capabilities,
            bridge_envelope
        ) VALUES (
            $1, $2, 'pgtest', 'active', 'pgtest-pod',
            '\x00', '\x00', '\x00', '\x00',
            'pgtest-machine', '00000000-0000-0000-0000-000000000000', '00000000-0000-0000-0000-000000000000',
            'pgtest', 'pgtest', 'pgtest', 'pgtest',
            'pgtest', 'en_US', '1.0', 1, 1,
            'pgtest', 'pgtest', 0,
            '{}'::jsonb
        ) ON CONFLICT (uid) DO NOTHING`, uid, testTenantID)
	if err != nil {
		t.Fatalf("seed session uid=%d: %v", uid, err)
	}
	return func() {
		// Cascade clears asset rows via FK ON DELETE CASCADE.
		if _, err := pool.Exec(context.Background(),
			`DELETE FROM mbs_sessions WHERE uid = $1`, uid); err != nil {
			t.Logf("teardown delete uid=%d: %v", uid, err)
		}
	}
}

// uidFor maps a test name to a unique synthetic uid above any real
// production user id. Avoids collisions if tests run in parallel.
func uidFor(name string) int64 {
	var h int64 = 90_000_000_000_000 // far above any real Meta UID
	for _, c := range name {
		h = (h*31 + int64(c)) & 0x7fffffffffffffff
	}
	return h
}

// ─── UpsertAssets ────────────────────────────────────────────────────

func TestPgStore_UpsertAssets_Insert(t *testing.T) {
	pool := dialOrSkip(t)
	defer pool.Close()
	s := NewPgStore(pool)
	uid := uidFor(t.Name())
	cleanup := seedSession(t, pool, uid)
	defer cleanup()

	assets := []*AssetRow{{
		UID:                  uid,
		PageID:               "page-A",
		PageName:             "Page A",
		BusinessID:           "biz-1",
		BusinessName:         "Biz 1",
		WabaID:               "waba-1",
		WecMailboxID:         "wec-1",
		WecPhoneNumber:       "1234567890",
		IsPrimary:            true,
		WECAccountRegistered: true,
	}}
	if err := s.UpsertAssets(context.Background(), uid, assets); err != nil {
		t.Fatalf("UpsertAssets: %v", err)
	}
	got, err := s.ListAssets(context.Background(), uid)
	if err != nil {
		t.Fatalf("ListAssets: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 asset, got %d", len(got))
	}
	a := got[0]
	if a.PageID != "page-A" || a.BusinessID != "biz-1" || a.WabaID != "waba-1" ||
		a.WecMailboxID != "wec-1" || a.WecPhoneNumber != "1234567890" ||
		!a.IsPrimary || !a.WECAccountRegistered {
		t.Fatalf("asset mismatch: %+v", a)
	}
	if a.DiscoveredAt.IsZero() {
		t.Fatalf("DiscoveredAt should have been set by COALESCE($13, now())")
	}
}

func TestPgStore_UpsertAssets_PreservesDiscoveredAt(t *testing.T) {
	pool := dialOrSkip(t)
	defer pool.Close()
	s := NewPgStore(pool)
	uid := uidFor(t.Name())
	cleanup := seedSession(t, pool, uid)
	defer cleanup()

	ctx := context.Background()
	if err := s.UpsertAssets(ctx, uid, []*AssetRow{{UID: uid, PageID: "page-X", IsPrimary: true}}); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	first, err := s.ListAssets(ctx, uid)
	if err != nil || len(first) != 1 {
		t.Fatalf("ListAssets first: %v len=%d", err, len(first))
	}
	originalDiscoveredAt := first[0].DiscoveredAt

	// Update — different page_name — discovered_at must stay.
	if err := s.UpsertAssets(ctx, uid, []*AssetRow{{
		UID: uid, PageID: "page-X", PageName: "renamed", IsPrimary: true,
	}}); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	second, err := s.ListAssets(ctx, uid)
	if err != nil || len(second) != 1 {
		t.Fatalf("ListAssets second: %v len=%d", err, len(second))
	}
	if !second[0].DiscoveredAt.Equal(originalDiscoveredAt) {
		t.Fatalf("discovered_at changed: was %v, now %v", originalDiscoveredAt, second[0].DiscoveredAt)
	}
	if second[0].PageName != "renamed" {
		t.Fatalf("page_name not updated: %q", second[0].PageName)
	}
}

func TestPgStore_UpsertAssets_EmptySlice_NoOp(t *testing.T) {
	pool := dialOrSkip(t)
	defer pool.Close()
	s := NewPgStore(pool)
	// Note: no seedSession — empty-slice path MUST NOT touch the DB.
	if err := s.UpsertAssets(context.Background(), 999_999_999_999_999, nil); err != nil {
		t.Fatalf("UpsertAssets nil: %v", err)
	}
	if err := s.UpsertAssets(context.Background(), 999_999_999_999_999, []*AssetRow{}); err != nil {
		t.Fatalf("UpsertAssets empty: %v", err)
	}
}

func TestPgStore_UpsertAssets_ForeignKeyViolation(t *testing.T) {
	pool := dialOrSkip(t)
	defer pool.Close()
	s := NewPgStore(pool)
	// Deliberate: uid has no mbs_sessions row.
	uid := uidFor(t.Name())
	err := s.UpsertAssets(context.Background(), uid, []*AssetRow{{
		UID: uid, PageID: "doomed", IsPrimary: true,
	}})
	if err == nil {
		t.Fatalf("expected FK violation, got nil")
	}
	// Surface is fmt.Errorf-wrapped with "FK violation" message; we
	// don't expose a sentinel for FK because callers usually treat it
	// as a programmer error, not a recoverable condition. Just ensure
	// the message is informative.
	if !contains(err.Error(), "FK violation") {
		t.Fatalf("expected FK violation in message, got: %v", err)
	}
}

// ─── SetPrimaryAsset ─────────────────────────────────────────────────

func TestPgStore_SetPrimaryAsset_HappyPath(t *testing.T) {
	pool := dialOrSkip(t)
	defer pool.Close()
	s := NewPgStore(pool)
	uid := uidFor(t.Name())
	cleanup := seedSession(t, pool, uid)
	defer cleanup()
	ctx := context.Background()

	// Two assets; A primary, B not.
	if err := s.UpsertAssets(ctx, uid, []*AssetRow{
		{UID: uid, PageID: "A", IsPrimary: true},
		{UID: uid, PageID: "B", IsPrimary: false},
	}); err != nil {
		t.Fatalf("UpsertAssets: %v", err)
	}
	// Flip primary to B.
	if err := s.SetPrimaryAsset(ctx, uid, "B"); err != nil {
		t.Fatalf("SetPrimaryAsset(B): %v", err)
	}
	got, err := s.ListAssets(ctx, uid)
	if err != nil {
		t.Fatalf("ListAssets: %v", err)
	}
	for _, a := range got {
		switch a.PageID {
		case "A":
			if a.IsPrimary {
				t.Errorf("A should no longer be primary")
			}
		case "B":
			if !a.IsPrimary {
				t.Errorf("B should be primary")
			}
		}
	}
}

func TestPgStore_SetPrimaryAsset_NotFound(t *testing.T) {
	pool := dialOrSkip(t)
	defer pool.Close()
	s := NewPgStore(pool)
	uid := uidFor(t.Name())
	cleanup := seedSession(t, pool, uid)
	defer cleanup()

	err := s.SetPrimaryAsset(context.Background(), uid, "nonexistent")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got: %v", err)
	}
}

// ─── DeleteSession ───────────────────────────────────────────────────

func TestPgStore_DeleteSession_CascadesAssets(t *testing.T) {
	pool := dialOrSkip(t)
	defer pool.Close()
	s := NewPgStore(pool)
	uid := uidFor(t.Name())
	// No deferred cleanup — DeleteSession IS the cleanup.
	seedSession(t, pool, uid)
	ctx := context.Background()

	if err := s.UpsertAssets(ctx, uid, []*AssetRow{
		{UID: uid, PageID: "P1", IsPrimary: true},
		{UID: uid, PageID: "P2"},
	}); err != nil {
		t.Fatalf("UpsertAssets: %v", err)
	}
	if err := s.DeleteSession(ctx, uid); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	got, err := s.ListAssets(ctx, uid)
	if err != nil {
		t.Fatalf("ListAssets post-delete: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 assets after cascade, got %d", len(got))
	}
}

func TestPgStore_DeleteSession_NotFound(t *testing.T) {
	pool := dialOrSkip(t)
	defer pool.Close()
	s := NewPgStore(pool)
	err := s.DeleteSession(context.Background(), 999_888_777_666_555)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got: %v", err)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && indexOf(s, sub) >= 0
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
