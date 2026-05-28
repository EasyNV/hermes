package session

import (
	"sync/atomic"
	"testing"
	"time"
)

// Broadcaster fan-out tests. These pin the drop-don't-block policy
// and the lifecycle invariants that the listener relies on.

func TestBroadcaster_FanOutTwoSubscribers(t *testing.T) {
	bc := newBroadcaster()
	ch1, _ := bc.subscribe()
	ch2, _ := bc.subscribe()

	d := &InboundDelta{MID: "mid.$x", Text: "hi"}
	bc.dispatch(d)

	got1, got2 := receiveWithin(t, ch1, time.Second), receiveWithin(t, ch2, time.Second)
	if got1 != d || got2 != d {
		t.Errorf("both subscribers should receive the delta; got1=%p got2=%p want=%p",
			got1, got2, d)
	}
}

func TestBroadcaster_SlowSubscriberDropped(t *testing.T) {
	bc := newBroadcaster()
	var dropped atomic.Int64
	bc.onDropped = func() { dropped.Add(1) }

	chSlow, _ := bc.subscribe() // never read → buffer fills
	chFast, _ := bc.subscribe()

	// Drain fast subscriber concurrently. The exact count of drops is
	// timing-dependent (fast subscriber may also briefly fall behind
	// under contention with race detector enabled), so we assert
	// BEHAVIORS rather than exact numbers:
	//
	//   - slow subscriber's drops > 0 (the policy fires)
	//   - fast subscriber consumes more than subscriberBuffer (it's
	//     genuinely draining, not just buffered)
	//   - some drops happened (proves slow path triggered)
	var fastReceived atomic.Int64
	doneDraining := make(chan struct{})
	go func() {
		defer close(doneDraining)
		for d := range chFast {
			_ = d
			fastReceived.Add(1)
		}
	}()

	totalSent := subscriberBuffer * 3 // 96 — guarantees drops on slow
	for i := 0; i < totalSent; i++ {
		bc.dispatch(&InboundDelta{MID: "mid.x"})
	}

	// Give the fast drainer time to catch up before close.
	time.Sleep(100 * time.Millisecond)
	bc.close()
	<-doneDraining

	if dropped.Load() == 0 {
		t.Errorf("slow subscriber should have caused drops, got 0")
	}
	if fastReceived.Load() < int64(subscriberBuffer) {
		t.Errorf("fast subscriber should consume at least the buffer (%d), got %d",
			subscriberBuffer, fastReceived.Load())
	}
	_ = chSlow
}

func TestBroadcaster_UnsubscribeRemoves(t *testing.T) {
	bc := newBroadcaster()
	var dropped atomic.Int64
	bc.onDropped = func() { dropped.Add(1) }

	ch1, unsub1 := bc.subscribe()
	ch2, _ := bc.subscribe()

	if bc.subscriberCount() != 2 {
		t.Fatalf("expected 2 subs, got %d", bc.subscriberCount())
	}

	unsub1()
	if bc.subscriberCount() != 1 {
		t.Errorf("after unsub, count should be 1, got %d", bc.subscriberCount())
	}

	// ch1 must be closed.
	select {
	case _, ok := <-ch1:
		if ok {
			t.Errorf("unsubscribed channel should be closed")
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("expected closed channel after unsub")
	}

	// Dispatch goes to ch2 only; no drops accumulate.
	bc.dispatch(&InboundDelta{MID: "mid.test"})
	if got := receiveWithin(t, ch2, time.Second); got == nil {
		t.Error("ch2 should still receive")
	}
	if dropped.Load() != 0 {
		t.Errorf("no drops expected (ch1 unsub'd before dispatch), got %d", dropped.Load())
	}
}

func TestBroadcaster_CloseClosesAllChannels(t *testing.T) {
	bc := newBroadcaster()
	ch1, _ := bc.subscribe()
	ch2, _ := bc.subscribe()
	bc.close()

	for i, ch := range []<-chan *InboundDelta{ch1, ch2} {
		select {
		case _, ok := <-ch:
			if ok {
				t.Errorf("chan %d should be closed after broadcaster.close", i)
			}
		case <-time.After(100 * time.Millisecond):
			t.Errorf("chan %d not closed", i)
		}
	}

	// Subscribe AFTER close returns a closed channel.
	ch3, _ := bc.subscribe()
	select {
	case _, ok := <-ch3:
		if ok {
			t.Errorf("post-close subscribe should return closed chan")
		}
	case <-time.After(100 * time.Millisecond):
		t.Errorf("post-close subscribe chan not closed")
	}

	// close is idempotent.
	bc.close()
	bc.close()

	// Dispatch on closed broadcaster is a no-op (no panic).
	bc.dispatch(&InboundDelta{MID: "mid.test"})
}

func receiveWithin(t *testing.T, ch <-chan *InboundDelta, d time.Duration) *InboundDelta {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(d):
		t.Fatalf("timed out waiting for delta")
		return nil
	}
}
