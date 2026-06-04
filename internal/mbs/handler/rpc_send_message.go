package handler

import (
	"context"
	"errors"
	"strconv"
	"time"

	"mbs-native/graphql"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/hermes-waba/hermes/internal/mbs/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// SendMessage delivers a single text message via MQTToT.
//
// Flow (the order is correctness-critical — tests pin every step):
//
//   1. Dedupe lookup. Empty/missing dedupe id → always miss.
//   2. Require tenant + tenant cross-check via GetSessionByTenant.
//   3. Resolve recipient → numeric thread_id:
//      - oneof = thread_id  → parse to int64
//      - oneof = phone      → normalize → cache lookup → live resolve
//                             via resolverFactory (cache write-back is
//                             best-effort)
//   4. session.Manager.Send(uid, threadID, text) — runs GetOrConnect
//      + Bootstrap + Send under the per-uid mutex.
//   5. Publish outbound event (ALWAYS — both success and failure).
//   6. On success: store in dedupe cache.
//
// The handler NEVER touches Connected.Client directly — that field
// stays concrete-only so tests can swap a fake Manager.
func (h *Handler) SendMessage(ctx context.Context, req *hermesv1.MbsSendMessageRequest) (*hermesv1.MbsSendMessageResponse, error) {
	tenantID, err := requireTenant(ctx)
	if err != nil {
		return nil, err
	}
	if req.Uid == 0 {
		return nil, status.Error(codes.InvalidArgument, "uid is required")
	}
	if req.Text == "" {
		return nil, status.Error(codes.InvalidArgument, "text is required")
	}

	// 1. Dedupe lookup — earliest possible (saves all downstream work).
	if cached, ok := h.dedupe.Lookup(req.Uid, req.ClientDedupeId); ok {
		h.recordSend("dedupe_hit")
		return cached, nil
	}

	// 2. Tenant cross-check + session row (needed for inline resolve).
	sessRow, err := h.store.GetSessionByTenant(ctx, tenantID, req.Uid)
	if err != nil {
		return nil, mapStoreErr(err)
	}

	// 3. Resolve recipient → numeric threadID.
	threadIDStr, err := h.resolveRecipient(ctx, sessRow.UID, req.Recipient, req.PageIdOverride)
	if err != nil {
		return nil, err // already gRPC-status-coded
	}
	threadID, err := strconv.ParseInt(threadIDStr, 10, 64)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "thread_id %q is not numeric", threadIDStr)
	}

	// 4. Send via manager. Manager owns the connect/bootstrap/send
	// sequencing under the per-uid mutex.
	sendStart := time.Now()
	result, sendErr := h.manager.Send(ctx, req.Uid, threadID, req.Text)
	latency := time.Since(sendStart)
	h.observeSendLatency(latency, sendErr == nil)

	// 5. Publish outbound — ALWAYS, success or failure. Consumers
	// track delivery attempts both ways.
	now := time.Now()
	switch {
	case sendErr != nil:
		h.publisher.PublishOutbound(req.Uid, tenantID, threadIDStr, "", "",
			latency.Milliseconds(), false, sendErr.Error(), now, req.ClientDedupeId)
		h.recordSend("err")
		return nil, mapSendErr(sendErr)
	case result == nil:
		// Manager returned (nil, nil) — shouldn't happen for the
		// production *manager.Send, but a misbehaving fake might.
		// Treat as internal error.
		h.publisher.PublishOutbound(req.Uid, tenantID, threadIDStr, "", "",
			latency.Milliseconds(), false, "manager: nil result without error", now, req.ClientDedupeId)
		h.recordSend("err")
		return nil, status.Error(codes.Internal, "send returned nil result without error")
	}

	resp := &hermesv1.MbsSendMessageResponse{
		ThreadId:  threadIDStr,
		Mid:       result.MID,
		Otid:      result.OTID,
		LatencyMs: latency.Milliseconds(),
		SentAt:    timestamppb.New(now),
	}
	h.publisher.PublishOutbound(req.Uid, tenantID, threadIDStr, result.MID, result.OTID,
		latency.Milliseconds(), true, "", now, req.ClientDedupeId)
	h.recordSend("ok")

	// 6. Cache successful response under dedupe id.
	h.dedupe.Store(req.Uid, req.ClientDedupeId, resp)
	return resp, nil
}

// resolveRecipient handles the proto oneof. Returns a numeric thread_id
// as string (preserves the proto's string-typed forward-compat), or a
// gRPC error suitable for return.
func (h *Handler) resolveRecipient(
	ctx context.Context,
	uid int64,
	recipient any, // isMbsSendMessageRequest_Recipient
	pageIDOverride string,
) (string, error) {
	switch r := recipient.(type) {
	case *hermesv1.MbsSendMessageRequest_ThreadId:
		if r.ThreadId == "" {
			return "", status.Error(codes.InvalidArgument, "thread_id is empty")
		}
		return r.ThreadId, nil

	case *hermesv1.MbsSendMessageRequest_Phone:
		if r.Phone == "" {
			return "", status.Error(codes.InvalidArgument, "phone is empty")
		}
		normalized, err := graphql.NormalizePhone(r.Phone)
		if err != nil {
			return "", status.Errorf(codes.InvalidArgument, "invalid phone: %v", err)
		}
		pageID, err := h.resolvePageID(ctx, uid, pageIDOverride)
		if err != nil {
			return "", err
		}

		// Cache hit short-circuit.
		if cached, lookupErr := h.store.GetPhoneThread(ctx, uid, pageID, normalized); lookupErr == nil {
			h.recordResolve("cache")
			return cached.ThreadID, nil
		} else if !errors.Is(lookupErr, store.ErrNotFound) {
			h.log.Warn().Err(lookupErr).
				Int64("uid", uid).Str("page", pageID).
				Msg("SendMessage: cache lookup failed, falling through to live")
		}

		// Live resolve.
		creds, _, err := h.decryptCredsForUID(ctx, uid)
		if err != nil {
			return "", mapSessionErr(err)
		}
		resolver, err := h.resolverFactory(creds, h.proxyURLForUID(ctx, uid))
		if err != nil {
			return "", status.Errorf(codes.Internal, "resolver init: %v", err)
		}
		threadID, mailboxID, err := resolver.ResolvePhoneToThreadID(ctx, pageID, normalized)
		if err != nil {
			return "", mapClientErr(err)
		}

		// Cache write-back (best-effort).
		if upErr := h.store.UpsertPhoneThread(ctx, &store.PhoneThreadRow{
			UID:          uid,
			PageID:       pageID,
			Phone:        normalized,
			ThreadID:     threadID,
			WecMailboxID: mailboxID,
		}); upErr != nil {
			h.log.Warn().Err(upErr).
				Int64("uid", uid).Str("page", pageID).
				Msg("SendMessage: cache write-back failed (non-fatal)")
		}
		h.recordResolve("live")
		return threadID, nil

	case nil:
		return "", status.Error(codes.InvalidArgument, "recipient is required (thread_id or phone)")

	default:
		return "", status.Errorf(codes.InvalidArgument, "unknown recipient type %T", recipient)
	}
}

// mapSendErr is a thin wrapper around mapClientErr that also handles
// the session-layer errors (claim conflict + drained/shutdown) that
// can come back from Manager.Send. Most send failures are client-side
// (network, MQTToT close, bootstrap fail) → mapClientErr.
func mapSendErr(err error) error {
	if err == nil {
		return nil
	}
	// Try session-specific mapping first (claim conflict + drained
	// + decrypt-failed). If nothing matched, fall through.
	if mapped := mapSessionErr(err); mapped != nil {
		// mapSessionErr always returns a gRPC status for non-nil err,
		// but only return it if it was one of the session sentinels.
		// We re-check by sniffing the status code: if it's Internal
		// AND the error doesn't match a session sentinel, fall through
		// to client mapping.
		//
		// Simpler: mapSessionErr returns mapStoreErr fallback for
		// unrecognized errors. We want client mapping for
		// "send failed with network err" — so just try mapClientErr
		// for non-session errors directly.
		if isSessionSentinel(err) {
			return mapped
		}
	}
	return mapClientErr(err)
}

// isSessionSentinel reports whether err is one of the session-layer
// typed errors (ErrShutdown, ErrDrained, ErrClaimConflict, or wrapped
// crypto.ErrDecryptFailed). Used to disambiguate session vs client
// errors in the send path.
func isSessionSentinel(err error) bool {
	if err == nil {
		return false
	}
	// Re-import the session sentinels via a small helper. Avoids
	// circular import (handler → session).
	return errorsIsAny(err, sessionSentinelErrs()...)
}

// observeSendLatency records the per-send histogram.
func (h *Handler) observeSendLatency(d time.Duration, ok bool) {
	if h.metrics == nil || h.metrics.SendLatency == nil {
		return
	}
	outcome := "err"
	if ok {
		outcome = "ok"
	}
	h.metrics.SendLatency.WithLabelValues(outcome).Observe(d.Seconds())
}

// recordSend increments SendTotal{outcome=...}.
func (h *Handler) recordSend(outcome string) {
	if h.metrics == nil || h.metrics.SendTotal == nil {
		return
	}
	h.metrics.SendTotal.WithLabelValues(outcome).Inc()
}
