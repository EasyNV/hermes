package session

import (
	"context"
	"sync"
)

// lockCtx acquires mu but honors ctx cancellation. It returns nil once the
// lock is held, or ctx.Err() if ctx fires before the lock is acquired. When
// ctx wins the race, a background goroutine still completes the Lock and
// immediately Unlocks so the mutex is never left permanently held by an
// abandoned waiter.
//
// Used by manager.Send to serialize per-uid Bootstrap+Send without letting a
// wedged session pile up an unbounded queue of blocked goroutines: a caller
// whose send-timeout (consumerSendTimeout) fires while waiting returns
// promptly with DeadlineExceeded (classified transient → bounded NATS
// redelivery) instead of blocking forever.
func lockCtx(ctx context.Context, mu *sync.Mutex) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// Fast path: uncontended lock (the common case — one send per uid at a
	// time) is grabbed inline with no goroutine. Only fall back to the
	// ctx-aware waiter when the lock is actually held by another send.
	if mu.TryLock() {
		return nil
	}
	acquired := make(chan struct{})
	go func() {
		mu.Lock()
		close(acquired)
	}()
	select {
	case <-acquired:
		return nil
	case <-ctx.Done():
		// ctx lost the lock race for us; ensure the eventual acquisition is
		// released so the mutex doesn't leak in the held state.
		go func() {
			<-acquired
			mu.Unlock()
		}()
		return ctx.Err()
	}
}
