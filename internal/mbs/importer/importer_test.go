package importer

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mbs-native/auth"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/hermes-waba/hermes/internal/mbs/handler"
	"github.com/hermes-waba/hermes/internal/mbs/store"
	"github.com/hermes-waba/hermes/internal/mbs/store/mock"
	"github.com/rs/zerolog"
)

// fixedNow is a deterministic clock for tests so CreatedAt comparisons
// don't race the system clock.
func fixedNow() time.Time {
	return time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
}

// quietLog returns a zero-output logger so test output stays clean.
// (Errors still fire as t.Errorf if the test asserts them; the
// zerolog noise is suppressed.)
func quietLog() zerolog.Logger {
	return zerolog.New(zerolog.Nop().Output(nil)).Level(zerolog.Disabled)
}

// writeCreds writes a Creds JSON file to dir/<uid>.json.
func writeCreds(t *testing.T, dir string, uid int64, mutators ...func(*auth.Creds)) string {
	t.Helper()
	c := validCreds(uid)
	for _, m := range mutators {
		m(c)
	}
	path := filepath.Join(dir, formatUID(uid)+".json")
	if err := c.Save(path); err != nil {
		t.Fatalf("Save creds: %v", err)
	}
	return path
}

func formatUID(uid int64) string {
	// Match auth.SessionPath naming.
	return time.Unix(0, uid).Format("") // unused — replaced below
}

// writeEnvelope writes a BridgeEnvelope to dir/<uid>.bridge.json.
func writeEnvelope(t *testing.T, dir string, uid int64, mut func(*auth.BridgeEnvelope)) string {
	t.Helper()
	env := &auth.BridgeEnvelope{
		Version:     auth.SupportedBridgeVersion,
		IssuedAt:    fixedNow().Unix(),
		AccessToken: validCreds(uid).AccessToken,
		UID:         uid,
		SessionKey:  validCreds(uid).SessionKey,
		Secret:      validCreds(uid).Secret,
		MachineID:   validCreds(uid).MachineID,
		Cookies: map[string]string{
			"c_user": uidStr(uid),
			"xs":     "session-xs-" + uidStr(uid),
			"datr":   "datr-token",
		},
	}
	if mut != nil {
		mut(env)
	}
	path := filepath.Join(dir, uidStr(uid)+".bridge.json")
	data, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write envelope: %v", err)
	}
	return path
}

func uidStr(uid int64) string {
	return strings.TrimPrefix(string([]byte{
		byte('0' + (uid/1000000000)%10),
		byte('0' + (uid/100000000)%10),
		byte('0' + (uid/10000000)%10),
		byte('0' + (uid/1000000)%10),
		byte('0' + (uid/100000)%10),
		byte('0' + (uid/10000)%10),
		byte('0' + (uid/1000)%10),
		byte('0' + (uid/100)%10),
		byte('0' + (uid/10)%10),
		byte('0' + uid%10),
	}), "0")
}

// Replace writeCreds path computation with a sensible uid stringification.
// (formatUID above was a stub.)
func init() {
	_ = formatUID // keep symbol
}

// ─── tests ────────────────────────────────────────────────────────────

func TestRun_HappyPath_CreatesRow(t *testing.T) {
	dir := t.TempDir()
	const uid int64 = 1674772559
	credsPath := filepath.Join(dir, "1674772559.json")
	c := validCreds(uid)
	if err := c.Save(credsPath); err != nil {
		t.Fatalf("Save creds: %v", err)
	}

	st := mock.NewStore()
	pub := newRecordingPublisher()
	stats, err := Run(context.Background(), Options{
		SessionsDir: dir,
		TenantID:    "tenant-A",
		Store:       st,
		DEK:         testDEK(t),
		Publisher:   pub,
		Logger:      quietLog(),
		Now:         fixedNow,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if stats.Total != 1 || stats.Imported != 1 || stats.Skipped != 0 || stats.Failed != 0 {
		t.Fatalf("stats: %+v", stats)
	}

	row, err := st.GetSession(context.Background(), uid)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if row.TenantID != "tenant-A" {
		t.Errorf("tenant mismatch: %q", row.TenantID)
	}
	if row.State != "active" {
		t.Errorf("state should be active: %q", row.State)
	}
	if row.PodID != "" {
		t.Errorf("PodID should be empty (importer doesn't claim): %q", row.PodID)
	}
	if len(row.EncryptedAccessToken) == 0 || len(row.EncryptedSecret) == 0 || len(row.EncryptedSessionKey) == 0 {
		t.Errorf("encrypted columns empty")
	}
	if len(row.EncryptedCookies) != 0 {
		t.Errorf("cookies should be empty (no envelope sidecar): %d bytes", len(row.EncryptedCookies))
	}

	// Lifecycle event emitted.
	if got := pub.lifecycleCount(); got != 1 {
		t.Errorf("want 1 lifecycle event, got %d", got)
	}
	if ev := pub.firstLifecycle(); ev.reason != "imported" {
		t.Errorf("want reason 'imported', got %q", ev.reason)
	}
}

func TestRun_WithEnvelope_PersistsCookies(t *testing.T) {
	dir := t.TempDir()
	const uid int64 = 42
	c := validCreds(uid)
	if err := c.Save(filepath.Join(dir, "42.json")); err != nil {
		t.Fatalf("Save creds: %v", err)
	}
	env := &auth.BridgeEnvelope{
		Version:     auth.SupportedBridgeVersion,
		AccessToken: c.AccessToken,
		UID:         uid,
		SessionKey:  c.SessionKey,
		Secret:      c.Secret,
		MachineID:   c.MachineID,
		Cookies:     map[string]string{"c_user": "42", "datr": "datr-test"},
	}
	envData, _ := json.Marshal(env)
	if err := os.WriteFile(filepath.Join(dir, "42.bridge.json"), envData, 0o600); err != nil {
		t.Fatalf("write envelope: %v", err)
	}

	st := mock.NewStore()
	stats, err := Run(context.Background(), Options{
		SessionsDir: dir, TenantID: "tenant-B",
		Store: st, DEK: testDEK(t),
		Publisher: handler.NopPublisher{}, Logger: quietLog(), Now: fixedNow,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Imported != 1 {
		t.Fatalf("stats: %+v", stats)
	}
	row, _ := st.GetSession(context.Background(), uid)
	if len(row.EncryptedCookies) == 0 {
		t.Error("EncryptedCookies should be populated when envelope present")
	}
	if len(row.BridgeEnvelope) == 0 {
		t.Error("BridgeEnvelope JSONB should be populated when envelope present")
	}
}

func TestRun_IdempotentSkip_NoForce(t *testing.T) {
	dir := t.TempDir()
	const uid int64 = 1
	c := validCreds(uid)
	_ = c.Save(filepath.Join(dir, "1.json"))

	st := mock.NewStore()
	// Pre-populate.
	_ = st.CreateSession(context.Background(), &store.SessionRow{
		UID: uid, TenantID: "tenant-A", State: "active",
		EncryptedAccessToken: []byte("EXISTING"),
		EncryptedSecret:      []byte("EXISTING"),
		EncryptedSessionKey:  []byte("EXISTING"),
	})

	stats, err := Run(context.Background(), Options{
		SessionsDir: dir, TenantID: "tenant-A",
		Store: st, DEK: testDEK(t),
		Publisher: handler.NopPublisher{}, Logger: quietLog(), Now: fixedNow,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Skipped != 1 || stats.Imported != 0 || stats.Forced != 0 || stats.Failed != 0 {
		t.Fatalf("stats: %+v", stats)
	}

	row, _ := st.GetSession(context.Background(), uid)
	if string(row.EncryptedAccessToken) != "EXISTING" {
		t.Error("existing row should NOT be overwritten when Force=false")
	}
}

func TestRun_ForceOverwrites_SameTenant(t *testing.T) {
	dir := t.TempDir()
	const uid int64 = 1
	c := validCreds(uid)
	_ = c.Save(filepath.Join(dir, "1.json"))

	st := mock.NewStore()
	_ = st.CreateSession(context.Background(), &store.SessionRow{
		UID: uid, TenantID: "tenant-A", State: "burned", // pretend a previous burn
		EncryptedAccessToken: []byte("STALE"),
		EncryptedSecret:      []byte("STALE"),
		EncryptedSessionKey:  []byte("STALE"),
		BurnedReason:         "checkpoint_required",
	})

	stats, err := Run(context.Background(), Options{
		SessionsDir: dir, TenantID: "tenant-A",
		Store: st, DEK: testDEK(t),
		Publisher: handler.NopPublisher{}, Logger: quietLog(),
		Force: true, Now: fixedNow,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Forced != 1 || stats.Imported != 0 || stats.Failed != 0 {
		t.Fatalf("stats: %+v", stats)
	}

	row, _ := st.GetSession(context.Background(), uid)
	if string(row.EncryptedAccessToken) == "STALE" {
		t.Error("EncryptedAccessToken should be replaced under --force")
	}
	if row.State != "active" {
		t.Errorf("state should be reset to active under --force, got %q", row.State)
	}
}

func TestRun_RefusesCrossTenant_EvenWithForce(t *testing.T) {
	// This is the critical security guarantee — --force is NOT a
	// tenant-bypass switch. Existing tenant-A row must NOT be
	// overwritten with tenant-B's import.
	dir := t.TempDir()
	const uid int64 = 1
	_ = validCreds(uid).Save(filepath.Join(dir, "1.json"))

	st := mock.NewStore()
	original := &store.SessionRow{
		UID: uid, TenantID: "tenant-A", State: "active",
		EncryptedAccessToken: []byte("TENANT-A-SECRET"),
		EncryptedSecret:      []byte("TENANT-A-SECRET"),
		EncryptedSessionKey:  []byte("TENANT-A-SECRET"),
	}
	_ = st.CreateSession(context.Background(), original)

	stats, err := Run(context.Background(), Options{
		SessionsDir: dir, TenantID: "tenant-B", // <-- DIFFERENT
		Store: st, DEK: testDEK(t),
		Publisher: handler.NopPublisher{}, Logger: quietLog(),
		Force: true, // even with force, must refuse
		Now:   fixedNow,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Failed != 1 || stats.Forced != 0 || stats.Imported != 0 {
		t.Fatalf("stats: %+v — cross-tenant import should fail-closed", stats)
	}

	row, _ := st.GetSession(context.Background(), uid)
	if row.TenantID != "tenant-A" {
		t.Errorf("tenant_id MUTATED: %q (was tenant-A) — SECURITY REGRESSION", row.TenantID)
	}
	if string(row.EncryptedAccessToken) != "TENANT-A-SECRET" {
		t.Errorf("secret OVERWRITTEN by cross-tenant import — SECURITY REGRESSION")
	}
}

func TestRun_DryRun_NoWrites(t *testing.T) {
	dir := t.TempDir()
	const uid int64 = 7
	_ = validCreds(uid).Save(filepath.Join(dir, "7.json"))

	st := mock.NewStore()
	pub := newRecordingPublisher()
	stats, err := Run(context.Background(), Options{
		SessionsDir: dir, TenantID: "tenant-A",
		Store: st, DEK: testDEK(t),
		Publisher: pub, Logger: quietLog(),
		DryRun: true, Now: fixedNow,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Imported != 1 || !stats.DryRun {
		t.Fatalf("stats: %+v", stats)
	}

	// No row created.
	if _, err := st.GetSession(context.Background(), uid); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("dry-run created a row: %v", err)
	}
	// No lifecycle emitted.
	if pub.lifecycleCount() != 0 {
		t.Errorf("dry-run should not emit lifecycle events, got %d", pub.lifecycleCount())
	}
}

func TestRun_BadJSON_FailedNotPanic(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "100.json")
	if err := os.WriteFile(bad, []byte("{not-json"), 0o600); err != nil {
		t.Fatalf("write bad json: %v", err)
	}
	st := mock.NewStore()
	stats, err := Run(context.Background(), Options{
		SessionsDir: dir, TenantID: "tenant-A",
		Store: st, DEK: testDEK(t),
		Publisher: handler.NopPublisher{}, Logger: quietLog(), Now: fixedNow,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Failed != 1 || stats.Imported != 0 {
		t.Fatalf("stats: %+v", stats)
	}
}

func TestRun_UIDMismatch_Failed(t *testing.T) {
	// Filename says uid=200 but the JSON's UserID says 999.
	dir := t.TempDir()
	c := validCreds(999)
	if err := c.Save(filepath.Join(dir, "200.json")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	st := mock.NewStore()
	stats, _ := Run(context.Background(), Options{
		SessionsDir: dir, TenantID: "tenant-A",
		Store: st, DEK: testDEK(t),
		Publisher: handler.NopPublisher{}, Logger: quietLog(), Now: fixedNow,
	})
	if stats.Failed != 1 {
		t.Fatalf("uid mismatch should fail: %+v", stats)
	}
}

func TestRun_MissingEnvelope_NotAnError(t *testing.T) {
	// Pre-Stage-D sessions had no <uid>.bridge.json sidecar. That's
	// the COMMON case, not an error. Import must proceed.
	dir := t.TempDir()
	_ = validCreds(5).Save(filepath.Join(dir, "5.json"))
	st := mock.NewStore()
	stats, _ := Run(context.Background(), Options{
		SessionsDir: dir, TenantID: "tenant-A",
		Store: st, DEK: testDEK(t),
		Publisher: handler.NopPublisher{}, Logger: quietLog(), Now: fixedNow,
	})
	if stats.Imported != 1 || stats.Failed != 0 {
		t.Fatalf("missing envelope should not fail: %+v", stats)
	}
}

func TestRun_MalformedEnvelope_Failed(t *testing.T) {
	dir := t.TempDir()
	_ = validCreds(5).Save(filepath.Join(dir, "5.json"))
	if err := os.WriteFile(filepath.Join(dir, "5.bridge.json"), []byte("not-json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	st := mock.NewStore()
	stats, _ := Run(context.Background(), Options{
		SessionsDir: dir, TenantID: "tenant-A",
		Store: st, DEK: testDEK(t),
		Publisher: handler.NopPublisher{}, Logger: quietLog(), Now: fixedNow,
	})
	if stats.Failed != 1 {
		t.Fatalf("malformed envelope should fail: %+v", stats)
	}
}

func TestRun_MultipleSessions_AllImported(t *testing.T) {
	dir := t.TempDir()
	uids := []int64{100, 200, 300}
	for _, u := range uids {
		_ = validCreds(u).Save(filepath.Join(dir, formatUidName(u)))
	}
	st := mock.NewStore()
	stats, err := Run(context.Background(), Options{
		SessionsDir: dir, TenantID: "tenant-A",
		Store: st, DEK: testDEK(t),
		Publisher: handler.NopPublisher{}, Logger: quietLog(), Now: fixedNow,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Total != 3 || stats.Imported != 3 {
		t.Fatalf("stats: %+v", stats)
	}
}

func TestRun_ContextCancellation_StopsCleanly(t *testing.T) {
	dir := t.TempDir()
	// Many sessions so cancel mid-run.
	for i := int64(1); i <= 20; i++ {
		_ = validCreds(i).Save(filepath.Join(dir, formatUidName(i)))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already-canceled context
	st := mock.NewStore()
	stats, err := Run(ctx, Options{
		SessionsDir: dir, TenantID: "tenant-A",
		Store: st, DEK: testDEK(t),
		Publisher: handler.NopPublisher{}, Logger: quietLog(), Now: fixedNow,
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("want context.Canceled, got %v", err)
	}
	if stats == nil {
		t.Fatal("stats should be non-nil on cancellation")
	}
}

func TestRun_OptionsValidation(t *testing.T) {
	dek := testDEK(t)
	st := mock.NewStore()
	dir := t.TempDir()

	tests := []struct {
		name string
		opts Options
		want string
	}{
		{"no dir", Options{TenantID: "t", Store: st, DEK: dek, Logger: quietLog()}, "SessionsDir"},
		{"no tenant", Options{SessionsDir: dir, Store: st, DEK: dek, Logger: quietLog()}, "TenantID"},
		{"no store", Options{SessionsDir: dir, TenantID: "t", DEK: dek, Logger: quietLog()}, "Store"},
		{"zero DEK", Options{SessionsDir: dir, TenantID: "t", Store: st, Logger: quietLog()}, "DEK"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Run(context.Background(), tc.opts)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestRun_PublisherNil_NoEmit(t *testing.T) {
	// Publisher: nil → skip emit silently. The Run wiring uses an
	// explicit nil-check so callers (cmd/mbs/main bootstrap path)
	// can omit Publisher when they don't want NATS events.
	dir := t.TempDir()
	_ = validCreds(1).Save(filepath.Join(dir, "1.json"))
	st := mock.NewStore()
	stats, err := Run(context.Background(), Options{
		SessionsDir: dir, TenantID: "tenant-A",
		Store: st, DEK: testDEK(t),
		Publisher: nil, Logger: quietLog(), Now: fixedNow,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Imported != 1 {
		t.Fatalf("stats: %+v", stats)
	}
	// No panic on nil publisher = pass.
}

func TestRun_AssetSynthesis_FromCreds(t *testing.T) {
	// When legacy creds.json has PageID / WABAID / WECMailboxID
	// populated, importer should synthesize a primary AssetRow.
	dir := t.TempDir()
	const uid int64 = 8
	c := validCreds(uid)
	c.PageID = "1234567890"
	c.PageName = "Test Page"
	c.WABAID = "9999"
	c.WECMailboxID = "5555"
	c.WECPhoneNumber = "+6281234567"
	c.BusinessID = "biz-1"
	c.WECAccountRegistered = true
	_ = c.Save(filepath.Join(dir, "8.json"))

	st := mock.NewStore()
	_, err := Run(context.Background(), Options{
		SessionsDir: dir, TenantID: "tenant-A",
		Store: st, DEK: testDEK(t),
		Publisher: handler.NopPublisher{}, Logger: quietLog(), Now: fixedNow,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	assets, err := st.ListAssets(context.Background(), uid)
	if err != nil {
		t.Fatalf("ListAssets: %v", err)
	}
	if len(assets) != 1 {
		t.Fatalf("want 1 asset, got %d", len(assets))
	}
	a := assets[0]
	if a.PageID != "1234567890" || a.WabaID != "9999" || a.WecMailboxID != "5555" {
		t.Errorf("asset fields wrong: %+v", a)
	}
	if !a.IsPrimary {
		t.Error("synthesized asset should be primary")
	}
}

// formatUidName returns "<uid>.json" — needed because the helper at the
// top of this file (formatUID) was the placeholder; replace with simple
// fmt-based variant here.
func formatUidName(uid int64) string {
	return uidToString(uid) + ".json"
}

func uidToString(uid int64) string {
	if uid == 0 {
		return "0"
	}
	neg := false
	if uid < 0 {
		neg = true
		uid = -uid
	}
	var buf [20]byte
	i := len(buf)
	for uid > 0 {
		i--
		buf[i] = byte('0' + uid%10)
		uid /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// ─── publisher recorder ───────────────────────────────────────────────

type lifecycleEvent struct {
	uid    int64
	tenant string
	prev   hermesv1.MbsSessionState
	next   hermesv1.MbsSessionState
	reason string
}

type recordingPub struct {
	events []lifecycleEvent
}

func newRecordingPublisher() *recordingPub {
	return &recordingPub{}
}

func (p *recordingPub) PublishInboundMessage(int64, string, string, string, string, string, string, string, time.Time) {
}

func (p *recordingPub) PublishOutbound(int64, string, string, string, string, int64, bool, string, time.Time) {
}

func (p *recordingPub) PublishSessionLifecycle(uid int64, tenant string, prev, next hermesv1.MbsSessionState, reason string, _ int32, _ string) {
	p.events = append(p.events, lifecycleEvent{uid, tenant, prev, next, reason})
}

func (p *recordingPub) lifecycleCount() int {
	return len(p.events)
}

func (p *recordingPub) firstLifecycle() lifecycleEvent {
	if len(p.events) == 0 {
		return lifecycleEvent{}
	}
	return p.events[0]
}

// Compile-time guard.
var _ handler.EventPublisher = (*recordingPub)(nil)
