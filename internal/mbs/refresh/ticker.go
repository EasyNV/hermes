package refresh

import (
	"context"
	"errors"
	"math/rand"
	"sync"
	"time"

	"mbs-native/auth"
	"mbs-native/web"

	"github.com/hermes-waba/hermes/internal/mbs/handler"
	"github.com/hermes-waba/hermes/internal/mbs/store"
	"github.com/hermes-waba/hermes/pkg/crypto"
	"github.com/rs/zerolog"
)

// Default tunables. Mirror the values in internal/mbs/config so the
// Ticker can boot with zero-value Options if a test wants defaults.
const (
	defaultInterval    = time.Hour
	defaultThreshold   = 30 * 24 * time.Hour
	defaultConcurrency = 5
	defaultJitterCap   = 5 * time.Minute
	defaultBatchSize   = 100

	// maxAttemptTimeout caps any per-attempt deadline. We never want
	// a single hung Ping to consume more than 30s of the tick budget
	// regardless of Interval/Concurrency arithmetic.
	maxAttemptTimeout = 30 * time.Second
)

// Ticker drives periodic cookie refresh for sessions this pod owns.
// One Ticker per process. Build via New, drive via Run.
type Ticker struct {
	store     store.Store
	dek       crypto.DataEncryptionKey
	publisher handler.EventPublisher
	podID     string

	interval    time.Duration
	threshold   time.Duration
	concurrency int
	jitterCap   time.Duration
	batchSize   int

	log     zerolog.Logger
	metrics *Metrics

	// clientFactory builds a refreshClient from decrypted creds +
	// cookies. Production uses defaultClientFactory (wraps
	// web.NewClient). Tests inject scripted fakes.
	clientFactory func(*auth.Creds, *web.Cookies) refreshClient

	// nowFn returns "current time" — fake clocks plug in here.
	nowFn func() time.Time

	// rng for jitter. math/rand (not crypto/rand) — jitter doesn't
	// need cryptographic strength. Each Ticker has its own *Rand
	// so concurrent Tickers (impossible today, defensive) don't
	// alias.
	rngMu sync.Mutex // rand.Rand isn't safe for concurrent use
	rng   *rand.Rand
}

// Options bundles Ticker constructor args. Required: Store, DEK,
// Publisher, PodID. Everything else defaults sanely.
type Options struct {
	Store     store.Store              // required
	DEK       crypto.DataEncryptionKey // required (zero-key rejected)
	Publisher handler.EventPublisher   // required
	PodID     string                   // required

	Interval    time.Duration // default 1h
	Threshold   time.Duration // default 30d
	Concurrency int           // default 5
	JitterCap   time.Duration // 0 disables jitter; <0 -> default 5m; cmd/mbs/main passes defaultJitterCap explicitly
	BatchSize   int           // default 100

	Logger  zerolog.Logger
	Metrics *Metrics

	// Optional test seams. Production leaves nil.
	ClientFactory func(*auth.Creds, *web.Cookies) refreshClient
	NowFn         func() time.Time

	// RandSeed lets tests seed the jitter rng deterministically.
	// Zero -> seeded from time.Now().UnixNano().
	RandSeed int64
}

// New builds a Ticker. Returns an error if any required field is
// missing or invalid (zero DEK) so a mis-wired cmd/mbs/main fails
// fast at startup instead of panicking on the first tick.
func New(opts Options) (*Ticker, error) {
	if opts.Store == nil {
		return nil, errors.New("refresh: Store is required")
	}
	if opts.DEK.IsZero() {
		return nil, errors.New("refresh: DEK is required (zero key)")
	}
	if opts.Publisher == nil {
		return nil, errors.New("refresh: Publisher is required")
	}
	if opts.PodID == "" {
		return nil, errors.New("refresh: PodID is required")
	}

	t := &Ticker{
		store:         opts.Store,
		dek:           opts.DEK,
		publisher:     opts.Publisher,
		podID:         opts.PodID,
		interval:      coalesceDuration(opts.Interval, defaultInterval),
		threshold:     coalesceDuration(opts.Threshold, defaultThreshold),
		concurrency:   coalesceInt(opts.Concurrency, defaultConcurrency),
		jitterCap:     opts.JitterCap, // 0 honored (no jitter); negative → default
		batchSize:     coalesceInt(opts.BatchSize, defaultBatchSize),
		log:           opts.Logger,
		metrics:       opts.Metrics,
		clientFactory: opts.ClientFactory,
		nowFn:         opts.NowFn,
	}
	if opts.JitterCap < 0 {
		t.jitterCap = defaultJitterCap
	}
	if t.clientFactory == nil {
		t.clientFactory = defaultClientFactory
	}
	if t.nowFn == nil {
		t.nowFn = time.Now
	}
	seed := opts.RandSeed
	if seed == 0 {
		seed = time.Now().UnixNano()
	}
	t.rng = rand.New(rand.NewSource(seed))

	return t, nil
}

// Run blocks until ctx is canceled. On first call, sleeps for a
// random jitter ∈ [0, JitterCap) so a fleet bounce doesn't all hit
// Meta in the same second. Subsequent ticks fire every Interval.
//
// Returns nil on graceful shutdown (ctx canceled), error only if a
// pathological internal failure makes the loop unrecoverable
// (today: never — list errors are logged + the loop keeps ticking).
func (t *Ticker) Run(ctx context.Context) error {
	jitter := t.jitter()
	t.log.Info().
		Str("pod_id", t.podID).
		Dur("interval", t.interval).
		Dur("threshold", t.threshold).
		Dur("jitter", jitter).
		Int("concurrency", t.concurrency).
		Int("batch_size", t.batchSize).
		Msg("refresh ticker started")

	if jitter > 0 {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(jitter):
		}
	}

	// First tick immediately after jitter.
	t.tickOnce(ctx)

	ticker := time.NewTicker(t.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			t.log.Info().Msg("refresh ticker stopped")
			return nil
		case <-ticker.C:
			t.tickOnce(ctx)
		}
	}
}

// tickOnce runs one pass: query stale sessions -> fan-out
// attemptRefresh -> aggregate. Errors are caught, logged, and
// counted; we never let one bad row abort the tick.
func (t *Ticker) tickOnce(ctx context.Context) {
	start := t.nowFn()
	defer func() {
		t.metrics.observeTickDuration(t.nowFn().Sub(start).Seconds())
	}()

	before := t.nowFn().Add(-t.threshold)
	rows, err := t.store.ListSessionsNeedingRefresh(ctx, before, t.podID, t.batchSize)
	if err != nil {
		t.log.Error().Err(err).Str("pod_id", t.podID).Msg("refresh: list query failed")
		return
	}
	t.metrics.setStale(len(rows))
	if len(rows) == 0 {
		t.log.Debug().Msg("refresh: nothing stale this tick")
		return
	}

	t.log.Info().
		Int("count", len(rows)).
		Time("threshold_before", before).
		Msg("refresh: tick starting")

	perAttemptTimeout := t.perAttemptTimeout()

	sem := make(chan struct{}, t.concurrency)
	resultsMu := sync.Mutex{}
	results := make([]attemptResult, 0, len(rows))
	var wg sync.WaitGroup

	for _, row := range rows {
		if ctx.Err() != nil {
			break
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return
		}
		wg.Add(1)
		go func(row *store.SessionRow) {
			defer wg.Done()
			defer func() { <-sem }()
			actx, cancel := context.WithTimeout(ctx, perAttemptTimeout)
			defer cancel()
			r := t.attemptRefresh(actx, row)
			resultsMu.Lock()
			results = append(results, r)
			resultsMu.Unlock()
		}(row)
	}
	wg.Wait()

	// Summary log.
	summary := summarize(results)
	t.log.Info().
		Int("processed", len(results)).
		Int("merged", summary["merge_cookies"]).
		Int("bumped", summary["bump_validated"]).
		Int("burned", summary["burn_permanent"]).
		Int("suspended", summary["suspend"]).
		Int("transient", summary["transient_error"]).
		Int("decrypt_failed", summary["decrypt_failed"]).
		Int("other", summary["other"]).
		Dur("dur", t.nowFn().Sub(start)).
		Msg("refresh: tick complete")
}

// perAttemptTimeout is the deadline for a single Ping + persist.
// (Interval/2) / Concurrency caps the worst case (we want all
// attempts to finish within half the tick interval). Floored at
// 5s, ceilinged at maxAttemptTimeout (30s).
func (t *Ticker) perAttemptTimeout() time.Duration {
	budget := t.interval / 2 / time.Duration(t.concurrency)
	if budget < 5*time.Second {
		budget = 5 * time.Second
	}
	if budget > maxAttemptTimeout {
		budget = maxAttemptTimeout
	}
	return budget
}

// jitter returns a random duration in [0, JitterCap). Returns 0 if
// JitterCap is 0 (tests that want deterministic boot).
func (t *Ticker) jitter() time.Duration {
	if t.jitterCap <= 0 {
		return 0
	}
	t.rngMu.Lock()
	defer t.rngMu.Unlock()
	return time.Duration(t.rng.Int63n(int64(t.jitterCap)))
}

func summarize(results []attemptResult) map[string]int {
	known := map[string]bool{
		"merge_cookies":   true,
		"bump_validated":  true,
		"burn_permanent":  true,
		"suspend":         true,
		"transient_error": true,
		"decrypt_failed":  true,
	}
	out := map[string]int{}
	for _, r := range results {
		if known[r.Outcome] {
			out[r.Outcome]++
		} else {
			out["other"]++
		}
	}
	return out
}

func coalesceDuration(v, def time.Duration) time.Duration {
	if v <= 0 {
		return def
	}
	return v
}

func coalesceInt(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}

// defaultClientFactory wraps web.New for production. Tests pass a
// scripted fake instead.
func defaultClientFactory(_ *auth.Creds, cookies *web.Cookies) refreshClient {
	c, err := web.New(cookies, web.Options{})
	if err != nil {
		// web.New only fails on nil/incomplete cookies — the caller
		// has already validated. Return a sentinel client whose
		// Ping always errors so the tick logs + retries.
		return &errClient{err: err}
	}
	return c
}

// errClient is returned by defaultClientFactory when cookies fail
// validation. attemptRefresh classifies the Ping error as transient.
type errClient struct{ err error }

func (c *errClient) Ping(_ context.Context) (*web.RefreshSignal, error) {
	return nil, c.err
}
