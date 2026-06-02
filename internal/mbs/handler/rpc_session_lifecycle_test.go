package handler

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"mbs-native/client"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/hermes-waba/hermes/internal/mbs/session"
	"github.com/hermes-waba/hermes/internal/mbs/store"
	"github.com/hermes-waba/hermes/internal/mbs/store/mock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ─────────────────────────────────────────────────────────────────────
// Test infrastructure: recordingPublisher + nopManager + fixture helpers
// ─────────────────────────────────────────────────────────────────────

// recordingPublisher captures lifecycle/outbound/inbound calls so tests
// can assert that the right event fired.
type recordingPublisher struct {
	inbound   atomic.Int64
	outbound  []recordedOutbound
	lifecycle []recordedLifecycle
}

type recordedOutbound struct {
	uid                      int64
	tenantID, threadID, mid  string
	ok                       bool
	errMsg                   string
}

type recordedLifecycle struct {
	uid       int64
	tenantID  string
	prev, nxt hermesv1.MbsSessionState
	reason    string
}

func (p *recordingPublisher) PublishInboundMessage(int64, string, string, string, string, string, string, string, time.Time) {
	p.inbound.Add(1)
}
func (p *recordingPublisher) PublishOutbound(uid int64, tenantID, threadID, mid, otid string, latencyMs int64, ok bool, errMsg string, sentAt time.Time, clientDedupeID []byte) {
	p.outbound = append(p.outbound, recordedOutbound{uid: uid, tenantID: tenantID, threadID: threadID, mid: mid, ok: ok, errMsg: errMsg})
}
func (p *recordingPublisher) PublishSessionLifecycle(uid int64, tenantID string, prev, nxt hermesv1.MbsSessionState, reason string, _ int32, _ string) {
	p.lifecycle = append(p.lifecycle, recordedLifecycle{uid: uid, tenantID: tenantID, prev: prev, nxt: nxt, reason: reason})
}

// nopManager satisfies session.Manager with no-op behavior. Used when
// the test exercises lifecycle RPCs that may touch Disconnect.
type nopManager struct {
	disconnectCalls atomic.Int64
}

func (m *nopManager) GetOrConnect(context.Context, int64) (*session.Connected, error) {
	return nil, nil
}
func (m *nopManager) Disconnect(int64) error {
	m.disconnectCalls.Add(1)
	return nil
}
func (m *nopManager) Subscribe(int64) (<-chan *session.InboundDelta, func()) {
	ch := make(chan *session.InboundDelta)
	close(ch)
	return ch, func() {}
}
func (m *nopManager) Send(context.Context, int64, int64, string) (*client.SendResult, error) {
	return nil, errors.New("nopManager: Send not configured for this test")
}
func (m *nopManager) Drain(context.Context) error    { return nil }
func (m *nopManager) Shutdown(context.Context) error { return nil }

// newLifecycleHandler builds a Handler suitable for lifecycle-RPC
// testing: real mock store + real (no-op) manager + recording
// publisher + dummy driver factory.
func newLifecycleHandler(t *testing.T) (*Handler, *mock.Store, *recordingPublisher, *nopManager) {
	t.Helper()
	st := mock.NewStore()
	pub := &recordingPublisher{}
	mgr := &nopManager{}
	dek := newTestDEK(t)

	h, err := NewHandler(Options{
		Store:         st,
		Manager:       mgr,
		Publisher:     pub,
		DriverFactory: DriverFactory(func(DriverOptions) Driver { return nil }),
		DEK:           dek,
		PodID:         "hermes-mbs-test",
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return h, st, pub, mgr
}

// seedActiveSession drops a session row + assets directly into the
// mock store. Returns the inserted row for convenience.
func seedActiveSession(t *testing.T, st *mock.Store, uid int64, tenantID string) *store.SessionRow {
	t.Helper()
	row := &store.SessionRow{
		UID:         uid,
		TenantID:    tenantID,
		DisplayName: "Test Session",
		State:       "active",
		DeviceID:    "device-xyz",
		AppVersion:  "551.0.0.55.106",
		CreatedAt:   time.Now().Add(-time.Hour),
		UpdatedAt:   time.Now(),
	}
	if err := st.CreateSession(context.Background(), row); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := st.UpsertAssets(context.Background(), uid, []*store.AssetRow{
		{UID: uid, PageID: "page-1", PageName: "Page One", WabaID: "waba-1", WecMailboxID: "mbox-1", IsPrimary: true},
	}); err != nil {
		t.Fatalf("UpsertAssets: %v", err)
	}
	return row
}

// ctxWith builds a context with tenant injected (bypassing the
// metadata interceptor — direct injection).
func ctxWith(tenantID string) context.Context {
	return withTenantForTest(context.Background(), tenantID)
}

// ─────────────────────────────────────────────────────────────────────
// ListSessions
// ─────────────────────────────────────────────────────────────────────

func TestListSessions_HappyPath(t *testing.T) {
	h, st, _, _ := newLifecycleHandler(t)
	seedActiveSession(t, st, 100, "tenant-A")
	seedActiveSession(t, st, 200, "tenant-A")
	seedActiveSession(t, st, 999, "tenant-B") // leak guard

	resp, err := h.ListSessions(ctxWith("tenant-A"), &hermesv1.ListMbsSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(resp.Sessions) != 2 {
		t.Errorf("expected 2 tenant-A sessions, got %d", len(resp.Sessions))
	}
	if resp.Page == nil || resp.Page.Total != 2 {
		t.Errorf("page.total: got %+v want 2", resp.Page)
	}
	for _, s := range resp.Sessions {
		if s.TenantId != "tenant-A" {
			t.Errorf("tenant leak: got %q", s.TenantId)
		}
		if s.PrimaryAsset == nil || s.PrimaryAsset.PageId != "page-1" {
			t.Errorf("primary asset not enriched: %+v", s.PrimaryAsset)
		}
	}
}

func TestListSessions_MissingTenant(t *testing.T) {
	h, _, _, _ := newLifecycleHandler(t)
	_, err := h.ListSessions(context.Background(), &hermesv1.ListMbsSessionsRequest{})
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("expected Unauthenticated, got %v", err)
	}
}

func TestListSessions_TenantBodyMismatch(t *testing.T) {
	h, _, _, _ := newLifecycleHandler(t)
	_, err := h.ListSessions(ctxWith("tenant-A"),
		&hermesv1.ListMbsSessionsRequest{TenantId: "tenant-B"})
	if status.Code(err) != codes.PermissionDenied {
		t.Errorf("body-tenant != ctx-tenant should be PermissionDenied, got %v", err)
	}
}

func TestListSessions_StateFilter(t *testing.T) {
	h, st, _, _ := newLifecycleHandler(t)
	seedActiveSession(t, st, 100, "tenant-A")
	burned := seedActiveSession(t, st, 200, "tenant-A")
	if err := st.BurnSession(context.Background(), burned.UID, "ops"); err != nil {
		t.Fatal(err)
	}

	resp, err := h.ListSessions(ctxWith("tenant-A"), &hermesv1.ListMbsSessionsRequest{
		StateFilter: hermesv1.MbsSessionState_MBS_SESSION_STATE_BURNED,
	})
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(resp.Sessions) != 1 {
		t.Errorf("expected 1 burned, got %d", len(resp.Sessions))
	}
	if resp.Sessions[0].State != hermesv1.MbsSessionState_MBS_SESSION_STATE_BURNED {
		t.Errorf("state filter leaked: %v", resp.Sessions[0].State)
	}
}

func TestListSessions_Pagination(t *testing.T) {
	h, st, _, _ := newLifecycleHandler(t)
	// Seed 5 sessions; request page 1 size 2.
	for i := int64(0); i < 5; i++ {
		seedActiveSession(t, st, 100+i, "tenant-A")
	}
	resp, err := h.ListSessions(ctxWith("tenant-A"), &hermesv1.ListMbsSessionsRequest{
		Page: &hermesv1.PageRequest{Page: 1, PageSize: 2},
	})
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(resp.Sessions) != 2 {
		t.Errorf("page size: got %d want 2", len(resp.Sessions))
	}
	if resp.Page.Total != 5 || resp.Page.TotalPages != 3 {
		t.Errorf("total/pages: got total=%d total_pages=%d want 5/3",
			resp.Page.Total, resp.Page.TotalPages)
	}
	if resp.Page.Page != 1 || resp.Page.PageSize != 2 {
		t.Errorf("page echo: got page=%d size=%d", resp.Page.Page, resp.Page.PageSize)
	}

	// Last page (page 3, 1 leftover).
	resp, _ = h.ListSessions(ctxWith("tenant-A"), &hermesv1.ListMbsSessionsRequest{
		Page: &hermesv1.PageRequest{Page: 3, PageSize: 2},
	})
	if len(resp.Sessions) != 1 {
		t.Errorf("last page size: got %d want 1", len(resp.Sessions))
	}
}

func TestListSessions_PaginationDefaults(t *testing.T) {
	h, st, _, _ := newLifecycleHandler(t)
	seedActiveSession(t, st, 100, "tenant-A")
	// nil PageRequest → defaults (page=1, size=50).
	resp, err := h.ListSessions(ctxWith("tenant-A"), &hermesv1.ListMbsSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if resp.Page.Page != 1 || resp.Page.PageSize != 50 {
		t.Errorf("defaults: got page=%d size=%d want 1/50", resp.Page.Page, resp.Page.PageSize)
	}
}

// ─────────────────────────────────────────────────────────────────────
// GetSessionStatus
// ─────────────────────────────────────────────────────────────────────

func TestGetSessionStatus_HappyPath(t *testing.T) {
	h, st, _, _ := newLifecycleHandler(t)
	seedActiveSession(t, st, 100, "tenant-A")

	resp, err := h.GetSessionStatus(ctxWith("tenant-A"),
		&hermesv1.GetMbsSessionStatusRequest{Uid: 100})
	if err != nil {
		t.Fatalf("GetSessionStatus: %v", err)
	}
	if resp.Session == nil || resp.Session.Uid != 100 {
		t.Errorf("session: %+v", resp.Session)
	}
	if resp.Session.PrimaryAsset == nil {
		t.Error("primary asset not enriched")
	}
}

func TestGetSessionStatus_NotFound(t *testing.T) {
	h, _, _, _ := newLifecycleHandler(t)
	_, err := h.GetSessionStatus(ctxWith("tenant-A"),
		&hermesv1.GetMbsSessionStatusRequest{Uid: 999})
	if status.Code(err) != codes.NotFound {
		t.Errorf("expected NotFound, got %v", err)
	}
}

func TestGetSessionStatus_TenantMismatch(t *testing.T) {
	h, st, _, _ := newLifecycleHandler(t)
	seedActiveSession(t, st, 100, "tenant-A")

	_, err := h.GetSessionStatus(ctxWith("tenant-B"),
		&hermesv1.GetMbsSessionStatusRequest{Uid: 100})
	if status.Code(err) != codes.PermissionDenied {
		t.Errorf("expected PermissionDenied, got %v", err)
	}
}

func TestGetSessionStatus_MissingUID(t *testing.T) {
	h, _, _, _ := newLifecycleHandler(t)
	_, err := h.GetSessionStatus(ctxWith("tenant-A"),
		&hermesv1.GetMbsSessionStatusRequest{})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────
// ListSessionAssets
// ─────────────────────────────────────────────────────────────────────

func TestListSessionAssets_HappyPath(t *testing.T) {
	h, st, _, _ := newLifecycleHandler(t)
	seedActiveSession(t, st, 100, "tenant-A")

	resp, err := h.ListSessionAssets(ctxWith("tenant-A"),
		&hermesv1.ListSessionAssetsRequest{Uid: 100})
	if err != nil {
		t.Fatalf("ListSessionAssets: %v", err)
	}
	if len(resp.Assets) != 1 || resp.Assets[0].PageId != "page-1" {
		t.Errorf("assets: %+v", resp.Assets)
	}
}

func TestListSessionAssets_TenantCrossCheck(t *testing.T) {
	h, st, _, _ := newLifecycleHandler(t)
	seedActiveSession(t, st, 100, "tenant-A")

	_, err := h.ListSessionAssets(ctxWith("tenant-B"),
		&hermesv1.ListSessionAssetsRequest{Uid: 100})
	if status.Code(err) != codes.PermissionDenied {
		t.Errorf("expected PermissionDenied, got %v", err)
	}
}

func TestListSessionAssets_NotFound(t *testing.T) {
	h, _, _, _ := newLifecycleHandler(t)
	_, err := h.ListSessionAssets(ctxWith("tenant-A"),
		&hermesv1.ListSessionAssetsRequest{Uid: 999})
	if status.Code(err) != codes.NotFound {
		t.Errorf("expected NotFound, got %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────
// BurnSession
// ─────────────────────────────────────────────────────────────────────

func TestBurnSession_HappyPath(t *testing.T) {
	h, st, pub, mgr := newLifecycleHandler(t)
	seedActiveSession(t, st, 100, "tenant-A")

	resp, err := h.BurnSession(ctxWith("tenant-A"),
		&hermesv1.BurnMbsSessionRequest{Uid: 100, Reason: "operator-request"})
	if err != nil {
		t.Fatalf("BurnSession: %v", err)
	}
	if resp.Session.State != hermesv1.MbsSessionState_MBS_SESSION_STATE_BURNED {
		t.Errorf("returned state: got %v want BURNED", resp.Session.State)
	}

	// Side effects:
	// 1. Manager.Disconnect called (idempotent — fired even when not connected).
	if mgr.disconnectCalls.Load() != 1 {
		t.Errorf("Disconnect calls: got %d want 1", mgr.disconnectCalls.Load())
	}
	// 2. Row in store is burned.
	row, _ := st.GetSession(context.Background(), 100)
	if row.State != "burned" || row.BurnedReason != "operator-request" {
		t.Errorf("store row: state=%q reason=%q", row.State, row.BurnedReason)
	}
	// 3. Lifecycle event published.
	if len(pub.lifecycle) != 1 {
		t.Fatalf("lifecycle events: got %d want 1", len(pub.lifecycle))
	}
	ev := pub.lifecycle[0]
	if ev.uid != 100 || ev.tenantID != "tenant-A" {
		t.Errorf("event metadata: %+v", ev)
	}
	if ev.prev != hermesv1.MbsSessionState_MBS_SESSION_STATE_ACTIVE {
		t.Errorf("prev state: got %v want ACTIVE", ev.prev)
	}
	if ev.nxt != hermesv1.MbsSessionState_MBS_SESSION_STATE_BURNED {
		t.Errorf("next state: got %v want BURNED", ev.nxt)
	}
	if ev.reason != "burned" {
		t.Errorf("reason: got %q want 'burned' (subject fragment)", ev.reason)
	}
}

func TestBurnSession_NotFound(t *testing.T) {
	h, _, pub, mgr := newLifecycleHandler(t)
	_, err := h.BurnSession(ctxWith("tenant-A"),
		&hermesv1.BurnMbsSessionRequest{Uid: 999, Reason: "test"})
	if status.Code(err) != codes.NotFound {
		t.Errorf("expected NotFound, got %v", err)
	}
	// No side effects: Disconnect must NOT fire if tenant check fails.
	if mgr.disconnectCalls.Load() != 0 {
		t.Errorf("Disconnect should not fire on NotFound, got %d", mgr.disconnectCalls.Load())
	}
	if len(pub.lifecycle) != 0 {
		t.Errorf("no lifecycle event expected, got %d", len(pub.lifecycle))
	}
}

func TestBurnSession_TenantMismatch(t *testing.T) {
	h, st, pub, mgr := newLifecycleHandler(t)
	seedActiveSession(t, st, 100, "tenant-A")

	_, err := h.BurnSession(ctxWith("tenant-B"),
		&hermesv1.BurnMbsSessionRequest{Uid: 100, Reason: "evil"})
	if status.Code(err) != codes.PermissionDenied {
		t.Errorf("expected PermissionDenied, got %v", err)
	}
	// Row must NOT be burned.
	row, _ := st.GetSession(context.Background(), 100)
	if row.State == "burned" {
		t.Error("cross-tenant burn should be rejected")
	}
	if mgr.disconnectCalls.Load() != 0 {
		t.Error("Disconnect should not fire on tenant mismatch")
	}
	if len(pub.lifecycle) != 0 {
		t.Error("no lifecycle event expected on tenant mismatch")
	}
}

func TestBurnSession_MissingUID(t *testing.T) {
	h, _, _, _ := newLifecycleHandler(t)
	_, err := h.BurnSession(ctxWith("tenant-A"),
		&hermesv1.BurnMbsSessionRequest{Reason: "x"})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────
// RemoveSession
// ─────────────────────────────────────────────────────────────────────

func TestRemoveSession_HappyPath(t *testing.T) {
	h, st, pub, mgr := newLifecycleHandler(t)
	seedActiveSession(t, st, 100, "tenant-A")

	resp, err := h.RemoveSession(ctxWith("tenant-A"),
		&hermesv1.RemoveMbsSessionRequest{Uid: 100})
	if err != nil {
		t.Fatalf("RemoveSession: %v", err)
	}
	if resp.Uid != 100 {
		t.Errorf("resp uid: got %d want 100", resp.Uid)
	}

	// Side effects:
	// 1. Manager.Disconnect called (tear down before delete).
	if mgr.disconnectCalls.Load() != 1 {
		t.Errorf("Disconnect calls: got %d want 1", mgr.disconnectCalls.Load())
	}
	// 2. Row is GONE from the store (hard delete, not soft-burn).
	if _, err := st.GetSession(context.Background(), 100); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("row should be deleted; GetSession err = %v want ErrNotFound", err)
	}
	// 3. Lifecycle event published: ACTIVE → UNSPECIFIED, reason "removed".
	if len(pub.lifecycle) != 1 {
		t.Fatalf("lifecycle events: got %d want 1", len(pub.lifecycle))
	}
	ev := pub.lifecycle[0]
	if ev.uid != 100 || ev.tenantID != "tenant-A" {
		t.Errorf("event metadata: %+v", ev)
	}
	if ev.prev != hermesv1.MbsSessionState_MBS_SESSION_STATE_ACTIVE {
		t.Errorf("prev state: got %v want ACTIVE", ev.prev)
	}
	if ev.nxt != hermesv1.MbsSessionState_MBS_SESSION_STATE_UNSPECIFIED {
		t.Errorf("next state: got %v want UNSPECIFIED", ev.nxt)
	}
	if ev.reason != "removed" {
		t.Errorf("reason: got %q want 'removed'", ev.reason)
	}
}

func TestRemoveSession_NotFound(t *testing.T) {
	h, _, pub, mgr := newLifecycleHandler(t)
	_, err := h.RemoveSession(ctxWith("tenant-A"),
		&hermesv1.RemoveMbsSessionRequest{Uid: 999})
	if status.Code(err) != codes.NotFound {
		t.Errorf("expected NotFound, got %v", err)
	}
	// No side effects: tenant check fails before Disconnect/delete.
	if mgr.disconnectCalls.Load() != 0 {
		t.Errorf("Disconnect should not fire on NotFound, got %d", mgr.disconnectCalls.Load())
	}
	if len(pub.lifecycle) != 0 {
		t.Errorf("no lifecycle event expected, got %d", len(pub.lifecycle))
	}
}

func TestRemoveSession_TenantMismatch(t *testing.T) {
	h, st, pub, mgr := newLifecycleHandler(t)
	seedActiveSession(t, st, 100, "tenant-A")

	_, err := h.RemoveSession(ctxWith("tenant-B"),
		&hermesv1.RemoveMbsSessionRequest{Uid: 100})
	if status.Code(err) != codes.PermissionDenied {
		t.Errorf("expected PermissionDenied, got %v", err)
	}
	// Row must still exist — cross-tenant removal rejected.
	if _, err := st.GetSession(context.Background(), 100); err != nil {
		t.Errorf("cross-tenant remove should not delete; GetSession err = %v", err)
	}
	if mgr.disconnectCalls.Load() != 0 {
		t.Error("Disconnect should not fire on tenant mismatch")
	}
	if len(pub.lifecycle) != 0 {
		t.Error("no lifecycle event expected on tenant mismatch")
	}
}

func TestRemoveSession_MissingUID(t *testing.T) {
	h, _, _, _ := newLifecycleHandler(t)
	_, err := h.RemoveSession(ctxWith("tenant-A"),
		&hermesv1.RemoveMbsSessionRequest{})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────
// Pagination helper tests (verify clamping math)
// ─────────────────────────────────────────────────────────────────────

func TestPaginate_Clamping(t *testing.T) {
	cases := []struct {
		name              string
		in                *hermesv1.PageRequest
		wantLimit, wantOff int
	}{
		{"nil", nil, 50, 0},
		{"zero", &hermesv1.PageRequest{}, 50, 0},
		{"basic", &hermesv1.PageRequest{Page: 2, PageSize: 10}, 10, 10},
		{"oversize clamped", &hermesv1.PageRequest{Page: 1, PageSize: 999}, 200, 0},
		{"undersize clamped", &hermesv1.PageRequest{Page: 1, PageSize: -5}, 50, 0}, // PageSize<=0 → default 50
		{"page 5 size 20", &hermesv1.PageRequest{Page: 5, PageSize: 20}, 20, 80},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotLimit, gotOff := paginate(c.in)
			if gotLimit != c.wantLimit || gotOff != c.wantOff {
				t.Errorf("got limit=%d off=%d want %d/%d",
					gotLimit, gotOff, c.wantLimit, c.wantOff)
			}
		})
	}
}

func TestTotalPages(t *testing.T) {
	cases := []struct {
		total int64
		size  int32
		want  int32
	}{
		{0, 50, 0},
		{1, 50, 1},
		{50, 50, 1},
		{51, 50, 2},
		{99, 50, 2},
		{100, 50, 2},
		{101, 50, 3},
	}
	for _, c := range cases {
		if got := totalPages(c.total, c.size); got != c.want {
			t.Errorf("total=%d size=%d: got %d want %d", c.total, c.size, got, c.want)
		}
	}
}
