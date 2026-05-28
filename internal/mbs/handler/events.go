package handler

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	natsgo "github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// EventPublisher is the small surface the handler needs to publish
// MBS events to NATS. Defined as an interface so tests can inject a
// recording fake and so a future swap (e.g., Kafka) is local.
//
// Subject mapping:
//
//   PublishInboundMessage   → hermes.mbs.message.inbound.{tenant}
//   PublishOutbound         → hermes.mbs.message.outbound.{tenant}
//   PublishSessionLifecycle → hermes.mbs.session.{state_str}.{tenant}
//
// state_str derivation is driven by `reason` (publisher's "created" /
// "connected" / "disconnected" / "burned" / "refreshed") OR falls
// back to stateToSubjectFragment(new_state). UNSPECIFIED + BRIDGING
// never publish lifecycle events (transient/in-process).
type EventPublisher interface {
	PublishInboundMessage(uid int64, tenantID, pageID, mailboxID, threadID, mid, senderPhone, text string, metaTs time.Time)
	PublishOutbound(uid int64, tenantID, threadID, mid, otid string, latencyMs int64, ok bool, errMsg string, sentAt time.Time)
	PublishSessionLifecycle(uid int64, tenantID string, prev, next hermesv1.MbsSessionState, reason string, connackRC int32, podID string)
}

// natsEventPublisher publishes via NATS JetStream using proto.Marshal.
// Mirrors internal/wa/session/events.go in spirit + structure.
type natsEventPublisher struct {
	js  jetStream
	log zerolog.Logger
	src string // "hermes-mbs"
}

// jetStream is the narrow surface we use from natsgo.JetStreamContext.
// Tests inject a fake; production injects the real JetStream context.
type jetStream interface {
	Publish(subj string, data []byte, opts ...natsgo.PubOpt) (*natsgo.PubAck, error)
}

// NewNatsEventPublisher constructs a publisher backed by a JetStream
// context. The caller owns the JS lifecycle (connect/close).
func NewNatsEventPublisher(js natsgo.JetStreamContext, log zerolog.Logger) EventPublisher {
	return &natsEventPublisher{
		js:  js,
		log: log,
		src: "hermes-mbs",
	}
}

// newNatsEventPublisherWithJS is the test-only constructor that takes
// our narrow jetStream interface directly.
func newNatsEventPublisherWithJS(js jetStream, log zerolog.Logger) *natsEventPublisher {
	return &natsEventPublisher{js: js, log: log, src: "hermes-mbs"}
}

func (p *natsEventPublisher) meta(tenantID string) *hermesv1.EventMeta {
	return &hermesv1.EventMeta{
		EventId:   uuid.New().String(),
		TenantId:  tenantID,
		Timestamp: timestamppb.Now(),
		Source:    p.src,
	}
}

// publish marshals + sends. Failures log only — callers don't need
// to know about NATS hiccups (the event is a side-channel notification).
// If publish reliability becomes critical, the right fix is an
// outbox table on the DB write, not a synchronous error return here.
func (p *natsEventPublisher) publish(subject, eventID string, msg proto.Message) {
	data, err := proto.Marshal(msg)
	if err != nil {
		p.log.Error().Err(err).Str("subject", subject).Msg("mbs publisher: marshal failed")
		return
	}
	if _, err := p.js.Publish(subject, data, natsgo.MsgId(eventID)); err != nil {
		p.log.Error().Err(err).Str("subject", subject).Msg("mbs publisher: publish failed")
	}
}

func (p *natsEventPublisher) PublishInboundMessage(uid int64, tenantID, pageID, mailboxID, threadID, mid, senderPhone, text string, metaTs time.Time) {
	if tenantID == "" {
		p.log.Warn().Int64("uid", uid).Str("mid", mid).Msg("mbs publisher: skip inbound — missing tenant_id")
		return
	}
	meta := p.meta(tenantID)
	event := &hermesv1.MbsInboundMessageEvent{
		Meta:          meta,
		Uid:           uid,
		PageId:        pageID,
		WecMailboxId:  mailboxID,
		ThreadId:      threadID,
		Mid:           mid,
		SenderPhone:   senderPhone,
		Text:          text,
		MetaTimestamp: timestamppb.New(metaTs),
	}
	p.publish(fmt.Sprintf("hermes.mbs.message.inbound.%s", tenantID), meta.EventId, event)
}

func (p *natsEventPublisher) PublishOutbound(uid int64, tenantID, threadID, mid, otid string, latencyMs int64, ok bool, errMsg string, sentAt time.Time) {
	if tenantID == "" {
		p.log.Warn().Int64("uid", uid).Str("mid", mid).Msg("mbs publisher: skip outbound — missing tenant_id")
		return
	}
	meta := p.meta(tenantID)
	event := &hermesv1.MbsOutboundEvent{
		Meta:      meta,
		Uid:       uid,
		ThreadId:  threadID,
		Mid:       mid,
		Otid:      otid,
		LatencyMs: latencyMs,
		Ok:        ok,
		Error:     errMsg,
		SentAt:    timestamppb.New(sentAt),
	}
	p.publish(fmt.Sprintf("hermes.mbs.message.outbound.%s", tenantID), meta.EventId, event)
}

func (p *natsEventPublisher) PublishSessionLifecycle(uid int64, tenantID string, prev, next hermesv1.MbsSessionState, reason string, connackRC int32, podID string) {
	if tenantID == "" {
		p.log.Warn().Int64("uid", uid).Msg("mbs publisher: skip lifecycle — missing tenant_id")
		return
	}

	// Derive the {state} fragment. Caller can pass reason as one of
	// the explicit subject fragments ("created"|"connected"|
	// "disconnected"|"burned"|"refreshed") to override the default
	// new_state-derived fragment. This matches the EVENTS.md schema.
	fragment := lifecycleSubjectFragment(reason, next)
	if fragment == "" {
		// Transient transition (UNSPECIFIED or BRIDGING) — no publish.
		return
	}

	meta := p.meta(tenantID)
	event := &hermesv1.MbsSessionLifecycleEvent{
		Meta:          meta,
		Uid:           uid,
		PreviousState: prev,
		NewState:      next,
		Reason:        reason,
		LastConnackRc: connackRC,
		PodId:         podID,
	}
	p.publish(fmt.Sprintf("hermes.mbs.session.%s.%s", fragment, tenantID), meta.EventId, event)
}

// lifecycleSubjectFragment picks the subject token for a lifecycle
// publish. Caller's reason wins if it's one of the canonical fragments
// (this lets refresh-ticker emit "refreshed" even when new_state is
// still ACTIVE). Otherwise fall back to mapping the new state.
func lifecycleSubjectFragment(reason string, next hermesv1.MbsSessionState) string {
	switch reason {
	case "created", "connected", "disconnected", "burned", "refreshed":
		return reason
	}
	return stateToSubjectFragment(next)
}

// ──────────────────────────────────────────────────────────────────
// nopPublisher — no-op publisher for tests that don't care about NATS
// ──────────────────────────────────────────────────────────────────

// NopPublisher is a sink-hole EventPublisher. Useful for handler
// constructor tests where Publisher is required but the test doesn't
// assert on publishes. Production code never uses this — every cmd/mbs
// wiring goes through NewNatsEventPublisher.
type NopPublisher struct{}

func (NopPublisher) PublishInboundMessage(int64, string, string, string, string, string, string, string, time.Time) {
}
func (NopPublisher) PublishOutbound(int64, string, string, string, string, int64, bool, string, time.Time) {
}
func (NopPublisher) PublishSessionLifecycle(int64, string, hermesv1.MbsSessionState, hermesv1.MbsSessionState, string, int32, string) {
}
