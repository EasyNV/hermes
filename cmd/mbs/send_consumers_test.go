package main

import (
	"strings"
	"testing"

	natsgo "github.com/nats-io/nats.go"
	"github.com/rs/zerolog"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
)

// ───────────────────────── tenantFromSubject ─────────────────────────

func TestTenantFromSubject_Happy(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"hermes.mbs.send.campaign.acme-123", "acme-123"},
		{"hermes.mbs.send.manual.tenant_with_underscore", "tenant_with_underscore"},
		{"hermes.mbs.send.campaign.UUID-abc-123-def", "UUID-abc-123-def"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := tenantFromSubject(c.in)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if got != c.want {
				t.Errorf("got %q want %q", got, c.want)
			}
		})
	}
}

func TestTenantFromSubject_Errors(t *testing.T) {
	cases := []struct {
		in   string
		hint string // substring expected in the err message
	}{
		{"", "missing prefix"},
		{"hermes.wa.send.campaign.tenant", "missing prefix"},
		{"hermes.mbs.send.campaign", "missing kind or tenant"},                  // no tenant token
		{"hermes.mbs.send.", "missing kind or tenant"},                          // no kind, no tenant
		{"hermes.mbs.send.campaign.", "missing kind or tenant"},                 // empty tenant after dot
		{"hermes.mbs.send..tenant", "missing kind or tenant"},                   // empty kind
		{"hermes.mbs.send.campaign.tenant.with.dots", "tenant token contains"},  // dots in tenant
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			_, err := tenantFromSubject(c.in)
			if err == nil {
				t.Fatalf("expected error for %q", c.in)
			}
			if !strings.Contains(err.Error(), c.hint) {
				t.Errorf("err %q should contain %q", err.Error(), c.hint)
			}
		})
	}
}

// ───────────────────── buildSendRequestFromTask ─────────────────────

func TestBuildSendRequestFromTask_ThreadIDPath(t *testing.T) {
	task := &hermesv1.MbsCampaignSendTask{
		CampaignId:     "camp-1",
		ContactId:      "contact-1",
		Uid:            12345,
		ThreadId:       "98765",
		ResolvedBody:   "Hello!",
		IdempotencyKey: "camp-1:contact-1",
	}
	req, err := buildSendRequestFromTask(task)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if req.Uid != 12345 {
		t.Errorf("Uid: got %d want 12345", req.Uid)
	}
	if req.Text != "Hello!" {
		t.Errorf("Text: got %q want Hello!", req.Text)
	}
	if string(req.ClientDedupeId) != "camp-1:contact-1" {
		t.Errorf("ClientDedupeId: got %q want camp-1:contact-1", string(req.ClientDedupeId))
	}
	tid, ok := req.Recipient.(*hermesv1.MbsSendMessageRequest_ThreadId)
	if !ok {
		t.Fatalf("Recipient: got %T want *MbsSendMessageRequest_ThreadId", req.Recipient)
	}
	if tid.ThreadId != "98765" {
		t.Errorf("ThreadId: got %q want 98765", tid.ThreadId)
	}
}

func TestBuildSendRequestFromTask_PhonePath(t *testing.T) {
	task := &hermesv1.MbsCampaignSendTask{
		Uid:            12345,
		RecipientPhone: "6281234567890",
		ResolvedBody:   "Hi",
	}
	req, err := buildSendRequestFromTask(task)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	phone, ok := req.Recipient.(*hermesv1.MbsSendMessageRequest_Phone)
	if !ok {
		t.Fatalf("Recipient: got %T want *MbsSendMessageRequest_Phone", req.Recipient)
	}
	if phone.Phone != "6281234567890" {
		t.Errorf("Phone: got %q want 6281234567890", phone.Phone)
	}
	if req.ClientDedupeId != nil {
		t.Errorf("ClientDedupeId should be nil when IdempotencyKey empty, got %v", req.ClientDedupeId)
	}
}

func TestBuildSendRequestFromTask_PageOverride(t *testing.T) {
	task := &hermesv1.MbsCampaignSendTask{
		Uid:            12345,
		ThreadId:       "98765",
		ResolvedBody:   "Hi",
		PageIdOverride: "page-xyz",
	}
	req, err := buildSendRequestFromTask(task)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if req.PageIdOverride != "page-xyz" {
		t.Errorf("PageIdOverride: got %q want page-xyz", req.PageIdOverride)
	}
}

func TestBuildSendRequestFromTask_Errors(t *testing.T) {
	cases := []struct {
		name string
		task *hermesv1.MbsCampaignSendTask
		hint string
	}{
		{
			name: "missing uid",
			task: &hermesv1.MbsCampaignSendTask{ResolvedBody: "x", ThreadId: "1"},
			hint: "uid is required",
		},
		{
			name: "empty body",
			task: &hermesv1.MbsCampaignSendTask{Uid: 1, ThreadId: "1"},
			hint: "resolved_body is empty",
		},
		{
			name: "no recipient",
			task: &hermesv1.MbsCampaignSendTask{Uid: 1, ResolvedBody: "x"},
			hint: "neither thread_id nor recipient_phone",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := buildSendRequestFromTask(c.task)
			if err == nil {
				t.Fatalf("expected error for %s", c.name)
			}
			if !strings.Contains(err.Error(), c.hint) {
				t.Errorf("err %q should contain %q", err.Error(), c.hint)
			}
		})
	}
}

// ────────────────── consumer wiring (start* funcs) ──────────────────

// subscribeRecorder records Subscribe calls so tests can assert the
// chunk-6 contract: campaign/manual consumers register with the
// correct subject + durable + Ack/MaxDeliver options.
type subscribeRecorder struct {
	calls []subscribeCall
}

type subscribeCall struct {
	subject string
	opts    []natsgo.SubOpt
}

func (s *subscribeRecorder) Subscribe(subject string, _ natsgo.MsgHandler, opts ...natsgo.SubOpt) (*natsgo.Subscription, error) {
	s.calls = append(s.calls, subscribeCall{subject: subject, opts: opts})
	// We return (nil, nil). The chunk-6 start* funcs never deref the
	// returned *Subscription (durable + subject are derived from
	// constants for the log line), so this is safe.
	return nil, nil
}

func TestStartCampaignConsumer_RegistersExpectedSubjectAndOpts(t *testing.T) {
	rec := &subscribeRecorder{}
	if err := startCampaignConsumer(rec, nil, zerolog.Nop()); err != nil {
		t.Fatalf("startCampaignConsumer: %v", err)
	}

	if len(rec.calls) != 1 {
		t.Fatalf("expected 1 Subscribe call, got %d", len(rec.calls))
	}
	got := rec.calls[0]
	if got.subject != "hermes.mbs.send.campaign.*" {
		t.Errorf("subject: got %q want hermes.mbs.send.campaign.*", got.subject)
	}
	if len(got.opts) < 4 {
		t.Errorf("opts: got %d, want at least 4 (Durable, ManualAck, AckWait, MaxDeliver)", len(got.opts))
	}
}

func TestStartManualConsumer_RegistersExpectedSubjectAndOpts(t *testing.T) {
	rec := &subscribeRecorder{}
	if err := startManualConsumer(rec, nil, zerolog.Nop()); err != nil {
		t.Fatalf("startManualConsumer: %v", err)
	}

	if len(rec.calls) != 1 {
		t.Fatalf("expected 1 Subscribe call, got %d", len(rec.calls))
	}
	got := rec.calls[0]
	if got.subject != "hermes.mbs.send.manual.*" {
		t.Errorf("subject: got %q want hermes.mbs.send.manual.*", got.subject)
	}
}
