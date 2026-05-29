package refresh

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"mbs-native/auth"
	"mbs-native/web"

	"github.com/hermes-waba/hermes/internal/mbs/handler"
	"github.com/hermes-waba/hermes/internal/mbs/store"
	"github.com/hermes-waba/hermes/internal/mbs/store/mock"
	"github.com/hermes-waba/hermes/pkg/crypto"
	"github.com/rs/zerolog"
)

// ─────────────────────────── Test fixtures ───────────────────────────

func newTestDEK(t *testing.T) crypto.DataEncryptionKey {
	t.Helper()
	var dek crypto.DataEncryptionKey
	if _, err := rand.Read(dek[:]); err != nil {
		t.Fatalf("rand DEK: %v", err)
	}
	return dek
}

// seedRow constructs a SessionRow whose encrypted columns are real
// AES-GCM ciphertext over the provided plaintext, with column-bound
// AAD. attemptRefresh decrypts them via session.DecryptCreds +
// crypto.DecryptAESGCM, so the test ciphertext must round-trip.
func seedRow(t *testing.T, dek crypto.DataEncryptionKey, uid int64, cookies map[string]string) *store.SessionRow {
	t.Helper()
	enc := func(col store.AADColumn, plaintext []byte) []byte {
		ct, err := crypto.EncryptAESGCM(dek, plaintext, store.BuildAAD(col, uid))
		if err != nil {
			t.Fatalf("encrypt %s: %v", col, err)
		}
		return ct
	}

	envelope := auth.BridgeEnvelope{
		Version:         auth.SupportedBridgeVersion,
		IssuedAt:        time.Now().Unix(),
		AccessToken:     "EAATESTtoken",
		UID:             uid,
		SessionKey:      "5.0sessionkey.1700000000.1-" + itoa(uid),
		Secret:          "deadbeefcafe0123456789abcdef0123",
		MachineID:       "TestMachineID0123456789a",
		Cookies:         cookies,
		LastRefreshedAt: time.Now().Add(-31 * 24 * time.Hour), // stale
	}
	envelopeJSON, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}

	return &store.SessionRow{
		UID:                  uid,
		TenantID:             "tenant-test",
		DisplayName:          "Test Page",
		State:                "active",
		PodID:                "pod-test",
		EncryptedAccessToken: enc(store.AADAccessToken, []byte(envelope.AccessToken)),
		EncryptedSecret:      enc(store.AADSecret, []byte(envelope.Secret)),
		EncryptedSessionKey:  enc(store.AADSessionKey, []byte(envelope.SessionKey)),
		EncryptedCookies:     enc(store.AADCookies, envelopeJSON),
		MachineID:            envelope.MachineID,
		DeviceID:             "device-uuid-test",
		FamilyDeviceID:       "family-uuid-test",
		AppVersion:           "534.0.0.0.0",
		BuildNumber:          "999999999",
		DeviceModel:          "Pixel 7",
		AndroidVer:           "33",
		Manufacturer:         "Google",
		Locale:               "en_US",
		Density:              "3.0",
		ABI:                  "arm64-v8a",
		VersionID:            "test",
		ScreenWidth:          1080,
		ScreenHeight:         2400,
		BridgeEnvelope:       envelopeJSON,
		LastRefreshedAt:      &envelope.LastRefreshedAt,
	}
}

func itoa(n int64) string {
	return string(rune('0'+(n%10))) + "1674772559" // simple deterministic suffix
}

func validCookies() map[string]string {
	return map[string]string{
		"c_user": "1674772559",
		"xs":     "32:abcdef123456:2:1700000000:-1:-1::AcXcoo",
		"datr":   "AbCDeFgHiJkLmNoPqRsTuVwX",
	}
}

// scriptedPingClient drives attemptRefresh through pre-recorded
// Ping responses. Production refreshClient is *web.Client but the
// interface is the only contract attempt.go uses.
type scriptedPingClient struct {
	signal *web.RefreshSignal
	err    error
}

func (c *scriptedPingClient) Ping(_ context.Context) (*web.RefreshSignal, error) {
	return c.signal, c.err
}

// recordingPublisher captures lifecycle events for assertion.
type recordingPublisher struct {
	lifecycle []lifecycleCall
}

type lifecycleCall struct {
	uid, prev, next, reason string
	tenantID                string
	podID                   string
}

func (p *recordingPublisher) PublishInboundMessage(int64, string, string, string, string, string, string, string, time.Time) {
}
func (p *recordingPublisher) PublishOutbound(int64, string, string, string, string, int64, bool, string, time.Time) {
}
func (p *recordingPublisher) PublishSessionLifecycle(uid int64, tenantID string, prev, next interface{}, reason string, _ int32, podID string) {
	// We can't reference hermesv1 enum types here without import noise — coerce.
}

// Use handler.NopPublisher for actual test runs; we'll assert state
// changes via the mock store instead.

// makeTicker builds a Ticker with a scripted client + fresh mock store.
func makeTicker(t *testing.T, dek crypto.DataEncryptionKey, client refreshClient) (*Ticker, *mock.Store) {
	t.Helper()
	st := mock.NewStore()
	tk, err := New(Options{
		Store:     st,
		DEK:       dek,
		Publisher: handler.NopPublisher{},
		PodID:     "pod-test",
		Logger:    zerolog.Nop(),
		Interval:  time.Minute,
		Threshold: time.Hour,
		Concurrency: 1,
		ClientFactory: func(*auth.Creds, *web.Cookies) refreshClient {
			return client
		},
		NowFn: func() time.Time { return time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return tk, st
}

// ────────────────────────── Test cases ──────────────────────────────

func TestAttempt_MergeCookies_Persists(t *testing.T) {
	dek := newTestDEK(t)
	row := seedRow(t, dek, 100, validCookies())

	// Scripted Ping returns updated cookies.
	respTime := time.Date(2026, 5, 29, 11, 59, 30, 0, time.UTC)
	newJar, err := web.FromEnvelope(map[string]string{
		"c_user": "1674772559",
		"xs":     "32:newxsvalue:2:1700100000:-1:-1::AcXcoo", // rotated
		"datr":   "AbCDeFgHiJkLmNoPqRsTuVwX",
	})
	if err != nil {
		t.Fatalf("build new cookies: %v", err)
	}
	client := &scriptedPingClient{
		signal: &web.RefreshSignal{
			CookiesChanged: true,
			Cookies:        newJar,
			ResponseTime:   respTime,
		},
	}
	tk, st := makeTicker(t, dek, client)
	if err := st.CreateSession(context.Background(), row); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got := tk.attemptRefresh(context.Background(), row)
	if got.Outcome != "merge_cookies" {
		t.Fatalf("outcome: got %q want merge_cookies (err=%v)", got.Outcome, got.Err)
	}
	if got.Err != nil {
		t.Errorf("unexpected err: %v", got.Err)
	}

	// Verify row's cookies updated (encrypted) and LastRefreshedAt advanced.
	after, err := st.GetSession(context.Background(), row.UID)
	if err != nil {
		t.Fatalf("re-fetch: %v", err)
	}
	if after.LastRefreshedAt == nil || !after.LastRefreshedAt.Equal(respTime) {
		t.Errorf("LastRefreshedAt: got %v want %v", after.LastRefreshedAt, respTime)
	}
	// Decrypt new cookies envelope and verify xs rotated.
	plain, err := crypto.DecryptAESGCM(dek, after.EncryptedCookies,
		store.BuildAAD(store.AADCookies, row.UID))
	if err != nil {
		t.Fatalf("decrypt new cookies: %v", err)
	}
	var newEnv auth.BridgeEnvelope
	if err := json.Unmarshal(plain, &newEnv); err != nil {
		t.Fatalf("unmarshal new envelope: %v", err)
	}
	if newEnv.Cookies["xs"] != "32:newxsvalue:2:1700100000:-1:-1::AcXcoo" {
		t.Errorf("new xs not persisted: got %q", newEnv.Cookies["xs"])
	}
}

func TestAttempt_BumpValidated_NoCookieChange(t *testing.T) {
	dek := newTestDEK(t)
	row := seedRow(t, dek, 101, validCookies())

	respTime := time.Date(2026, 5, 29, 11, 59, 45, 0, time.UTC)
	sameJar, _ := web.FromEnvelope(validCookies())
	client := &scriptedPingClient{
		signal: &web.RefreshSignal{
			CookiesChanged: false,
			Cookies:        sameJar,
			ResponseTime:   respTime,
		},
	}
	tk, st := makeTicker(t, dek, client)
	_ = st.CreateSession(context.Background(), row)

	got := tk.attemptRefresh(context.Background(), row)
	if got.Outcome != "bump_validated" {
		t.Fatalf("outcome: got %q want bump_validated (err=%v)", got.Outcome, got.Err)
	}

	after, _ := st.GetSession(context.Background(), row.UID)
	// LastRefreshedAt unchanged (still stale), but envelope inside
	// EncryptedCookies should have LastValidatedAt advanced.
	plain, _ := crypto.DecryptAESGCM(dek, after.EncryptedCookies,
		store.BuildAAD(store.AADCookies, row.UID))
	var env auth.BridgeEnvelope
	_ = json.Unmarshal(plain, &env)
	if !env.LastValidatedAt.Equal(respTime) {
		t.Errorf("LastValidatedAt: got %v want %v", env.LastValidatedAt, respTime)
	}
}

func TestAttempt_BurnPermanent_OnTokenInvalidated(t *testing.T) {
	dek := newTestDEK(t)
	row := seedRow(t, dek, 102, validCookies())

	client := &scriptedPingClient{err: web.ErrTokenInvalidated}
	tk, st := makeTicker(t, dek, client)
	_ = st.CreateSession(context.Background(), row)

	got := tk.attemptRefresh(context.Background(), row)
	if got.Outcome != "burn_permanent" {
		t.Fatalf("outcome: got %q want burn_permanent (err=%v)", got.Outcome, got.Err)
	}
	if got.Reason != "token_invalidated" {
		t.Errorf("reason: got %q want token_invalidated", got.Reason)
	}

	after, _ := st.GetSession(context.Background(), row.UID)
	if after.State != "burned" {
		t.Errorf("state: got %q want burned", after.State)
	}
}

func TestAttempt_Suspend_OnCheckpoint(t *testing.T) {
	dek := newTestDEK(t)
	row := seedRow(t, dek, 103, validCookies())

	client := &scriptedPingClient{err: web.ErrCheckpointRequired}
	tk, st := makeTicker(t, dek, client)
	_ = st.CreateSession(context.Background(), row)

	got := tk.attemptRefresh(context.Background(), row)
	if got.Outcome != "suspend" {
		t.Fatalf("outcome: got %q want suspend (err=%v)", got.Outcome, got.Err)
	}
	if got.Reason != "checkpoint_required" {
		t.Errorf("reason: got %q want checkpoint_required", got.Reason)
	}

	after, _ := st.GetSession(context.Background(), row.UID)
	if after.State != "suspended" {
		t.Errorf("state: got %q want suspended", after.State)
	}
}

func TestAttempt_TransientError_NoStateChange(t *testing.T) {
	dek := newTestDEK(t)
	row := seedRow(t, dek, 104, validCookies())

	client := &scriptedPingClient{err: errors.New("connection reset")}
	tk, st := makeTicker(t, dek, client)
	_ = st.CreateSession(context.Background(), row)

	got := tk.attemptRefresh(context.Background(), row)
	if got.Outcome != "transient_error" {
		t.Fatalf("outcome: got %q want transient_error (err=%v)", got.Outcome, got.Err)
	}

	after, _ := st.GetSession(context.Background(), row.UID)
	if after.State != "active" {
		t.Errorf("state should remain active, got %q", after.State)
	}
}

func TestAttempt_CtxCancelMidPing(t *testing.T) {
	dek := newTestDEK(t)
	row := seedRow(t, dek, 105, validCookies())

	// Simulate Ping that respects ctx.
	client := &ctxRespectingClient{}
	tk, st := makeTicker(t, dek, client)
	_ = st.CreateSession(context.Background(), row)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already canceled

	got := tk.attemptRefresh(ctx, row)
	if got.Outcome != "transient_error" {
		t.Fatalf("outcome: got %q want transient_error (err=%v)", got.Outcome, got.Err)
	}
	if got.Reason != "ctx_canceled" {
		t.Errorf("reason: got %q want ctx_canceled", got.Reason)
	}
}

type ctxRespectingClient struct{}

func (c *ctxRespectingClient) Ping(ctx context.Context) (*web.RefreshSignal, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &web.RefreshSignal{}, nil
}

func TestAttempt_DecryptFailure_OnWrongDEK(t *testing.T) {
	dekA := newTestDEK(t)
	dekB := newTestDEK(t)
	row := seedRow(t, dekA, 106, validCookies())

	tk, st := makeTicker(t, dekB, &scriptedPingClient{}) // wrong DEK
	_ = st.CreateSession(context.Background(), row)

	got := tk.attemptRefresh(context.Background(), row)
	if got.Outcome != "decrypt_failed" {
		t.Fatalf("outcome: got %q want decrypt_failed (err=%v)", got.Outcome, got.Err)
	}

	// Crucial: state must NOT have changed. DEK drift should never
	// burn good sessions.
	after, _ := st.GetSession(context.Background(), row.UID)
	if after.State != "active" {
		t.Errorf("state should remain active on decrypt failure, got %q", after.State)
	}
}

func TestAttempt_NoCookies_LegacyRow(t *testing.T) {
	dek := newTestDEK(t)
	row := seedRow(t, dek, 107, validCookies())
	row.EncryptedCookies = nil // legacy pre-Stage-D row

	tk, st := makeTicker(t, dek, &scriptedPingClient{})
	_ = st.CreateSession(context.Background(), row)

	got := tk.attemptRefresh(context.Background(), row)
	if got.Outcome != "transient_error" {
		t.Fatalf("outcome: got %q want transient_error (err=%v)", got.Outcome, got.Err)
	}
	if got.Reason != "no_cookies" {
		t.Errorf("reason: got %q want no_cookies", got.Reason)
	}
}

// TestAttempt_EnvelopeUnmarshalFailure: the cookies column decrypts
// but the bytes inside aren't valid envelope JSON. Treated as
// transient — operator triage required.
func TestAttempt_EnvelopeUnmarshalFailure(t *testing.T) {
	dek := newTestDEK(t)
	row := seedRow(t, dek, 108, validCookies())

	// Re-encrypt cookies column with garbage payload.
	garbageEnc, err := crypto.EncryptAESGCM(dek, []byte("not-json"), store.BuildAAD(store.AADCookies, row.UID))
	if err != nil {
		t.Fatalf("encrypt garbage: %v", err)
	}
	row.EncryptedCookies = garbageEnc

	tk, st := makeTicker(t, dek, &scriptedPingClient{})
	_ = st.CreateSession(context.Background(), row)

	got := tk.attemptRefresh(context.Background(), row)
	if got.Outcome != "envelope_unmarshal_failed" {
		t.Fatalf("outcome: got %q want envelope_unmarshal_failed (err=%v)", got.Outcome, got.Err)
	}
}

// TestAttempt_SuspendOnConsent + TestAttempt_SuspendOnChallenge round
// out the 5-sentinel coverage from chunk-7's classify.go.
func TestAttempt_SuspendOnConsent(t *testing.T) {
	dek := newTestDEK(t)
	row := seedRow(t, dek, 109, validCookies())

	tk, st := makeTicker(t, dek, &scriptedPingClient{err: web.ErrConsentRequired})
	_ = st.CreateSession(context.Background(), row)

	got := tk.attemptRefresh(context.Background(), row)
	if got.Outcome != "suspend" || got.Reason != "consent_required" {
		t.Fatalf("got outcome=%q reason=%q want suspend/consent_required (err=%v)",
			got.Outcome, got.Reason, got.Err)
	}
}

func TestAttempt_SuspendOnChallenge(t *testing.T) {
	dek := newTestDEK(t)
	row := seedRow(t, dek, 110, validCookies())

	tk, st := makeTicker(t, dek, &scriptedPingClient{err: web.ErrChallengeRequired})
	_ = st.CreateSession(context.Background(), row)

	got := tk.attemptRefresh(context.Background(), row)
	if got.Outcome != "suspend" || got.Reason != "challenge_required" {
		t.Fatalf("got outcome=%q reason=%q want suspend/challenge_required (err=%v)",
			got.Outcome, got.Reason, got.Err)
	}
}

func TestAttempt_BurnOnAccountSuspended(t *testing.T) {
	dek := newTestDEK(t)
	row := seedRow(t, dek, 111, validCookies())

	tk, st := makeTicker(t, dek, &scriptedPingClient{err: web.ErrAccountSuspended})
	_ = st.CreateSession(context.Background(), row)

	got := tk.attemptRefresh(context.Background(), row)
	if got.Outcome != "burn_permanent" || got.Reason != "account_suspended" {
		t.Fatalf("got outcome=%q reason=%q want burn_permanent/account_suspended (err=%v)",
			got.Outcome, got.Reason, got.Err)
	}
}

// (httptest unused but kept for future end-to-end integration if needed)
var _ = httptest.NewServer
var _ = http.MethodGet

// TestAttempt_LatencyIsPopulated pins F2 (audit): the deferred
// Latency write must reach the caller. Trivially false with a
// plain-return + value-receiver function; requires named return
// or pointer-result.
func TestAttempt_LatencyIsPopulated(t *testing.T) {
	dek := newTestDEK(t)
	row := seedRow(t, dek, 999, validCookies())

	respTime := time.Date(2026, 5, 29, 12, 0, 30, 0, time.UTC)
	sameJar, _ := web.FromEnvelope(validCookies())

	// Inject a NowFn that advances by 5s between start and end so
	// we have a non-zero latency to verify.
	var calls int
	now := func() time.Time {
		calls++
		if calls == 1 {
			return time.Date(2026, 5, 29, 12, 0, 25, 0, time.UTC)
		}
		return time.Date(2026, 5, 29, 12, 0, 30, 0, time.UTC)
	}

	st := mock.NewStore()
	tk, err := New(Options{
		Store: st, DEK: dek, Publisher: handler.NopPublisher{}, PodID: "pod-test",
		Logger:    zerolog.Nop(),
		Interval:  time.Minute,
		Threshold: time.Hour,
		ClientFactory: func(*auth.Creds, *web.Cookies) refreshClient {
			return &scriptedPingClient{
				signal: &web.RefreshSignal{
					CookiesChanged: false,
					Cookies:        sameJar,
					ResponseTime:   respTime,
				},
			}
		},
		NowFn: now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_ = st.CreateSession(context.Background(), row)

	got := tk.attemptRefresh(context.Background(), row)
	if got.Latency != 5*time.Second {
		t.Errorf("Latency: got %v want 5s (deferred mutation reached caller?)", got.Latency)
	}
}
