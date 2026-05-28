package session

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mbs-native/auth"
	"mbs-native/client"
	"mbs-native/fb"

	"github.com/hermes-waba/hermes/internal/mbs/store"
	"github.com/hermes-waba/hermes/internal/mbs/store/mock"
	"github.com/hermes-waba/hermes/pkg/crypto"
	"github.com/rs/zerolog"
)

// ─────────────────────────────────────────────────────────────────────
// fakeClient — implements clientI for tests
// ─────────────────────────────────────────────────────────────────────

type fakeClient struct {
	inbox        chan *client.InboxItem
	warmupCalls  atomic.Int64
	lsCalls      atomic.Int64
	closeCalls   atomic.Int64
	pollCalls    atomic.Int64
	connectErr   error // injects warmup + lightspeed failure
	warmupBlocks chan struct{}
}

func newFakeClient() *fakeClient {
	return &fakeClient{inbox: make(chan *client.InboxItem, 8)}
}

func (f *fakeClient) WarmupAnalyticsSession(ctx context.Context) error {
	f.warmupCalls.Add(1)
	if f.warmupBlocks != nil {
		select {
		case <-f.warmupBlocks:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return f.connectErr
}

func (f *fakeClient) ConnectLightspeed(ctx context.Context, _ *client.LightspeedAssets) error {
	f.lsCalls.Add(1)
	return f.connectErr
}

func (f *fakeClient) Close() error {
	f.closeCalls.Add(1)
	// Closing the inbox lets the listener goroutine exit cleanly.
	defer func() { recover() }() // safe if test already closed it
	close(f.inbox)
	return nil
}

func (f *fakeClient) SnapshotPoll(ctx context.Context, db string) (*fb.LsResp, error) {
	f.pollCalls.Add(1)
	return &fb.LsResp{}, nil
}

func (f *fakeClient) InboxChan() <-chan *client.InboxItem { return f.inbox }
func (f *fakeClient) RawClient() *client.Client           { return nil } // tests don't drive Send through fake

// ─────────────────────────────────────────────────────────────────────
// Test helpers
// ─────────────────────────────────────────────────────────────────────

// fakeFactory yields a fresh fakeClient per uid. The test holds a
// reference to inspect counters. Production code uses
// defaultClientFactory which builds real *client.Client.
type fakeFactory struct {
	mu      sync.Mutex
	clients map[int64]*fakeClient
}

func newFakeFactory() *fakeFactory {
	return &fakeFactory{clients: map[int64]*fakeClient{}}
}

func (ff *fakeFactory) factory() clientFactory {
	return func(creds *auth.Creds) clientI {
		ff.mu.Lock()
		defer ff.mu.Unlock()
		fc := newFakeClient()
		ff.clients[creds.UserID] = fc
		return fc
	}
}

func (ff *fakeFactory) get(uid int64) *fakeClient {
	ff.mu.Lock()
	defer ff.mu.Unlock()
	return ff.clients[uid]
}

func (ff *fakeFactory) count() int {
	ff.mu.Lock()
	defer ff.mu.Unlock()
	return len(ff.clients)
}

// setupManager builds a manager + mock store + seeded session at uid.
// Returns the manager, the mock store (so tests can inspect/mutate),
// the fake factory (so tests can grab the per-uid fakeClient), and the
// DEK (so tests can encrypt their own values).
func setupManager(t *testing.T, uid int64) (*manager, *mock.Store, *fakeFactory, crypto.DataEncryptionKey) {
	t.Helper()
	dek := genDEK(t)
	st := mock.NewStore()
	ff := newFakeFactory()

	m := NewManager(Opts{
		Store:         st,
		DEK:           dek,
		PodID:         "hermes-mbs-test",
		Logger:        zerolog.Nop(),
		ClientFactory: ff.factory(),
	}).(*manager)

	// Seed a session row with valid encrypted creds + a primary asset.
	row, _, _, _ := seedRow(t, dek, uid)
	row.TenantID = "tenant-A"
	if err := st.CreateSession(context.Background(), row); err != nil {
		t.Fatalf("seed CreateSession: %v", err)
	}
	if err := st.UpsertAssets(context.Background(), uid, []*store.AssetRow{
		{
			UID: uid, PageID: "1219576644562769",
			PageName: "Firwanata",
			WabaID: "1147297338458228", WecMailboxID: "1153441357849273",
			IsPrimary: true,
		},
	}); err != nil {
		t.Fatalf("seed UpsertAssets: %v", err)
	}
	return m, st, ff, dek
}

// ─────────────────────────────────────────────────────────────────────
// GetOrConnect tests
// ─────────────────────────────────────────────────────────────────────

func TestManager_GetOrConnect_HappyPath(t *testing.T) {
	m, st, ff, _ := setupManager(t, 61590134170831)

	cn, err := m.GetOrConnect(context.Background(), 61590134170831)
	if err != nil {
		t.Fatalf("GetOrConnect: %v", err)
	}
	if cn == nil {
		t.Fatal("expected non-nil Connected")
	}
	if cn.UID != 61590134170831 {
		t.Errorf("UID: got %d", cn.UID)
	}
	if cn.TenantID != "tenant-A" {
		t.Errorf("TenantID: got %q", cn.TenantID)
	}
	if cn.Creds == nil || cn.Creds.AccessToken == "" {
		t.Errorf("Creds should be decrypted")
	}
	if cn.PrimaryAsset == nil || cn.PrimaryAsset.PageID != "1219576644562769" {
		t.Errorf("PrimaryAsset: %+v", cn.PrimaryAsset)
	}

	// Side effects: warmup + lightspeed both called exactly once;
	// pod_id claim acquired in store.
	fc := ff.get(61590134170831)
	if fc.warmupCalls.Load() != 1 || fc.lsCalls.Load() != 1 {
		t.Errorf("expected 1 warmup + 1 ls, got %d + %d",
			fc.warmupCalls.Load(), fc.lsCalls.Load())
	}
	row, _ := st.GetSession(context.Background(), 61590134170831)
	if row.PodID != "hermes-mbs-test" {
		t.Errorf("pod_id should be claimed by hermes-mbs-test, got %q", row.PodID)
	}
}

func TestManager_GetOrConnect_CachedReturns(t *testing.T) {
	m, _, ff, _ := setupManager(t, 61590134170831)

	first, _ := m.GetOrConnect(context.Background(), 61590134170831)
	second, _ := m.GetOrConnect(context.Background(), 61590134170831)

	if first != second {
		t.Errorf("expected cached Connected returned, got distinct pointers")
	}
	fc := ff.get(61590134170831)
	if fc.warmupCalls.Load() != 1 || fc.lsCalls.Load() != 1 {
		t.Errorf("second call should not re-CONNECT, got warmup=%d ls=%d",
			fc.warmupCalls.Load(), fc.lsCalls.Load())
	}
	if ff.count() != 1 {
		t.Errorf("expected 1 client.New, got %d", ff.count())
	}
}

func TestManager_GetOrConnect_SingleFlight(t *testing.T) {
	m, _, ff, _ := setupManager(t, 61590134170831)
	// Block the first warmup so concurrent callers stack up.
	releaseBlock := make(chan struct{})

	// Pre-seed the factory by setting blocker BEFORE first call. We
	// hook in by using the manager's mutex serialization: 10 concurrent
	// calls will all serialize through one ms.mu.Lock and only ONE will
	// reach the factory.
	//
	// We can verify single-flight by counting factory invocations
	// (ff.count) after all goroutines complete.

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = m.GetOrConnect(context.Background(), 61590134170831)
		}()
	}
	close(releaseBlock) // no-op for this test
	wg.Wait()

	if ff.count() != 1 {
		t.Errorf("expected 1 factory call (single-flight), got %d", ff.count())
	}
	fc := ff.get(61590134170831)
	if fc.warmupCalls.Load() != 1 {
		t.Errorf("warmup should fire exactly once, got %d", fc.warmupCalls.Load())
	}
}

func TestManager_GetOrConnect_ClaimConflict(t *testing.T) {
	m, st, ff, _ := setupManager(t, 61590134170831)
	// Simulate another pod already owning the session.
	row, _ := st.GetSession(context.Background(), 61590134170831)
	row.PodID = "hermes-mbs-other"
	// Mock store mutates via GetSession returning a copy; force via re-seed.
	_ = st.BurnSession(context.Background(), 61590134170831, "test-burn") // discards pod_id; reset
	// Easier path: directly write via store API. We don't have an
	// "Update arbitrary fields" method, so use the mock's internal
	// state via the GetSession then mutate-and-reflect path:
	// the mock returns pointers from internal state via a copy, so we
	// need to claim from another pod first.
	_, _, _ = st.ClaimSession(context.Background(), 61590134170831, "hermes-mbs-other")

	_, err := m.GetOrConnect(context.Background(), 61590134170831)
	if err == nil {
		t.Fatal("expected ErrClaimConflict")
	}
	var conflict *ErrClaimConflict
	if !errors.As(err, &conflict) {
		t.Errorf("expected *ErrClaimConflict in chain, got %v", err)
	}
	if conflict != nil && conflict.OwnerPodID != "hermes-mbs-other" {
		t.Errorf("OwnerPodID: got %q want hermes-mbs-other", conflict.OwnerPodID)
	}
	// errors.Is path also works
	if !errors.Is(err, ErrClaimConflictSentinel) {
		t.Errorf("expected errors.Is to match ErrClaimConflictSentinel")
	}
	// No factory invocation — we never reached client.New.
	if ff.count() != 0 {
		t.Errorf("factory should not be called on claim conflict, got %d", ff.count())
	}
}

func TestManager_GetOrConnect_DecryptFails_ReleasesClaim(t *testing.T) {
	const uid = int64(61590134170831)
	badDEK := genDEK(t)
	goodDEK := genDEK(t)
	if badDEK == goodDEK {
		t.Fatal("DEK collision")
	}

	st := mock.NewStore()
	ff := newFakeFactory()
	m := NewManager(Opts{
		Store: st, DEK: badDEK, PodID: "hermes-mbs-test",
		Logger: zerolog.Nop(), ClientFactory: ff.factory(),
	}).(*manager)

	// Encrypt the row with goodDEK; manager has badDEK → decrypt fails.
	row, _, _, _ := seedRow(t, goodDEK, uid)
	row.TenantID = "tenant-A"
	if err := st.CreateSession(context.Background(), row); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, err := m.GetOrConnect(context.Background(), uid)
	if err == nil {
		t.Fatal("expected decrypt error")
	}
	if !errors.Is(err, crypto.ErrDecryptFailed) {
		t.Errorf("expected ErrDecryptFailed in chain, got %v", err)
	}

	// Critical: claim was released on the failed connect path so
	// another pod (or retry) could pick it up.
	got, _ := st.GetSession(context.Background(), uid)
	if got.PodID != "" {
		t.Errorf("claim should be released on decrypt failure, pod_id=%q", got.PodID)
	}
	if ff.count() != 0 {
		t.Errorf("factory should not run on decrypt failure, got %d", ff.count())
	}
}

func TestManager_GetOrConnect_AfterDrain_Rejects(t *testing.T) {
	m, _, _, _ := setupManager(t, 61590134170831)
	_ = m.Drain(context.Background())
	_, err := m.GetOrConnect(context.Background(), 61590134170831)
	if !errors.Is(err, ErrDrained) {
		t.Errorf("expected ErrDrained, got %v", err)
	}
}

func TestManager_GetOrConnect_AfterShutdown_Rejects(t *testing.T) {
	m, _, _, _ := setupManager(t, 61590134170831)
	_ = m.Shutdown(context.Background())
	_, err := m.GetOrConnect(context.Background(), 61590134170831)
	if !errors.Is(err, ErrShutdown) {
		t.Errorf("expected ErrShutdown, got %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────
// Disconnect / Shutdown tests
// ─────────────────────────────────────────────────────────────────────

func TestManager_Disconnect_ReleasesClaim(t *testing.T) {
	const uid = int64(61590134170831)
	m, st, ff, _ := setupManager(t, uid)
	_, _ = m.GetOrConnect(context.Background(), uid)

	if err := m.Disconnect(uid); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	row, _ := st.GetSession(context.Background(), uid)
	if row.PodID != "" {
		t.Errorf("pod_id should be released on Disconnect, got %q", row.PodID)
	}
	fc := ff.get(uid)
	if fc.closeCalls.Load() != 1 {
		t.Errorf("Close should be called once, got %d", fc.closeCalls.Load())
	}
	// Repeated Disconnect is a no-op.
	if err := m.Disconnect(uid); err != nil {
		t.Errorf("second Disconnect should be no-op, got %v", err)
	}
}

func TestManager_Shutdown_DisconnectsAll(t *testing.T) {
	const u1, u2 = int64(100), int64(200)

	dek := genDEK(t)
	st := mock.NewStore()
	ff := newFakeFactory()
	m := NewManager(Opts{
		Store: st, DEK: dek, PodID: "hermes-mbs-test",
		Logger: zerolog.Nop(), ClientFactory: ff.factory(),
	}).(*manager)

	for _, u := range []int64{u1, u2} {
		row, _, _, _ := seedRow(t, dek, u)
		row.TenantID = "t"
		_ = st.CreateSession(context.Background(), row)
		_ = st.UpsertAssets(context.Background(), u, []*store.AssetRow{
			{UID: u, PageID: "1", WabaID: "1", WecMailboxID: "1", IsPrimary: true},
		})
		_, err := m.GetOrConnect(context.Background(), u)
		if err != nil {
			t.Fatalf("connect uid=%d: %v", u, err)
		}
	}

	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	for _, u := range []int64{u1, u2} {
		row, _ := st.GetSession(context.Background(), u)
		if row.PodID != "" {
			t.Errorf("uid=%d pod_id should be released, got %q", u, row.PodID)
		}
		if ff.get(u).closeCalls.Load() != 1 {
			t.Errorf("uid=%d Close should fire once", u)
		}
	}

	// Shutdown sets ErrShutdown for subsequent GetOrConnect.
	if _, err := m.GetOrConnect(context.Background(), u1); !errors.Is(err, ErrShutdown) {
		t.Errorf("post-Shutdown GetOrConnect should ErrShutdown, got %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────
// Subscribe tests (broadcaster fan-out)
// ─────────────────────────────────────────────────────────────────────

func TestManager_Subscribe_ReceivesDispatch(t *testing.T) {
	const uid = int64(61590134170831)
	m, _, _, _ := setupManager(t, uid)
	ch, unsub := m.Subscribe(uid)
	defer unsub()

	// Manually dispatch via the broadcaster (avoids the listener
	// dependency on fb.ExtractMessages).
	m.sessionsMu.RLock()
	ms := m.sessions[uid]
	m.sessionsMu.RUnlock()
	if ms == nil {
		t.Fatal("Subscribe should have created muxedSession")
	}
	want := &InboundDelta{UID: uid, MID: "mid.$test", Text: "hello"}
	ms.bc.dispatch(want)

	select {
	case got := <-ch:
		if got != want {
			t.Errorf("got %+v want %+v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for delta")
	}
}

func TestManager_Subscribe_UnsubscribeIdempotent(t *testing.T) {
	m, _, _, _ := setupManager(t, 61590134170831)
	_, unsub := m.Subscribe(61590134170831)
	unsub()
	unsub() // must not panic
}

func TestManager_Subscribe_ChannelClosesOnDisconnect(t *testing.T) {
	const uid = int64(61590134170831)
	m, _, _, _ := setupManager(t, uid)
	_, _ = m.GetOrConnect(context.Background(), uid)

	ch, _ := m.Subscribe(uid)
	_ = m.Disconnect(uid)

	select {
	case _, ok := <-ch:
		if ok {
			t.Errorf("expected closed channel after Disconnect")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for chan close")
	}
}