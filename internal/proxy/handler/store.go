package handler

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Sentinel errors returned by Store methods.
var (
	ErrNotFound       = errors.New("not found")
	ErrHasAssignments = errors.New("proxy has assigned numbers")
)

// ProxyRow is a database row from the proxies table.
type ProxyRow struct {
	ID              string
	TenantID        string
	Host            string
	Port            int32
	Username        string
	Password        string
	Type            string
	Status          string
	BanCount        int32
	AssignedCount   int32
	LastHealthCheck *time.Time
	CreatedAt       time.Time
}

// WaNumberRow is a lightweight view of a WA number for assignment display.
type WaNumberRow struct {
	ID          string
	Phone       string
	DisplayName string
	Status      string
}

const proxyCols = "id, tenant_id, host, port, username, password, type, status, ban_count, assigned_count, last_health_check, created_at"

// Store defines the data-access operations the handler depends on.
type Store interface {
	CreateProxy(ctx context.Context, tenantID, host string, port int32, username, password, proxyType string) (*ProxyRow, error)
	ProxyExistsByHostPort(ctx context.Context, tenantID, host string, port int32) (bool, error)
	GetProxy(ctx context.Context, id string) (*ProxyRow, error)
	ListProxies(ctx context.Context, tenantID, status, proxyType string, page, pageSize int32) ([]*ProxyRow, int64, error)
	UpdateProxy(ctx context.Context, id, host string, port int32, username, password, proxyType, status string) (*ProxyRow, error)
	DeleteProxy(ctx context.Context, id string, force bool) (int32, error)
	GetAssignedNumbers(ctx context.Context, proxyID string) ([]*WaNumberRow, error)
	AssignProxy(ctx context.Context, waNumberID, proxyID string) (*ProxyRow, error)
	UnassignProxy(ctx context.Context, waNumberID string) error
	GetBestProxy(ctx context.Context, tenantID, proxyType string) (*ProxyRow, bool, error)
	FlagProxy(ctx context.Context, id string) (*ProxyRow, error)
	IncrementBanCount(ctx context.Context, proxyID string) (int32, error)
	UpdateProxyHealth(ctx context.Context, id, status string) error
	GetAllProxiesForTenant(ctx context.Context, tenantID string) ([]*ProxyRow, error)
}

// PgStore implements Store using a pgxpool connection pool.
type PgStore struct {
	pool *pgxpool.Pool
}

func NewPgStore(pool *pgxpool.Pool) *PgStore {
	return &PgStore{pool: pool}
}

func scanProxy(row pgx.Row) (*ProxyRow, error) {
	p := &ProxyRow{}
	err := row.Scan(
		&p.ID, &p.TenantID, &p.Host, &p.Port, &p.Username, &p.Password,
		&p.Type, &p.Status, &p.BanCount, &p.AssignedCount, &p.LastHealthCheck, &p.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return p, nil
}

func scanProxies(rows pgx.Rows) ([]*ProxyRow, error) {
	defer rows.Close()
	var result []*ProxyRow
	for rows.Next() {
		p := &ProxyRow{}
		if err := rows.Scan(
			&p.ID, &p.TenantID, &p.Host, &p.Port, &p.Username, &p.Password,
			&p.Type, &p.Status, &p.BanCount, &p.AssignedCount, &p.LastHealthCheck, &p.CreatedAt,
		); err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, rows.Err()
}

// ---------------------------------------------------------------------------
// Store method implementations
// ---------------------------------------------------------------------------

func (s *PgStore) CreateProxy(ctx context.Context, tenantID, host string, port int32, username, password, proxyType string) (*ProxyRow, error) {
	row := s.pool.QueryRow(ctx,
		"INSERT INTO proxies (tenant_id, host, port, username, password, type) VALUES ($1,$2,$3,$4,$5,$6) RETURNING "+proxyCols,
		tenantID, host, port, username, password, proxyType,
	)
	p, err := scanProxy(row)
	if err != nil {
		return nil, fmt.Errorf("inserting proxy: %w", err)
	}
	return p, nil
}

func (s *PgStore) ProxyExistsByHostPort(ctx context.Context, tenantID, host string, port int32) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM proxies WHERE tenant_id=$1 AND host=$2 AND port=$3)",
		tenantID, host, port,
	).Scan(&exists)
	return exists, err
}

func (s *PgStore) GetProxy(ctx context.Context, id string) (*ProxyRow, error) {
	return scanProxy(s.pool.QueryRow(ctx, "SELECT "+proxyCols+" FROM proxies WHERE id=$1", id))
}

func (s *PgStore) ListProxies(ctx context.Context, tenantID, statusFilter, typeFilter string, page, pageSize int32) ([]*ProxyRow, int64, error) {
	where := "WHERE tenant_id = $1"
	args := []any{tenantID}
	idx := 2

	if statusFilter != "" {
		where += fmt.Sprintf(" AND status = $%d", idx)
		args = append(args, statusFilter)
		idx++
	}
	if typeFilter != "" {
		where += fmt.Sprintf(" AND type = $%d", idx)
		args = append(args, typeFilter)
		idx++
	}

	var total int64
	if err := s.pool.QueryRow(ctx, "SELECT COUNT(*) FROM proxies "+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("counting proxies: %w", err)
	}

	offset := (page - 1) * pageSize
	query := fmt.Sprintf("SELECT %s FROM proxies %s ORDER BY created_at DESC LIMIT $%d OFFSET $%d", proxyCols, where, idx, idx+1)
	args = append(args, pageSize, offset)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("querying proxies: %w", err)
	}
	list, err := scanProxies(rows)
	if err != nil {
		return nil, 0, fmt.Errorf("scanning proxies: %w", err)
	}
	return list, total, nil
}

func (s *PgStore) UpdateProxy(ctx context.Context, id, host string, port int32, username, password, proxyType, statusStr string) (*ProxyRow, error) {
	var setClauses []string
	var args []any
	idx := 1

	add := func(col, val string) {
		setClauses = append(setClauses, fmt.Sprintf("%s = $%d", col, idx))
		args = append(args, val)
		idx++
	}

	if host != "" {
		add("host", host)
	}
	if port != 0 {
		setClauses = append(setClauses, fmt.Sprintf("port = $%d", idx))
		args = append(args, port)
		idx++
	}
	if username != "" {
		add("username", username)
	}
	if password != "" {
		add("password", password)
	}
	if proxyType != "" {
		add("type", proxyType)
	}
	if statusStr != "" {
		add("status", statusStr)
	}

	if len(setClauses) == 0 {
		return s.GetProxy(ctx, id)
	}

	query := fmt.Sprintf("UPDATE proxies SET %s WHERE id = $%d RETURNING %s",
		strings.Join(setClauses, ", "), idx, proxyCols)
	args = append(args, id)

	return scanProxy(s.pool.QueryRow(ctx, query, args...))
}

func (s *PgStore) DeleteProxy(ctx context.Context, id string, force bool) (int32, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	var assignedCount int32
	err = tx.QueryRow(ctx, "SELECT assigned_count FROM proxies WHERE id=$1 FOR UPDATE", id).Scan(&assignedCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("locking proxy: %w", err)
	}

	var unassigned int32
	if assignedCount > 0 {
		if !force {
			return 0, ErrHasAssignments
		}
		tag, err := tx.Exec(ctx, "UPDATE wa_numbers SET proxy_id = NULL WHERE proxy_id = $1", id)
		if err != nil {
			return 0, fmt.Errorf("unassigning numbers: %w", err)
		}
		unassigned = int32(tag.RowsAffected())
	}

	if _, err := tx.Exec(ctx, "DELETE FROM proxies WHERE id=$1", id); err != nil {
		return 0, fmt.Errorf("deleting proxy: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("committing: %w", err)
	}
	return unassigned, nil
}

func (s *PgStore) GetAssignedNumbers(ctx context.Context, proxyID string) ([]*WaNumberRow, error) {
	rows, err := s.pool.Query(ctx,
		"SELECT id, phone, display_name, status FROM wa_numbers WHERE proxy_id=$1", proxyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []*WaNumberRow
	for rows.Next() {
		n := &WaNumberRow{}
		if err := rows.Scan(&n.ID, &n.Phone, &n.DisplayName, &n.Status); err != nil {
			return nil, err
		}
		result = append(result, n)
	}
	return result, rows.Err()
}

func (s *PgStore) AssignProxy(ctx context.Context, waNumberID, proxyID string) (*ProxyRow, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// Verify proxy exists.
	var proxyExists bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM proxies WHERE id=$1)", proxyID).Scan(&proxyExists); err != nil {
		return nil, fmt.Errorf("checking proxy: %w", err)
	}
	if !proxyExists {
		return nil, ErrNotFound
	}

	// Get WA number's current proxy assignment.
	var oldProxyID *string
	err = tx.QueryRow(ctx, "SELECT proxy_id FROM wa_numbers WHERE id=$1 FOR UPDATE", waNumberID).Scan(&oldProxyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("getting wa_number: %w", err)
	}

	// Decrement old proxy count if reassigning.
	if oldProxyID != nil && *oldProxyID != proxyID {
		if _, err := tx.Exec(ctx, "UPDATE proxies SET assigned_count = GREATEST(assigned_count - 1, 0) WHERE id=$1", *oldProxyID); err != nil {
			return nil, fmt.Errorf("decrementing old proxy: %w", err)
		}
	}

	// Set new proxy on wa_number.
	if _, err := tx.Exec(ctx, "UPDATE wa_numbers SET proxy_id=$1 WHERE id=$2", proxyID, waNumberID); err != nil {
		return nil, fmt.Errorf("assigning proxy: %w", err)
	}

	// Increment new proxy count if changed.
	if oldProxyID == nil || *oldProxyID != proxyID {
		if _, err := tx.Exec(ctx, "UPDATE proxies SET assigned_count = assigned_count + 1 WHERE id=$1", proxyID); err != nil {
			return nil, fmt.Errorf("incrementing proxy count: %w", err)
		}
	}

	p := &ProxyRow{}
	err = tx.QueryRow(ctx, "SELECT "+proxyCols+" FROM proxies WHERE id=$1", proxyID).Scan(
		&p.ID, &p.TenantID, &p.Host, &p.Port, &p.Username, &p.Password,
		&p.Type, &p.Status, &p.BanCount, &p.AssignedCount, &p.LastHealthCheck, &p.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("fetching updated proxy: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing: %w", err)
	}
	return p, nil
}

func (s *PgStore) UnassignProxy(ctx context.Context, waNumberID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	var proxyID *string
	err = tx.QueryRow(ctx, "SELECT proxy_id FROM wa_numbers WHERE id=$1 FOR UPDATE", waNumberID).Scan(&proxyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("getting wa_number: %w", err)
	}
	if proxyID == nil {
		return ErrNotFound
	}

	if _, err := tx.Exec(ctx, "UPDATE wa_numbers SET proxy_id = NULL WHERE id=$1", waNumberID); err != nil {
		return fmt.Errorf("clearing proxy: %w", err)
	}
	if _, err := tx.Exec(ctx, "UPDATE proxies SET assigned_count = GREATEST(assigned_count - 1, 0) WHERE id=$1", *proxyID); err != nil {
		return fmt.Errorf("decrementing proxy count: %w", err)
	}

	return tx.Commit(ctx)
}

func (s *PgStore) GetBestProxy(ctx context.Context, tenantID, proxyType string) (*ProxyRow, bool, error) {
	// Fetch tenant limit.
	var maxPerProxy int32
	err := s.pool.QueryRow(ctx, "SELECT max_numbers_per_proxy FROM tenants WHERE id=$1", tenantID).Scan(&maxPerProxy)
	if errors.Is(err, pgx.ErrNoRows) {
		maxPerProxy = 0
	} else if err != nil {
		return nil, false, fmt.Errorf("getting tenant config: %w", err)
	}

	// Build best-proxy query.
	query := "SELECT " + proxyCols + " FROM proxies WHERE tenant_id=$1 AND status='active'"
	args := []any{tenantID}
	idx := 2

	if proxyType != "" {
		query += fmt.Sprintf(" AND type=$%d", idx)
		args = append(args, proxyType)
		idx++
	}
	if maxPerProxy > 0 {
		query += fmt.Sprintf(" AND assigned_count < $%d", idx)
		args = append(args, maxPerProxy)
		idx++
	}
	query += " ORDER BY ban_count ASC, assigned_count ASC LIMIT 1"

	p, err := scanProxy(s.pool.QueryRow(ctx, query, args...))
	if err != nil {
		return nil, false, fmt.Errorf("querying best proxy: %w", err)
	}
	if p != nil {
		return p, false, nil
	}

	// No proxy found — determine if pool is exhausted (proxies exist but all at capacity).
	if maxPerProxy > 0 {
		checkQuery := "SELECT EXISTS(SELECT 1 FROM proxies WHERE tenant_id=$1 AND status='active'"
		checkArgs := []any{tenantID}
		if proxyType != "" {
			checkQuery += " AND type=$2"
			checkArgs = append(checkArgs, proxyType)
		}
		checkQuery += ")"

		var hasActive bool
		if err := s.pool.QueryRow(ctx, checkQuery, checkArgs...).Scan(&hasActive); err != nil {
			return nil, false, fmt.Errorf("checking active proxies: %w", err)
		}
		if hasActive {
			return nil, true, nil
		}
	}

	return nil, false, nil
}

func (s *PgStore) FlagProxy(ctx context.Context, id string) (*ProxyRow, error) {
	p, err := scanProxy(s.pool.QueryRow(ctx,
		"UPDATE proxies SET status='flagged' WHERE id=$1 RETURNING "+proxyCols, id))
	if err != nil {
		return nil, fmt.Errorf("flagging proxy: %w", err)
	}
	if p == nil {
		return nil, ErrNotFound
	}
	return p, nil
}

func (s *PgStore) IncrementBanCount(ctx context.Context, proxyID string) (int32, error) {
	var newCount int32
	err := s.pool.QueryRow(ctx,
		"UPDATE proxies SET ban_count = ban_count + 1 WHERE id=$1 RETURNING ban_count", proxyID,
	).Scan(&newCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	return newCount, err
}

func (s *PgStore) UpdateProxyHealth(ctx context.Context, id, status string) error {
	tag, err := s.pool.Exec(ctx,
		"UPDATE proxies SET status=$1, last_health_check=now() WHERE id=$2", status, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PgStore) GetAllProxiesForTenant(ctx context.Context, tenantID string) ([]*ProxyRow, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+proxyCols+" FROM proxies WHERE tenant_id=$1", tenantID)
	if err != nil {
		return nil, err
	}
	return scanProxies(rows)
}
