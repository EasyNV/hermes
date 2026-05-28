package handler

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	natsgo "github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
)

// recordingJS is a fake jetStream that records every Publish call.
type recordingJS struct {
	mu       sync.Mutex
	calls    []recordedPublish
	failNext bool
}

type recordedPublish struct {
	subject string
	data    []byte
	opts    []natsgo.PubOpt
}

func (r *recordingJS) Publish(subj string, data []byte, opts ...natsgo.PubOpt) (*natsgo.PubAck, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failNext {
		r.failNext = false
		return nil, errors.New("simulated publish failure")
	}
	r.calls = append(r.calls, recordedPublish{subject: subj, data: data, opts: opts})
	return &natsgo.PubAck{}, nil
}

func (r *recordingJS) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *recordingJS) last() recordedPublish {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) == 0 {
		return recordedPublish{}
	}
	return r.calls[len(r.calls)-1]
}

func newRecordingPublisher(t *testing.T) (*natsEventPublisher, *recordingJS) {
	t.Helper()
	js := &recordingJS{}
	return newNatsEventPublisherWithJS(js, zerolog.Nop()), js
}

func TestEvents_InboundSubject(t *testing.T) {
	p, js := newRecordingPublisher(t)
	p.PublishInboundMessage(100, "tenant-A", "page-1", "mbox-1", "thread-1", "mid.$x", "62812", "hello", time.Now())

	if js.count() != 1 {
		t.Fatalf("expected 1 publish, got %d", js.count())
	}
	if js.last().subject != "hermes.mbs.message.inbound.tenant-A" {
		t.Errorf("subject: got %q", js.last().subject)
	}
	if len(js.last().opts) == 0 {
		t.Error("expected MsgId opt for dedupe at NATS layer")
	}
}

func TestEvents_OutboundSubject(t *testing.T) {
	p, js := newRecordingPublisher(t)
	p.PublishOutbound(100, "tenant-B", "thread-1", "mid.$y", "otid-1", 42, true, "", time.Now())

	if js.last().subject != "hermes.mbs.message.outbound.tenant-B" {
		t.Errorf("subject: got %q", js.last().subject)
	}
}

func TestEvents_LifecycleStateMap(t *testing.T) {
	cases := []struct {
		next   hermesv1.MbsSessionState
		reason string
		want   string // subject fragment (empty = no publish expected)
	}{
		// Default mapping by new_state
		{hermesv1.MbsSessionState_MBS_SESSION_STATE_ACTIVE, "", "connected"},
		{hermesv1.MbsSessionState_MBS_SESSION_STATE_SUSPENDED, "", "disconnected"},
		{hermesv1.MbsSessionState_MBS_SESSION_STATE_BURNED, "", "burned"},
		// reason override
		{hermesv1.MbsSessionState_MBS_SESSION_STATE_ACTIVE, "created", "created"},
		{hermesv1.MbsSessionState_MBS_SESSION_STATE_ACTIVE, "refreshed", "refreshed"},
		// BRIDGING + UNSPECIFIED = no publish
		{hermesv1.MbsSessionState_MBS_SESSION_STATE_BRIDGING, "", ""},
		{hermesv1.MbsSessionState_MBS_SESSION_STATE_UNSPECIFIED, "", ""},
	}
	for _, c := range cases {
		p, js := newRecordingPublisher(t)
		p.PublishSessionLifecycle(100, "tenant-A",
			hermesv1.MbsSessionState_MBS_SESSION_STATE_UNSPECIFIED, c.next, c.reason, 0, "pod-1")
		switch {
		case c.want == "":
			if js.count() != 0 {
				t.Errorf("next=%v reason=%q: expected no publish, got %d", c.next, c.reason, js.count())
			}
		default:
			if js.count() != 1 {
				t.Errorf("next=%v reason=%q: expected 1 publish, got %d", c.next, c.reason, js.count())
				continue
			}
			want := "hermes.mbs.session." + c.want + ".tenant-A"
			if js.last().subject != want {
				t.Errorf("subject: got %q want %q", js.last().subject, want)
			}
		}
	}
}

func TestEvents_EmptyTenantSkipped(t *testing.T) {
	p, js := newRecordingPublisher(t)
	p.PublishInboundMessage(100, "", "p", "m", "t", "mid", "62", "hi", time.Now())
	p.PublishOutbound(100, "", "t", "mid", "otid", 1, true, "", time.Now())
	p.PublishSessionLifecycle(100, "",
		hermesv1.MbsSessionState_MBS_SESSION_STATE_UNSPECIFIED,
		hermesv1.MbsSessionState_MBS_SESSION_STATE_ACTIVE,
		"connected", 0, "p")
	if js.count() != 0 {
		t.Errorf("empty tenant should produce 0 publishes, got %d", js.count())
	}
}

func TestEvents_PublishFailureLoggedNotPanic(t *testing.T) {
	p, js := newRecordingPublisher(t)
	js.failNext = true

	// Should not panic.
	p.PublishInboundMessage(100, "tenant-A", "p", "m", "t", "mid", "62", "hi", time.Now())

	// The failNext call didn't append; subsequent calls work.
	p.PublishInboundMessage(100, "tenant-A", "p", "m", "t", "mid2", "62", "hi2", time.Now())
	if js.count() != 1 {
		t.Errorf("expected 1 successful publish after recovery, got %d", js.count())
	}
}

func TestEvents_NopPublisherImplementsInterface(t *testing.T) {
	var p EventPublisher = NopPublisher{}
	// Just ensure call paths don't panic.
	p.PublishInboundMessage(0, "t", "", "", "", "", "", "", time.Time{})
	p.PublishOutbound(0, "t", "", "", "", 0, true, "", time.Time{})
	p.PublishSessionLifecycle(0, "t",
		hermesv1.MbsSessionState_MBS_SESSION_STATE_UNSPECIFIED,
		hermesv1.MbsSessionState_MBS_SESSION_STATE_ACTIVE, "", 0, "")
}

func TestEvents_SubjectIsValidNATS(t *testing.T) {
	// NATS subjects can't contain spaces or "." in tokens except as
	// delimiters. Validate the formed subjects don't fail trivially.
	p, js := newRecordingPublisher(t)
	p.PublishInboundMessage(100, "tenant-A", "p", "m", "t", "mid", "", "hi", time.Now())
	subj := js.last().subject
	if strings.Contains(subj, " ") {
		t.Errorf("subject contains space: %q", subj)
	}
	parts := strings.Split(subj, ".")
	if len(parts) < 4 {
		t.Errorf("subject malformed: %q", subj)
	}
}
