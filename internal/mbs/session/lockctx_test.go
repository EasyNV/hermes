package session

import (
	"context"
	"sync"
	"testing"
	"time"
)

// lockCtx must acquire an uncontended mutex immediately and return nil.
func TestLockCtx_AcquiresUncontended(t *testing.T) {
	var mu sync.Mutex
	if err := lockCtx(context.Background(), &mu); err != nil {
		t.Fatalf("lockCtx on free mutex: %v", err)
	}
	// We hold it now; a TryLock-style check: a second lockCtx with an
	// already-cancelled ctx must fail fast rather than block.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := lockCtx(ctx, &mu); err == nil {
		t.Fatal("expected error acquiring held mutex with cancelled ctx")
	}
	mu.Unlock()
}

// lockCtx with an already-cancelled ctx must return ctx.Err() without blocking,
// even when the mutex is free (fast-path guard).
func TestLockCtx_PreCancelledFastPath(t *testing.T) {
	var mu sync.Mutex
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := lockCtx(ctx, &mu)
	if err == nil {
		t.Fatal("expected ctx error on pre-cancelled fast path")
	}
	// Mutex must remain free (we never acquired it).
	if !mu.TryLock() {
		t.Fatal("mutex should be free after pre-cancelled lockCtx")
	}
	mu.Unlock()
}

// When the mutex is held and ctx fires while waiting, lockCtx returns ctx.Err()
// promptly, and — critically — once the holder releases, the mutex is NOT left
// permanently locked by the abandoned waiter (no leak).
func TestLockCtx_CancelWhileWaiting_NoLeak(t *testing.T) {
	var mu sync.Mutex
	mu.Lock() // hold it

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := lockCtx(ctx, &mu)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected ctx error while mutex held")
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("lockCtx blocked too long (%v); should return ~ctx deadline", elapsed)
	}

	// Release the holder. The abandoned waiter goroutine will acquire then
	// immediately unlock, so the mutex must become free for a fresh acquire.
	mu.Unlock()

	// Give the cleanup goroutine a moment, then a fresh lockCtx must succeed.
	ok := false
	for i := 0; i < 100; i++ {
		if err := lockCtx(context.Background(), &mu); err == nil {
			ok = true
			mu.Unlock()
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !ok {
		t.Fatal("mutex leaked: fresh lockCtx never succeeded after holder released")
	}
}

// Two goroutines contending via lockCtx must be mutually exclusive: the second
// only enters the critical section after the first leaves.
func TestLockCtx_Serializes(t *testing.T) {
	var mu sync.Mutex
	var inside int32
	var maxConcurrent int32
	var wg sync.WaitGroup

	bump := func() {
		if err := lockCtx(context.Background(), &mu); err != nil {
			t.Errorf("lockCtx: %v", err)
			return
		}
		defer mu.Unlock()
		inside++
		if inside > maxConcurrent {
			maxConcurrent = inside
		}
		time.Sleep(5 * time.Millisecond)
		inside--
	}

	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); bump() }()
	}
	wg.Wait()

	if maxConcurrent != 1 {
		t.Fatalf("lockCtx allowed %d concurrent holders, want 1", maxConcurrent)
	}
}
