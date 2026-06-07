package session

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"mbs-native/client"

	"github.com/hermes-waba/hermes/internal/mbs/observability"
	"github.com/hermes-waba/hermes/internal/mbs/store"
	"github.com/hermes-waba/hermes/pkg/crypto"
	"github.com/rs/zerolog"
)

// muxedSession is the per-uid state inside manager. The mu mutex
// serializes connect/disconnect for this uid (single-flight). bc is
// shared by every Subscriber for this uid.
type muxedSession struct {
	uid int64

	mu sync.Mutex // serializes connect/disconnect for this uid

	// sendMu serializes Bootstrap+Send for this uid across ALL producers
	// (campaign consumer, manual consumer, retries). Distinct from mu so a
	// long send never blocks an unrelated connect/health-check/disconnect for
	// the same uid (and so Send → GetOrConnect, which takes mu internally,
	// can't self-deadlock). A session's MQTToT socket is single stateful
	// stream: interleaving Bootstrap(threadA)/Send/Bootstrap(threadB) from two
	// goroutines corrupts thread context and is an OPSEC hazard. Acquired
	// ctx-aware (lockCtx) so a wedged session doesn't pile up blocked senders.
	sendMu sync.Mutex

	connected      *Connected           // nil until connected; guarded by mu
	client         clientI              // the injected client (real or fake); guarded by mu
	listenerCtx    context.Context      // nil until connected; guarded by mu
	listenerCancel context.CancelFunc   // nil until connected; guarded by mu

	bc *broadcaster // shared across the uid's lifetime; lazily created on first Subscribe

	// selfFBID caches the session's admin/self messaging FBID once any poll
	// derives it (from a multi-thread snapshot). It survives listener
	// restarts (reconnects) because muxedSession outlives the listener. A
	// later single-thread poll — where self can't be derived by intersection
	// — reads this hint so outbound-echo dropping and customer-index
	// exclusion keep working. Atomic: the listener goroutine writes it; a
	// freshly-spawned reconnect listener reads it. (Closes TR-G2.)
	selfFBID atomic.Uint64
}

// manager is the concrete Manager implementation. Construct via NewManager.
type manager struct {
	store         store.Store
	dek           crypto.DataEncryptionKey
	podID         string
	log           zerolog.Logger
	metrics       *observability.Metrics // optional; nil-safe
	clientFactory clientFactory
	proxyResolver ProxyResolver // optional; nil → always direct (no proxy)
	proxyRequired bool          // PROXY_REQUIRED: hard-fail connect if no proxy resolved
	onDelta       DeltaHook // optional; called once per delta before broadcast

	sessionsMu sync.RWMutex
	sessions   map[int64]*muxedSession

	drained  atomic.Bool
	shutdown atomic.Bool
}

// ProxyResolver resolves a session's pinned proxy_id into a dialable proxy
// URL (scheme://user:pass@host:port). Returns "" when the session has no
// proxy (or the proxy can't be resolved) → direct connection under the soft
// policy. Implementations call the proxy service's GetProxy and format the
// URL. The Manager calls this on every connect/reconnect, so the sticky pin
// (mbs_sessions.proxy_id) is re-read each time and the self-heal redial
// rebuilds the SAME proxy dialer — never silently dropping to direct.
//
// proxyID is the value from mbs_sessions.proxy_id (already "" when NULL).
// tenantID is passed for auto-assign (Phase: step 8) and audit.
type ProxyResolver func(ctx context.Context, uid int64, tenantID, proxyID string) (proxyURL string)

// Opts bundles manager constructor args. Logger and Metrics may be
// zero-valued; the manager logs nothing and skips metric updates in
// that case.
type Opts struct {
	Store         store.Store
	DEK           crypto.DataEncryptionKey
	PodID         string
	Logger        zerolog.Logger
	Metrics       *observability.Metrics
	ClientFactory clientFactory // nil → defaultClientFactory

	// ProxyResolver, if set, maps a session's proxy_id → proxy URL so the
	// MQTT legs route through the assigned proxy. nil → all sessions connect
	// direct (no proxy). See ProxyResolver docs for the sticky/self-heal
	// contract.
	ProxyResolver ProxyResolver

	// ProxyRequired, when true, hard-fails connect (ErrProxyRequired) if the
	// resolver returns no proxy URL — the PROXY_REQUIRED policy. Default
	// false = soft (connect direct + WARN when no proxy).
	ProxyRequired bool

	// OnDelta, if set, is invoked by every per-uid listener EXACTLY ONCE
	// per InboundDelta, before broadcast to Subscribers. Use case:
	// per-delta NATS publish that must not multiply with subscriber
	// count. Hook MUST NOT block — see DeltaHook docs.
	OnDelta DeltaHook
}

// NewManager constructs a Manager. The opts.Store and opts.DEK are
// required; opts.PodID defaults to "hermes-mbs"; opts.ClientFactory
// defaults to defaultClientFactory.
func NewManager(opts Opts) Manager {
	cf := opts.ClientFactory
	if cf == nil {
		cf = defaultClientFactory
	}
	pid := opts.PodID
	if pid == "" {
		pid = "hermes-mbs"
	}
	return &manager{
		store:         opts.Store,
		dek:           opts.DEK,
		podID:         pid,
		log:           opts.Logger,
		metrics:       opts.Metrics,
		clientFactory: cf,
		proxyResolver: opts.ProxyResolver,
		proxyRequired: opts.ProxyRequired,
		onDelta:       opts.OnDelta,
		sessions:      make(map[int64]*muxedSession),
	}
}

// ─────────────────────────────────────────────────────────────────────
// GetOrConnect — single-flight per uid
// ─────────────────────────────────────────────────────────────────────

func (m *manager) GetOrConnect(ctx context.Context, uid int64) (*Connected, error) {
	if m.shutdown.Load() {
		return nil, ErrShutdown
	}
	if m.drained.Load() {
		return nil, ErrDrained
	}

	// Get (or create) the per-uid muxedSession holder.
	ms := m.getOrCreateMux(uid)

	// Serialize connect for this uid.
	ms.mu.Lock()
	defer ms.mu.Unlock()

	// Fast path: already connected AND the connection is still alive.
	// A cached client whose socket has died (broken pipe / reset / silent
	// half-open drop detected on the next write) must NOT be handed back —
	// every send/poll on it loops forever on "broken pipe". Drop the
	// corpse and fall through to a fresh connect. The pod_id claim is kept
	// (ClaimSession re-claims idempotently on the same pod); the
	// broadcaster + subscribers survive so in-flight Listen RPCs reattach
	// to the new client's listener transparently.
	if ms.connected != nil {
		if ms.client != nil && ms.client.Closed() {
			m.log.Warn().Int64("uid", uid).Msg("session: cached client dead, reconnecting")
			m.dropDeadLocked(ms)
			// fall through to slow path
		} else {
			return ms.connected, nil
		}
	}

	// Slow path: do the full connect flow. Errors leave ms.connected
	// nil; caller will see the error and any pod_id claim acquired
	// during the failed attempt has been rolled back.
	cn, err := m.connect(ctx, ms)
	if err != nil {
		return nil, err
	}
	ms.connected = cn
	return cn, nil
}

func (m *manager) getOrCreateMux(uid int64) *muxedSession {
	m.sessionsMu.RLock()
	ms, ok := m.sessions[uid]
	m.sessionsMu.RUnlock()
	if ok {
		return ms
	}
	m.sessionsMu.Lock()
	defer m.sessionsMu.Unlock()
	// Double-check after upgrading to write.
	if ms, ok := m.sessions[uid]; ok {
		return ms
	}
	ms = &muxedSession{uid: uid, bc: newBroadcaster()}
	if m.metrics != nil {
		ms.bc.onDropped = func() {
			// Reserved for chunk 5: metrics.SubscriberDropped.Inc()
			// (the field doesn't exist on the Metrics struct yet).
		}
	}
	m.sessions[uid] = ms
	return ms
}

// dropDeadLocked tears down a cached-but-dead client so GetOrConnect can
// re-dial. Caller MUST hold ms.mu. Unlike Disconnect it deliberately
// does NOT release the pod_id claim (the immediate reconnect re-claims it
// idempotently on the same pod) and does NOT close the broadcaster (Listen
// subscribers stay attached and reattach to the fresh listener). It cancels
// the old listener goroutine and Closes the old client.
//
// Safe to Close synchronously under ms.mu: this is only called when
// Closed()==true, so the client's read loop has already exited (or is
// about to — Close's conn.Close() unblocks a parked ReadFrame), and the
// production client has no OnError callback that could re-enter the lock.
func (m *manager) dropDeadLocked(ms *muxedSession) {
	c := ms.client
	if ms.listenerCancel != nil {
		ms.listenerCancel()
		ms.listenerCancel = nil
		ms.listenerCtx = nil
	}
	ms.connected = nil
	ms.client = nil
	if c != nil {
		_ = c.Close()
	}
	if m.metrics != nil {
		m.metrics.ConnectedSessions.Dec()
	}
}

// reconnectAsync re-establishes a session whose connection died, off the
// listener goroutine. Spawned as a detached goroutine because the calling
// listener is exiting and GetOrConnect's dead-client drop cancels THAT
// listener under ms.mu — calling it inline would deadlock.
//
// GetOrConnect's fast path sees Closed()==true, calls dropDeadLocked, and
// re-dials. Guard against shutdown/drain so a bounce doesn't fight the
// graceful-shutdown path. Best-effort: a failed re-dial logs and relies on
// the next listener tick (the fresh listener fires onDead again) or a send.
func (m *manager) reconnectAsync(uid int64) {
	go func() {
		if m.shutdown.Load() || m.drained.Load() {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), reconnectPerUIDLimit)
		defer cancel()
		if _, err := m.GetOrConnect(ctx, uid); err != nil {
			m.log.Warn().Err(err).Int64("uid", uid).Msg("session: async reconnect failed")
		}
	}()
}

// reconnectPerUIDLimit caps one async reconnect attempt's warmup +
// Lightspeed CONNECT path. Matches cmd/mbs startup reconnect tunable.
const reconnectPerUIDLimit = 30 * time.Second

// connect runs the full claim → decrypt → connect sequence. Holds
// ms.mu (caller's responsibility).
func (m *manager) connect(ctx context.Context, ms *muxedSession) (*Connected, error) {
	uid := ms.uid

	// 1. Claim pod_id ownership.
	claimed, ownerPodID, err := m.store.ClaimSession(ctx, uid, m.podID)
	if err != nil {
		return nil, fmt.Errorf("session: claim session: %w", err)
	}
	if !claimed {
		return nil, &ErrClaimConflict{UID: uid, OwnerPodID: ownerPodID}
	}

	// From this point, any failure must release the claim so a retry
	// (or another pod) can take over.
	releaseOnError := func() {
		// Use background ctx — if the user's ctx is dead we still want
		// to release. 5s bounded to not hang shutdown.
		relCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if relErr := m.store.ReleaseSession(relCtx, uid, m.podID); relErr != nil {
			m.log.Warn().Err(relErr).Int64("uid", uid).Msg("session: release-on-error failed")
		}
	}

	// 2. Load encrypted row.
	row, err := m.store.GetSession(ctx, uid)
	if err != nil {
		releaseOnError()
		return nil, fmt.Errorf("session: load row: %w", err)
	}

	// 3. Decrypt creds.
	creds, err := decryptCreds(m.dek, row)
	if err != nil {
		releaseOnError()
		return nil, err // already labeled by decryptCreds
	}

	// 4. Load assets + pick primary.
	assets, err := m.store.ListAssets(ctx, uid)
	if err != nil {
		releaseOnError()
		return nil, fmt.Errorf("session: list assets: %w", err)
	}
	primary := pickPrimary(assets)

	// 5. Build Lightspeed routing assets.
	lsAssets, err := buildLightspeedAssets(primary)
	if err != nil {
		releaseOnError()
		return nil, err
	}

	// Denormalize asset fields into creds (mbs-native/client paths read
	// PageID/WABAID/WECMailboxID from creds directly).
	if primary != nil {
		creds.PageID = primary.PageID
		creds.WABAID = primary.WabaID
		creds.WECMailboxID = primary.WecMailboxID
		creds.WECPhoneNumber = primary.WecPhoneNumber
		creds.PageName = primary.PageName
		creds.BusinessID = primary.BusinessID
		creds.WECAccountRegistered = primary.WECAccountRegistered
	}

	// 6. Resolve the session's sticky proxy (anti-ban). Re-read on EVERY
	// connect — including the self-heal redial path (reconnectAsync →
	// GetOrConnect → connect) — so a dead-socket recovery rebuilds the SAME
	// proxy dialer and never silently drops to a direct connection. Empty
	// proxyURL = direct (soft policy D3); the resolver logs/handles the
	// no-proxy and unresolvable cases.
	proxyURL := ""
	if m.proxyResolver != nil {
		pid := ""
		if row.ProxyID != nil {
			pid = *row.ProxyID
		}
		proxyURL = m.proxyResolver(ctx, uid, row.TenantID, pid)
		if proxyURL != "" {
			m.log.Debug().Int64("uid", uid).Msg("session: connecting through assigned proxy")
		}
	}
	// PROXY_REQUIRED (D3 hard): refuse to connect direct when a proxy is
	// mandated but none could be resolved — never leak the datacenter IP.
	if m.proxyRequired && proxyURL == "" {
		releaseOnError()
		m.log.Error().Int64("uid", uid).Msg("session: PROXY_REQUIRED set but no proxy resolved; refusing direct connect")
		return nil, ErrProxyRequired
	}
	if m.proxyResolver != nil && proxyURL == "" {
		m.log.Warn().Int64("uid", uid).Msg("session: no proxy resolved, connecting direct (soft policy)")
	}

	// 7. Build the client (proxy dialer installed when proxyURL != "") and
	// run CONNECT #1 + Lightspeed.
	c := m.clientFactory(creds, proxyURL)
	if err := c.WarmupAnalyticsSession(ctx); err != nil {
		releaseOnError()
		_ = c.Close()
		m.recordConnack(uid, "burned", connackFromErr(err))
		return nil, fmt.Errorf("session: warmup CONNECT #1: %w", err)
	}
	if err := c.ConnectLightspeed(ctx, lsAssets); err != nil {
		releaseOnError()
		_ = c.Close()
		m.recordConnack(uid, "burned", connackFromErr(err))
		return nil, fmt.Errorf("session: connect lightspeed: %w", err)
	}

	// 8. Record successful CONNECT.
	m.recordConnack(uid, "active", int16Ptr(0))

	// 9. Spawn listener goroutine. onDead self-heals the inbound path:
	// when the listener's poll detects a dead socket, it triggers an
	// async reconnect (drop dead client + re-dial + fresh listener) so a
	// pure-inbound session recovers without waiting for an outbound send.
	lctx, lcancel := context.WithCancel(context.Background())
	ms.listenerCtx = lctx
	ms.listenerCancel = lcancel
	ms.client = c // retained for Disconnect → Close
	lis := newListener(uid, row.TenantID, creds.PageID, creds.WECMailboxID, c, ms.bc, m.onDelta, &ms.selfFBID, m.log)
	lis.onDead = func() { m.reconnectAsync(uid) }
	go lis.run(lctx)

	cn := &Connected{
		UID:          uid,
		TenantID:     row.TenantID,
		Client:       extractRealClient(c),
		Creds:        creds,
		PrimaryAsset: primary,
		ConnectedAt:  time.Now(),
	}
	if m.metrics != nil {
		m.metrics.ConnectedSessions.Inc()
	}
	return cn, nil
}

// recordConnack persists the CONNECT outcome. Best-effort — log on
// failure, don't fail the connect call because of an audit write.
func (m *manager) recordConnack(uid int64, state string, connackRC *int16) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.store.UpdateSessionState(ctx, uid, state, connackRC); err != nil {
		// Ignore ErrNotImplemented during chunk-3 dev (tests with
		// limited mocks). Real PgStore is wired.
		if !errors.Is(err, store.ErrNotImplemented) {
			m.log.Warn().Err(err).Int64("uid", uid).Msg("session: record connack failed")
		}
	}
}

// connackFromErr extracts a CONNACK rc if the err carries one. Today
// the client doesn't surface rc as a typed error — so we record 255
// ("unknown") for any CONNECT failure. Chunk 5 can wire a real
// typed error.
func connackFromErr(err error) *int16 {
	if err == nil {
		return int16Ptr(0)
	}
	return int16Ptr(255)
}

func int16Ptr(v int16) *int16 { return &v }

// extractRealClient returns *client.Client when c is the real wrapper,
// else nil. Tests inject fakeClient and don't need the concrete client;
// their *Connected.Client stays nil and they assert via the fake's
// counters.
func extractRealClient(c clientI) *client.Client {
	return c.RawClient()
}

// ─────────────────────────────────────────────────────────────────────
// Disconnect / Subscribe / Drain / Shutdown
// ─────────────────────────────────────────────────────────────────────

// Disconnect tears down the client for uid, releases the pod_id claim,
// closes all subscriber channels, and removes the cache entry.
// Idempotent: no-op if uid isn't currently connected.
func (m *manager) Disconnect(uid int64) error {
	m.sessionsMu.RLock()
	ms, ok := m.sessions[uid]
	m.sessionsMu.RUnlock()
	if !ok {
		return nil
	}

	ms.mu.Lock()
	cn := ms.connected
	c := ms.client
	if ms.listenerCancel != nil {
		ms.listenerCancel()
		ms.listenerCancel = nil
		ms.listenerCtx = nil
	}
	ms.connected = nil
	ms.client = nil
	ms.mu.Unlock()

	// Close subscriber channels — Subscribers that haven't yet read see
	// a closed channel and exit cleanly.
	ms.bc.close()

	// Tear down the underlying client (via clientI so fake or real both
	// get Close called). Closing without ms.mu held to avoid deadlock
	// with the listener goroutine's final receive.
	if c != nil {
		_ = c.Close()
	}

	// Release pod_id ownership. Best-effort with bounded ctx.
	relCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.store.ReleaseSession(relCtx, uid, m.podID); err != nil {
		m.log.Warn().Err(err).Int64("uid", uid).Msg("session: release on disconnect failed")
	}

	// Remove the cache entry.
	m.sessionsMu.Lock()
	delete(m.sessions, uid)
	m.sessionsMu.Unlock()

	if m.metrics != nil && cn != nil {
		m.metrics.ConnectedSessions.Dec()
	}
	return nil
}

// Subscribe returns a buffered (cap=32) channel for inbound deltas
// plus an unsubscribe func. Creates a muxedSession on-demand if uid
// has not yet been connected — the channel returns no deltas until
// GetOrConnect fires. This lets handlers subscribe before connecting
// (race-free Listen RPC).
func (m *manager) Subscribe(uid int64) (<-chan *InboundDelta, func()) {
	ms := m.getOrCreateMux(uid)
	return ms.bc.subscribe()
}

// Send runs GetOrConnect → Bootstrap(threadID) → Send(text). All three
// steps must succeed; partial failures propagate to caller.
//
// The entire sequence is serialized per uid via ms.sendMu (acquired
// ctx-aware) so concurrent producers — campaign consumer, manual consumer,
// and NATS redelivery retries — cannot interleave Bootstrap/Send on the uid's
// single MQTToT socket. The per-uid connect mutex (mu, inside GetOrConnect) is
// a DIFFERENT lock, so an in-flight send never blocks connect/health/disconnect
// for the same uid. If ctx fires while waiting for sendMu, returns ctx.Err()
// (a wedged session sheds load instead of queueing blocked goroutines).
//
// The handler uses this instead of touching Connected.Client directly
// so that test code can swap in a fake Manager.
func (m *manager) Send(ctx context.Context, uid, threadID int64, text string) (*client.SendResult, error) {
	ms := m.getOrCreateMux(uid)
	if err := lockCtx(ctx, &ms.sendMu); err != nil {
		return nil, err
	}
	defer ms.sendMu.Unlock()

	cn, err := m.GetOrConnect(ctx, uid)
	if err != nil {
		return nil, err
	}
	if cn.Client == nil {
		// This indicates a test that injected a fake clientFactory
		// returning a fake whose RawClient() is nil but the test
		// reached the production Send path anyway. Surface explicitly
		// rather than panicking on nil-deref.
		return nil, errors.New("session: Send: Connected.Client is nil (fake factory in production path?)")
	}
	if err := cn.Client.Bootstrap(ctx, threadID); err != nil {
		return nil, err
	}
	return cn.Client.Send(ctx, threadID, text)
}

// Drain marks the manager as not-accepting-new-connections. Existing
// Connected sessions keep serving. Idempotent.
func (m *manager) Drain(ctx context.Context) error {
	m.drained.Store(true)
	return nil
}

// Shutdown disconnects every connected uid and releases every claim.
// Sequential per-uid disconnect; ctx fires → return ctx.Err() and
// leave the remaining entries (they'll release on the NEXT pod's
// startup, or via a manual `mbs-native` CLI command if needed).
func (m *manager) Shutdown(ctx context.Context) error {
	m.drained.Store(true)
	m.shutdown.Store(true)

	m.sessionsMu.RLock()
	uids := make([]int64, 0, len(m.sessions))
	for uid := range m.sessions {
		uids = append(uids, uid)
	}
	m.sessionsMu.RUnlock()

	for _, uid := range uids {
		if err := ctx.Err(); err != nil {
			return err
		}
		_ = m.Disconnect(uid)
	}
	return nil
}

// Sentinel referenced from tests / handler.
var _ Manager = (*manager)(nil)
var _ = errors.Is // keep errors import warm for releaseOnError path
