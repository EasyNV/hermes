package mock

import (
	"context"
	"testing"

	"github.com/hermes-waba/hermes/internal/mbs/store"
)

// TestListReconnectableSessions verifies the I3 fix: a pod must reclaim BOTH
// the sessions it already owns AND orphaned sessions (pod_id="") left behind by
// a prior graceful shutdown — while never adopting a session owned by a
// different live pod. This is the exact selection logic that, when missing,
// left the active session unclaimed (pod_id="") with no resident listener, so
// inbound replies were never polled.
func TestListReconnectableSessions(t *testing.T) {
	ctx := context.Background()
	s := NewStore()

	const self = "hermes-mbs-prod-0"

	seed := func(uid int64, state, podID string) {
		if err := s.CreateSession(ctx, &store.SessionRow{
			UID:   uid,
			State: state,
			PodID: podID,
		}); err != nil {
			t.Fatalf("seed uid=%d: %v", uid, err)
		}
	}

	// 1: active + owned by self          → reclaim
	seed(1, "active", self)
	// 2: active + orphaned (pod_id="")    → reclaim (the regression case)
	seed(2, "active", "")
	// 3: active + owned by ANOTHER pod    → skip (don't steal a live pod's session)
	seed(3, "active", "hermes-mbs-prod-1")
	// 4: burned + orphaned                → skip (not active)
	seed(4, "burned", "")
	// 5: burned + owned by self           → skip (not active)
	seed(5, "burned", self)

	rows, err := s.ListReconnectableSessions(ctx, self)
	if err != nil {
		t.Fatalf("ListReconnectableSessions: %v", err)
	}

	got := make(map[int64]bool, len(rows))
	for _, r := range rows {
		got[r.UID] = true
	}

	want := map[int64]bool{1: true, 2: true}
	if len(got) != len(want) {
		t.Fatalf("reclaim set size: got %v, want %v", keys(got), keys(want))
	}
	for uid := range want {
		if !got[uid] {
			t.Errorf("uid %d should be reclaimable but was not selected", uid)
		}
	}
	// Explicitly assert the dangerous one is absent.
	if got[3] {
		t.Error("uid 3 owned by another live pod must NOT be selected for reclaim")
	}
}

func keys(m map[int64]bool) []int64 {
	out := make([]int64, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
