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

var (
	ErrNotFound      = errors.New("not found")
	ErrAlreadyExists = errors.New("already exists")
)

// ---------------------------------------------------------------------------
// Row types
// ---------------------------------------------------------------------------

type UserRow struct {
	ID           string
	TenantID     string
	Email        string
	PasswordHash string
	Role         string
	CreatedAt    time.Time
}

type TenantRow struct {
	ID                 string
	Name               string
	SettingsJSON       string
	MaxNumbersPerProxy int32
	CreatedAt          time.Time
}

type WorkspaceRow struct {
	ID           string
	TenantID     string
	Name         string
	SettingsJSON string
	DailyCap     int32
	CreatedAt    time.Time
}

type DashboardStatsRow struct {
	ActiveNumbers           int32
	TotalNumbers            int32
	MessagesSentToday       int64
	MessagesReceivedToday   int64
	ActiveCampaigns         int32
	UnassignedConversations int32
	ActiveProxies           int32
	TotalProxies            int32
	BansToday               int32
	TotalContacts           int64
}

// ---------------------------------------------------------------------------
// Store interface
// ---------------------------------------------------------------------------

type Store interface {
	// Users
	GetUserByEmail(ctx context.Context, email string) (*UserRow, error)
	GetUserByID(ctx context.Context, id string) (*UserRow, error)
	CreateUser(ctx context.Context, tenantID, email, passwordHash, role string) (*UserRow, error)
	ListUsers(ctx context.Context, workspaceID string, page, pageSize int32) ([]*UserRow, int64, error)
	UpdateUser(ctx context.Context, id, email, role, passwordHash string) (*UserRow, error)
	DeleteUser(ctx context.Context, id string) error
	GetUserWorkspaceIDs(ctx context.Context, userID string) ([]string, error)
	AddWorkspaceMember(ctx context.Context, userID, workspaceID, role string) error

	// Tenants
	CreateTenant(ctx context.Context, name, settingsJSON string) (*TenantRow, error)
	GetTenant(ctx context.Context, id string) (*TenantRow, error)
	ListTenants(ctx context.Context, page, pageSize int32) ([]*TenantRow, int64, error)
	UpdateTenant(ctx context.Context, id, name, settingsJSON string) (*TenantRow, error)

	// Workspaces
	CreateWorkspace(ctx context.Context, tenantID, name, settingsJSON string, dailyCap int32) (*WorkspaceRow, error)
	GetWorkspace(ctx context.Context, id string) (*WorkspaceRow, error)
	ListWorkspaces(ctx context.Context, tenantID string, page, pageSize int32) ([]*WorkspaceRow, int64, error)
	UpdateWorkspace(ctx context.Context, id, name, settingsJSON string, dailyCap int32) (*WorkspaceRow, error)
	DeleteWorkspace(ctx context.Context, id string) error

	// Refresh tokens
	SaveRefreshToken(ctx context.Context, tokenID, userID string, expiresAt time.Time) error
	GetRefreshToken(ctx context.Context, tokenID string) (string, error)
	DeleteRefreshToken(ctx context.Context, tokenID string) error
	DeleteUserRefreshTokens(ctx context.Context, userID string) error

	// Dashboard
	GetDashboardStats(ctx context.Context, tenantID, workspaceID string) (*DashboardStatsRow, error)
}

// ---------------------------------------------------------------------------
// PgStore
// ---------------------------------------------------------------------------

type PgStore struct {
	pool *pgxpool.Pool
}

func NewPgStore(pool *pgxpool.Pool) *PgStore {
	return &PgStore{pool: pool}
}

// ---------------------------------------------------------------------------
// Users
// ---------------------------------------------------------------------------

const userCols = "id, tenant_id, email, password_hash, role, created_at"

func scanUser(row pgx.Row) (*UserRow, error) {
	u := &UserRow{}
	err := row.Scan(&u.ID, &u.TenantID, &u.Email, &u.PasswordHash, &u.Role, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (s *PgStore) GetUserByEmail(ctx context.Context, email string) (*UserRow, error) {
	return scanUser(s.pool.QueryRow(ctx, "SELECT "+userCols+" FROM users WHERE email=$1", email))
}

func (s *PgStore) GetUserByID(ctx context.Context, id string) (*UserRow, error) {
	return scanUser(s.pool.QueryRow(ctx, "SELECT "+userCols+" FROM users WHERE id=$1", id))
}

func (s *PgStore) CreateUser(ctx context.Context, tenantID, email, passwordHash, role string) (*UserRow, error) {
	row := s.pool.QueryRow(ctx,
		"INSERT INTO users (tenant_id, email, password_hash, role) VALUES ($1,$2,$3,$4) RETURNING "+userCols,
		tenantID, email, passwordHash, role,
	)
	u, err := scanUser(row)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") || strings.Contains(err.Error(), "unique constraint") {
			return nil, ErrAlreadyExists
		}
		return nil, fmt.Errorf("creating user: %w", err)
	}
	return u, nil
}

func (s *PgStore) ListUsers(ctx context.Context, workspaceID string, page, pageSize int32) ([]*UserRow, int64, error) {
	var total int64
	err := s.pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM workspace_members wm JOIN users u ON u.id=wm.user_id WHERE wm.workspace_id=$1",
		workspaceID).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("counting users: %w", err)
	}

	offset := (page - 1) * pageSize
	rows, err := s.pool.Query(ctx,
		"SELECT u."+userCols+" FROM users u JOIN workspace_members wm ON wm.user_id=u.id WHERE wm.workspace_id=$1 ORDER BY u.created_at DESC LIMIT $2 OFFSET $3",
		workspaceID, pageSize, offset,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("listing users: %w", err)
	}
	defer rows.Close()

	var result []*UserRow
	for rows.Next() {
		u := &UserRow{}
		if err := rows.Scan(&u.ID, &u.TenantID, &u.Email, &u.PasswordHash, &u.Role, &u.CreatedAt); err != nil {
			return nil, 0, err
		}
		result = append(result, u)
	}
	return result, total, rows.Err()
}

func (s *PgStore) UpdateUser(ctx context.Context, id, email, role, passwordHash string) (*UserRow, error) {
	var setClauses []string
	var args []any
	idx := 1

	if email != "" {
		setClauses = append(setClauses, fmt.Sprintf("email=$%d", idx))
		args = append(args, email)
		idx++
	}
	if role != "" {
		setClauses = append(setClauses, fmt.Sprintf("role=$%d", idx))
		args = append(args, role)
		idx++
	}
	if passwordHash != "" {
		setClauses = append(setClauses, fmt.Sprintf("password_hash=$%d", idx))
		args = append(args, passwordHash)
		idx++
	}
	if len(setClauses) == 0 {
		return s.GetUserByID(ctx, id)
	}

	query := fmt.Sprintf("UPDATE users SET %s WHERE id=$%d RETURNING %s",
		strings.Join(setClauses, ", "), idx, userCols)
	args = append(args, id)
	return scanUser(s.pool.QueryRow(ctx, query, args...))
}

func (s *PgStore) DeleteUser(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, "DELETE FROM users WHERE id=$1", id)
	if err != nil {
		return fmt.Errorf("deleting user: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PgStore) GetUserWorkspaceIDs(ctx context.Context, userID string) ([]string, error) {
	rows, err := s.pool.Query(ctx, "SELECT workspace_id FROM workspace_members WHERE user_id=$1", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *PgStore) AddWorkspaceMember(ctx context.Context, userID, workspaceID, role string) error {
	_, err := s.pool.Exec(ctx,
		"INSERT INTO workspace_members (user_id, workspace_id, role) VALUES ($1,$2,$3) ON CONFLICT (user_id, workspace_id) DO UPDATE SET role=$3",
		userID, workspaceID, role,
	)
	return err
}

// ---------------------------------------------------------------------------
// Tenants
// ---------------------------------------------------------------------------

const tenantCols = "id, name, settings_json, max_numbers_per_proxy, created_at"

func scanTenant(row pgx.Row) (*TenantRow, error) {
	t := &TenantRow{}
	err := row.Scan(&t.ID, &t.Name, &t.SettingsJSON, &t.MaxNumbersPerProxy, &t.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return t, nil
}

func (s *PgStore) CreateTenant(ctx context.Context, name, settingsJSON string) (*TenantRow, error) {
	if settingsJSON == "" {
		settingsJSON = "{}"
	}
	return scanTenant(s.pool.QueryRow(ctx,
		"INSERT INTO tenants (name, settings_json) VALUES ($1,$2) RETURNING "+tenantCols,
		name, settingsJSON,
	))
}

func (s *PgStore) GetTenant(ctx context.Context, id string) (*TenantRow, error) {
	return scanTenant(s.pool.QueryRow(ctx, "SELECT "+tenantCols+" FROM tenants WHERE id=$1", id))
}

func (s *PgStore) ListTenants(ctx context.Context, page, pageSize int32) ([]*TenantRow, int64, error) {
	var total int64
	if err := s.pool.QueryRow(ctx, "SELECT COUNT(*) FROM tenants").Scan(&total); err != nil {
		return nil, 0, err
	}

	offset := (page - 1) * pageSize
	rows, err := s.pool.Query(ctx,
		"SELECT "+tenantCols+" FROM tenants ORDER BY created_at DESC LIMIT $1 OFFSET $2",
		pageSize, offset,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var result []*TenantRow
	for rows.Next() {
		t := &TenantRow{}
		if err := rows.Scan(&t.ID, &t.Name, &t.SettingsJSON, &t.MaxNumbersPerProxy, &t.CreatedAt); err != nil {
			return nil, 0, err
		}
		result = append(result, t)
	}
	return result, total, rows.Err()
}

func (s *PgStore) UpdateTenant(ctx context.Context, id, name, settingsJSON string) (*TenantRow, error) {
	var setClauses []string
	var args []any
	idx := 1

	if name != "" {
		setClauses = append(setClauses, fmt.Sprintf("name=$%d", idx))
		args = append(args, name)
		idx++
	}
	if settingsJSON != "" {
		setClauses = append(setClauses, fmt.Sprintf("settings_json=$%d", idx))
		args = append(args, settingsJSON)
		idx++
	}
	if len(setClauses) == 0 {
		return s.GetTenant(ctx, id)
	}

	query := fmt.Sprintf("UPDATE tenants SET %s WHERE id=$%d RETURNING %s",
		strings.Join(setClauses, ", "), idx, tenantCols)
	args = append(args, id)
	return scanTenant(s.pool.QueryRow(ctx, query, args...))
}

// ---------------------------------------------------------------------------
// Workspaces
// ---------------------------------------------------------------------------

const workspaceCols = "id, tenant_id, name, settings_json, daily_cap, created_at"

func scanWorkspace(row pgx.Row) (*WorkspaceRow, error) {
	w := &WorkspaceRow{}
	err := row.Scan(&w.ID, &w.TenantID, &w.Name, &w.SettingsJSON, &w.DailyCap, &w.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return w, nil
}

func (s *PgStore) CreateWorkspace(ctx context.Context, tenantID, name, settingsJSON string, dailyCap int32) (*WorkspaceRow, error) {
	if settingsJSON == "" {
		settingsJSON = "{}"
	}
	if dailyCap <= 0 {
		dailyCap = 200
	}
	return scanWorkspace(s.pool.QueryRow(ctx,
		"INSERT INTO workspaces (tenant_id, name, settings_json, daily_cap) VALUES ($1,$2,$3,$4) RETURNING "+workspaceCols,
		tenantID, name, settingsJSON, dailyCap,
	))
}

func (s *PgStore) GetWorkspace(ctx context.Context, id string) (*WorkspaceRow, error) {
	return scanWorkspace(s.pool.QueryRow(ctx, "SELECT "+workspaceCols+" FROM workspaces WHERE id=$1", id))
}

func (s *PgStore) ListWorkspaces(ctx context.Context, tenantID string, page, pageSize int32) ([]*WorkspaceRow, int64, error) {
	var total int64
	if err := s.pool.QueryRow(ctx, "SELECT COUNT(*) FROM workspaces WHERE tenant_id=$1", tenantID).Scan(&total); err != nil {
		return nil, 0, err
	}

	offset := (page - 1) * pageSize
	rows, err := s.pool.Query(ctx,
		"SELECT "+workspaceCols+" FROM workspaces WHERE tenant_id=$1 ORDER BY created_at DESC LIMIT $2 OFFSET $3",
		tenantID, pageSize, offset,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var result []*WorkspaceRow
	for rows.Next() {
		w := &WorkspaceRow{}
		if err := rows.Scan(&w.ID, &w.TenantID, &w.Name, &w.SettingsJSON, &w.DailyCap, &w.CreatedAt); err != nil {
			return nil, 0, err
		}
		result = append(result, w)
	}
	return result, total, rows.Err()
}

func (s *PgStore) UpdateWorkspace(ctx context.Context, id, name, settingsJSON string, dailyCap int32) (*WorkspaceRow, error) {
	var setClauses []string
	var args []any
	idx := 1

	if name != "" {
		setClauses = append(setClauses, fmt.Sprintf("name=$%d", idx))
		args = append(args, name)
		idx++
	}
	if settingsJSON != "" {
		setClauses = append(setClauses, fmt.Sprintf("settings_json=$%d", idx))
		args = append(args, settingsJSON)
		idx++
	}
	if dailyCap > 0 {
		setClauses = append(setClauses, fmt.Sprintf("daily_cap=$%d", idx))
		args = append(args, dailyCap)
		idx++
	}
	if len(setClauses) == 0 {
		return s.GetWorkspace(ctx, id)
	}

	query := fmt.Sprintf("UPDATE workspaces SET %s WHERE id=$%d RETURNING %s",
		strings.Join(setClauses, ", "), idx, workspaceCols)
	args = append(args, id)
	return scanWorkspace(s.pool.QueryRow(ctx, query, args...))
}

func (s *PgStore) DeleteWorkspace(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, "DELETE FROM workspaces WHERE id=$1", id)
	if err != nil {
		return fmt.Errorf("deleting workspace: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------------------------------------------------------------------
// Refresh Tokens
// ---------------------------------------------------------------------------

func (s *PgStore) SaveRefreshToken(ctx context.Context, tokenID, userID string, expiresAt time.Time) error {
	_, err := s.pool.Exec(ctx,
		"INSERT INTO refresh_tokens (id, user_id, expires_at) VALUES ($1,$2,$3)",
		tokenID, userID, expiresAt,
	)
	return err
}

func (s *PgStore) GetRefreshToken(ctx context.Context, tokenID string) (string, error) {
	var userID string
	err := s.pool.QueryRow(ctx,
		"SELECT user_id FROM refresh_tokens WHERE id=$1 AND expires_at > now()",
		tokenID,
	).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return userID, err
}

func (s *PgStore) DeleteRefreshToken(ctx context.Context, tokenID string) error {
	_, err := s.pool.Exec(ctx, "DELETE FROM refresh_tokens WHERE id=$1", tokenID)
	return err
}

func (s *PgStore) DeleteUserRefreshTokens(ctx context.Context, userID string) error {
	_, err := s.pool.Exec(ctx, "DELETE FROM refresh_tokens WHERE user_id=$1", userID)
	return err
}

// ---------------------------------------------------------------------------
// Dashboard Stats (cross-service read on shared DB)
// ---------------------------------------------------------------------------

func (s *PgStore) GetDashboardStats(ctx context.Context, tenantID, workspaceID string) (*DashboardStatsRow, error) {
	stats := &DashboardStatsRow{}

	// Numbers
	s.pool.QueryRow(ctx, "SELECT COUNT(*) FILTER (WHERE status='active'), COUNT(*) FROM wa_numbers WHERE tenant_id=$1", tenantID).
		Scan(&stats.ActiveNumbers, &stats.TotalNumbers)

	// Messages today
	s.pool.QueryRow(ctx,
		"SELECT COALESCE(SUM(CASE WHEN direction='outbound' THEN 1 ELSE 0 END),0), COALESCE(SUM(CASE WHEN direction='inbound' THEN 1 ELSE 0 END),0) FROM messages WHERE created_at >= CURRENT_DATE AND conversation_id IN (SELECT id FROM conversations WHERE workspace_id = COALESCE(NULLIF($1,''), workspace_id))",
		workspaceID).Scan(&stats.MessagesSentToday, &stats.MessagesReceivedToday)

	// Active campaigns
	s.pool.QueryRow(ctx, "SELECT COUNT(*) FROM campaigns WHERE status='running' AND workspace_id IN (SELECT id FROM workspaces WHERE tenant_id=$1)", tenantID).
		Scan(&stats.ActiveCampaigns)

	// Unassigned conversations
	if workspaceID != "" {
		s.pool.QueryRow(ctx, "SELECT COUNT(*) FROM conversations WHERE workspace_id=$1 AND status='unassigned'", workspaceID).
			Scan(&stats.UnassignedConversations)
	} else {
		s.pool.QueryRow(ctx, "SELECT COUNT(*) FROM conversations WHERE status='unassigned' AND workspace_id IN (SELECT id FROM workspaces WHERE tenant_id=$1)", tenantID).
			Scan(&stats.UnassignedConversations)
	}

	// Proxies
	s.pool.QueryRow(ctx, "SELECT COUNT(*) FILTER (WHERE status='active'), COUNT(*) FROM proxies WHERE tenant_id=$1", tenantID).
		Scan(&stats.ActiveProxies, &stats.TotalProxies)

	// Bans today
	s.pool.QueryRow(ctx, "SELECT COUNT(*) FROM wa_numbers WHERE tenant_id=$1 AND status='banned' AND last_ban_at >= CURRENT_DATE", tenantID).
		Scan(&stats.BansToday)

	// Contacts
	s.pool.QueryRow(ctx, "SELECT COUNT(*) FROM contacts WHERE tenant_id=$1", tenantID).
		Scan(&stats.TotalContacts)

	return stats, nil
}
