package handler

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Sentinel errors returned by Store methods.
var (
	ErrNotFound = errors.New("not found")
)

// WaNumberRow is a database row from the wa_numbers table.
type WaNumberRow struct {
	ID             string
	TenantID       string
	JID            string
	Phone          string
	DisplayName    string
	Status         string
	ProxyID        *string
	HealthScore    int32
	DailySentCount int32
	TotalSent      int64
	BanCount       int32
	LastBanAt      *time.Time
	ConnectedAt    *time.Time
	PodID          string
	CreatedAt      time.Time
}

const waNumberCols = "id, tenant_id, jid, phone, display_name, status, proxy_id, health_score, daily_sent_count, total_sent, ban_count, last_ban_at, connected_at, pod_id, created_at"

// Store defines the data-access operations the handler depends on.
type Store interface {
	GetWaNumber(ctx context.Context, id string) (*WaNumberRow, error)
	ListWaNumbersByPod(ctx context.Context, podID, statusFilter string, page, pageSize int32) ([]*WaNumberRow, int64, error)
	SetWaNumberConnected(ctx context.Context, id, jid, podID string) error
	SetWaNumberDisconnected(ctx context.Context, id string) error
	SetWaNumberBanned(ctx context.Context, id string) error
	IncrementSentCount(ctx context.Context, id string) error
	GetTenantID(ctx context.Context, waNumberID string) (string, error)
}

// PgStore implements Store using a pgxpool connection pool.
type PgStore struct {
	pool *pgxpool.Pool
}

func NewPgStore(pool *pgxpool.Pool) *PgStore {
	return &PgStore{pool: pool}
}

func scanWaNumber(row pgx.Row) (*WaNumberRow, error) {
	r := &WaNumberRow{}
	err := row.Scan(
		&r.ID, &r.TenantID, &r.JID, &r.Phone, &r.DisplayName, &r.Status,
		&r.ProxyID, &r.HealthScore, &r.DailySentCount, &r.TotalSent,
		&r.BanCount, &r.LastBanAt, &r.ConnectedAt, &r.PodID, &r.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return r, nil
}

func (s *PgStore) GetWaNumber(ctx context.Context, id string) (*WaNumberRow, error) {
	row := s.pool.QueryRow(ctx, "SELECT "+waNumberCols+" FROM wa_numbers WHERE id=$1", id)
	r, err := scanWaNumber(row)
	if err != nil {
		return nil, fmt.Errorf("getting wa_number: %w", err)
	}
	return r, nil
}

func (s *PgStore) ListWaNumbersByPod(ctx context.Context, podID, statusFilter string, page, pageSize int32) ([]*WaNumberRow, int64, error) {
	where := "WHERE pod_id = $1"
	args := []any{podID}
	idx := 2

	if statusFilter != "" {
		where += fmt.Sprintf(" AND status = $%d", idx)
		args = append(args, statusFilter)
		idx++
	}

	var total int64
	if err := s.pool.QueryRow(ctx, "SELECT COUNT(*) FROM wa_numbers "+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("counting wa_numbers: %w", err)
	}

	offset := (page - 1) * pageSize
	query := fmt.Sprintf("SELECT %s FROM wa_numbers %s ORDER BY created_at DESC LIMIT $%d OFFSET $%d", waNumberCols, where, idx, idx+1)
	args = append(args, pageSize, offset)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("querying wa_numbers: %w", err)
	}
	defer rows.Close()

	var result []*WaNumberRow
	for rows.Next() {
		r := &WaNumberRow{}
		if err := rows.Scan(
			&r.ID, &r.TenantID, &r.JID, &r.Phone, &r.DisplayName, &r.Status,
			&r.ProxyID, &r.HealthScore, &r.DailySentCount, &r.TotalSent,
			&r.BanCount, &r.LastBanAt, &r.ConnectedAt, &r.PodID, &r.CreatedAt,
		); err != nil {
			return nil, 0, fmt.Errorf("scanning wa_number: %w", err)
		}
		result = append(result, r)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return result, total, nil
}

func (s *PgStore) SetWaNumberConnected(ctx context.Context, id, jid, podID string) error {
	tag, err := s.pool.Exec(ctx,
		"UPDATE wa_numbers SET status='active', jid=$1, pod_id=$2, connected_at=now() WHERE id=$3",
		jid, podID, id,
	)
	if err != nil {
		return fmt.Errorf("setting connected: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PgStore) SetWaNumberDisconnected(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx,
		"UPDATE wa_numbers SET status='disconnected', pod_id='' WHERE id=$1", id,
	)
	if err != nil {
		return fmt.Errorf("setting disconnected: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PgStore) SetWaNumberBanned(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx,
		"UPDATE wa_numbers SET status='banned', ban_count=ban_count+1, last_ban_at=now(), pod_id='' WHERE id=$1", id,
	)
	if err != nil {
		return fmt.Errorf("setting banned: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PgStore) IncrementSentCount(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx,
		"UPDATE wa_numbers SET daily_sent_count=daily_sent_count+1, total_sent=total_sent+1 WHERE id=$1", id,
	)
	if err != nil {
		return fmt.Errorf("incrementing sent count: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PgStore) GetTenantID(ctx context.Context, waNumberID string) (string, error) {
	var tenantID string
	err := s.pool.QueryRow(ctx, "SELECT tenant_id FROM wa_numbers WHERE id=$1", waNumberID).Scan(&tenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("getting tenant_id: %w", err)
	}
	return tenantID, nil
}
