package dispatch_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rs/zerolog"

	"github.com/hermes-waba/hermes/internal/notify/dispatch"
)

func TestDispatchTelegram(t *testing.T) {
	tests := []struct {
		name       string
		webhookURL string
		status     int
		wantErr    bool
	}{
		{
			name:       "successful telegram dispatch",
			webhookURL: "123456:ABC-DEF|987654321",
			status:     200,
		},
		{
			name:       "telegram returns 400",
			webhookURL: "123456:ABC-DEF|987654321",
			status:     400,
			wantErr:    true,
		},
		{
			name:       "invalid webhook_url format",
			webhookURL: "no-pipe-here",
			wantErr:    true,
		},
		{
			name:       "empty bot token",
			webhookURL: "|987654321",
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Verify request format
				if r.Method != http.MethodPost {
					t.Errorf("expected POST, got %s", r.Method)
				}
				if ct := r.Header.Get("Content-Type"); ct != "application/json" {
					t.Errorf("expected Content-Type application/json, got %s", ct)
				}
				body, _ := io.ReadAll(r.Body)
				var payload map[string]string
				if err := json.Unmarshal(body, &payload); err != nil {
					t.Errorf("invalid JSON body: %v", err)
				}
				if payload["chat_id"] == "" {
					t.Error("expected non-empty chat_id")
				}
				if payload["parse_mode"] != "HTML" {
					t.Errorf("expected parse_mode HTML, got %s", payload["parse_mode"])
				}
				w.WriteHeader(tt.status)
			}))
			defer srv.Close()

			d := dispatch.New(nil, nil, zerolog.Nop())
			d.TelegramAPI = srv.URL

			target := dispatch.Target{Type: "webhook", WebhookURL: tt.webhookURL, WebhookType: "telegram"}
			result := d.Dispatch(context.Background(), target, "Test Title", "Test Body", "")

			if tt.wantErr && result.Err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && result.Err != nil {
				t.Fatalf("unexpected error: %v", result.Err)
			}
			if result.LatencyMs < 0 {
				t.Errorf("latency should be non-negative, got %d", result.LatencyMs)
			}
		})
	}
}

func TestDispatchDiscord(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		wantErr bool
	}{
		{name: "successful discord dispatch", status: 204},
		{name: "discord returns 429 rate limit", status: 429, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				var payload map[string]interface{}
				if err := json.Unmarshal(body, &payload); err != nil {
					t.Errorf("invalid JSON body: %v", err)
				}
				embeds, ok := payload["embeds"].([]interface{})
				if !ok || len(embeds) == 0 {
					t.Error("expected non-empty embeds array")
				}
				w.WriteHeader(tt.status)
			}))
			defer srv.Close()

			d := dispatch.New(nil, nil, zerolog.Nop())
			target := dispatch.Target{Type: "webhook", WebhookURL: srv.URL, WebhookType: "discord"}
			result := d.Dispatch(context.Background(), target, "Test Title", "Test Body", "")

			if tt.wantErr && result.Err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && result.Err != nil {
				t.Fatalf("unexpected error: %v", result.Err)
			}
			if !tt.wantErr && result.HTTPStatus != tt.status {
				t.Errorf("expected http_status=%d, got %d", tt.status, result.HTTPStatus)
			}
		})
	}
}

func TestDispatchCustom(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		wantErr bool
	}{
		{name: "successful custom dispatch", status: 200},
		{name: "custom returns 500", status: 500, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				var payload map[string]string
				if err := json.Unmarshal(body, &payload); err != nil {
					t.Errorf("invalid JSON body: %v", err)
				}
				if payload["title"] != "Test Title" {
					t.Errorf("expected title 'Test Title', got %s", payload["title"])
				}
				if payload["body"] != "Test Body" {
					t.Errorf("expected body 'Test Body', got %s", payload["body"])
				}
				w.WriteHeader(tt.status)
			}))
			defer srv.Close()

			d := dispatch.New(nil, nil, zerolog.Nop())
			target := dispatch.Target{Type: "webhook", WebhookURL: srv.URL, WebhookType: "custom"}
			result := d.Dispatch(context.Background(), target, "Test Title", "Test Body", "")

			if tt.wantErr && result.Err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && result.Err != nil {
				t.Fatalf("unexpected error: %v", result.Err)
			}
		})
	}
}

func TestDispatchBrowserPush(t *testing.T) {
	d := dispatch.New(nil, nil, zerolog.Nop())
	target := dispatch.Target{Type: "browser_push"}
	result := d.Dispatch(context.Background(), target, "Title", "Body", "tenant-1")

	if result.Err != nil {
		t.Fatalf("browser_push with nil NATS should succeed, got error: %v", result.Err)
	}
}

func TestDispatchUnknownType(t *testing.T) {
	d := dispatch.New(nil, nil, zerolog.Nop())
	target := dispatch.Target{Type: "unknown"}
	result := d.Dispatch(context.Background(), target, "Title", "Body", "")

	if result.Err == nil {
		t.Fatal("expected error for unknown type")
	}
}
