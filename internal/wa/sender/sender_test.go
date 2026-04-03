package sender

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
)

// mockWaClient implements WaClient for testing.
type mockWaClient struct {
	sendMsgFn     func(ctx context.Context, recipientJID string, contentType hermesv1.ContentType, body, mediaURL, filename, caption string) (string, time.Time, error)
	sendPresenceFn func(recipientJID string, composing bool) error
}

func (m *mockWaClient) SendMsg(ctx context.Context, recipientJID string, contentType hermesv1.ContentType, body, mediaURL, filename, caption string) (string, time.Time, error) {
	if m.sendMsgFn != nil {
		return m.sendMsgFn(ctx, recipientJID, contentType, body, mediaURL, filename, caption)
	}
	return "msg-test", time.Now(), nil
}

func (m *mockWaClient) SendPresence(recipientJID string, composing bool) error {
	if m.sendPresenceFn != nil {
		return m.sendPresenceFn(recipientJID, composing)
	}
	return nil
}

// ---------------------------------------------------------------------------
// TestSendMessage
// ---------------------------------------------------------------------------

func TestSendMessage(t *testing.T) {
	tests := []struct {
		name        string
		contentType hermesv1.ContentType
		body        string
		client      *mockWaClient
		wantMsgID   string
		wantErr     bool
	}{
		{
			name:        "text message delegates to client",
			contentType: hermesv1.ContentType_CONTENT_TYPE_TEXT,
			body:        "Hello!",
			client: &mockWaClient{
				sendMsgFn: func(_ context.Context, _ string, ct hermesv1.ContentType, body, _, _, _ string) (string, time.Time, error) {
					if ct != hermesv1.ContentType_CONTENT_TYPE_TEXT {
						t.Errorf("expected TEXT content type, got %v", ct)
					}
					if body != "Hello!" {
						t.Errorf("expected body 'Hello!', got %q", body)
					}
					return "wa-msg-1", time.Now(), nil
				},
			},
			wantMsgID: "wa-msg-1",
		},
		{
			name:        "client error propagated",
			contentType: hermesv1.ContentType_CONTENT_TYPE_TEXT,
			body:        "Hi",
			client: &mockWaClient{
				sendMsgFn: func(_ context.Context, _ string, _ hermesv1.ContentType, _, _, _, _ string) (string, time.Time, error) {
					return "", time.Time{}, fmt.Errorf("connection lost")
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := New()
			msgID, _, err := s.SendMessage(context.Background(), tt.client, "628@s.whatsapp.net", tt.contentType, tt.body, "", "", "")
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if msgID != tt.wantMsgID {
				t.Errorf("message ID: got %q, want %q", msgID, tt.wantMsgID)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestSendTypingIndicator
// ---------------------------------------------------------------------------

func TestSendTypingIndicator(t *testing.T) {
	tests := []struct {
		name          string
		durationMs    int32
		client        *mockWaClient
		wantSleepMs   int64
		wantComposing int32
		wantPaused    int32
		wantErr       bool
	}{
		{
			name:          "sends composing then paused with specified duration",
			durationMs:    2000,
			client:        &mockWaClient{},
			wantSleepMs:   2000,
			wantComposing: 1,
			wantPaused:    1,
		},
		{
			name:          "defaults to 3000ms when duration is zero",
			durationMs:    0,
			client:        &mockWaClient{},
			wantSleepMs:   3000,
			wantComposing: 1,
			wantPaused:    1,
		},
		{
			name:       "composing error stops early",
			durationMs: 1000,
			client: &mockWaClient{
				sendPresenceFn: func(_ string, composing bool) error {
					if composing {
						return fmt.Errorf("presence error")
					}
					return nil
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var sleepDuration atomic.Int64
			var composingCount, pausedCount atomic.Int32

			tt.client.sendPresenceFn = func(originalFn func(string, bool) error) func(string, bool) error {
				return func(jid string, composing bool) error {
					if originalFn != nil {
						if err := originalFn(jid, composing); err != nil {
							return err
						}
					}
					if composing {
						composingCount.Add(1)
					} else {
						pausedCount.Add(1)
					}
					return nil
				}
			}(tt.client.sendPresenceFn)

			s := NewWithSleep(func(d time.Duration) {
				sleepDuration.Store(d.Milliseconds())
			})

			err := s.SendTypingIndicator(context.Background(), tt.client, "628@s.whatsapp.net", tt.durationMs)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if got := sleepDuration.Load(); got != tt.wantSleepMs {
				t.Errorf("sleep duration: got %dms, want %dms", got, tt.wantSleepMs)
			}
			if got := composingCount.Load(); got != tt.wantComposing {
				t.Errorf("composing calls: got %d, want %d", got, tt.wantComposing)
			}
			if got := pausedCount.Load(); got != tt.wantPaused {
				t.Errorf("paused calls: got %d, want %d", got, tt.wantPaused)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestTypingIndicatorSequence
// ---------------------------------------------------------------------------

func TestTypingIndicatorSequence(t *testing.T) {
	var calls []string
	client := &mockWaClient{
		sendPresenceFn: func(_ string, composing bool) error {
			if composing {
				calls = append(calls, "composing")
			} else {
				calls = append(calls, "paused")
			}
			return nil
		},
	}

	sleepCalled := false
	s := NewWithSleep(func(d time.Duration) {
		sleepCalled = true
		calls = append(calls, "sleep")
	})

	err := s.SendTypingIndicator(context.Background(), client, "628@s.whatsapp.net", 1500)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !sleepCalled {
		t.Error("expected sleep to be called")
	}

	// Verify correct sequence: composing → sleep → paused.
	expected := []string{"composing", "sleep", "paused"}
	if len(calls) != len(expected) {
		t.Fatalf("call count: got %d, want %d: %v", len(calls), len(expected), calls)
	}
	for i, want := range expected {
		if calls[i] != want {
			t.Errorf("call[%d]: got %q, want %q", i, calls[i], want)
		}
	}
}
