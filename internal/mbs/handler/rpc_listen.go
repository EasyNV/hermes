package handler

import (
	"time"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/hermes-waba/hermes/internal/mbs/session"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Listen streams inbound message deltas for a session. Long-lived
// server-stream: stays open until the client cancels or the underlying
// session burns.
//
// Flow:
//
//   1. requireTenant + tenant cross-check
//   2. manager.GetOrConnect — ensures MQTToT is up + listener spawned
//   3. manager.Subscribe(uid) — returns a fan-out channel
//   4. for each delta: optionally skip (since filter), then stream.Send
//
// NATS publish is the LISTENER's job (chunk-3 reopen), NOT the handler's.
// This decouples publish-count from subscriber-count: N concurrent
// Listen RPCs = still exactly 1 publish per delta.
//
// SubscribersGauge tracks live Listen streams (Inc on subscribe, Dec
// on return). InboundCount is incremented per delta successfully
// streamed to the client.
func (h *Handler) Listen(req *hermesv1.MbsListenRequest, stream hermesv1.HermesMbs_ListenServer) error {
	ctx := stream.Context()
	tenantID, err := requireTenant(ctx)
	if err != nil {
		return err
	}
	if req.Uid == 0 {
		return status.Error(codes.InvalidArgument, "uid is required")
	}

	// Tenant cross-check. We don't need the row beyond confirming
	// ownership — the listener already knows tenantID via the chunk-3
	// reopen DeltaHook plumbing.
	if _, err := h.store.GetSessionByTenant(ctx, tenantID, req.Uid); err != nil {
		return mapStoreErr(err)
	}

	// Ensure the manager has a live connection (also starts the
	// listener goroutine if first GetOrConnect for this uid).
	if _, err := h.manager.GetOrConnect(ctx, req.Uid); err != nil {
		return mapSessionErr(err)
	}

	ch, unsub := h.manager.Subscribe(req.Uid)
	defer unsub()

	h.incSubscribers()
	defer h.decSubscribers()

	// Convert *Timestamp → time.Time once. zero ⇒ no filtering.
	var sinceCutoff time.Time
	if req.Since != nil {
		sinceCutoff = req.Since.AsTime()
	}

	for {
		select {
		case <-ctx.Done():
			// Client cancelled, deadline exceeded, or stream closed.
			// Return the ctx error so gRPC reports the right code.
			return ctx.Err()

		case delta, ok := <-ch:
			if !ok {
				// Manager.Disconnect closed the channel (session burned
				// or shutting down). Caller should reconnect.
				return status.Error(codes.Unavailable, "subscription closed by server")
			}
			if delta == nil {
				continue
			}
			if !sinceCutoff.IsZero() && delta.ReceivedAt.Before(sinceCutoff) {
				continue
			}
			if err := stream.Send(deltaToInboundMessage(delta)); err != nil {
				// Send failure usually means the client disconnected.
				// Return so the deferred unsub/gauge-dec run.
				return err
			}
			h.incInbound()
		}
	}
}

// deltaToInboundMessage converts a session.InboundDelta to the proto
// stream message. SenderUid is currently 0 (fb.ExtractMessages doesn't
// surface it as an int64). SenderPhone is whatever the listener
// populated (chunk 3: always ""; chunk 5+ may reverse-lookup).
//
// RawDelta is intentionally omitted in chunk 4 — proto reserves the
// field for future include_payload support. Empty string means "not
// requested".
func deltaToInboundMessage(d *session.InboundDelta) *hermesv1.MbsInboundMessage {
	return &hermesv1.MbsInboundMessage{
		ThreadId:    d.ThreadID,
		Mid:         d.MID,
		SenderUid:   0, // not extracted by fb.ExtractMessages today
		SenderPhone: d.SenderPhone,
		Text:        d.Text,
		ReceivedAt:  timestamppb.New(d.ReceivedAt),
	}
}

// ─────────────────────────────────────────────────────────────────────
// Metrics helpers (nil-safe)
// ─────────────────────────────────────────────────────────────────────

func (h *Handler) incSubscribers() {
	if h.metrics == nil || h.metrics.SubscribersGauge == nil {
		return
	}
	h.metrics.SubscribersGauge.Inc()
}
func (h *Handler) decSubscribers() {
	if h.metrics == nil || h.metrics.SubscribersGauge == nil {
		return
	}
	h.metrics.SubscribersGauge.Dec()
}
func (h *Handler) incInbound() {
	if h.metrics == nil || h.metrics.InboundCount == nil {
		return
	}
	h.metrics.InboundCount.Inc()
}
