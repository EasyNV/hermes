package handler

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"mbs-native/auth"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/hermes-waba/hermes/internal/mbs/store"
	"github.com/hermes-waba/hermes/internal/mbs/store/mock"
	"github.com/hermes-waba/hermes/pkg/crypto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ─────────────────────────────────────────────────────────────────────
// fakePhoneResolver — scriptable resolver for ResolvePhone tests
// ─────────────────────────────────────────────────────────────────────

type fakePhoneResolver struct {
	mu         atomic.Int64 // call counter
	thread     string
	mailbox    string
	err        error
	seenPage   atomic.Value // string
	seenPhone  atomic.Value // string
}

func (f *fakePhoneResolver) ResolvePhoneToThreadID(ctx context.Context, pageID, phone string) (string, string, error) {
	f.mu.Add(1)
	f.seenPage.Store(pageID)
	f.seenPhone.Store(phone)
	if f.err != nil {
		return "", "", f.err
	}
	return f.thread, f.mailbox, nil
}

// seedEncryptedSession seeds a session row with valid encrypted creds
// (AAD-bound per column) so decryptCredsForUID succeeds.
func seedEncryptedSession(t *testing.T, st *mock.Store, dek crypto.DataEncryptionKey, uid int64, tenantID string) {
	t.Helper()
	enc := func(col store.AADColumn, pt string) []byte {
		ct, err := crypto.EncryptAESGCM(dek, []byte(pt), store.BuildAAD(col, uid))
		if err != nil {
			t.Fatalf("encrypt %s: %v", col, err)
		}
		return ct
	}
	row := &store.SessionRow{
		UID:                  uid,
		TenantID:             tenantID,
		State:                "active",
		EncryptedAccessToken: enc(store.AADAccessToken, "EAAB-plaintext"),
		EncryptedSecret:      enc(store.AADSecret, "cac415ec0937d6f1c78cf6fba753c9d1"),
		EncryptedSessionKey:  enc(store.AADSessionKey, "5.0oor9VhiOfiTgg.1778254326.11-1"),

		// Plaintext identity fields required for auth.Creds.Validate().
		FamilyDeviceID:   "7a17b762-668d-4bef-a9cf-cd0abd58231c",
		DeviceID:         "7a17b762-668d-4bef-a9cf-cd0abd58231d",
		AppVersion:       "551.0.0.55.106",
		BuildNumber:      "955655792",
		DeviceModel:      "SM-S931B",
		AndroidVer:       "15",
		Manufacturer:     "samsung",
		Locale:           "en_US",
		Density:          "2.99375",
		ScreenWidth:      1080,
		ScreenHeight:     2340,
		ABI:              "arm64-v8a",
		VersionID:        "26854813974149875",
		MQTTCapabilities: 514,
	}
	if err := st.CreateSession(context.Background(), row); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := st.UpsertAssets(context.Background(), uid, []*store.AssetRow{
		{UID: uid, PageID: "page-PRIMARY", PageName: "P", WabaID: "w", WecMailboxID: "m", IsPrimary: true},
	}); err != nil {
		t.Fatalf("UpsertAssets: %v", err)
	}
}

// newResolveHandler returns a handler with an injected fakePhoneResolver
// so tests can script the live-resolve outcome.
func newResolveHandler(t *testing.T, resolver *fakePhoneResolver) (*Handler, *mock.Store, *recordingPublisher, crypto.DataEncryptionKey) {
	t.Helper()
	st := mock.NewStore()
	pub := &recordingPublisher{}
	dek := newTestDEK(t)

	factory := PhoneResolverFactory(func(*auth.Creds) (PhoneResolver, error) {
		return resolver, nil
	})

	h, err := NewHandler(Options{
		Store:           st,
		Manager:         &nopManager{},
		Publisher:       pub,
		DriverFactory:   DriverFactory(func(DriverOptions) Driver { return nil }),
		ResolverFactory: factory,
		DEK:             dek,
		PodID:           "pod-test",
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return h, st, pub, dek
}

// ─────────────────────────────────────────────────────────────────────
// Tests
// ─────────────────────────────────────────────────────────────────────

func TestResolvePhone_CacheHit_NoLiveCall(t *testing.T) {
	resolver := &fakePhoneResolver{thread: "should-not-see", mailbox: "should-not-see"}
	h, st, _, _ := newResolveHandler(t, resolver)
	seedEncryptedSession(t, st, h.dek, 100, "tenant-A")

	// Seed cache.
	if err := st.UpsertPhoneThread(context.Background(), &store.PhoneThreadRow{
		UID: 100, PageID: "page-PRIMARY", Phone: "6281234567890",
		ThreadID: "cached-thread-1", WecMailboxID: "cached-mbox",
	}); err != nil {
		t.Fatal(err)
	}

	resp, err := h.ResolvePhone(ctxWith("tenant-A"), &hermesv1.ResolvePhoneRequest{
		Uid: 100, Phone: "0812-3456-7890",
	})
	if err != nil {
		t.Fatalf("ResolvePhone: %v", err)
	}
	if resp.ThreadId != "cached-thread-1" || !resp.WasCached {
		t.Errorf("expected cache hit, got %+v", resp)
	}
	if resp.WecMailboxId != "cached-mbox" {
		t.Errorf("mailbox: got %q", resp.WecMailboxId)
	}
	if resp.NormalizedPhone != "6281234567890" {
		t.Errorf("normalized: got %q want 6281234567890", resp.NormalizedPhone)
	}
	if resp.PageId != "page-PRIMARY" {
		t.Errorf("page_id: got %q", resp.PageId)
	}
	// CRITICAL: no live resolve call.
	if resolver.mu.Load() != 0 {
		t.Errorf("cache hit must not invoke live resolver, calls=%d", resolver.mu.Load())
	}
}

func TestResolvePhone_CacheMiss_LiveResolveAndWriteback(t *testing.T) {
	resolver := &fakePhoneResolver{thread: "live-thread-99", mailbox: "live-mbox-1"}
	h, st, _, _ := newResolveHandler(t, resolver)
	seedEncryptedSession(t, st, h.dek, 100, "tenant-A")

	resp, err := h.ResolvePhone(ctxWith("tenant-A"), &hermesv1.ResolvePhoneRequest{
		Uid: 100, Phone: "+62 812-3456-7890",
	})
	if err != nil {
		t.Fatalf("ResolvePhone: %v", err)
	}
	if resp.ThreadId != "live-thread-99" || resp.WasCached {
		t.Errorf("expected live resolve, got %+v", resp)
	}
	if resolver.mu.Load() != 1 {
		t.Errorf("live resolver should fire exactly once, got %d", resolver.mu.Load())
	}
	if got, _ := resolver.seenPhone.Load().(string); got != "6281234567890" {
		t.Errorf("resolver saw phone=%q want normalized 6281234567890", got)
	}
	if got, _ := resolver.seenPage.Load().(string); got != "page-PRIMARY" {
		t.Errorf("resolver saw page=%q want page-PRIMARY", got)
	}

	// Cache write-back.
	cached, err := st.GetPhoneThread(context.Background(), 100, "page-PRIMARY", "6281234567890")
	if err != nil {
		t.Fatalf("expected cache write-back, got %v", err)
	}
	if cached.ThreadID != "live-thread-99" || cached.WecMailboxID != "live-mbox-1" {
		t.Errorf("cache row: %+v", cached)
	}

	// Second call should hit cache.
	resp2, _ := h.ResolvePhone(ctxWith("tenant-A"), &hermesv1.ResolvePhoneRequest{
		Uid: 100, Phone: "6281234567890",
	})
	if !resp2.WasCached {
		t.Errorf("second call should be cached")
	}
	if resolver.mu.Load() != 1 {
		t.Errorf("second call must not re-resolve, calls=%d", resolver.mu.Load())
	}
}

func TestResolvePhone_BypassCache_ForcesLive(t *testing.T) {
	resolver := &fakePhoneResolver{thread: "live-thread", mailbox: "live-mbox"}
	h, st, _, _ := newResolveHandler(t, resolver)
	seedEncryptedSession(t, st, h.dek, 100, "tenant-A")

	// Pre-warm cache.
	if err := st.UpsertPhoneThread(context.Background(), &store.PhoneThreadRow{
		UID: 100, PageID: "page-PRIMARY", Phone: "6281234567890",
		ThreadID: "stale-thread", WecMailboxID: "stale-mbox",
	}); err != nil {
		t.Fatal(err)
	}

	resp, err := h.ResolvePhone(ctxWith("tenant-A"), &hermesv1.ResolvePhoneRequest{
		Uid: 100, Phone: "6281234567890", BypassCache: true,
	})
	if err != nil {
		t.Fatalf("ResolvePhone: %v", err)
	}
	if resp.WasCached {
		t.Error("BypassCache=true should report was_cached=false")
	}
	if resp.ThreadId != "live-thread" {
		t.Errorf("got %q want live-thread (stale cache should be overwritten)", resp.ThreadId)
	}
	if resolver.mu.Load() != 1 {
		t.Errorf("bypass must hit live, calls=%d", resolver.mu.Load())
	}

	// Cache row should be updated with fresh value.
	updated, _ := st.GetPhoneThread(context.Background(), 100, "page-PRIMARY", "6281234567890")
	if updated.ThreadID != "live-thread" {
		t.Errorf("write-back: got %q want live-thread", updated.ThreadID)
	}
}

func TestResolvePhone_PageIDOverride(t *testing.T) {
	resolver := &fakePhoneResolver{thread: "t-override", mailbox: "m-override"}
	h, st, _, _ := newResolveHandler(t, resolver)
	seedEncryptedSession(t, st, h.dek, 100, "tenant-A")

	resp, err := h.ResolvePhone(ctxWith("tenant-A"), &hermesv1.ResolvePhoneRequest{
		Uid: 100, Phone: "6281234567890", PageIdOverride: "page-OTHER",
	})
	if err != nil {
		t.Fatalf("ResolvePhone: %v", err)
	}
	if resp.PageId != "page-OTHER" {
		t.Errorf("page override ignored: got %q want page-OTHER", resp.PageId)
	}
	if got, _ := resolver.seenPage.Load().(string); got != "page-OTHER" {
		t.Errorf("resolver saw page=%q want page-OTHER", got)
	}
}

func TestResolvePhone_NoPrimaryPage_NoOverride_FailedPrecondition(t *testing.T) {
	resolver := &fakePhoneResolver{}
	h, st, _, _ := newResolveHandler(t, resolver)

	// Seed session WITHOUT assets.
	dek := h.dek
	enc := func(col store.AADColumn, pt string) []byte {
		ct, _ := crypto.EncryptAESGCM(dek, []byte(pt), store.BuildAAD(col, 100))
		return ct
	}
	_ = st.CreateSession(context.Background(), &store.SessionRow{
		UID: 100, TenantID: "tenant-A", State: "active",
		EncryptedAccessToken: enc(store.AADAccessToken, "EAAB"),
		EncryptedSecret:      enc(store.AADSecret, "cac4"),
		EncryptedSessionKey:  enc(store.AADSessionKey, "5.0"),
		FamilyDeviceID:       "fd-1", DeviceID: "d-1", AppVersion: "551",
		BuildNumber: "1", DeviceModel: "M", AndroidVer: "15", Manufacturer: "s",
		Locale: "en_US", Density: "3", ScreenWidth: 1, ScreenHeight: 1, ABI: "a", VersionID: "v",
		MQTTCapabilities: 1,
	})

	_, err := h.ResolvePhone(ctxWith("tenant-A"), &hermesv1.ResolvePhoneRequest{
		Uid: 100, Phone: "6281234567890",
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("expected FailedPrecondition, got %v", err)
	}
}

func TestResolvePhone_InvalidPhone_InvalidArgument(t *testing.T) {
	resolver := &fakePhoneResolver{}
	h, st, _, _ := newResolveHandler(t, resolver)
	seedEncryptedSession(t, st, h.dek, 100, "tenant-A")

	cases := []string{
		"",        // empty (caught before normalize, but same code)
		"abc",     // non-digits only
		"12345",   // too short post-normalize (<8 digits)
		"1234567890123456", // too long (>15 digits)
	}
	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			_, err := h.ResolvePhone(ctxWith("tenant-A"), &hermesv1.ResolvePhoneRequest{
				Uid: 100, Phone: p,
			})
			if status.Code(err) != codes.InvalidArgument {
				t.Errorf("phone=%q: expected InvalidArgument, got %v", p, err)
			}
		})
	}
}

func TestResolvePhone_TenantMismatch_PermissionDenied(t *testing.T) {
	resolver := &fakePhoneResolver{}
	h, st, _, _ := newResolveHandler(t, resolver)
	seedEncryptedSession(t, st, h.dek, 100, "tenant-A")

	_, err := h.ResolvePhone(ctxWith("tenant-B"), &hermesv1.ResolvePhoneRequest{
		Uid: 100, Phone: "6281234567890",
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Errorf("expected PermissionDenied, got %v", err)
	}
	if resolver.mu.Load() != 0 {
		t.Errorf("tenant mismatch must NOT invoke resolver, calls=%d", resolver.mu.Load())
	}
}

func TestResolvePhone_MissingTenant(t *testing.T) {
	resolver := &fakePhoneResolver{}
	h, st, _, _ := newResolveHandler(t, resolver)
	seedEncryptedSession(t, st, h.dek, 100, "tenant-A")

	_, err := h.ResolvePhone(context.Background(), &hermesv1.ResolvePhoneRequest{
		Uid: 100, Phone: "6281234567890",
	})
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("expected Unauthenticated, got %v", err)
	}
}

func TestResolvePhone_NotFound(t *testing.T) {
	resolver := &fakePhoneResolver{}
	h, _, _, _ := newResolveHandler(t, resolver)

	_, err := h.ResolvePhone(ctxWith("tenant-A"), &hermesv1.ResolvePhoneRequest{
		Uid: 999, Phone: "6281234567890",
	})
	if status.Code(err) != codes.NotFound {
		t.Errorf("expected NotFound, got %v", err)
	}
}

func TestResolvePhone_ResolverError_Mapped(t *testing.T) {
	// CreateCustomerError-shaped error → FailedPrecondition.
	resolver := &fakePhoneResolver{
		err: errors.New("create_customer rejected (phone=62 page=p mailbox=m code=ERR): bad number"),
	}
	h, st, _, _ := newResolveHandler(t, resolver)
	seedEncryptedSession(t, st, h.dek, 100, "tenant-A")

	_, err := h.ResolvePhone(ctxWith("tenant-A"), &hermesv1.ResolvePhoneRequest{
		Uid: 100, Phone: "6281234567890",
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("CreateCustomerError should map to FailedPrecondition, got %v", err)
	}
}

func TestResolvePhone_DecryptFails_Unauthenticated(t *testing.T) {
	resolver := &fakePhoneResolver{}
	h, st, _, _ := newResolveHandler(t, resolver)

	// Seed with a DIFFERENT DEK so handler's dek can't decrypt.
	wrongDEK := newTestDEK(t)
	seedEncryptedSession(t, st, wrongDEK, 100, "tenant-A")

	_, err := h.ResolvePhone(ctxWith("tenant-A"), &hermesv1.ResolvePhoneRequest{
		Uid: 100, Phone: "6281234567890",
	})
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("decrypt failure should map to Unauthenticated, got %v", err)
	}
	if resolver.mu.Load() != 0 {
		t.Errorf("decrypt fail must NOT reach resolver, calls=%d", resolver.mu.Load())
	}
}

func TestResolvePhone_CacheWritebackFailure_Survives(t *testing.T) {
	// Use a wrapper store that fails UpsertPhoneThread but otherwise
	// behaves like mock — verifies cache write-back failure doesn't
	// crash the resolve response.
	resolver := &fakePhoneResolver{thread: "t", mailbox: "m"}
	h, st, _, _ := newResolveHandler(t, resolver)
	seedEncryptedSession(t, st, h.dek, 100, "tenant-A")

	// Wrap the store with a fail-on-upsert decorator. Easiest path:
	// swap h.store with the wrapper after construction.
	h.store = &upsertFailStore{Store: st}

	resp, err := h.ResolvePhone(ctxWith("tenant-A"), &hermesv1.ResolvePhoneRequest{
		Uid: 100, Phone: "6281234567890",
	})
	if err != nil {
		t.Fatalf("resolve should succeed even if cache write fails: %v", err)
	}
	if resp.ThreadId != "t" {
		t.Errorf("got %q want t", resp.ThreadId)
	}
}

// upsertFailStore is a test wrapper that injects a failure into
// UpsertPhoneThread while delegating everything else.
type upsertFailStore struct {
	store.Store
}

func (u *upsertFailStore) UpsertPhoneThread(context.Context, *store.PhoneThreadRow) error {
	return errors.New("simulated DB error on cache write")
}

// Sanity: ensure NormalizePhone via PhoneResolver wiring still
// surfaces the expected normalized form on writeback path.
func TestResolvePhone_NormalizedFormPersistedToCache(t *testing.T) {
	resolver := &fakePhoneResolver{thread: "t", mailbox: "m"}
	h, st, _, _ := newResolveHandler(t, resolver)
	seedEncryptedSession(t, st, h.dek, 100, "tenant-A")

	_, err := h.ResolvePhone(ctxWith("tenant-A"), &hermesv1.ResolvePhoneRequest{
		Uid: 100, Phone: "  +62 (812) 3456-7890  ",
	})
	if err != nil {
		t.Fatalf("ResolvePhone: %v", err)
	}
	// Cache must be keyed by the NORMALIZED form, not the raw input.
	row, err := st.GetPhoneThread(context.Background(), 100, "page-PRIMARY", "6281234567890")
	if err != nil {
		t.Fatalf("cache row should be keyed by normalized phone: %v", err)
	}
	if row.Phone != "6281234567890" {
		t.Errorf("stored phone: got %q want 6281234567890", row.Phone)
	}

	// And nothing should be cached under the raw form.
	if _, err := st.GetPhoneThread(context.Background(), 100, "page-PRIMARY", strings.TrimSpace("+62 (812) 3456-7890")); err == nil {
		t.Error("raw-formatted phone should NOT be in cache")
	}
}
