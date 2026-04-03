package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
)

// Target describes where to deliver a notification.
type Target struct {
	Type        string // "browser_push", "sound", "webhook"
	WebhookURL  string
	WebhookType string // "telegram", "discord", "custom"
}

// Result captures the outcome of a dispatch attempt.
type Result struct {
	HTTPStatus int
	LatencyMs  int32
	Err        error
}

// Dispatcher sends notifications to external targets (webhooks, WS via NATS).
type Dispatcher struct {
	httpClient *http.Client
	nc         *nats.Conn
	logger     zerolog.Logger

	// TelegramAPI overrides the Telegram base URL for testing.
	TelegramAPI string
}

// New creates a Dispatcher. Pass a non-nil httpClient to override the default
// (10s timeout) — useful for tests. Pass nil nc if NATS is unavailable.
func New(httpClient *http.Client, nc *nats.Conn, logger zerolog.Logger) *Dispatcher {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &Dispatcher{
		httpClient: httpClient,
		nc:         nc,
		logger:     logger,
	}
}

// Dispatch sends a notification to the given target. tenantID is used for
// browser_push/sound to route the WS event via NATS.
func (d *Dispatcher) Dispatch(ctx context.Context, target Target, title, body, tenantID string) Result {
	start := time.Now()

	switch target.Type {
	case "browser_push", "sound":
		return d.dispatchWS(target.Type, title, body, tenantID, start)
	case "webhook":
		switch target.WebhookType {
		case "telegram":
			return d.dispatchTelegram(ctx, target.WebhookURL, title, body, start)
		case "discord":
			return d.dispatchDiscord(ctx, target.WebhookURL, title, body, start)
		case "custom":
			return d.dispatchCustom(ctx, target.WebhookURL, title, body, start)
		default:
			return Result{Err: fmt.Errorf("unknown webhook type: %s", target.WebhookType), LatencyMs: ms(start)}
		}
	default:
		return Result{Err: fmt.Errorf("unknown notification type: %s", target.Type), LatencyMs: ms(start)}
	}
}

// dispatchWS publishes a WebSocket notification event to NATS for the gateway.
func (d *Dispatcher) dispatchWS(notifType, title, body, tenantID string, start time.Time) Result {
	if d.nc == nil {
		d.logger.Debug().Str("type", notifType).Msg("NATS not connected, skipping WS notification")
		return Result{LatencyMs: ms(start)}
	}

	payload, _ := json.Marshal(map[string]string{
		"type":              "notification",
		"notification_type": notifType,
		"title":             title,
		"body":              body,
	})

	subject := fmt.Sprintf("hermes.notify.ws.%s", tenantID)
	if err := d.nc.Publish(subject, payload); err != nil {
		return Result{Err: fmt.Errorf("publishing WS event: %w", err), LatencyMs: ms(start)}
	}
	return Result{LatencyMs: ms(start)}
}

// dispatchTelegram sends a message via the Telegram Bot API.
// webhook_url format: "BOT_TOKEN|CHAT_ID"
func (d *Dispatcher) dispatchTelegram(ctx context.Context, webhookURL, title, body string, start time.Time) Result {
	parts := strings.SplitN(webhookURL, "|", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return Result{Err: fmt.Errorf("invalid telegram webhook_url: expected BOT_TOKEN|CHAT_ID"), LatencyMs: ms(start)}
	}
	botToken, chatID := parts[0], parts[1]

	baseURL := d.telegramBaseURL()
	apiURL := fmt.Sprintf("%s/bot%s/sendMessage", baseURL, botToken)

	text := fmt.Sprintf("<b>%s</b>\n\n%s", title, body)
	reqBody := map[string]string{
		"chat_id":    chatID,
		"text":       text,
		"parse_mode": "HTML",
	}

	return d.doPost(ctx, apiURL, reqBody, start)
}

// dispatchDiscord sends an embed to a Discord webhook URL.
func (d *Dispatcher) dispatchDiscord(ctx context.Context, webhookURL, title, body string, start time.Time) Result {
	if webhookURL == "" {
		return Result{Err: fmt.Errorf("discord webhook_url is empty"), LatencyMs: ms(start)}
	}

	reqBody := map[string]interface{}{
		"embeds": []map[string]interface{}{
			{
				"title":       title,
				"description": body,
				"color":       3447003, // blue
			},
		},
	}

	return d.doPost(ctx, webhookURL, reqBody, start)
}

// dispatchCustom POSTs a JSON body to an arbitrary webhook URL.
func (d *Dispatcher) dispatchCustom(ctx context.Context, webhookURL, title, body string, start time.Time) Result {
	if webhookURL == "" {
		return Result{Err: fmt.Errorf("custom webhook_url is empty"), LatencyMs: ms(start)}
	}

	reqBody := map[string]string{
		"title": title,
		"body":  body,
	}

	return d.doPost(ctx, webhookURL, reqBody, start)
}

// doPost marshals payload as JSON and POSTs it to the given URL.
func (d *Dispatcher) doPost(ctx context.Context, url string, payload interface{}, start time.Time) Result {
	jsonBody, err := json.Marshal(payload)
	if err != nil {
		return Result{Err: fmt.Errorf("marshaling request body: %w", err), LatencyMs: ms(start)}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jsonBody))
	if err != nil {
		return Result{Err: fmt.Errorf("creating request: %w", err), LatencyMs: ms(start)}
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return Result{Err: fmt.Errorf("sending request: %w", err), LatencyMs: ms(start)}
	}
	defer resp.Body.Close()

	latency := ms(start)
	if resp.StatusCode >= 400 {
		return Result{
			HTTPStatus: resp.StatusCode,
			Err:        fmt.Errorf("webhook returned status %d", resp.StatusCode),
			LatencyMs:  latency,
		}
	}

	return Result{HTTPStatus: resp.StatusCode, LatencyMs: latency}
}

func (d *Dispatcher) telegramBaseURL() string {
	if d.TelegramAPI != "" {
		return d.TelegramAPI
	}
	return "https://api.telegram.org"
}

func ms(start time.Time) int32 {
	return int32(time.Since(start).Milliseconds())
}
