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

// ─── burned-session consumer tunables ───────────────────────────────
const (
	// HERMES_MBS is owned by hermes-mbs; on a cold start the campaign service
	// may boot first, so the subscribe retries until the stream exists
	// (mirrors the result-consumer bind retry).
	mbsBurnedBindAttempts = 15
	mbsBurnedBindBackoff  = 2 * time.Second

	mbsBurnedAckWait    = 30 * time.Second
	mbsBurnedMaxDeliver = 5
)

// startMbsBurnedConsumer subscribes to hermes.mbs.session.burned.* (bound to the
// HERMES_MBS stream owned by hermes-mbs) and feeds each MbsSessionLifecycleEvent
// to the engine's HandleSessionBurned. This is the G2 parity fix: when an MBS
// session burns mid-campaign, proactively disable it as a sender across all
// campaigns so the campaign_senders table reflects reality immediately, rather
// than relying solely on the state-gated selection JOIN to mask it at pick time.
//
// Only the 'burned' subject is consumed — 'disconnected' sessions auto-reconnect
// and must NOT be disabled; only a true burn is terminal.
//
// Durable push consumer, ManualAck. HandleSessionBurned always returns true to
// Ack (idempotent; a transient DB error logs + Acks, the selection JOIN is the
// backstop so a burned sender can never actually be picked).
func startMbsBurnedConsumer(js natsgo.JetStreamContext, eng *engine.Engine, log zerolog.Logger) error {
	handlerFn := func(msg *natsgo.Msg) {
		var ev hermesv1.MbsSessionLifecycleEvent
		if err := proto.Unmarshal(msg.Data, &ev); err != nil {
			log.Error().Err(err).Msg("mbs burned consumer: bad proto — dropping poison")
			_ = msg.Ack()
			return
		}
		if eng.HandleSessionBurned(context.Background(), &ev) {
			_ = msg.Ack()
		} else {
			_ = msg.Nak()
		}
	}

	var lastErr error
	for attempt := 1; attempt <= mbsBurnedBindAttempts; attempt++ {
		_, err := js.Subscribe(
			"hermes.mbs.session.burned.*",
			handlerFn,
			natsgo.BindStream("HERMES_MBS"),
			natsgo.Durable("campaign-mbs-burned"),
			natsgo.ManualAck(),
			natsgo.AckWait(mbsBurnedAckWait),
			natsgo.MaxDeliver(mbsBurnedMaxDeliver),
			natsgo.DeliverAll(),
		)
		if err == nil {
			log.Info().
				Str("subject", "hermes.mbs.session.burned.*").
				Str("durable", "campaign-mbs-burned").
				Msg("mbs burned-session consumer started")
			return nil
		}
		lastErr = err
		log.Warn().Err(err).Int("attempt", attempt).
			Msg("mbs burned consumer: HERMES_MBS not ready, retrying")
		time.Sleep(mbsBurnedBindBackoff)
	}
	return fmt.Errorf("subscribe mbs burned after %d attempts: %w", mbsBurnedBindAttempts, lastErr)
}
