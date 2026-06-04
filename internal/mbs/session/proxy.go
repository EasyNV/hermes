// Proxy resolution for MBS sessions (anti-ban, Phase 1).
//
// The Manager calls a ProxyResolver on every connect/reconnect to turn a
// session's pinned proxy_id (mbs_sessions.proxy_id) into a dialable proxy
// URL. This file provides the production resolver built on the hermes-proxy
// gRPC client, plus the URL formatting shared with WA's convention.
//
// Sticky (D4): the pin lives in mbs_sessions.proxy_id. This resolver reads it
// on every call, so the self-heal redial rebuilds the SAME proxy — it never
// silently drops to direct.
//
// Auto-assign (D5, step 8): when a session has no pin and ProxyResolverConfig
// .AutoAssign is set, the resolver pulls a best proxy from the shared pool,
// assigns it (target=MBS), and persists the pin so it sticks thereafter.
package session

import (
	"context"
	"fmt"
	"net/url"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/rs/zerolog"
	"google.golang.org/grpc"
)

// ProxyClient is the subset of hermesv1.HermesProxyClient the resolver needs.
// Narrowed to an interface so the resolver is unit-testable without a real
// gRPC connection. *hermesv1.HermesProxyClient satisfies it directly.
type ProxyClient interface {
	GetProxy(ctx context.Context, in *hermesv1.ProxyGetRequest, opts ...grpc.CallOption) (*hermesv1.ProxyGetResponse, error)
	GetBestProxy(ctx context.Context, in *hermesv1.ProxyGetBestRequest, opts ...grpc.CallOption) (*hermesv1.ProxyGetBestResponse, error)
	AssignProxy(ctx context.Context, in *hermesv1.ProxyAssignRequest, opts ...grpc.CallOption) (*hermesv1.ProxyAssignResponse, error)
}

// ProxyResolverConfig configures NewProxyResolver.
type ProxyResolverConfig struct {
	// AutoAssign pulls + pins a best proxy from the pool when a session has
	// no proxy_id yet (D5). When false, an unpinned session connects direct.
	AutoAssign bool
	// Required hard-fails resolution (returns an error-signalling empty URL
	// plus a logged error) when no proxy can be resolved (PROXY_REQUIRED).
	// NOTE: the Manager treats empty URL as "direct"; hard-fail enforcement
	// for PROXY_REQUIRED is applied at the Manager level (it has the connect
	// error path). This flag is surfaced here only for logging clarity.
	Required bool
}

// NewProxyResolver builds the production ProxyResolver from a proxy gRPC
// client. A nil client yields a resolver that always returns "" (direct) —
// matching the soft policy when the proxy service is unavailable.
func NewProxyResolver(pc ProxyClient, cfg ProxyResolverConfig, log zerolog.Logger) ProxyResolver {
	return func(ctx context.Context, uid int64, tenantID, proxyID string) string {
		if pc == nil {
			return ""
		}

		// 1. Existing sticky pin → resolve it directly.
		if proxyID != "" {
			url := fetchProxyURL(ctx, pc, proxyID)
			if url != "" {
				return url
			}
			// The pinned proxy could not be resolved (deleted / proxy
			// service hiccup). Fall through to auto-assign if enabled so a
			// dangling pin self-heals; otherwise direct.
			log.Warn().Int64("uid", uid).Msg("mbs proxy: pinned proxy unresolvable")
		}

		// 2. No (usable) pin. Auto-assign a best proxy when enabled.
		if !cfg.AutoAssign {
			return ""
		}
		best, err := pc.GetBestProxy(ctx, &hermesv1.ProxyGetBestRequest{TenantId: tenantID})
		if err != nil || best.GetProxy() == nil {
			log.Warn().Int64("uid", uid).Err(err).Msg("mbs proxy: no proxy available to auto-assign")
			return ""
		}
		chosen := best.GetProxy()

		// Pin it (target=MBS, the uid as decimal string) so it sticks.
		if _, err := pc.AssignProxy(ctx, &hermesv1.ProxyAssignRequest{
			ProxyId:    chosen.Id,
			TargetType: hermesv1.ProxyTargetType_PROXY_TARGET_TYPE_MBS,
			TargetId:   fmt.Sprintf("%d", uid),
		}); err != nil {
			// Could not persist the pin — still use the proxy for THIS connect
			// (better than direct) but it won't stick. Log so we notice churn.
			log.Warn().Int64("uid", uid).Str("proxy_id", chosen.Id).Err(err).
				Msg("mbs proxy: auto-assign pin failed; using proxy for this connect only")
		} else {
			log.Info().Int64("uid", uid).Str("proxy_id", chosen.Id).
				Msg("mbs proxy: auto-assigned best proxy from pool")
		}
		return proxyURLFromProto(chosen)
	}
}

// ProxyURLFromProto is the exported form of proxyURLFromProto, used by the
// handler's login leg (which pulls a proxy from the pool before any session
// row exists and must format the URL itself). Empty/zero proxy → "".
func ProxyURLFromProto(p *hermesv1.Proxy) string {
	return proxyURLFromProto(p)
}

// fetchProxyURL resolves a proxy_id → URL via GetProxy. Returns "" on any
// error or missing proxy (caller decides fallback).
func fetchProxyURL(ctx context.Context, pc ProxyClient, proxyID string) string {
	resp, err := pc.GetProxy(ctx, &hermesv1.ProxyGetRequest{Id: proxyID})
	if err != nil || resp.GetProxy() == nil {
		return ""
	}
	return proxyURLFromProto(resp.GetProxy())
}

// proxyURLFromProto formats a proxy proto into a scheme://user:pass@host:port
// URL. Mirrors WA's buildProxyURL convention (socks5 default, http when typed
// HTTP; credentials encoded via url.UserPassword). Empty host → "".
func proxyURLFromProto(p *hermesv1.Proxy) string {
	if p == nil || p.Host == "" {
		return ""
	}
	scheme := "socks5"
	if p.Type == hermesv1.ProxyType_PROXY_TYPE_HTTP {
		scheme = "http"
	}
	u := &url.URL{
		Scheme: scheme,
		Host:   fmt.Sprintf("%s:%d", p.Host, p.Port),
	}
	if p.Username != "" {
		u.User = url.UserPassword(p.Username, p.Password)
	}
	return u.String()
}
