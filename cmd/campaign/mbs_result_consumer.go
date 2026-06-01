package main

import (
	"context"
	"fmt"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
	"google.golang.org/protobuf/proto"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/hermes-waba/hermes/internal/campaign/engine"
)

// ─── close-the-loop tunables ────────────────────────────────────────
const (
	// HERMES_MBS is created by hermes-mbs. On a cold start the campaign
	// service may boot first, so the result-consumer subscribe retries
	// until the stream exists (mirrors carrying gap C3-G1 sub-bind retry).
	mbsResultBindAttempts = 15
	mbsResultBindBackoff  = 2 * time.Second

	mbsResultAckWait    = 30 * time.Second
	mbsResultMaxDeliver = 5

	// Stuck-queued reaper: contacts queued longer than this with no result
	// event are timed out to 'failed'. 5m comfortably exceeds the worst-case
	// send path (warmup + bootstrap + send ≈ a few seconds) plus redelivery.
	reaperInterval  = 60 * time.Second
	reaperThreshold = 5 * time.Minute
)

// startMbsResultConsumer subscribes to hermes.mbs.message.outbound.* (bound to
// the HERMES_MBS stream owned by hermes-mbs) and feeds each MbsOutboundEvent to
// the engine's HandleMbsResult. This is the missing consumer the events.proto
// contract always named ("Consumers: ... hermes-campaign (delivery tracking)")
// but that was never built — the cause of the open-loop "completed but not
// delivered" bug.
//
// Durable push consumer, ManualAck. HandleMbsResult returns true to Ack
// (we never redeliver a result event — the reaper is the backstop for any
// contact left stuck in 'queued').
func startMbsResultConsumer(js natsgo.JetStreamContext, eng *engine.Engine, log zerolog.Logger) error {
	handlerFn := func(msg *natsgo.Msg) {
		var ev hermesv1.MbsOutboundEvent
		if err := proto.Unmarshal(msg.Data, &ev); err != nil {
			log.Error().Err(err).Msg("mbs result consumer: bad proto — dropping poison")
			_ = msg.Ack()
			return
		}
		if eng.HandleMbsResult(context.Background(), &ev) {
			_ = msg.Ack()
		} else {
			_ = msg.Nak()
		}
	}

	var lastErr error
	for attempt := 1; attempt <= mbsResultBindAttempts; attempt++ {
		_, err := js.Subscribe(
			"hermes.mbs.message.outbound.*",
			handlerFn,
			natsgo.BindStream("HERMES_MBS"),
			natsgo.Durable("campaign-mbs-result"),
			natsgo.ManualAck(),
			natsgo.AckWait(mbsResultAckWait),
			natsgo.MaxDeliver(mbsResultMaxDeliver),
			natsgo.DeliverAll(),
		)
		if err == nil {
			log.Info().
				Str("subject", "hermes.mbs.message.outbound.*").
				Str("durable", "campaign-mbs-result").
				Msg("mbs result consumer started")
			return nil
		}
		lastErr = err
		log.Warn().Err(err).Int("attempt", attempt).
			Msg("mbs result consumer: HERMES_MBS not ready, retrying")
		time.Sleep(mbsResultBindBackoff)
	}
	return fmt.Errorf("subscribe mbs result after %d attempts: %w", mbsResultBindAttempts, lastErr)
}

// runStuckQueuedReaper ticks every reaperInterval and times out any contact
// stuck in 'queued' past reaperThreshold. Blocks until ctx is cancelled.
func runStuckQueuedReaper(ctx context.Context, eng *engine.Engine, log zerolog.Logger) {
	ticker := time.NewTicker(reaperInterval)
	defer ticker.Stop()
	log.Info().
		Dur("interval", reaperInterval).
		Dur("threshold", reaperThreshold).
		Msg("stuck-queued reaper started")
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			eng.ReapStuckQueued(ctx, reaperThreshold)
		}
	}
}
