package bridge

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hermes-waba/hermes/internal/mbs/handler"
	"github.com/rs/zerolog"
	"go.mau.fi/mautrix-meta/pkg/messagix"
	"go.mau.fi/mautrix-meta/pkg/messagix/cookies"
	"go.mau.fi/mautrix-meta/pkg/messagix/types"
	"go.mau.fi/util/exhttp"
)

// Deps is the dependency bundle for NewDriverFactory. All fields are
// optional — zero-values produce sensible production defaults.
type Deps struct {
	// Logger is the base logger for every driver created by this
	// factory. Per-attempt loggers add tenant + redacted email tags.
	// Zero value → zerolog.Nop().
	Logger zerolog.Logger

	// AssetDiscoverer fetches WABA pages post-login. Zero → production
	// graphql-backed discoverer with 30s timeout. Tests inject a
	// scripted/fake discoverer to keep unit tests offline.
	AssetDiscoverer AssetDiscoverer

	// DisableTLSVerify is for mitm capture only. When true, the
	// underlying messagix client skips cert verification — wire-tap
	// via Burp / mitmproxy. NEVER set in production.
	DisableTLSVerify bool

	// Timeout caps the overall login attempt. Default 180s. Matches
	// real Meta CAA login envelope (usually <60s; 180s gives 3x
	// headroom for slow networks).
	Timeout time.Duration

	// Await2FATimeout caps how long a single 2FA prompt can sit
	// awaiting user input. Default 120s. Per-prompt, not cumulative.
	Await2FATimeout time.Duration

	// ClientFactory builds a fresh loginClient per attempt. Zero →
	// production wrapper around messagix.NewClient. Tests inject a
	// fake to bypass HTTP entirely. proxyURL routes the login HTTP
	// through the session's assigned proxy ("" = direct).
	ClientFactory func(log zerolog.Logger, disableTLS bool, proxyURL string) (loginClient, error)
}

// NewDriverFactory returns a handler.DriverFactory that yields fresh
// MautrixDrivers per BridgeLogin RPC. Wire this into Handler.Options:
//
//	handler.NewHandler(handler.Options{
//	    DriverFactory: bridge.NewDriverFactory(bridge.Deps{
//	        Logger: log, AssetDiscoverer: bridge.DefaultDiscoverer(),
//	    }),
//	    ...
//	})
func NewDriverFactory(deps Deps) handler.DriverFactory {
	resolved := resolveDeps(deps)
	return func(opts handler.DriverOptions) handler.Driver {
		// Per-invocation timeout precedence: handler's DriverOptions
		// override deps defaults.
		timeout := resolved.Timeout
		if opts.Timeout > 0 {
			timeout = opts.Timeout
		}
		await := resolved.Await2FATimeout
		if opts.Await2FATimeout > 0 {
			await = opts.Await2FATimeout
		}

		// Per-invocation logger: opts.Logger is the per-attempt
		// logger from the handler (carries tenant + redacted email).
		// Fall back to the deps logger if opts.Logger looks
		// uninitialized (zerolog.Disabled == "off" + nil writer).
		log := opts.Logger
		if log.GetLevel() == zerolog.Disabled {
			log = resolved.Logger
		}

		return &MautrixDriver{
			log:             log,
			timeout:         timeout,
			await2FATimeout: await,
			disableTLS:      resolved.DisableTLSVerify,
			proxyURL:        opts.ProxyURL,
			assetDiscoverer: resolved.AssetDiscoverer,
			clientFactory:   resolved.ClientFactory,
			inputs:          make(chan handler.DriverInput, inputChannelBuffer),
		}
	}
}

// DefaultDiscoverer returns the production graphql-backed AssetDiscoverer.
// Exposed so cmd/mbs/main.go can wire it explicitly when overriding
// the timeout (or leave it nil and let NewDriverFactory pick the default).
func DefaultDiscoverer() AssetDiscoverer {
	return newGraphQLAssetDiscoverer(0)
}

// MautrixDriver is the production handler.Driver. One per BridgeLogin
// RPC invocation; not reusable. The handler creates via the factory,
// calls Run once, optionally Submits user inputs, and calls Close in
// defer (idempotent).
type MautrixDriver struct {
	log             zerolog.Logger
	timeout         time.Duration
	await2FATimeout time.Duration
	disableTLS      bool
	// proxyURL routes the login HTTP through the session's assigned
	// proxy. Empty string → direct dial. Resolved by the handler at
	// BridgeLogin start and passed via DriverOptions.
	proxyURL        string
	assetDiscoverer AssetDiscoverer
	clientFactory   func(log zerolog.Logger, disableTLS bool, proxyURL string) (loginClient, error)

	// inputs is buffered so handler.BridgeReaderLoop can Submit
	// without blocking even if the login loop hasn't reached the
	// matching prompt yet. The loop drains lazily.
	inputs chan handler.DriverInput

	// runCtx + runCancel are set in Run and torn down in Close.
	// Close cancels runCtx → login loop exits within
	// displayWaitInterval (worst-case during a sleep).
	mu        sync.Mutex
	runCtx    context.Context
	runCancel context.CancelFunc
	closed    atomic.Bool

	// runOnce guards against duplicate Run invocations. handler
	// contract is "Run once per Driver"; enforce it.
	runOnce sync.Once
	runErr  error
}

// Run starts the login state machine on a goroutine. Returns the
// outbound update channel (closed by the loop on terminal) and any
// startup error.
//
// Contract:
//   - Single-use. Second Run returns the cached first error.
//   - Closes the returned channel when terminal (Success/Failure/ctx).
//   - Honors ctx cancellation — propagates to the running login loop.
func (d *MautrixDriver) Run(ctx context.Context, req handler.DriverStartRequest) (<-chan handler.DriverUpdate, error) {
	updates := make(chan handler.DriverUpdate, 16)
	d.runOnce.Do(func() {
		if d.closed.Load() {
			d.runErr = errors.New("bridge: Run on closed driver")
			close(updates)
			return
		}

		client, err := d.clientFactory(d.log, d.disableTLS, d.proxyURL)
		if err != nil {
			d.runErr = err
			close(updates)
			return
		}

		// Cap the attempt with d.timeout; this is the floor — ctx
		// from caller wins if shorter.
		runCtx, cancel := context.WithTimeout(ctx, d.timeout)
		d.mu.Lock()
		d.runCtx = runCtx
		d.runCancel = cancel
		d.mu.Unlock()

		runner := &loginLoopRunner{
			ctx:          runCtx,
			client:       client,
			req:          req,
			updates:      updates,
			inputs:       d.inputs,
			discoverer:   d.assetDiscoverer,
			log:          d.log,
			awaitTimeout: d.await2FATimeout,
		}
		go runner.run()
	})
	if d.runErr != nil {
		return updates, d.runErr
	}
	return updates, nil
}

// Submit forwards a user-provided input to the running login loop.
// Non-blocking — the input channel is buffered (cap=4). If the buffer
// fills (extreme corner case: client spamming inputs faster than the
// loop consumes), Submit returns an error rather than blocking the
// handler's reader goroutine.
func (d *MautrixDriver) Submit(input handler.DriverInput) error {
	if d.closed.Load() {
		return errors.New("bridge: Submit on closed driver")
	}
	select {
	case d.inputs <- input:
		return nil
	default:
		return errors.New("bridge: input buffer full")
	}
}

// Close cancels the driver context (if Run was called) and marks the
// driver closed. Idempotent — safe to call multiple times and from
// any goroutine. The login loop observes ctx cancellation and exits
// within displayWaitInterval.
func (d *MautrixDriver) Close() error {
	if d.closed.Swap(true) {
		return nil
	}
	d.mu.Lock()
	if d.runCancel != nil {
		d.runCancel()
	}
	d.mu.Unlock()
	return nil
}

// ─────────────────────────────────────────────────────────────────────
// Production client factory
// ─────────────────────────────────────────────────────────────────────

// productionClientFactory builds a real messagix.Client wired for
// MessengerLite (the only platform that exposes DoLoginSteps).
// Mirrors the POC's setup at re/mbs/mbs-bridge-login/main.go.
//
// ⚠️ TLS verification caveat: when disableTLS=true, this mutates the
// PACKAGE-GLOBAL messagix.DisableTLSVerification. The mutation is
// process-wide and one-way (mautrix-meta has no API to reset it).
// hermes-mbs production pods MUST NEVER enable disableTLS in a multi-
// tenant context — once flipped, EVERY mautrix-meta HTTP call in the
// process (refresh ticker, asset discoverer, future RPCs) skips
// verification until restart. The knob exists strictly for offline
// wire-capture sessions (mitmproxy / Burp) during reverse-engineering.
//
// See .hermes/plans/2026-05-29_stage-e1-chunk5-step8-hostile-audit.md F1.
func productionClientFactory(log zerolog.Logger, disableTLS bool, proxyURL string) (loginClient, error) {
	if disableTLS {
		log.Warn().
			Bool("process_wide", true).
			Bool("unrecoverable_until_restart", true).
			Msg("bridge: TLS verification DISABLED for the entire process " +
				"(mautrix-meta package-global). MUST NOT be enabled in " +
				"a multi-tenant pod — every mautrix HTTP call now skips " +
				"cert validation.")
		messagix.DisableTLSVerification = true
	}
	// MessengerLite cookie scaffold — required by NewClient validation.
	c := &cookies.Cookies{Platform: types.MessengerLite}
	c.UpdateValues(make(map[cookies.MetaCookieName]string))

	cli := messagix.NewClient(c, log, &messagix.Config{
		ClientSettings: exhttp.ClientSettings{},
	})

	// Route login HTTP through the session's assigned proxy when one is
	// pinned. messagix.SetProxy handles http/https (http.ProxyURL) and
	// socks5 (proxy.FromURL) schemes; empty string → direct dial.
	if proxyURL != "" {
		if err := cli.SetProxy(proxyURL); err != nil {
			return nil, err
		}
		log.Debug().Bool("proxied", true).Msg("bridge: login HTTP routed through assigned proxy")
	}

	return &messagixLoginClient{c: cli}, nil
}

// resolveDeps fills in defaults for empty fields.
type resolvedDeps struct {
	Logger           zerolog.Logger
	AssetDiscoverer  AssetDiscoverer
	DisableTLSVerify bool
	Timeout          time.Duration
	Await2FATimeout  time.Duration
	ClientFactory    func(log zerolog.Logger, disableTLS bool, proxyURL string) (loginClient, error)
}

func resolveDeps(d Deps) resolvedDeps {
	out := resolvedDeps{
		Logger:           d.Logger,
		AssetDiscoverer:  d.AssetDiscoverer,
		DisableTLSVerify: d.DisableTLSVerify,
		Timeout:          d.Timeout,
		Await2FATimeout:  d.Await2FATimeout,
		ClientFactory:    d.ClientFactory,
	}
	if out.AssetDiscoverer == nil {
		out.AssetDiscoverer = DefaultDiscoverer()
	}
	if out.Timeout <= 0 {
		out.Timeout = 180 * time.Second
	}
	if out.Await2FATimeout <= 0 {
		out.Await2FATimeout = 120 * time.Second
	}
	if out.ClientFactory == nil {
		out.ClientFactory = productionClientFactory
	}
	return out
}

// Compile-time guards.
var _ handler.Driver = (*MautrixDriver)(nil)
