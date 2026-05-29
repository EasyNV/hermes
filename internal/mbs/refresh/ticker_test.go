package refresh

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mbs-native/auth"
	"mbs-native/web"

	"github.com/hermes-waba/hermes/internal/mbs/handler"
	"github.com/hermes-waba/hermes/internal/mbs/store"
	"github.com/hermes-waba/hermes/internal/mbs/store/mock"
	"github.com/rs/zerolog"
)

// ─────────────────────────── New() validation ─────────────────────────

func TestNew_RejectsMissingRequired(t *testing.T) {
	dek := newTestDEK(t)
	st := mock.NewStore()

	cases := map[string]Options{
		"no Store":     {DEK: dek, Publisher: handler.NopPublisher{}, PodID: "pod"},
		"no DEK":       {Store: st, Publisher: handler.NopPublisher{}, PodID: "pod"},
		"no Publisher": {Store: st, DEK: dek, PodID: "pod"},
		"no PodID":     {Store: st, DEK: dek, Publisher: handler.NopPublisher{}},
	}
	for name, opts := range cases {
		if _, err := New(opts); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}

	if _, err := New(Options{
		Store: st, DEK: dek, Publisher: handler.NopPublisher{}, PodID: "pod",
	}); err != nil {
		t.Errorf("full opts should construct: %v", err)
	}
}

func TestNew_AppliesDefaults(t *testing.T) {
	dek := newTestDEK(t)
	st := mock.NewStore()

	tk, err := New(Options{
		Store: st, DEK: dek, Publisher: handler.NopPublisher{}, PodID: "pod",
	})
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	if tk.interval != defaultInterval {
		t.Errorf("interval default: got %v want %v", tk.interval, defaultInterval)
	}
	if tk.threshold != defaultThreshold {
		t.Errorf("threshold default: got %v want %v", tk.threshold, defaultThreshold)
	}
	if tk.concurrency != defaultConcurrency {
		t.Errorf("concurrency default: got %d want %d", tk.concurrency, defaultConcurrency)
	}
	if tk.batchSize != defaultBatchSize {
		t.Errorf("batchSize default: got %d want %d", tk.batchSize, defaultBatchSize)
	}
}

// ──────────────────────── jitter + perAttemptTimeout ─────────────────

func TestJitter_BoundedAndDeterministicWithSeed(t *testing.T) {
	dek := newTestDEK(t)
	tk, err := New(Options{
		Store: mock.NewStore(), DEK: dek, Publisher: handler.NopPublisher{}, PodID: "pod",
		JitterCap: 5 * time.Minute,
		RandSeed:  42,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Drawing twice with the same seed gives reproducible values.
	a := tk.jitter()
	b := tk.jitter()
	if a < 0 || a >= 5*time.Minute {
		t.Errorf("jitter out of range: %v", a)
	}
	if b < 0 || b >= 5*time.Minute {
		t.Errorf("jitter[2] out of range: %v", b)
	}

	// Same seed -> same first draw.
	tk2, _ := New(Options{
		Store: mock.NewStore(), DEK: dek, Publisher: handler.NopPublisher{}, PodID: "pod",
		JitterCap: 5 * time.Minute,
		RandSeed:  42,
	})
	if a2 := tk2.jitter(); a2 != a {
		t.Errorf("seeded jitter mismatch: %v vs %v", a, a2)
	}
}

func TestJitter_ZeroCapReturnsZero(t *testing.T) {
	dek := newTestDEK(t)
	tk, _ := New(Options{
		Store: mock.NewStore(), DEK: dek, Publisher: handler.NopPublisher{}, PodID: "pod",
		JitterCap: 0,
	})
	if got := tk.jitter(); got != 0 {
		t.Errorf("jitter with zero cap: got %v want 0", got)
	}
}

func TestPerAttemptTimeout_Bounds(t *testing.T) {
	dek := newTestDEK(t)

	// Big interval, low concurrency -> capped at 30s.
	tk1, _ := New(Options{
		Store: mock.NewStore(), DEK: dek, Publisher: handler.NopPublisher{}, PodID: "pod",
		Interval: 24 * time.Hour, Concurrency: 1,
	})
	if got := tk1.perAttemptTimeout(); got != maxAttemptTimeout {
		t.Errorf("big interval: got %v want %v", got, maxAttemptTimeout)
	}

	// Small interval, high concurrency -> floored at 5s.
	tk2, _ := New(Options{
		Store: mock.NewStore(), DEK: dek, Publisher: handler.NopPublisher{}, PodID: "pod",
		Interval: time.Second, Concurrency: 100,
	})
	if got := tk2.perAttemptTimeout(); got != 5*time.Second {
		t.Errorf("small interval: got %v want 5s", got)
	}
}

// ──────────────────────── tickOnce + Run ─────────────────────────────

func TestTickOnce_FansOutAndAggregates(t *testing.T) {
	dek := newTestDEK(t)
	st := mock.NewStore()

	// Seed 3 rows. All stale (LastRefreshedAt = 31d ago via seedRow).
	rows := []*store.SessionRow{
		seedRow(t, dek, 200, validCookies()),
		seedRow(t, dek, 201, validCookies()),
		seedRow(t, dek, 202, validCookies()),
	}
	for _, r := range rows {
		if err := st.CreateSession(context.Background(), r); err != nil {
			t.Fatalf("seed %d: %v", r.UID, err)
		}
	}

	// Client returns "no cookie change" for every Ping -> bump_validated.
	respTime := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	sameJar, _ := web.FromEnvelope(validCookies())

	var pings atomic.Int32
	tk, err := New(Options{
		Store: st, DEK: dek, Publisher: handler.NopPublisher{}, PodID: "pod-test",
		Interval: time.Minute, Threshold: time.Hour, Concurrency: 3,
		BatchSize: 100,
		Logger:    zerolog.Nop(),
		ClientFactory: func(*auth.Creds, *web.Cookies) refreshClient {
			return &countingClient{
				signal: &web.RefreshSignal{
					CookiesChanged: false,
					Cookies:        sameJar,
					ResponseTime:   respTime,
				},
				counter: &pings,
			}
		},
		NowFn: func() time.Time { return respTime },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	tk.tickOnce(context.Background())

	if got := pings.Load(); got != 3 {
		t.Errorf("Ping calls: got %d want 3", got)
	}
}

type countingClient struct {
	signal  *web.RefreshSignal
	err     error
	counter *atomic.Int32
}

func (c *countingClient) Ping(_ context.Context) (*web.RefreshSignal, error) {
	c.counter.Add(1)
	return c.signal, c.err
}

func TestTickOnce_EmptyListIsNoOp(t *testing.T) {
	dek := newTestDEK(t)
	st := mock.NewStore() // no sessions seeded

	var pings atomic.Int32
	tk, _ := New(Options{
		Store: st, DEK: dek, Publisher: handler.NopPublisher{}, PodID: "pod",
		Logger: zerolog.Nop(),
		ClientFactory: func(*auth.Creds, *web.Cookies) refreshClient {
			return &countingClient{counter: &pings, signal: &web.RefreshSignal{}}
		},
	})

	tk.tickOnce(context.Background())
	if got := pings.Load(); got != 0 {
		t.Errorf("expected 0 Pings on empty list, got %d", got)
	}
}

func TestRun_ExitsOnCtxCancel(t *testing.T) {
	dek := newTestDEK(t)
	tk, _ := New(Options{
		Store: mock.NewStore(), DEK: dek, Publisher: handler.NopPublisher{}, PodID: "pod",
		Interval:  10 * time.Millisecond,
		JitterCap: 0, // no jitter -> immediate first tick
		Logger:    zerolog.Nop(),
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- tk.Run(ctx) }()

	// Let it run a few ticks then cancel.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned err: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not exit within 1s of ctx cancel")
	}
}

func TestRun_RespectsJitterBeforeFirstTick(t *testing.T) {
	dek := newTestDEK(t)
	st := mock.NewStore()

	// Seed one row so a tick would Ping if it ran.
	row := seedRow(t, dek, 300, validCookies())
	_ = st.CreateSession(context.Background(), row)

	var pings atomic.Int32
	tk, err := New(Options{
		Store: st, DEK: dek, Publisher: handler.NopPublisher{}, PodID: "pod-test",
		Interval:  10 * time.Second,
		Threshold: time.Hour,
		JitterCap: 500 * time.Millisecond,
		RandSeed:  1, // deterministic — will pick some value in [0, 500ms)
		Logger:    zerolog.Nop(),
		ClientFactory: func(*auth.Creds, *web.Cookies) refreshClient {
			return &countingClient{counter: &pings, signal: &web.RefreshSignal{}}
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = tk.Run(ctx) }()

	// Cancel before jitter elapses (1ms < 500ms).
	time.Sleep(1 * time.Millisecond)
	cancel()
	// Give Run a moment to exit.
	time.Sleep(20 * time.Millisecond)

	// If jitter was respected, we should NOT have pinged.
	if got := pings.Load(); got != 0 {
		t.Errorf("ping fired before jitter elapsed: got %d want 0", got)
	}
}

// TestTickOnce_RespectsConcurrencySemaphore: verify only N goroutines
// run Ping concurrently. We use a barrier client that blocks until
// released, count peak concurrent Pings.
func TestTickOnce_RespectsConcurrencySemaphore(t *testing.T) {
	dek := newTestDEK(t)
	st := mock.NewStore()

	// Seed 10 rows.
	for i := int64(400); i < 410; i++ {
		_ = st.CreateSession(context.Background(), seedRow(t, dek, i, validCookies()))
	}

	respTime := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	sameJar, _ := web.FromEnvelope(validCookies())

	var (
		mu      sync.Mutex
		active  int
		peak    int
		release = make(chan struct{})
	)
	client := &barrierClient{
		signal: &web.RefreshSignal{
			CookiesChanged: false,
			Cookies:        sameJar,
			ResponseTime:   respTime,
		},
		onPing: func() {
			mu.Lock()
			active++
			if active > peak {
				peak = active
			}
			mu.Unlock()
			<-release
			mu.Lock()
			active--
			mu.Unlock()
		},
	}

	tk, _ := New(Options{
		Store: st, DEK: dek, Publisher: handler.NopPublisher{}, PodID: "pod-test",
		Interval: time.Minute, Threshold: time.Hour, Concurrency: 3,
		BatchSize: 100,
		Logger:    zerolog.Nop(),
		ClientFactory: func(*auth.Creds, *web.Cookies) refreshClient { return client },
		NowFn:         func() time.Time { return respTime },
	})

	done := make(chan struct{})
	go func() {
		tk.tickOnce(context.Background())
		close(done)
	}()

	// Wait for the semaphore to fill up, then release.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		full := active == 3
		mu.Unlock()
		if full {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	close(release)
	<-done

	mu.Lock()
	gotPeak := peak
	mu.Unlock()
	if gotPeak != 3 {
		t.Errorf("concurrency cap: peak active=%d want 3", gotPeak)
	}
}

type barrierClient struct {
	signal *web.RefreshSignal
	onPing func()
}

func (c *barrierClient) Ping(_ context.Context) (*web.RefreshSignal, error) {
	if c.onPing != nil {
		c.onPing()
	}
	return c.signal, nil
}

// TestSummarize_BucketsOutcomes pins the log-summary mapper.
func TestSummarize_BucketsOutcomes(t *testing.T) {
	results := []attemptResult{
		{Outcome: "merge_cookies"},
		{Outcome: "merge_cookies"},
		{Outcome: "bump_validated"},
		{Outcome: "burn_permanent"},
		{Outcome: "suspend"},
		{Outcome: "transient_error"},
		{Outcome: "encrypt_failed"}, // unknown bucket -> "other"
	}
	got := summarize(results)
	if got["merge_cookies"] != 2 {
		t.Errorf("merge_cookies: got %d want 2", got["merge_cookies"])
	}
	if got["other"] != 1 {
		t.Errorf("other: got %d want 1", got["other"])
	}
}
