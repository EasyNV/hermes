package handler

import (
	"context"
	"errors"

	"mbs-native/graphql"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/hermes-waba/hermes/internal/mbs/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ResolvePhone turns an E.164 phone number into the FB customer_id
// (= thread_id) for a session's WABA-connected page. Hot path —
// every send-by-phone hits this first.
//
// Path:
//
//   1. requireTenant + tenant cross-check
//   2. Normalize phone (graphql.NormalizePhone — strips formatting,
//      validates length, defaults to ID-region for leading-0 numbers)
//   3. Pick page (override > primary-from-assets)
//   4. Cache lookup (skipped if BypassCache)
//   5. Live resolve via graphql.ResolvePhoneToThreadID
//   6. Write-back to cache (best-effort)
//   7. Return response with was_cached + page_id + mailbox
//
// MQTToT is NOT touched. Resolve only needs decrypted creds + HTTP.
// This is why we built `decryptCredsForUID` standalone in handler.go
// rather than gating it behind session.Manager.GetOrConnect.
func (h *Handler) ResolvePhone(ctx context.Context, req *hermesv1.ResolvePhoneRequest) (*hermesv1.ResolvePhoneResponse, error) {
	tenantID, err := requireTenant(ctx)
	if err != nil {
		return nil, err
	}
	if req.Uid == 0 {
		return nil, status.Error(codes.InvalidArgument, "uid is required")
	}
	if req.Phone == "" {
		return nil, status.Error(codes.InvalidArgument, "phone is required")
	}

	// Tenant cross-check — this also gives us the session row for
	// asset lookup below.
	sessRow, err := h.store.GetSessionByTenant(ctx, tenantID, req.Uid)
	if err != nil {
		return nil, mapStoreErr(err)
	}

	// Normalize early — fail fast on garbage input. graphql.NormalizePhone
	// strips non-digits, handles leading-00 / leading-0 / E.164, and
	// validates length [8,15].
	normalized, err := graphql.NormalizePhone(req.Phone)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid phone: %v", err)
	}

	// Pick the page: explicit override wins, else the primary asset.
	pageID, err := h.resolvePageID(ctx, sessRow.UID, req.PageIdOverride)
	if err != nil {
		return nil, err
	}

	// Cache lookup (unless bypassed).
	if !req.BypassCache {
		if cached, lookupErr := h.store.GetPhoneThread(ctx, req.Uid, pageID, normalized); lookupErr == nil {
			h.recordResolve("cache")
			return &hermesv1.ResolvePhoneResponse{
				ThreadId:        cached.ThreadID,
				NormalizedPhone: normalized,
				PageId:          pageID,
				WecMailboxId:    cached.WecMailboxID,
				WasCached:       true,
			}, nil
		} else if !errors.Is(lookupErr, store.ErrNotFound) {
			// Real lookup error (DB down etc.) — fall through to live
			// resolve as a graceful degradation. Cache is presentation;
			// the resolve itself is the source of truth.
			h.log.Warn().Err(lookupErr).
				Int64("uid", req.Uid).Str("page", pageID).
				Msg("ResolvePhone: cache lookup failed, falling through to live")
		}
	}

	// Live resolve — needs decrypted creds.
	creds, _, err := h.decryptCredsForUID(ctx, req.Uid)
	if err != nil {
		return nil, mapSessionErr(err) // covers crypto.ErrDecryptFailed → Unauthenticated
	}

	resolver, err := h.resolverFactory(creds)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "resolver init: %v", err)
	}

	threadID, mailboxID, err := resolver.ResolvePhoneToThreadID(ctx, pageID, normalized)
	if err != nil {
		return nil, mapClientErr(err)
	}

	// Write-back. Best-effort — log on failure but still return the
	// live result. The next call simply re-resolves; no correctness
	// impact (BizInboxWhatsAppCustomerMutation is deterministic per
	// (page, phone)).
	if upErr := h.store.UpsertPhoneThread(ctx, &store.PhoneThreadRow{
		UID:          req.Uid,
		PageID:       pageID,
		Phone:        normalized,
		ThreadID:     threadID,
		WecMailboxID: mailboxID,
	}); upErr != nil {
		h.log.Warn().Err(upErr).
			Int64("uid", req.Uid).Str("page", pageID).
			Msg("ResolvePhone: cache write-back failed (non-fatal)")
	}

	h.recordResolve("live")
	return &hermesv1.ResolvePhoneResponse{
		ThreadId:        threadID,
		NormalizedPhone: normalized,
		PageId:          pageID,
		WecMailboxId:    mailboxID,
		WasCached:       false,
	}, nil
}

// resolvePageID picks the page to resolve against:
//
//   - If override is non-empty, use it.
//   - Else look up the session's primary asset and use that page_id.
//   - Else FailedPrecondition (session has no primary page configured —
//     this is a legitimate state right after bridge if the account has
//     no WABA-connected page).
//
// We do NOT validate the override against ListAssets — Sam may legitimately
// want to send from a page that's not currently flagged primary. The
// downstream graphql call will fail with CreateCustomerError if the
// override is bogus, which we'll map to FailedPrecondition.
func (h *Handler) resolvePageID(ctx context.Context, uid int64, override string) (string, error) {
	if override != "" {
		return override, nil
	}
	primary := h.lookupPrimaryAsset(ctx, uid)
	if primary == nil || primary.PageID == "" {
		return "", status.Error(codes.FailedPrecondition,
			"session has no primary page configured; pass page_id_override")
	}
	return primary.PageID, nil
}

// recordResolve increments ResolveTotal{source=...}. nil-safe.
func (h *Handler) recordResolve(source string) {
	if h.metrics == nil || h.metrics.ResolveTotal == nil {
		return
	}
	h.metrics.ResolveTotal.WithLabelValues(source).Inc()
}
