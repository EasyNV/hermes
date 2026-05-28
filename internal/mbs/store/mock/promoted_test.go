package mock

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/hermes-waba/hermes/internal/mbs/store"
)

// seedSessionWithTenant is a helper for cross-tenant filtering tests.
func seedSessionWithTenant(s *Store, uid int64, tenantID, state string, updatedAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[uid] = &store.SessionRow{
		UID:       uid,
		TenantID:  tenantID,
		State:     state,
		CreatedAt: time.Now(),
		UpdatedAt: updatedAt,
	}
}

// ─────────────────────────────────────────────────────────────────────
// GetSessionByTenant (chunk 4 promotion)
// ─────────────────────────────────────────────────────────────────────

func TestGetSessionByTenant_Hit(t *testing.T) {
	s := NewStore()
	seedSessionWithTenant(s, 100, "tenant-A", "active", time.Now())

	row, err := s.GetSessionByTenant(context.Background(), "tenant-A", 100)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if row.UID != 100 || row.TenantID != "tenant-A" {
		t.Errorf("got %+v", row)
	}
}

func TestGetSessionByTenant_TenantMismatch(t *testing.T) {
	s := NewStore()
	seedSessionWithTenant(s, 100, "tenant-A", "active", time.Now())

	_, err := s.GetSessionByTenant(context.Background(), "tenant-B", 100)
	if !errors.Is(err, store.ErrTenantMismatch) {
		t.Errorf("expected ErrTenantMismatch, got %v", err)
	}
}

func TestGetSessionByTenant_NotFound(t *testing.T) {
	s := NewStore()
	_, err := s.GetSessionByTenant(context.Background(), "tenant-A", 999)
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────
// ListSessions (chunk 4 promotion)
// ─────────────────────────────────────────────────────────────────────

func TestListSessions_FilterByTenant(t *testing.T) {
	s := NewStore()
	now := time.Now()
	// Two tenants, multiple sessions each.
	for i, uid := range []int64{1, 2, 3} {
		seedSessionWithTenant(s, uid, "tenant-A", "active", now.Add(time.Duration(i)*time.Second))
	}
	for i, uid := range []int64{10, 11} {
		seedSessionWithTenant(s, uid, "tenant-B", "active", now.Add(time.Duration(i)*time.Second))
	}

	rows, total, err := s.ListSessions(context.Background(), "tenant-A", "", 0, 0)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if total != 3 {
		t.Errorf("total: got %d want 3", total)
	}
	if len(rows) != 3 {
		t.Errorf("rows: got %d want 3", len(rows))
	}
	for _, r := range rows {
		if r.TenantID != "tenant-A" {
			t.Errorf("tenant leak: %q", r.TenantID)
		}
	}
}

func TestListSessions_StateFilter(t *testing.T) {
	s := NewStore()
	now := time.Now()
	seedSessionWithTenant(s, 1, "tenant-A", "active", now)
	seedSessionWithTenant(s, 2, "tenant-A", "burned", now)
	seedSessionWithTenant(s, 3, "tenant-A", "active", now)

	rows, total, err := s.ListSessions(context.Background(), "tenant-A", "active", 0, 0)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if total != 2 || len(rows) != 2 {
		t.Errorf("expected 2 active sessions, got total=%d rows=%d", total, len(rows))
	}
	for _, r := range rows {
		if r.State != "active" {
			t.Errorf("state leak: %q", r.State)
		}
	}
}

func TestListSessions_Pagination(t *testing.T) {
	s := NewStore()
	base := time.Now()
	uids := []int64{}
	for i := int64(0); i < 7; i++ {
		uid := 100 + i
		uids = append(uids, uid)
		// Distinct updated_at so order is deterministic. Newer first by
		// updated_at DESC ⇒ later-added uids should be first in results.
		seedSessionWithTenant(s, uid, "tenant-A", "active", base.Add(time.Duration(i)*time.Millisecond))
	}
	// Sort for our expectation: updated_at DESC ⇒ highest uid first.
	sort.Slice(uids, func(i, j int) bool { return uids[i] > uids[j] })

	// First page (limit=3, offset=0).
	rows, total, err := s.ListSessions(context.Background(), "tenant-A", "", 3, 0)
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if total != 7 {
		t.Errorf("total: got %d want 7", total)
	}
	if len(rows) != 3 {
		t.Errorf("page1 size: got %d want 3", len(rows))
	}
	for i, r := range rows {
		if r.UID != uids[i] {
			t.Errorf("page1[%d]: got uid=%d want %d", i, r.UID, uids[i])
		}
	}

	// Second page (limit=3, offset=3).
	rows, _, _ = s.ListSessions(context.Background(), "tenant-A", "", 3, 3)
	if len(rows) != 3 {
		t.Errorf("page2 size: got %d want 3", len(rows))
	}
	for i, r := range rows {
		if r.UID != uids[3+i] {
			t.Errorf("page2[%d]: got uid=%d want %d", i, r.UID, uids[3+i])
		}
	}

	// Third page partial (limit=3, offset=6, only 1 row left).
	rows, _, _ = s.ListSessions(context.Background(), "tenant-A", "", 3, 6)
	if len(rows) != 1 {
		t.Errorf("page3 size: got %d want 1", len(rows))
	}

	// Offset past end → empty, total intact.
	rows, total, _ = s.ListSessions(context.Background(), "tenant-A", "", 10, 100)
	if len(rows) != 0 || total != 7 {
		t.Errorf("offset past end: got rows=%d total=%d want 0,7", len(rows), total)
	}
}

func TestListSessions_DefaultsAndLimits(t *testing.T) {
	s := NewStore()
	// default limit (passed 0) should be 50; we just check no panic
	// + behavior with empty store.
	rows, total, err := s.ListSessions(context.Background(), "tenant-X", "", 0, -5)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if total != 0 || len(rows) != 0 {
		t.Errorf("empty store should return 0,0; got %d, %d", len(rows), total)
	}
}

// ─────────────────────────────────────────────────────────────────────
// PhoneThread CRUD (chunk 4 promotion)
// ─────────────────────────────────────────────────────────────────────

func TestPhoneThread_RoundTripAndUpsert(t *testing.T) {
	s := NewStore()
	ctx := context.Background()

	// Miss before write.
	_, err := s.GetPhoneThread(ctx, 100, "page-1", "6281234567890")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expected ErrNotFound on miss, got %v", err)
	}

	// First write.
	first := &store.PhoneThreadRow{
		UID: 100, PageID: "page-1", Phone: "6281234567890",
		ThreadID: "thread-A", WecMailboxID: "mbox-1",
	}
	if err := s.UpsertPhoneThread(ctx, first); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// Read back.
	got, err := s.GetPhoneThread(ctx, 100, "page-1", "6281234567890")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ThreadID != "thread-A" || got.WecMailboxID != "mbox-1" {
		t.Errorf("roundtrip mismatch: %+v", got)
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt should be auto-set on insert")
	}
	firstCreated := got.CreatedAt

	// Update: thread_id + mailbox change, last_send_at stays nil ⇒ preserves prior nil.
	updated := &store.PhoneThreadRow{
		UID: 100, PageID: "page-1", Phone: "6281234567890",
		ThreadID: "thread-B", WecMailboxID: "mbox-2",
	}
	if err := s.UpsertPhoneThread(ctx, updated); err != nil {
		t.Fatalf("upsert update: %v", err)
	}
	got, _ = s.GetPhoneThread(ctx, 100, "page-1", "6281234567890")
	if got.ThreadID != "thread-B" || got.WecMailboxID != "mbox-2" {
		t.Errorf("update should overwrite thread_id+mailbox: %+v", got)
	}
	if !got.CreatedAt.Equal(firstCreated) {
		t.Errorf("CreatedAt must be preserved across upsert; was %v now %v",
			firstCreated, got.CreatedAt)
	}
	if got.LastSendAt != nil {
		t.Errorf("LastSendAt should remain nil; got %v", got.LastSendAt)
	}

	// Update with last_send_at set ⇒ rolls forward.
	when := time.Now().Add(-time.Minute)
	thirds := &store.PhoneThreadRow{
		UID: 100, PageID: "page-1", Phone: "6281234567890",
		ThreadID: "thread-B", WecMailboxID: "mbox-2",
		LastSendAt: &when,
	}
	if err := s.UpsertPhoneThread(ctx, thirds); err != nil {
		t.Fatalf("upsert with last_send_at: %v", err)
	}
	got, _ = s.GetPhoneThread(ctx, 100, "page-1", "6281234567890")
	if got.LastSendAt == nil || !got.LastSendAt.Equal(when) {
		t.Errorf("LastSendAt should be %v; got %v", when, got.LastSendAt)
	}

	// Subsequent upsert with nil LastSendAt MUST NOT clobber the set value.
	fourth := &store.PhoneThreadRow{
		UID: 100, PageID: "page-1", Phone: "6281234567890",
		ThreadID: "thread-B", WecMailboxID: "mbox-2",
	}
	_ = s.UpsertPhoneThread(ctx, fourth)
	got, _ = s.GetPhoneThread(ctx, 100, "page-1", "6281234567890")
	if got.LastSendAt == nil || !got.LastSendAt.Equal(when) {
		t.Errorf("LastSendAt must NOT be cleared by nil-bearing upsert; got %v", got.LastSendAt)
	}
}

func TestPhoneThread_UpsertNilRow(t *testing.T) {
	s := NewStore()
	if err := s.UpsertPhoneThread(context.Background(), nil); err == nil {
		t.Error("expected error on nil row")
	}
}

func TestPhoneThread_IsolatedByCompositeKey(t *testing.T) {
	s := NewStore()
	ctx := context.Background()
	rows := []*store.PhoneThreadRow{
		{UID: 100, PageID: "page-1", Phone: "62A", ThreadID: "t1", WecMailboxID: "m1"},
		{UID: 100, PageID: "page-2", Phone: "62A", ThreadID: "t2", WecMailboxID: "m2"},
		{UID: 200, PageID: "page-1", Phone: "62A", ThreadID: "t3", WecMailboxID: "m3"},
	}
	for _, r := range rows {
		if err := s.UpsertPhoneThread(ctx, r); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}
	// Each must be retrievable independently — proving (uid, page, phone)
	// is the composite key.
	for _, want := range rows {
		got, err := s.GetPhoneThread(ctx, want.UID, want.PageID, want.Phone)
		if err != nil {
			t.Fatalf("get uid=%d page=%s: %v", want.UID, want.PageID, err)
		}
		if got.ThreadID != want.ThreadID {
			t.Errorf("composite key collision: uid=%d page=%s got=%s want=%s",
				want.UID, want.PageID, got.ThreadID, want.ThreadID)
		}
	}
}
