package main

import (
	"fmt"
	"time"

	natsgo "github.com/nats-io/nats.go"
)

// jetStreamStreamManager is the slice of natsgo.JetStreamContext the
// stream-ensure path uses. Defined locally so tests inject a recorder
// without spinning up a real NATS server.
type jetStreamStreamManager interface {
	AddStream(cfg *natsgo.StreamConfig, opts ...natsgo.JSOpt) (*natsgo.StreamInfo, error)
}

// ensureStreams idempotently creates the two JetStream streams that
// hermes-mbs owns. Shape per parent plan Contract 9:
//
//	HERMES_MBS       — message + lifecycle events (publisher side)
//	HERMES_MBS_SEND  — campaign + manual send work queue (consumer side)
//
// Replicas parameter is K8s-aware:
//   - 1 in docker-compose (no NATS cluster)
//   - 3 in a clustered K8s NATS deployment
//
// JetStream rejects Replicas > cluster-size; the operator MUST set
// MBS_STREAM_REPLICAS to match the cluster shape. Replicas<=0 is
// normalized to 1 so a typo doesn't fail boot.
//
// AddStream is idempotent w.r.t. an EXISTING stream of identical
// config. If a previous run created the stream with different config
// the call returns an ErrStreamNameAlreadyInUse-style error and the
// operator must reconcile (delete-and-recreate, or edit via the NATS
// CLI). This matches hermes-wa behavior — failing boot here is the
// right call.
//
// Duplicates: 60s gives publishers a 60-second dedup window keyed on
// the msg id passed via natsgo.MsgId() — chunk-4's natsEventPublisher
// already passes EventMeta.EventId, so redelivery of the same logical
// event is suppressed at the broker.
func ensureStreams(js jetStreamStreamManager, replicas int) error {
	if replicas <= 0 {
		replicas = 1
	}

	// Events stream (HERMES_MBS): pub-side messages + lifecycle.
	// Limits retention (time-based) so a slow consumer doesn't push
	// back on the publisher.
	if _, err := js.AddStream(&natsgo.StreamConfig{
		Name:       "HERMES_MBS",
		Subjects:   []string{"hermes.mbs.message.>", "hermes.mbs.session.>"},
		Storage:    natsgo.FileStorage,
		Retention:  natsgo.LimitsPolicy,
		MaxAge:     7 * 24 * time.Hour,
		Replicas:   replicas,
		Duplicates: 60 * time.Second,
	}); err != nil {
		return fmt.Errorf("ensure HERMES_MBS stream: %w", err)
	}

	// Send-task stream (HERMES_MBS_SEND): work-queue semantics so each
	// task is processed exactly once across the consumer fleet.
	// 24h max age caps the catch-up window after a long outage.
	if _, err := js.AddStream(&natsgo.StreamConfig{
		Name:       "HERMES_MBS_SEND",
		Subjects:   []string{"hermes.mbs.send.>"},
		Storage:    natsgo.FileStorage,
		Retention:  natsgo.WorkQueuePolicy,
		MaxAge:     24 * time.Hour,
		Replicas:   replicas,
		Duplicates: 60 * time.Second,
	}); err != nil {
		return fmt.Errorf("ensure HERMES_MBS_SEND stream: %w", err)
	}

	return nil
}
