package handler

import (
	"context"
	"errors"

	"github.com/rs/zerolog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/hermes-waba/hermes/internal/notify/dispatch"
)

// Notifier abstracts notification dispatch for testability.
type Notifier interface {
	Dispatch(ctx context.Context, target dispatch.Target, title, body, tenantID string) dispatch.Result
}

// Handler implements the HermesNotifyServer gRPC interface.
type Handler struct {
	hermesv1.UnimplementedHermesNotifyServer
	store    Store
	notifier Notifier
	logger   zerolog.Logger
}

// New creates a Handler with the given dependencies.
func New(store Store, notifier Notifier, logger zerolog.Logger) *Handler {
	return &Handler{
		store:    store,
		notifier: notifier,
		logger:   logger,
	}
}

// ---------------------------------------------------------------------------
// ConfigureNotification — upsert a notification config.
// ---------------------------------------------------------------------------

func (h *Handler) ConfigureNotification(ctx context.Context, req *hermesv1.NotifyConfigureRequest) (*hermesv1.NotifyConfigureResponse, error) {
	if req.WorkspaceId == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id is required")
	}
	typStr := notificationTypeToStr(req.Type)
	if typStr == "" {
		return nil, status.Error(codes.InvalidArgument, "valid notification type is required")
	}
	whStr := webhookTypeToStr(req.WebhookType)
	if typStr == "webhook" {
		if req.WebhookUrl == "" {
			return nil, status.Error(codes.InvalidArgument, "webhook_url is required for webhook type")
		}
		if whStr == "" {
			return nil, status.Error(codes.InvalidArgument, "webhook_type is required for webhook type")
		}
	}

	row, wasUpdated, err := h.store.UpsertConfig(ctx, req.WorkspaceId, typStr, req.WebhookUrl, whStr, req.Enabled)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "upserting config: %v", err)
	}

	return &hermesv1.NotifyConfigureResponse{
		Config:  configRowToProto(row),
		Updated: wasUpdated,
	}, nil
}

// ---------------------------------------------------------------------------
// GetNotificationConfig
// ---------------------------------------------------------------------------

func (h *Handler) GetNotificationConfig(ctx context.Context, req *hermesv1.NotifyGetConfigRequest) (*hermesv1.NotifyGetConfigResponse, error) {
	if req.Id == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	row, err := h.store.GetConfig(ctx, req.Id)
	if err != nil {
		return nil, mapStoreErr(err, "getting config")
	}
	return &hermesv1.NotifyGetConfigResponse{Config: configRowToProto(row)}, nil
}

// ---------------------------------------------------------------------------
// ListNotificationConfigs
// ---------------------------------------------------------------------------

func (h *Handler) ListNotificationConfigs(ctx context.Context, req *hermesv1.NotifyListConfigsRequest) (*hermesv1.NotifyListConfigsResponse, error) {
	if req.WorkspaceId == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id is required")
	}
	rows, err := h.store.ListConfigs(ctx, req.WorkspaceId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "listing configs: %v", err)
	}
	configs := make([]*hermesv1.NotificationConfig, len(rows))
	for i, row := range rows {
		configs[i] = configRowToProto(row)
	}
	return &hermesv1.NotifyListConfigsResponse{Configs: configs}, nil
}

// ---------------------------------------------------------------------------
// UpdateNotificationConfig — partial update (zero-value fields are skipped).
// ---------------------------------------------------------------------------

func (h *Handler) UpdateNotificationConfig(ctx context.Context, req *hermesv1.NotifyUpdateConfigRequest) (*hermesv1.NotifyUpdateConfigResponse, error) {
	if req.Id == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	typStr := notificationTypeToStr(req.Type)   // "" if UNSPECIFIED → don't change
	whStr := webhookTypeToStr(req.WebhookType)  // "" if UNSPECIFIED → don't change

	row, err := h.store.UpdateConfig(ctx, req.Id, typStr, req.WebhookUrl, whStr, req.Enabled)
	if err != nil {
		return nil, mapStoreErr(err, "updating config")
	}
	return &hermesv1.NotifyUpdateConfigResponse{Config: configRowToProto(row)}, nil
}

// ---------------------------------------------------------------------------
// DeleteNotificationConfig
// ---------------------------------------------------------------------------

func (h *Handler) DeleteNotificationConfig(ctx context.Context, req *hermesv1.NotifyDeleteConfigRequest) (*hermesv1.NotifyDeleteConfigResponse, error) {
	if req.Id == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	if err := h.store.DeleteConfig(ctx, req.Id); err != nil {
		return nil, mapStoreErr(err, "deleting config")
	}
	return &hermesv1.NotifyDeleteConfigResponse{}, nil
}

// ---------------------------------------------------------------------------
// TestNotification — sends a test notification using an existing config.
// ---------------------------------------------------------------------------

func (h *Handler) TestNotification(ctx context.Context, req *hermesv1.NotifyTestRequest) (*hermesv1.NotifyTestResponse, error) {
	if req.ConfigId == "" {
		return nil, status.Error(codes.InvalidArgument, "config_id is required")
	}
	row, err := h.store.GetConfig(ctx, req.ConfigId)
	if err != nil {
		return nil, mapStoreErr(err, "getting config for test")
	}

	target := dispatch.Target{
		Type:        row.Type,
		WebhookURL:  row.WebhookURL,
		WebhookType: row.WebhookType,
	}
	result := h.notifier.Dispatch(ctx, target, "Test Notification", "This is a test notification from Hermès.", "")

	resp := &hermesv1.NotifyTestResponse{
		Success:    result.Err == nil,
		HttpStatus: int32(result.HTTPStatus),
		LatencyMs:  result.LatencyMs,
	}
	if result.Err != nil {
		resp.Error = result.Err.Error()
	}
	return resp, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func mapStoreErr(err error, action string) error {
	if errors.Is(err, ErrNotFound) {
		return status.Error(codes.NotFound, "notification config not found")
	}
	return status.Errorf(codes.Internal, "%s: %v", action, err)
}

func configRowToProto(row ConfigRow) *hermesv1.NotificationConfig {
	return &hermesv1.NotificationConfig{
		Id:          row.ID,
		WorkspaceId: row.WorkspaceID,
		Type:        strToNotificationType(row.Type),
		WebhookUrl:  row.WebhookURL,
		WebhookType: strToWebhookType(row.WebhookType),
		Enabled:     row.Enabled,
		CreatedAt:   timestamppb.New(row.CreatedAt),
	}
}

func notificationTypeToStr(t hermesv1.NotificationType) string {
	switch t {
	case hermesv1.NotificationType_NOTIFICATION_TYPE_BROWSER_PUSH:
		return "browser_push"
	case hermesv1.NotificationType_NOTIFICATION_TYPE_SOUND:
		return "sound"
	case hermesv1.NotificationType_NOTIFICATION_TYPE_WEBHOOK:
		return "webhook"
	default:
		return ""
	}
}

func strToNotificationType(s string) hermesv1.NotificationType {
	switch s {
	case "browser_push":
		return hermesv1.NotificationType_NOTIFICATION_TYPE_BROWSER_PUSH
	case "sound":
		return hermesv1.NotificationType_NOTIFICATION_TYPE_SOUND
	case "webhook":
		return hermesv1.NotificationType_NOTIFICATION_TYPE_WEBHOOK
	default:
		return hermesv1.NotificationType_NOTIFICATION_TYPE_UNSPECIFIED
	}
}

func webhookTypeToStr(t hermesv1.WebhookType) string {
	switch t {
	case hermesv1.WebhookType_WEBHOOK_TYPE_TELEGRAM:
		return "telegram"
	case hermesv1.WebhookType_WEBHOOK_TYPE_DISCORD:
		return "discord"
	case hermesv1.WebhookType_WEBHOOK_TYPE_CUSTOM:
		return "custom"
	default:
		return ""
	}
}

func strToWebhookType(s string) hermesv1.WebhookType {
	switch s {
	case "telegram":
		return hermesv1.WebhookType_WEBHOOK_TYPE_TELEGRAM
	case "discord":
		return hermesv1.WebhookType_WEBHOOK_TYPE_DISCORD
	case "custom":
		return hermesv1.WebhookType_WEBHOOK_TYPE_CUSTOM
	default:
		return hermesv1.WebhookType_WEBHOOK_TYPE_UNSPECIFIED
	}
}
