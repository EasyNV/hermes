package session

import (
	"sync"
)

// subscriberBuffer is the per-subscriber channel buffer size. Small
// enough that a stuck consumer is detected within seconds (the listener
// drops deltas for that subscriber), large enough that a brief consumer
// stall doesn't cause data loss.
//
// Chosen as 32 per chunk-3 plan decision; hardcoded for now, promote to
// config in chunk 5 if metrics show drop rates.
const subscriberBuffer = 32

// broadcaster is the per-uid fan-out mechanism. The listener goroutine
// calls dispatch(delta) to deliver one delta to all current subscribers
// via non-blocking sends. A slow subscriber drops the delta (counter
// metric increments); other subscribers still receive it.
type broadcaster struct {
	mu       sync.RWMutex
	subs     map[uint64]chan *InboundDelta
	nextID   uint64
	closed   bool
	onDropped func() // optional callback for metrics.SubscriberDropped.Inc()
}

func newBroadcaster() *broadcaster {
	return &broadcaster{
		subs: make(map[uint64]chan *InboundDelta),
	}
}

// subscribe returns a channel + unsubscribe func. If the broadcaster
// has been closed (Disconnect already ran), the returned channel is
// closed immediately and the unsubscribe is a no-op.
func (b *broadcaster) subscribe() (<-chan *InboundDelta, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		ch := make(chan *InboundDelta)
		close(ch)
		return ch, func() {}
	}

	id := b.nextID
	b.nextID++
	ch := make(chan *InboundDelta, subscriberBuffer)
	b.subs[id] = ch

	// Unsubscribe is safe to call multiple times: only the first call
	// removes + closes; subsequent calls find the entry missing and
	// no-op.
	var once sync.Once
	unsub := func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			if existing, ok := b.subs[id]; ok {
				delete(b.subs, id)
				close(existing)
			}
		})
	}
	return ch, unsub
}

// dispatch fans out one delta to all current subscribers. Non-blocking:
// a subscriber whose buffer is full drops THIS delta; counter increments
// if onDropped is set. Other subscribers are unaffected.
func (b *broadcaster) dispatch(d *InboundDelta) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.closed {
		return
	}
	for _, ch := range b.subs {
		select {
		case ch <- d:
		default:
			if b.onDropped != nil {
				b.onDropped()
			}
		}
	}
}

// close marks the broadcaster as closed and closes all subscriber
// channels. Idempotent; safe to call concurrently with subscribe.
func (b *broadcaster) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for id, ch := range b.subs {
		close(ch)
		delete(b.subs, id)
	}
}

// subscriberCount returns the current number of active subscribers.
// For metrics + tests.
func (b *broadcaster) subscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs)
}
