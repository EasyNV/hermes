package handler

import (
	"testing"
	"time"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
)

func mkResp(mid string) *hermesv1.MbsSendMessageResponse {
	return &hermesv1.MbsSendMessageResponse{Mid: mid}
}

func TestDedupe_HitWithinTTL(t *testing.T) {
	c := newSendDedupeCache(8, time.Hour)
	c.Store(100, []byte("idempotent-key-1"), mkResp("mid.$x"))

	got, hit := c.Lookup(100, []byte("idempotent-key-1"))
	if !hit {
		t.Fatal("expected hit")
	}
	if got.Mid != "mid.$x" {
		t.Errorf("mid mismatch: %q", got.Mid)
	}
}

func TestDedupe_MissAfterTTL(t *testing.T) {
	now := time.Now()
	c := newSendDedupeCache(8, time.Minute)
	c.now = func() time.Time { return now }

	c.Store(100, []byte("k"), mkResp("mid.$x"))
	// Roll the clock forward past TTL.
	c.now = func() time.Time { return now.Add(2 * time.Minute) }

	if _, hit := c.Lookup(100, []byte("k")); hit {
		t.Errorf("expected miss after TTL")
	}
	if c.Len() != 0 {
		t.Errorf("expired entry should be purged on Lookup, len=%d", c.Len())
	}
}

func TestDedupe_EvictsAtCapacity(t *testing.T) {
	c := newSendDedupeCache(3, time.Hour)
	c.Store(1, []byte("a"), mkResp("a"))
	c.Store(2, []byte("b"), mkResp("b"))
	c.Store(3, []byte("c"), mkResp("c"))
	if c.Len() != 3 {
		t.Fatalf("setup: len=%d", c.Len())
	}

	// Adding a 4th should evict the oldest (uid=1, key=a).
	c.Store(4, []byte("d"), mkResp("d"))
	if c.Len() != 3 {
		t.Errorf("len after eviction: got %d want 3", c.Len())
	}
	if _, hit := c.Lookup(1, []byte("a")); hit {
		t.Errorf("oldest entry should have been evicted")
	}
	// Newest 3 should remain.
	for _, k := range []struct {
		uid int64
		k   []byte
	}{{2, []byte("b")}, {3, []byte("c")}, {4, []byte("d")}} {
		if _, hit := c.Lookup(k.uid, k.k); !hit {
			t.Errorf("uid=%d key=%s should still be cached", k.uid, k.k)
		}
	}
}

func TestDedupe_EmptyKeyBypass(t *testing.T) {
	c := newSendDedupeCache(8, time.Hour)
	c.Store(100, nil, mkResp("x"))
	c.Store(100, []byte{}, mkResp("y"))

	if c.Len() != 0 {
		t.Errorf("empty dedupe keys must not populate cache, len=%d", c.Len())
	}
	if _, hit := c.Lookup(100, nil); hit {
		t.Errorf("nil key lookup should miss")
	}
}

func TestDedupe_KeysIsolatedByUID(t *testing.T) {
	c := newSendDedupeCache(8, time.Hour)
	c.Store(100, []byte("shared-key"), mkResp("uid-100"))
	c.Store(200, []byte("shared-key"), mkResp("uid-200"))

	got100, ok100 := c.Lookup(100, []byte("shared-key"))
	got200, ok200 := c.Lookup(200, []byte("shared-key"))
	if !ok100 || !ok200 {
		t.Fatal("both uids should hit")
	}
	if got100.Mid != "uid-100" || got200.Mid != "uid-200" {
		t.Errorf("cross-pollination: got100=%v got200=%v", got100.Mid, got200.Mid)
	}
}

func TestDedupe_RestoreUpdatesRefresh(t *testing.T) {
	// Storing the same key twice should overwrite + bump TTL, not append.
	now := time.Now()
	c := newSendDedupeCache(8, time.Minute)
	c.now = func() time.Time { return now }

	c.Store(1, []byte("k"), mkResp("v1"))
	if c.Len() != 1 {
		t.Fatalf("setup: %d", c.Len())
	}
	// Roll forward 45s, then restore — expiresAt should be now+60s, not 15s remaining.
	c.now = func() time.Time { return now.Add(45 * time.Second) }
	c.Store(1, []byte("k"), mkResp("v2"))
	if c.Len() != 1 {
		t.Errorf("re-store should not grow cache: %d", c.Len())
	}
	// Roll forward another 50s (95s from start). v1 would have expired
	// but the refresh extends to now+50+60 = 155s from start.
	c.now = func() time.Time { return now.Add(95 * time.Second) }
	got, hit := c.Lookup(1, []byte("k"))
	if !hit {
		t.Fatalf("expected hit (refreshed TTL)")
	}
	if got.Mid != "v2" {
		t.Errorf("stale entry: got %s want v2", got.Mid)
	}
}

func TestDedupe_DefaultsApplied(t *testing.T) {
	// cap <= 0 and ttl <= 0 should fall back to defaults.
	c := newSendDedupeCache(0, 0)
	if c.cap != 1024 {
		t.Errorf("default cap: got %d want 1024", c.cap)
	}
	if c.ttl != 5*time.Minute {
		t.Errorf("default ttl: got %v want 5m", c.ttl)
	}
}
