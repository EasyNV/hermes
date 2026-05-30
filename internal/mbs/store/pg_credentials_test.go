package store

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────────────
// PgStore integration tests for the credential-update surface
// (UpdateSessionTokens, UpdateSessionCookies).
//
// Same gating as pg_assets_test.go: MBS_PGTEST_DSN must be set or the
// tests skip. Uses the same dialOrSkip / seedSession / uidFor helpers.
//
// Run via:
//
//   PGPW=$(cat deploy/secrets/prod/postgres-password)
//   MBS_PGTEST_DSN="postgres://hermes:***@127.0.0.1:15432/hermes?sslmode=disable" \
//       go test ./internal/mbs/store -run TestPgStore_Credentials -count=1 -v
// ─────────────────────────────────────────────────────────────────────

func TestPgStore_UpdateSessionTokens_HappyPath(t *testing.T) {
	pool := dialOrSkip(t)
	defer pool.Close()
	s := NewPgStore(pool)
	uid := uidFor(t.Name())
	cleanup := seedSession(t, pool, uid)
	defer cleanup()

	newAT := []byte("fresh-access-token-ciphertext-AAA")
	newSec := []byte("fresh-secret-ciphertext-BBB")
	newSK := []byte("fresh-session-key-ciphertext-CCC")

	if err := s.UpdateSessionTokens(context.Background(), uid, newAT, newSec, newSK); err != nil {
		t.Fatalf("UpdateSessionTokens: %v", err)
	}

	// Read back via raw SQL to assert column-level effect.
	var gotAT, gotSec, gotSK []byte
	var gotCookies []byte // must be untouched
	if err := pool.QueryRow(context.Background(), `
        SELECT access_token, secret, session_key, cookies
          FROM mbs_sessions WHERE uid = $1`, uid).
		Scan(&gotAT, &gotSec, &gotSK, &gotCookies); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !bytes.Equal(gotAT, newAT) {
		t.Errorf("access_token: got %q want %q", gotAT, newAT)
	}
	if !bytes.Equal(gotSec, newSec) {
		t.Errorf("secret: got %q want %q", gotSec, newSec)
	}
	if !bytes.Equal(gotSK, newSK) {
		t.Errorf("session_key: got %q want %q", gotSK, newSK)
	}
	// Cookies were '\x00' in seed; UpdateSessionTokens must NOT touch them.
	if !bytes.Equal(gotCookies, []byte{0x00}) {
		t.Errorf("UpdateSessionTokens leaked into cookies: got %q want \\x00", gotCookies)
	}
}

func TestPgStore_UpdateSessionTokens_NotFound(t *testing.T) {
	pool := dialOrSkip(t)
	defer pool.Close()
	s := NewPgStore(pool)
	missing := uidFor(t.Name()) // never seeded
	err := s.UpdateSessionTokens(context.Background(), missing,
		[]byte("x"), []byte("y"), []byte("z"))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestPgStore_UpdateSessionTokens_EmptyBytesAllowed(t *testing.T) {
	pool := dialOrSkip(t)
	defer pool.Close()
	s := NewPgStore(pool)
	uid := uidFor(t.Name())
	cleanup := seedSession(t, pool, uid)
	defer cleanup()

	// Empty slices represent "sealed empty" — the encryption layer
	// distinguishes them from nil at the AAD boundary; the store layer
	// just persists bytes.
	if err := s.UpdateSessionTokens(context.Background(), uid, []byte{}, []byte{}, []byte{}); err != nil {
		t.Fatalf("UpdateSessionTokens empty: %v", err)
	}
	var gotAT, gotSec, gotSK []byte
	if err := pool.QueryRow(context.Background(),
		`SELECT access_token, secret, session_key FROM mbs_sessions WHERE uid = $1`, uid).
		Scan(&gotAT, &gotSec, &gotSK); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(gotAT) != 0 || len(gotSec) != 0 || len(gotSK) != 0 {
		t.Errorf("expected all empty, got at=%q sec=%q sk=%q", gotAT, gotSec, gotSK)
	}
}

func TestPgStore_UpdateSessionTokens_UpdatedAtAdvances(t *testing.T) {
	pool := dialOrSkip(t)
	defer pool.Close()
	s := NewPgStore(pool)
	uid := uidFor(t.Name())
	cleanup := seedSession(t, pool, uid)
	defer cleanup()

	var before time.Time
	if err := pool.QueryRow(context.Background(),
		`SELECT updated_at FROM mbs_sessions WHERE uid = $1`, uid).Scan(&before); err != nil {
		t.Fatalf("read before: %v", err)
	}
	// Postgres NOW() resolves to statement-time so a tight retry can land
	// in the same microsecond. Sleep enough to guarantee a delta.
	time.Sleep(5 * time.Millisecond)
	if err := s.UpdateSessionTokens(context.Background(), uid, []byte("a"), []byte("b"), []byte("c")); err != nil {
		t.Fatalf("UpdateSessionTokens: %v", err)
	}
	var after time.Time
	if err := pool.QueryRow(context.Background(),
		`SELECT updated_at FROM mbs_sessions WHERE uid = $1`, uid).Scan(&after); err != nil {
		t.Fatalf("read after: %v", err)
	}
	if !after.After(before) {
		t.Errorf("updated_at did not advance: before=%v after=%v", before, after)
	}
}

// ─── UpdateSessionCookies ─────────────────────────────────────────

func TestPgStore_UpdateSessionCookies_HappyPath(t *testing.T) {
	pool := dialOrSkip(t)
	defer pool.Close()
	s := NewPgStore(pool)
	uid := uidFor(t.Name())
	cleanup := seedSession(t, pool, uid)
	defer cleanup()

	cookies := []byte("encrypted-cookie-blob-PQR")
	refreshed := time.Now().UTC().Truncate(time.Second)
	validated := refreshed.Add(2 * time.Second)

	if err := s.UpdateSessionCookies(context.Background(), uid, cookies, refreshed, validated); err != nil {
		t.Fatalf("UpdateSessionCookies: %v", err)
	}
	var gotCookies, gotAT []byte
	var gotRefreshed, gotValidated *time.Time
	if err := pool.QueryRow(context.Background(), `
        SELECT cookies, access_token, last_refreshed_at, last_validated_at
          FROM mbs_sessions WHERE uid = $1`, uid).
		Scan(&gotCookies, &gotAT, &gotRefreshed, &gotValidated); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !bytes.Equal(gotCookies, cookies) {
		t.Errorf("cookies: got %q want %q", gotCookies, cookies)
	}
	if gotRefreshed == nil || !gotRefreshed.Equal(refreshed) {
		t.Errorf("last_refreshed_at: got %v want %v", gotRefreshed, refreshed)
	}
	if gotValidated == nil || !gotValidated.Equal(validated) {
		t.Errorf("last_validated_at: got %v want %v", gotValidated, validated)
	}
	// access_token from seed was '\x00'; UpdateSessionCookies must not touch it.
	if !bytes.Equal(gotAT, []byte{0x00}) {
		t.Errorf("UpdateSessionCookies leaked into access_token: got %q", gotAT)
	}
}

func TestPgStore_UpdateSessionCookies_NotFound(t *testing.T) {
	pool := dialOrSkip(t)
	defer pool.Close()
	s := NewPgStore(pool)
	missing := uidFor(t.Name())
	err := s.UpdateSessionCookies(context.Background(), missing,
		[]byte("x"), time.Now(), time.Now())
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestPgStore_UpdateSessionCookies_RefreshThenValidate(t *testing.T) {
	// Simulates the refresh ticker's two distinct write shapes:
	//   1. cookies changed → refreshed bumped, validated bumped
	//   2. cookies unchanged → SAME cookies replayed, only validated bumped
	pool := dialOrSkip(t)
	defer pool.Close()
	s := NewPgStore(pool)
	uid := uidFor(t.Name())
	cleanup := seedSession(t, pool, uid)
	defer cleanup()

	cookies := []byte("v1-cookies")
	t1 := time.Now().UTC().Truncate(time.Second)
	if err := s.UpdateSessionCookies(context.Background(), uid, cookies, t1, t1); err != nil {
		t.Fatalf("write v1: %v", err)
	}
	// Validation tick — same cookies, advanced validated_at, refreshed_at unchanged.
	t2 := t1.Add(time.Minute)
	if err := s.UpdateSessionCookies(context.Background(), uid, cookies, t1, t2); err != nil {
		t.Fatalf("write v1 validated: %v", err)
	}
	var gotRefreshed, gotValidated *time.Time
	if err := pool.QueryRow(context.Background(),
		`SELECT last_refreshed_at, last_validated_at FROM mbs_sessions WHERE uid = $1`, uid).
		Scan(&gotRefreshed, &gotValidated); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if gotRefreshed == nil || !gotRefreshed.Equal(t1) {
		t.Errorf("refreshed should still be t1: got %v want %v", gotRefreshed, t1)
	}
	if gotValidated == nil || !gotValidated.Equal(t2) {
		t.Errorf("validated should be t2: got %v want %v", gotValidated, t2)
	}
}

func TestPgStore_UpdateSessionCookies_ZeroTimestampsAllowed(t *testing.T) {
	// Defensive: caller may pass zero time.Time (no value yet known).
	// Pg accepts the zero time as NULL-equivalent via the driver's
	// time.Time encoding; we just need this not to error.
	pool := dialOrSkip(t)
	defer pool.Close()
	s := NewPgStore(pool)
	uid := uidFor(t.Name())
	cleanup := seedSession(t, pool, uid)
	defer cleanup()

	if err := s.UpdateSessionCookies(context.Background(), uid, []byte("x"), time.Time{}, time.Time{}); err != nil {
		t.Fatalf("UpdateSessionCookies zero-time: %v", err)
	}
}