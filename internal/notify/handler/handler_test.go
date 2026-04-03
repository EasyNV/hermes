package handler_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/hermes-waba/hermes/internal/notify/dispatch"
	"github.com/hermes-waba/hermes/internal/notify/handler"
)

// ---------------------------------------------------------------------------
// Mock Store
// ---------------------------------------------------------------------------

type mockStore struct {
	configs map[string]handler.ConfigRow
	byKey   map[string]string // "workspace_id:type:webhook_type" → ID
	nextID  int
}

func newMockStore() *mockStore {
	return &mockStore{
		configs: make(map[string]handler.ConfigRow),
		byKey:   make(map[string]string),
	}
}

func (m *mockStore) UpsertConfig(_ context.Context, workspaceID, typ, webhookURL, webhookType string, enabled bool) (handler.ConfigRow, bool, error) {
	key := workspaceID + ":" + typ + ":" + webhookType
	if id, ok := m.byKey[key]; ok {
		row := m.configs[id]
		row.WebhookURL = webhookURL
		row.Enabled = enabled
		m.configs[id] = row
		return row, true, nil
	}
	m.nextID++
	id := fmt.Sprintf("cfg-%d", m.nextID)
	row := handler.ConfigRow{
		ID:          id,
		WorkspaceID: workspaceID,
		Type:        typ,
		WebhookURL:  webhookURL,
		WebhookType: webhookType,
		Enabled:     enabled,
		CreatedAt:   time.Now(),
	}
	m.configs[id] = row
	m.byKey[key] = id
	return row, false, nil
}

func (m *mockStore) GetConfig(_ context.Context, id string) (handler.ConfigRow, error) {
	row, ok := m.configs[id]
	if !ok {
		return handler.ConfigRow{}, handler.ErrNotFound
	}
	return row, nil
}

func (m *mockStore) ListConfigs(_ context.Context, workspaceID string) ([]handler.ConfigRow, error) {
	var rows []handler.ConfigRow
	for _, row := range m.configs {
		if row.WorkspaceID == workspaceID {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

func (m *mockStore) UpdateConfig(_ context.Context, id, typ, webhookURL, webhookType string, enabled bool) (handler.ConfigRow, error) {
	row, ok := m.configs[id]
	if !ok {
		return handler.ConfigRow{}, handler.ErrNotFound
	}
	if typ != "" {
		row.Type = typ
	}
	if webhookURL != "" {
		row.WebhookURL = webhookURL
	}
	if webhookType != "" {
		row.WebhookType = webhookType
	}
	row.Enabled = enabled
	m.configs[id] = row
	return row, nil
}

func (m *mockStore) DeleteConfig(_ context.Context, id string) error {
	if _, ok := m.configs[id]; !ok {
		return handler.ErrNotFound
	}
	// Also remove from byKey
	row := m.configs[id]
	key := row.WorkspaceID + ":" + row.Type + ":" + row.WebhookType
	delete(m.byKey, key)
	delete(m.configs, id)
	return nil
}

func (m *mockStore) ListEnabledConfigs(_ context.Context, workspaceID string) ([]handler.ConfigRow, error) {
	var rows []handler.ConfigRow
	for _, row := range m.configs {
		if row.WorkspaceID == workspaceID && row.Enabled {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

// ---------------------------------------------------------------------------
// Mock Notifier
// ---------------------------------------------------------------------------

type mockNotifier struct {
	result dispatch.Result
}

func (m *mockNotifier) Dispatch(_ context.Context, _ dispatch.Target, _, _, _ string) dispatch.Result {
	return m.result
}

// ---------------------------------------------------------------------------
// ConfigureNotification Tests
// ---------------------------------------------------------------------------

func TestConfigureNotification(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(*mockStore)
		req     *hermesv1.NotifyConfigureRequest
		wantErr codes.Code
		check   func(t *testing.T, resp *hermesv1.NotifyConfigureResponse)
	}{
		{
			name: "missing workspace_id",
			req: &hermesv1.NotifyConfigureRequest{
				Type: hermesv1.NotificationType_NOTIFICATION_TYPE_WEBHOOK,
			},
			wantErr: codes.InvalidArgument,
		},
		{
			name: "missing type",
			req: &hermesv1.NotifyConfigureRequest{
				WorkspaceId: "ws-1",
			},
			wantErr: codes.InvalidArgument,
		},
		{
			name: "webhook without url",
			req: &hermesv1.NotifyConfigureRequest{
				WorkspaceId: "ws-1",
				Type:        hermesv1.NotificationType_NOTIFICATION_TYPE_WEBHOOK,
				WebhookType: hermesv1.WebhookType_WEBHOOK_TYPE_TELEGRAM,
			},
			wantErr: codes.InvalidArgument,
		},
		{
			name: "webhook without webhook_type",
			req: &hermesv1.NotifyConfigureRequest{
				WorkspaceId: "ws-1",
				Type:        hermesv1.NotificationType_NOTIFICATION_TYPE_WEBHOOK,
				WebhookUrl:  "token|chat",
			},
			wantErr: codes.InvalidArgument,
		},
		{
			name: "create browser_push config",
			req: &hermesv1.NotifyConfigureRequest{
				WorkspaceId: "ws-1",
				Type:        hermesv1.NotificationType_NOTIFICATION_TYPE_BROWSER_PUSH,
				Enabled:     true,
			},
			check: func(t *testing.T, resp *hermesv1.NotifyConfigureResponse) {
				if resp.Updated {
					t.Error("expected Updated=false for new config")
				}
				if resp.Config.Type != hermesv1.NotificationType_NOTIFICATION_TYPE_BROWSER_PUSH {
					t.Errorf("expected type BROWSER_PUSH, got %v", resp.Config.Type)
				}
				if !resp.Config.Enabled {
					t.Error("expected enabled=true")
				}
			},
		},
		{
			name: "create webhook/telegram config",
			req: &hermesv1.NotifyConfigureRequest{
				WorkspaceId: "ws-1",
				Type:        hermesv1.NotificationType_NOTIFICATION_TYPE_WEBHOOK,
				WebhookUrl:  "bottoken|12345",
				WebhookType: hermesv1.WebhookType_WEBHOOK_TYPE_TELEGRAM,
				Enabled:     true,
			},
			check: func(t *testing.T, resp *hermesv1.NotifyConfigureResponse) {
				if resp.Updated {
					t.Error("expected Updated=false for new config")
				}
				if resp.Config.WebhookUrl != "bottoken|12345" {
					t.Errorf("expected webhook_url bottoken|12345, got %s", resp.Config.WebhookUrl)
				}
			},
		},
		{
			name: "upsert updates existing config",
			setup: func(s *mockStore) {
				s.UpsertConfig(context.Background(), "ws-1", "webhook", "old-token|12345", "telegram", true)
			},
			req: &hermesv1.NotifyConfigureRequest{
				WorkspaceId: "ws-1",
				Type:        hermesv1.NotificationType_NOTIFICATION_TYPE_WEBHOOK,
				WebhookUrl:  "new-token|12345",
				WebhookType: hermesv1.WebhookType_WEBHOOK_TYPE_TELEGRAM,
				Enabled:     false,
			},
			check: func(t *testing.T, resp *hermesv1.NotifyConfigureResponse) {
				if !resp.Updated {
					t.Error("expected Updated=true for upsert")
				}
				if resp.Config.WebhookUrl != "new-token|12345" {
					t.Errorf("expected updated webhook_url, got %s", resp.Config.WebhookUrl)
				}
				if resp.Config.Enabled {
					t.Error("expected enabled=false after upsert")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newMockStore()
			if tt.setup != nil {
				tt.setup(store)
			}
			h := handler.New(store, nil, zerolog.Nop())

			resp, err := h.ConfigureNotification(context.Background(), tt.req)

			if tt.wantErr != 0 {
				assertGRPCCode(t, err, tt.wantErr)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if resp.Config == nil {
				t.Fatal("response Config is nil")
			}
			if tt.check != nil {
				tt.check(t, resp)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// GetNotificationConfig Tests
// ---------------------------------------------------------------------------

func TestGetNotificationConfig(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(*mockStore)
		req     *hermesv1.NotifyGetConfigRequest
		wantErr codes.Code
	}{
		{
			name:    "missing id",
			req:     &hermesv1.NotifyGetConfigRequest{},
			wantErr: codes.InvalidArgument,
		},
		{
			name:    "not found",
			req:     &hermesv1.NotifyGetConfigRequest{Id: "nonexistent"},
			wantErr: codes.NotFound,
		},
		{
			name: "found",
			setup: func(s *mockStore) {
				s.UpsertConfig(context.Background(), "ws-1", "sound", "", "", true)
			},
			req: &hermesv1.NotifyGetConfigRequest{Id: "cfg-1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newMockStore()
			if tt.setup != nil {
				tt.setup(store)
			}
			h := handler.New(store, nil, zerolog.Nop())

			resp, err := h.GetNotificationConfig(context.Background(), tt.req)

			if tt.wantErr != 0 {
				assertGRPCCode(t, err, tt.wantErr)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if resp.Config == nil {
				t.Fatal("response Config is nil")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ListNotificationConfigs Tests
// ---------------------------------------------------------------------------

func TestListNotificationConfigs(t *testing.T) {
	store := newMockStore()
	store.UpsertConfig(context.Background(), "ws-1", "sound", "", "", true)
	store.UpsertConfig(context.Background(), "ws-1", "webhook", "url", "discord", true)
	store.UpsertConfig(context.Background(), "ws-2", "sound", "", "", true) // different workspace

	h := handler.New(store, nil, zerolog.Nop())

	t.Run("missing workspace_id", func(t *testing.T) {
		_, err := h.ListNotificationConfigs(context.Background(), &hermesv1.NotifyListConfigsRequest{})
		assertGRPCCode(t, err, codes.InvalidArgument)
	})

	t.Run("returns configs for workspace", func(t *testing.T) {
		resp, err := h.ListNotificationConfigs(context.Background(), &hermesv1.NotifyListConfigsRequest{WorkspaceId: "ws-1"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(resp.Configs) != 2 {
			t.Errorf("expected 2 configs, got %d", len(resp.Configs))
		}
	})
}

// ---------------------------------------------------------------------------
// UpdateNotificationConfig Tests
// ---------------------------------------------------------------------------

func TestUpdateNotificationConfig(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(*mockStore)
		req     *hermesv1.NotifyUpdateConfigRequest
		wantErr codes.Code
		check   func(t *testing.T, resp *hermesv1.NotifyUpdateConfigResponse)
	}{
		{
			name:    "missing id",
			req:     &hermesv1.NotifyUpdateConfigRequest{},
			wantErr: codes.InvalidArgument,
		},
		{
			name:    "not found",
			req:     &hermesv1.NotifyUpdateConfigRequest{Id: "nonexistent"},
			wantErr: codes.NotFound,
		},
		{
			name: "update enabled only",
			setup: func(s *mockStore) {
				s.UpsertConfig(context.Background(), "ws-1", "webhook", "url", "discord", true)
			},
			req: &hermesv1.NotifyUpdateConfigRequest{
				Id:      "cfg-1",
				Enabled: false,
			},
			check: func(t *testing.T, resp *hermesv1.NotifyUpdateConfigResponse) {
				if resp.Config.Enabled {
					t.Error("expected enabled=false")
				}
				if resp.Config.WebhookUrl != "url" {
					t.Error("webhook_url should be preserved")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newMockStore()
			if tt.setup != nil {
				tt.setup(store)
			}
			h := handler.New(store, nil, zerolog.Nop())

			resp, err := h.UpdateNotificationConfig(context.Background(), tt.req)

			if tt.wantErr != 0 {
				assertGRPCCode(t, err, tt.wantErr)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.check != nil {
				tt.check(t, resp)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// DeleteNotificationConfig Tests
// ---------------------------------------------------------------------------

func TestDeleteNotificationConfig(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(*mockStore)
		req     *hermesv1.NotifyDeleteConfigRequest
		wantErr codes.Code
	}{
		{
			name:    "missing id",
			req:     &hermesv1.NotifyDeleteConfigRequest{},
			wantErr: codes.InvalidArgument,
		},
		{
			name:    "not found",
			req:     &hermesv1.NotifyDeleteConfigRequest{Id: "nonexistent"},
			wantErr: codes.NotFound,
		},
		{
			name: "success",
			setup: func(s *mockStore) {
				s.UpsertConfig(context.Background(), "ws-1", "sound", "", "", true)
			},
			req: &hermesv1.NotifyDeleteConfigRequest{Id: "cfg-1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newMockStore()
			if tt.setup != nil {
				tt.setup(store)
			}
			h := handler.New(store, nil, zerolog.Nop())

			_, err := h.DeleteNotificationConfig(context.Background(), tt.req)

			if tt.wantErr != 0 {
				assertGRPCCode(t, err, tt.wantErr)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestNotification Tests
// ---------------------------------------------------------------------------

func TestTestNotification(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(*mockStore)
		result  dispatch.Result
		req     *hermesv1.NotifyTestRequest
		wantErr codes.Code
		check   func(t *testing.T, resp *hermesv1.NotifyTestResponse)
	}{
		{
			name:    "missing config_id",
			req:     &hermesv1.NotifyTestRequest{},
			wantErr: codes.InvalidArgument,
		},
		{
			name:    "config not found",
			req:     &hermesv1.NotifyTestRequest{ConfigId: "nonexistent"},
			wantErr: codes.NotFound,
		},
		{
			name: "successful dispatch",
			setup: func(s *mockStore) {
				s.UpsertConfig(context.Background(), "ws-1", "webhook", "url", "custom", true)
			},
			result: dispatch.Result{HTTPStatus: 200, LatencyMs: 42},
			req:    &hermesv1.NotifyTestRequest{ConfigId: "cfg-1"},
			check: func(t *testing.T, resp *hermesv1.NotifyTestResponse) {
				if !resp.Success {
					t.Error("expected success=true")
				}
				if resp.HttpStatus != 200 {
					t.Errorf("expected http_status=200, got %d", resp.HttpStatus)
				}
				if resp.LatencyMs != 42 {
					t.Errorf("expected latency_ms=42, got %d", resp.LatencyMs)
				}
			},
		},
		{
			name: "failed dispatch",
			setup: func(s *mockStore) {
				s.UpsertConfig(context.Background(), "ws-1", "webhook", "url", "custom", true)
			},
			result: dispatch.Result{HTTPStatus: 500, Err: fmt.Errorf("webhook returned status 500"), LatencyMs: 100},
			req:    &hermesv1.NotifyTestRequest{ConfigId: "cfg-1"},
			check: func(t *testing.T, resp *hermesv1.NotifyTestResponse) {
				if resp.Success {
					t.Error("expected success=false")
				}
				if resp.Error == "" {
					t.Error("expected non-empty error")
				}
				if resp.HttpStatus != 500 {
					t.Errorf("expected http_status=500, got %d", resp.HttpStatus)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newMockStore()
			if tt.setup != nil {
				tt.setup(store)
			}
			notifier := &mockNotifier{result: tt.result}
			h := handler.New(store, notifier, zerolog.Nop())

			resp, err := h.TestNotification(context.Background(), tt.req)

			if tt.wantErr != 0 {
				assertGRPCCode(t, err, tt.wantErr)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.check != nil {
				tt.check(t, resp)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func assertGRPCCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error with code %v, got nil", want)
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %v", err)
	}
	if st.Code() != want {
		t.Errorf("expected code %v, got %v: %s", want, st.Code(), st.Message())
	}
}
