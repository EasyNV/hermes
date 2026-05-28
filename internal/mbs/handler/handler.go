// Package handler implements the HermesMbs gRPC service. It owns the
// gateway-facing API surface for Meta Business Suite session lifecycle,
// bridge-login orchestration, phone resolution, send, and inbound
// streaming. The handler is the encryption boundary: secrets cross
// from plaintext (bridge driver, decrypted creds) to ciphertext
// (BYTEA columns) here and nowhere else.
//
// Layering:
//
//   gateway → grpc → tenant interceptor → handler.<RPC>
//                                            │
//                                            ├─► store (encrypted)
//                                            ├─► session.Manager (MQTToT)
//                                            ├─► bridge.Driver (CAA login)
//                                            ├─► resolverFactory (graphql)
//                                            └─► EventPublisher (NATS)
//
// Defense in depth: every RPC reads tenant_id from incoming gRPC metadata
// AND cross-checks that the session's stored tenant_id matches. A stolen
// or forged x-tenant-id header is necessary but not sufficient to access
// another tenant's session.
package handler

import (
	"context"
	"errors"
	"fmt"
	"time"

	"mbs-native/auth"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/hermes-waba/hermes/internal/mbs/session"
	"github.com/hermes-waba/hermes/internal/mbs/store"
	"github.com/hermes-waba/hermes/pkg/crypto"
	"github.com/rs/zerolog"
)

// Handler implements hermesv1.HermesMbsServer.
type Handler struct {
	hermesv1.UnimplementedHermesMbsServer

	store           store.Store
	manager         session.Manager
	publisher       EventPublisher
	driverFactory   DriverFactory
	resolverFactory PhoneResolverFactory
	dek             crypto.DataEncryptionKey
	dedupe          *sendDedupeCache
	podID           string
	log             zerolog.Logger
	metrics         *HandlerMetrics

	// bridgeSem caps concurrent BridgeLogin streams. Mautrix-meta bloks
	// traversal is expensive; without a cap a flood of /BridgeLogin
	// calls could OOM the pod. Acquire with bounded timeout — exceed
	// → codes.ResourceExhausted.
	bridgeSem chan struct{}

	// bridgeAcquireTimeout caps how long BridgeLogin will wait for a
	// semaphore slot before returning ResourceExhausted. Default 100ms
	// — fast enough that callers don't sit on a connection, slow
	// enough that a brief burst can self-resolve.
	bridgeAcquireTimeout time.Duration
}

// Options bundles handler constructor inputs. Required fields are
// listed in NewHandler's validation; nil/zero values for the rest
// fall back to sensible defaults.
type Options struct {
	// Required
	Store         store.Store
	Manager       session.Manager
	Publisher     EventPublisher
	DriverFactory DriverFactory
	DEK           crypto.DataEncryptionKey
	PodID         string

	// Optional — defaults applied if zero-valued
	ResolverFactory PhoneResolverFactory // default: graphql.New adapter
	Logger          zerolog.Logger
	Metrics         *HandlerMetrics
	MaxConcurrentBridgeLogins int           // default 4
	DedupeCacheCap            int           // default 1024
	DedupeTTL                 time.Duration // default 5*time.Minute
	BridgeAcquireTimeout      time.Duration // default 100*time.Millisecond
}

// NewHandler builds a Handler from Options. Returns an error if any
// required field is missing (so a mis-wired cmd/mbs/main.go fails fast
// at startup instead of panicking on the first RPC).
func NewHandler(opts Options) (*Handler, error) {
	if opts.Store == nil {
		return nil, errors.New("handler: Store is required")
	}
	if opts.Manager == nil {
		return nil, errors.New("handler: Manager is required")
	}
	if opts.Publisher == nil {
		return nil, errors.New("handler: Publisher is required")
	}
	if opts.DriverFactory == nil {
		return nil, errors.New("handler: DriverFactory is required")
	}
	if opts.DEK.IsZero() {
		return nil, errors.New("handler: DEK is required (zero key)")
	}
	if opts.PodID == "" {
		return nil, errors.New("handler: PodID is required")
	}

	maxBridge := opts.MaxConcurrentBridgeLogins
	if maxBridge <= 0 {
		maxBridge = 4
	}
	dedupeCap := opts.DedupeCacheCap
	if dedupeCap <= 0 {
		dedupeCap = 1024
	}
	dedupeTTL := opts.DedupeTTL
	if dedupeTTL <= 0 {
		dedupeTTL = 5 * time.Minute
	}
	acquireTimeout := opts.BridgeAcquireTimeout
	if acquireTimeout <= 0 {
		acquireTimeout = 100 * time.Millisecond
	}

	resolverFactory := opts.ResolverFactory
	if resolverFactory == nil {
		resolverFactory = defaultResolverFactory
	}

	h := &Handler{
		store:                opts.Store,
		manager:              opts.Manager,
		publisher:            opts.Publisher,
		driverFactory:        opts.DriverFactory,
		resolverFactory:      resolverFactory,
		dek:                  opts.DEK,
		podID:                opts.PodID,
		log:                  opts.Logger,
		metrics:              opts.Metrics,
		bridgeSem:            make(chan struct{}, maxBridge),
		bridgeAcquireTimeout: acquireTimeout,
		dedupe:               newSendDedupeCache(dedupeCap, dedupeTTL),
	}
	return h, nil
}

// firstNonEmpty returns the first non-empty string from xs, or "" if all
// are empty. Helper for "use override if set, else default" patterns.
func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

// decryptCredsForUID loads a session row by uid (no tenant check — the
// caller is responsible for tenant validation upstream), decrypts the
// three secret columns via column-bound AAD, and returns the resulting
// *auth.Creds plus the original row.
//
// The row is returned so callers that need plaintext + raw row fields
// (BridgeEnvelope, MachineID) don't have to make a second store hit.
//
// The returned *auth.Creds contains plaintext access_token/secret/
// session_key — caller MUST treat as a transient secret.
func (h *Handler) decryptCredsForUID(ctx context.Context, uid int64) (*auth.Creds, *store.SessionRow, error) {
	row, err := h.store.GetSession(ctx, uid)
	if err != nil {
		return nil, nil, err
	}
	creds, err := session.DecryptCreds(h.dek, row)
	if err != nil {
		return nil, nil, fmt.Errorf("handler: decrypt creds: %w", err)
	}
	return creds, row, nil
}
