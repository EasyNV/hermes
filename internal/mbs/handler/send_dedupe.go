package handler

import (
	"crypto/sha256"
	"sync"
	"time"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
)

// sendDedupeCache is an LRU keyed by (uid, sha256(client_dedupe_id))
// → cached SendMessage response. Single-pod scope only.
//
// K8s migration path: replace with Redis (cluster-wide) or a Postgres
// dedupe table (durable). The interface (Lookup/Store) stays the same;
// the implementation swap is local.
//
// Sizing rationale:
//   - cap=1024 entries — covers ~10 minutes of bursty sends at 100rpm
//   - ttl=5m — matches typical retry windows (gateway, campaign engine)
//     without lingering long enough to confuse legitimate re-sends
//
// Eviction policy: list-based LRU. O(N) per Lookup/Store, but with
// N=1024 max this is still sub-microsecond — orders of magnitude
// cheaper than the SHA256 we already compute per call.
type sendDedupeCache struct {
	mu    sync.Mutex
	items map[dedupeKey]dedupeEntry
	order []dedupeKey // front = oldest, back = newest
	cap   int
	ttl   time.Duration

	// now is injectable so tests can pin time. Production uses time.Now.
	now func() time.Time
}

// dedupeKey is fixed-size so it's a valid map key. We hash the variable-
// length client_dedupe_id to 32 bytes via SHA-256; collisions are
// cryptographically unrealistic at our table sizes.
type dedupeKey struct {
	uid    int64
	digest [sha256.Size]byte
}

type dedupeEntry struct {
	result    *hermesv1.MbsSendMessageResponse
	expiresAt time.Time
}

func newSendDedupeCache(cap int, ttl time.Duration) *sendDedupeCache {
	if cap <= 0 {
		cap = 1024
	}
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return &sendDedupeCache{
		items: make(map[dedupeKey]dedupeEntry, cap),
		order: make([]dedupeKey, 0, cap),
		cap:   cap,
		ttl:   ttl,
		now:   time.Now,
	}
}

// Lookup returns the cached response iff present, non-expired, and
// the dedupe id is non-empty. Empty dedupe id ⇒ caller opted out of
// dedupe; always reports miss.
func (c *sendDedupeCache) Lookup(uid int64, dedupeID []byte) (*hermesv1.MbsSendMessageResponse, bool) {
	if len(dedupeID) == 0 {
		return nil, false
	}
	k := dedupeKey{uid: uid, digest: sha256.Sum256(dedupeID)}

	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.items[k]
	if !ok {
		return nil, false
	}
	if c.now().After(entry.expiresAt) {
		// Lazy purge expired entry on Lookup.
		c.deleteLocked(k)
		return nil, false
	}
	return entry.result, true
}

// Store caches the response. No-op when dedupeID is empty (caller
// opted out). Evicts the oldest entry when cap is reached.
func (c *sendDedupeCache) Store(uid int64, dedupeID []byte, resp *hermesv1.MbsSendMessageResponse) {
	if len(dedupeID) == 0 || resp == nil {
		return
	}
	k := dedupeKey{uid: uid, digest: sha256.Sum256(dedupeID)}

	c.mu.Lock()
	defer c.mu.Unlock()

	// If key already exists, refresh in place and move to MRU.
	if _, exists := c.items[k]; exists {
		c.items[k] = dedupeEntry{result: resp, expiresAt: c.now().Add(c.ttl)}
		c.bumpLocked(k)
		return
	}

	// Evict the oldest if at capacity.
	for len(c.items) >= c.cap && len(c.order) > 0 {
		oldest := c.order[0]
		c.deleteLocked(oldest)
	}

	c.items[k] = dedupeEntry{result: resp, expiresAt: c.now().Add(c.ttl)}
	c.order = append(c.order, k)
}

// Len returns the current entry count. For metrics / debug; tests
// also use it to verify eviction occurred.
func (c *sendDedupeCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}

// deleteLocked removes a key from both the map and the order slice.
// Caller MUST hold c.mu.
func (c *sendDedupeCache) deleteLocked(k dedupeKey) {
	delete(c.items, k)
	for i, ck := range c.order {
		if ck == k {
			c.order = append(c.order[:i], c.order[i+1:]...)
			return
		}
	}
}

// bumpLocked moves k to the back of order (MRU). Caller MUST hold c.mu.
func (c *sendDedupeCache) bumpLocked(k dedupeKey) {
	for i, ck := range c.order {
		if ck == k {
			c.order = append(append(c.order[:i], c.order[i+1:]...), k)
			return
		}
	}
}
