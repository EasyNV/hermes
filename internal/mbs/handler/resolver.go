package handler

import (
	"context"

	"mbs-native/auth"
	"mbs-native/graphql"

	"github.com/hermes-waba/hermes/internal/mbs/store"
)

// PhoneResolver is the small surface the handler needs from a graphql
// client to perform a live phone → thread_id resolve. Defining an
// interface lets tests inject a fake without spinning up a real
// graphql.Client (which needs an HTTP server + valid creds).
//
// One method per resolve operation. If a future resolver supports
// additional ops (e.g., batch resolve), expand here.
type PhoneResolver interface {
	// ResolvePhoneToThreadID hits BizInboxWhatsAppCustomerMutation
	// for (pageID, phone) and returns (customer_id, wec_mailbox_id, err).
	// phone is the normalized E.164 form (minus leading +).
	ResolvePhoneToThreadID(ctx context.Context, pageID, phone string) (customerID, wecMailboxID string, err error)
}

// PhoneResolverFactory builds a PhoneResolver from a decrypted Creds and an
// optional per-session proxy URL (anti-ban). proxyURL "" → direct. Handler
// invokes per call (cheap; just wraps an HTTP client). Tests inject a closure
// returning a fake; production uses defaultResolverFactory.
type PhoneResolverFactory func(creds *auth.Creds, proxyURL string) (PhoneResolver, error)

// defaultResolverFactory wraps graphql.NewWithProxy so we don't have to import
// graphql at the handler call site. It is the only production path. proxyURL,
// when non-empty, routes the /graphql HTTP leg through the session's assigned
// proxy while preserving the utls fingerprint.
func defaultResolverFactory(creds *auth.Creds, proxyURL string) (PhoneResolver, error) {
	gc, err := graphql.NewWithProxy(creds, proxyURL)
	if err != nil {
		return nil, err
	}
	return &graphqlAdapter{gc: gc}, nil
}

// graphqlAdapter adapts *graphql.Client to the local PhoneResolver
// interface. Stays at one method; the handler only uses
// ResolvePhoneToThreadID. If the handler grows to call other graphql
// methods, expand both this adapter and the PhoneResolver interface.
type graphqlAdapter struct {
	gc *graphql.Client
}

func (g *graphqlAdapter) ResolvePhoneToThreadID(ctx context.Context, pageID, phone string) (string, string, error) {
	return g.gc.ResolvePhoneToThreadID(ctx, pageID, phone)
}

// proxyURLForSession resolves the proxy URL for a session row via the optional
// ProxyResolver. Returns "" (direct) when no resolver is wired or the session
// has no proxy. Used to route the /graphql legs (phone-resolve, send) through
// the session's sticky proxy — same anti-ban posture as the MQTT legs.
func (h *Handler) proxyURLForSession(ctx context.Context, row *store.SessionRow) string {
	if h.proxyResolver == nil || row == nil {
		return ""
	}
	pid := ""
	if row.ProxyID != nil {
		pid = *row.ProxyID
	}
	return h.proxyResolver(ctx, row.UID, row.TenantID, pid)
}

// proxyURLForUID is proxyURLForSession for call sites that have only a uid
// (e.g. the send path's inline resolver). Loads the session row to read its
// proxy pin. Returns "" (direct) on any lookup miss — the proxy is an
// optimization, never a hard dependency for resolution.
func (h *Handler) proxyURLForUID(ctx context.Context, uid int64) string {
	if h.proxyResolver == nil {
		return ""
	}
	row, err := h.store.GetSession(ctx, uid)
	if err != nil {
		return ""
	}
	return h.proxyURLForSession(ctx, row)
}
