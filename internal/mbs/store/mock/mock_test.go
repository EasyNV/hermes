package mock

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hermes-waba/hermes/internal/mbs/store"
)

// seedSession is a test helper that drops a row directly into the mock,
// bypassing CreateSession's "already exists" guard.
func seedSession(s *Store, uid int64, state, podID string, refreshedAt *time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[uid] = &store.SessionRow{
		UID:             uid,
		TenantID:        "tenant-A",
		State:           state,
		PodID:           podID,
		LastRefreshedAt: refreshedAt,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}
}

// ─────────────────────────────────────────────────────────────────────
// ClaimSession semantics (the most important test — PgStore must match)
// ─────────────────────────────────────────────────────────────────────

func TestClaim_UnclaimedSucceeds(t *testing.T) {
	s := NewStore()
	seedSession(s, 100, "active", "", nil)

	claimed, owner, err := s.ClaimSession(context.Background(), 100, "hermes-mbs-0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !claimed {
		t.Errorf("expected claimed=true on unclaimed session")
	}
	if owner != "hermes-mbs-0" {
		t.Errorf("expected owner=hermes-mbs-0, got %q", owner)
	}

	// Side-effect check: row's pod_id now reflects ownership.
	row, _ := s.GetSession(context.Background(), 100)
	if row.PodID != "hermes-mbs-0" {
		t.Errorf("expected stored pod_id=hermes-mbs-0, got %q", row.PodID)
	}
}

func TestClaim_SamePodSucceedsIdempotent(t *testing.T) {
	s := NewStore()
	seedSession(s, 100, "active", "hermes-mbs-0", nil)

	claimed, owner, err := s.ClaimSession(context.Background(), 100, "hermes-mbs-0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !claimed {
		t.Errorf("re-claim by same pod should succeed (idempotent)")
	}
	if owner != "hermes-mbs-0" {
		t.Errorf("owner mismatch: got %q", owner)
	}
}

func TestClaim_OtherPodFails(t *testing.T) {
	s := NewStore()
	seedSession(s, 100, "active", "hermes-mbs-3", nil)

	claimed, owner, err := s.ClaimSession(context.Background(), 100, "hermes-mbs-0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if claimed {
		t.Errorf("expected claimed=false when held by other pod")
	}
	if owner != "hermes-mbs-3" {
		t.Errorf("expected owner=hermes-mbs-3, got %q", owner)
	}

	// Side-effect check: original owner intact.
	row, _ := s.GetSession(context.Background(), 100)
	if row.PodID != "hermes-mbs-3" {
		t.Errorf("ownership should be unchanged, got %q", row.PodID)
	}
}

func TestClaim_NotFound(t *testing.T) {
	s := NewStore()
	_, _, err := s.ClaimSession(context.Background(), 999, "hermes-mbs-0")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────
// Release semantics
// ─────────────────────────────────────────────────────────────────────

func TestRelease_OnlyByOwner(t *testing.T) {
	s := NewStore()
	seedSession(s, 100, "active", "hermes-mbs-0", nil)

	// Wrong pod release: no error, no effect.
	if err := s.ReleaseSession(context.Background(), 100, "hermes-mbs-3"); err != nil {
		t.Errorf("release-by-wrong-pod should be no-op, got error: %v", err)
	}
	row, _ := s.GetSession(context.Background(), 100)
	if row.PodID != "hermes-mbs-0" {
		t.Errorf("wrong-pod release should not change ownership; got %q", row.PodID)
	}

	// Correct pod release: pod_id cleared.
	if err := s.ReleaseSession(context.Background(), 100, "hermes-mbs-0"); err != nil {
		t.Errorf("release-by-owner errored: %v", err)
	}
	row, _ = s.GetSession(context.Background(), 100)
	if row.PodID != "" {
		t.Errorf("after release, pod_id should be empty; got %q", row.PodID)
	}
}

// ─────────────────────────────────────────────────────────────────────
// Listers
// ─────────────────────────────────────────────────────────────────────

func TestListSessionsByPod_FilterByPodAndState(t *testing.T) {
	s := NewStore()
	seedSession(s, 100, "active", "hermes-mbs-0", nil)
	seedSession(s, 101, "active", "hermes-mbs-1", nil)
	seedSession(s, 102, "burned", "hermes-mbs-0", nil)
	seedSession(s, 103, "", "", nil) // unclaimed, no state

	// Only my pod + only active state.
	got, err := s.ListSessionsByPod(context.Background(), "hermes-mbs-0", "active")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if len(got) != 1 || got[0].UID != 100 {
		t.Errorf("expected exactly uid=100 (pod=hermes-mbs-0, state=active), got %+v", got)
	}

	// Pod filter only.
	got, err = s.ListSessionsByPod(context.Background(), "hermes-mbs-0", "")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 sessions on hermes-mbs-0, got %d", len(got))
	}
}

func TestListSessionsNeedingRefresh_PodAndStateAndThreshold(t *testing.T) {
	s := NewStore()
	now := time.Date(2026, 5, 27, 12, 0, 0, 0, time.UTC)
	old := now.Add(-31 * 24 * time.Hour)
	fresh := now.Add(-1 * time.Hour)

	seedSession(s, 100, "active", "hermes-mbs-0", &old)       // due
	seedSession(s, 101, "active", "hermes-mbs-0", &fresh)     // not due
	seedSession(s, 102, "active", "hermes-mbs-0", nil)        // never refreshed → due
	seedSession(s, 103, "active", "hermes-mbs-1", &old)       // due but other pod
	seedSession(s, 104, "burned", "hermes-mbs-0", &old)       // due but not active

	threshold := now.Add(-30 * 24 * time.Hour) // refresh things older than 30d

	got, err := s.ListSessionsNeedingRefresh(context.Background(), threshold, "hermes-mbs-0", 10)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 sessions due (100, 102), got %d: %+v", len(got), uidsOf(got))
	}

	// 102 (NULL → NULLS FIRST) comes before 100 (older non-NULL).
	if got[0].UID != 102 || got[1].UID != 100 {
		t.Errorf("expected order [102, 100] (NULLS FIRST), got %v", uidsOf(got))
	}

	// Limit honored.
	got, _ = s.ListSessionsNeedingRefresh(context.Background(), threshold, "hermes-mbs-0", 1)
	if len(got) != 1 {
		t.Errorf("limit=1 should return 1 row, got %d", len(got))
	}
}

// ─────────────────────────────────────────────────────────────────────
// Health (Ping)
// ─────────────────────────────────────────────────────────────────────

func TestPing_HealthyByDefault(t *testing.T) {
	s := NewStore()
	if err := s.Ping(context.Background()); err != nil {
		t.Errorf("expected healthy ping, got %v", err)
	}
}

func TestPing_InjectedErrorPropagates(t *testing.T) {
	s := NewStore()
	want := errors.New("simulated DB outage")
	s.SetPingError(want)
	if err := s.Ping(context.Background()); !errors.Is(err, want) {
		t.Errorf("expected injected error, got %v", err)
	}
	// Restore healthy
	s.SetPingError(nil)
	if err := s.Ping(context.Background()); err != nil {
		t.Errorf("expected healthy after restore, got %v", err)
	}
}

func uidsOf(rows []*store.SessionRow) []int64 {
	out := make([]int64, len(rows))
	for i, r := range rows {
		out[i] = r.UID
	}
	return out
}
