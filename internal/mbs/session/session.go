// Package session owns the in-memory cache of connected mbs-native
// client.Client instances for hermes-mbs. It serializes connect/disconnect
// per-uid (in-process mutex), enforces single-pod-per-session via the
// store's pod_id claim, decrypts secret columns via the DEK at connect
// time, and fans out inbound deltas to multiple Subscribers.
//
// Lifecycle, per uid:
//
//	GetOrConnect ───► claim pod_id ───► decrypt creds ───► client.New
//	                                                              │
//	                                                      WarmupAnalyticsSession
//	                                                              │
//	                                                      ConnectLightspeed(assets)
//	                                                              │
//	                                                      spawn listener goroutine
//	                                                              │
//	                                                      cache *Connected ───► return
//
// Tear-down: Disconnect cancels the listener, closes client, releases
// pod_id, closes all subscriber channels, removes the cache entry.
package session

import (
	"context"
	"time"

	"mbs-native/auth"
	"mbs-native/client"
	"mbs-native/fb"

	"github.com/hermes-waba/hermes/internal/mbs/store"
)

// Manager is the public interface. Implementations: NewManager builds a
// production *manager backed by store.Store + DEK; tests inject the
// same manager with a fake clientFactory.
type Manager interface {
	// GetOrConnect returns a connected *Connected for uid. See package
	// doc for the lifecycle. Single-flight: concurrent calls for the
	// same uid see one client.New + one CONNECT round-trip.
	GetOrConnect(ctx context.Context, uid int64) (*Connected, error)

	// Disconnect tears down the client for uid, releases the pod_id
	// claim, and removes the cache entry. Idempotent.
	Disconnect(uid int64) error

	// Subscribe returns a buffered (cap=32) channel that receives every
	// InboundDelta for uid, plus an unsubscribe function (safe to call
	// multiple times, safe after Disconnect).
	//
	// Backpressure policy: drop-don't-block. A slow subscriber drops
	// deltas (and increments metrics.SubscriberDropped if metrics is
	// set); other subscribers are unaffected.
	Subscribe(uid int64) (<-chan *InboundDelta, func())

	// Send delivers a text message to threadID on uid's session,
	// running GetOrConnect + Bootstrap + Send under the per-uid mutex.
	// The handler uses this instead of reaching into Connected.Client
	// directly — keeps the *client.Client concrete pointer hidden so
	// tests can inject a fake Manager without exposing the client
	// interface dance.
	//
	// Returns a *client.SendResult on success (MID, OTID, latency).
	// Errors propagate from any of the underlying steps; the handler
	// is responsible for mapping them to gRPC status.
	Send(ctx context.Context, uid, threadID int64, text string) (*client.SendResult, error)

	// Drain marks the manager as not-accepting-new-connections.
	// Existing Connected sessions keep serving. Idempotent.
	Drain(ctx context.Context) error

	// Shutdown disconnects all uids and releases all claims. Honors
	// ctx for a bounded shutdown duration; per-uid disconnects run
	// sequentially.
	Shutdown(ctx context.Context) error
}

// Connected is the cached in-memory representation of one connected
// session. Returned by GetOrConnect.
//
// Callers MUST NOT call Client.Close directly — that bypasses the
// pod_id release. Use Manager.Disconnect instead.
type Connected struct {
	UID          int64
	TenantID     string
	Client       *client.Client   // through WarmupAnalyticsSession + ConnectLightspeed
	Creds        *auth.Creds      // decrypted; lives in memory only
	PrimaryAsset *store.AssetRow  // for Lightspeed routing + UI display
	ConnectedAt  time.Time
}

// InboundDelta is one inbound event delivered to all Subscribers of a
// uid. One client.InboxItem may produce zero, one, or many records
// (a single /ls_resp envelope can batch several messages).
//
// SenderPhone is left EMPTY in chunk 3 — fb.ExtractMessages produces
// SenderURL + SenderName (display), not E.164. The handler (chunk 4)
// enriches via mbs_phone_threads reverse lookup.
type InboundDelta struct {
	UID           int64
	TenantID      string         // filled by listener from manager's session row
	PageID        string         // filled by listener from session creds (primary asset)
	MailboxID     string         // filled by listener from session creds (WEC mailbox)
	ThreadID      string         // OTID (19-digit) from FB payload
	MID           string         // server-assigned, "mid.$cAAA..."
	SenderPhone   string         // chunk 3: empty (filled by handler)
	SenderFBID    uint64         // messaging FBID of the author (snapshot poll path)
	SenderName    string         // display name from FB payload
	SenderURL     string         // fb://profile/<id>
	Text          string         // message body; empty for non-message events
	Kind          string         // FB payload "subkind" hint
	ReceivedAt    time.Time      // local clock at dispatch
	MetaTimestamp time.Time      // from Meta (zero if absent in payload)

	// Raw is set when the source was a client.Inbox push (small).
	// Nil for SnapshotPoll batches to avoid retaining large envelopes
	// per-subscriber.
	Raw *client.InboxItem
}

// DeltaHook is an optional per-delta callback invoked by the listener
// EXACTLY ONCE per delta, BEFORE broadcaster dispatch. Use case
// (chunk 4): publish each inbound delta to NATS exactly once,
// independent of the number of Subscribers.
//
// Contract:
//   - Called on the listener goroutine. Must NOT block (publish path
//     must be async or fast); a slow hook stalls all Subscribers for
//     the same uid.
//   - Called with a fully-populated *InboundDelta (TenantID set).
//   - Survives panics: the listener recovers and logs (publishing a
//     bad delta must not kill the listener).
type DeltaHook func(*InboundDelta)

// clientI is the subset of *client.Client used by Manager. Production
// code uses *client.Client (which satisfies this interface); tests
// inject a fake implementation.
//
// Keep this surface minimal — every method added forces fake updates
// and couples more of client to session.
type clientI interface {
	WarmupAnalyticsSession(ctx context.Context) error
	ConnectLightspeed(ctx context.Context, assets *client.LightspeedAssets) error
	Close() error
	SnapshotPoll(ctx context.Context, db string) (*fb.LsResp, error)
	InboxChan() <-chan *client.InboxItem

	// Closed reports whether the underlying connection is dead (socket
	// dropped, broken pipe, or Close called). The Manager checks this
	// before reusing a cached client and re-dials when it returns true.
	Closed() bool

	// RawClient returns the underlying *client.Client for callers that
	// need to invoke methods outside the clientI surface (Send, Reply,
	// SetTyping, MarkRead). Tests with fakeClient return nil — handlers
	// that hit the fake path should test via the fake's counters
	// instead of by calling Send.
	RawClient() *client.Client
}

// clientFactory builds a clientI from creds. Production path returns
// a real *client.Client; tests return a fakeClient.
type clientFactory func(creds *auth.Creds) clientI

// defaultClientFactory wraps client.New and adapts it to clientI by
// exposing the Inbox channel via InboxChan().
func defaultClientFactory(creds *auth.Creds) clientI {
	c := client.New(creds)
	return &realClient{Client: c}
}

// realClient adapts *client.Client to the clientI interface. The only
// non-trivial method is InboxChan, which surfaces the read-only side
// of c.Inbox so tests can mock the channel without exposing the
// concrete chan type to the rest of the package.
type realClient struct {
	*client.Client
}

func (r *realClient) InboxChan() <-chan *client.InboxItem {
	return r.Client.Inbox
}

func (r *realClient) RawClient() *client.Client {
	return r.Client
}
