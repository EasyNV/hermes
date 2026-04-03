package handler

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when a notification config does not exist.
var ErrNotFound = errors.New("notification config not found")

// ConfigRow represents a row in the notification_configs table.
type ConfigRow struct {
	ID          string
	WorkspaceID string
	Type        string // "browser_push", "sound", "webhook"
	WebhookURL  string
	WebhookType string // "", "telegram", "discord", "custom"
	Enabled     bool
	CreatedAt   time.Time
}

// Store defines the data access interface for notification configs.
type Store interface {
	UpsertConfig(ctx context.Context, workspaceID, typ, webhookURL, webhookType string, enabled bool) (ConfigRow, bool, error)
	GetConfig(ctx context.Context, id string) (ConfigRow, error)
	ListConfigs(ctx context.Context, workspaceID string) ([]ConfigRow, error)
	UpdateConfig(ctx context.Context, id, typ, webhookURL, webhookType string, enabled bool) (ConfigRow, error)
	DeleteConfig(ctx context.Context, id string) error
	ListEnabledConfigs(ctx context.Context, workspaceID string) ([]ConfigRow, error)
}

type pgStore struct {
	pool *pgxpool.Pool
}

// NewPGStore returns a Store backed by PostgreSQL.
func NewPGStore(pool *pgxpool.Pool) Store {
	return &pgStore{pool: pool}
}

func (s *pgStore) UpsertConfig(ctx context.Context, workspaceID, typ, webhookURL, webhookType string, enabled bool) (ConfigRow, bool, error) {
	var row ConfigRow
	var wasUpdated bool
	err := s.pool.QueryRow(ctx, `
		INSERT INTO notification_configs (workspace_id, type, webhook_url, webhook_type, enabled)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (workspace_id, type, webhook_type) DO UPDATE
		SET webhook_url = EXCLUDED.webhook_url, enabled = EXCLUDED.enabled
		RETURNING id, workspace_id, type, webhook_url, webhook_type, enabled, created_at, (xmax != 0)
	`, workspaceID, typ, webhookURL, webhookType, enabled).Scan(
		&row.ID, &row.WorkspaceID, &row.Type, &row.WebhookURL,
		&row.WebhookType, &row.Enabled, &row.CreatedAt, &wasUpdated,
	)
	if err != nil {
		return row, false, fmt.Errorf("upserting config: %w", err)
	}
	return row, wasUpdated, nil
}

func (s *pgStore) GetConfig(ctx context.Context, id string) (ConfigRow, error) {
	var row ConfigRow
	err := s.pool.QueryRow(ctx, `
		SELECT id, workspace_id, type, webhook_url, webhook_type, enabled, created_at
		FROM notification_configs WHERE id = $1
	`, id).Scan(
		&row.ID, &row.WorkspaceID, &row.Type, &row.WebhookURL,
		&row.WebhookType, &row.Enabled, &row.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return row, ErrNotFound
		}
		return row, fmt.Errorf("getting config: %w", err)
	}
	return row, nil
}

func (s *pgStore) ListConfigs(ctx context.Context, workspaceID string) ([]ConfigRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, workspace_id, type, webhook_url, webhook_type, enabled, created_at
		FROM notification_configs WHERE workspace_id = $1
		ORDER BY created_at
	`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("listing configs: %w", err)
	}
	defer rows.Close()

	var configs []ConfigRow
	for rows.Next() {
		var row ConfigRow
		if err := rows.Scan(&row.ID, &row.WorkspaceID, &row.Type, &row.WebhookURL,
			&row.WebhookType, &row.Enabled, &row.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning config row: %w", err)
		}
		configs = append(configs, row)
	}
	return configs, rows.Err()
}

// UpdateConfig performs a partial update. Empty strings for typ/webhookURL/webhookType
// preserve the existing value. The enabled field is always applied.
func (s *pgStore) UpdateConfig(ctx context.Context, id, typ, webhookURL, webhookType string, enabled bool) (ConfigRow, error) {
	var row ConfigRow
	err := s.pool.QueryRow(ctx, `
		UPDATE notification_configs SET
			type = CASE WHEN $2 = '' THEN type ELSE $2 END,
			webhook_url = CASE WHEN $3 = '' THEN webhook_url ELSE $3 END,
			webhook_type = CASE WHEN $4 = '' THEN webhook_type ELSE $4 END,
			enabled = $5
		WHERE id = $1
		RETURNING id, workspace_id, type, webhook_url, webhook_type, enabled, created_at
	`, id, typ, webhookURL, webhookType, enabled).Scan(
		&row.ID, &row.WorkspaceID, &row.Type, &row.WebhookURL,
		&row.WebhookType, &row.Enabled, &row.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return row, ErrNotFound
		}
		return row, fmt.Errorf("updating config: %w", err)
	}
	return row, nil
}

func (s *pgStore) DeleteConfig(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM notification_configs WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("deleting config: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *pgStore) ListEnabledConfigs(ctx context.Context, workspaceID string) ([]ConfigRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, workspace_id, type, webhook_url, webhook_type, enabled, created_at
		FROM notification_configs WHERE workspace_id = $1 AND enabled = true
		ORDER BY created_at
	`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("listing enabled configs: %w", err)
	}
	defer rows.Close()

	var configs []ConfigRow
	for rows.Next() {
		var row ConfigRow
		if err := rows.Scan(&row.ID, &row.WorkspaceID, &row.Type, &row.WebhookURL,
			&row.WebhookType, &row.Enabled, &row.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning config row: %w", err)
		}
		configs = append(configs, row)
	}
	return configs, rows.Err()
}
