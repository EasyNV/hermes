package sender

import (
	"context"
	"time"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
)

// WaClient abstracts the whatsmeow client methods needed for sending.
// The real implementation wraps *whatsmeow.Client and handles JID parsing,
// message proto construction, and media upload internally.
type WaClient interface {
	SendMsg(ctx context.Context, recipientJID string, contentType hermesv1.ContentType, body, mediaURL, filename, caption string) (messageID string, sentAt time.Time, err error)
	SendPresence(recipientJID string, composing bool) error
}

// Sender handles message sending with typing indicator support.
type Sender interface {
	SendMessage(ctx context.Context, client WaClient, recipientJID string, contentType hermesv1.ContentType, body, mediaURL, filename, caption string) (messageID string, sentAt time.Time, err error)
	SendTypingIndicator(ctx context.Context, client WaClient, recipientJID string, durationMs int32) error
}

type realSender struct {
	sleepFn func(time.Duration)
}

// New creates a Sender with real time.Sleep for typing delays.
func New() Sender {
	return &realSender{sleepFn: time.Sleep}
}

// NewWithSleep creates a Sender with an injectable sleep function (for testing).
func NewWithSleep(fn func(time.Duration)) Sender {
	return &realSender{sleepFn: fn}
}

func (s *realSender) SendMessage(ctx context.Context, client WaClient, recipientJID string, contentType hermesv1.ContentType, body, mediaURL, filename, caption string) (string, time.Time, error) {
	return client.SendMsg(ctx, recipientJID, contentType, body, mediaURL, filename, caption)
}

func (s *realSender) SendTypingIndicator(_ context.Context, client WaClient, recipientJID string, durationMs int32) error {
	if durationMs <= 0 {
		durationMs = 3000
	}
	if err := client.SendPresence(recipientJID, true); err != nil {
		return err
	}
	s.sleepFn(time.Duration(durationMs) * time.Millisecond)
	return client.SendPresence(recipientJID, false)
}
