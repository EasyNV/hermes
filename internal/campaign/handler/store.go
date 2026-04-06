package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Sentinel errors returned by Store methods.
var (
	ErrNotFound = errors.New("not found")
)

// ---------------------------------------------------------------------------
// Row types
// ---------------------------------------------------------------------------

type TemplateRow struct {
	ID          string
	WorkspaceID string
	Name        string
	Body        string
	MediaURL    string
	MediaType   string
	Variables   []byte // JSON array of variable names
	CreatedBy   string
	CreatedAt   time.Time
}

func (r *TemplateRow) VariableNames() []string {
	var v []string
	_ = json.Unmarshal(r.Variables, &v)
	return v
}

type CampaignRow struct {
	ID                string
	WorkspaceID       string
	TemplateID        string
	Name              string
	Status            string
	ScheduleAt        *time.Time
	DailyCapPerNum    int32
	BanPauseThreshold int32
	RotationStrategy  string
	DelayMinMs        int32
	DelayMaxMs        int32
	TotalContacts     int32
	SentCount         int32
	FailedCount       int32
	RepliedCount      int32
	BannedCount       int32
	CreatedBy         string
	CreatedAt         time.Time
	StartedAt         *time.Time
	CompletedAt       *time.Time
}

type CampaignNumberRow struct {
	CampaignID string
	WaNumberID string
	Status     string
	SentCount  int32
	FailedCount int32
}

type CampaignContactJoinRow struct {
	CampaignID   string
	ContactID    string
	WaNumberID   *string
	Status       string
	SentAt       *time.Time
	DeliveredAt  *time.Time
	FailedAt     *time.Time
	Error        string
	ContactName  string
	ContactPhone string
}

type PendingContactRow struct {
	ContactID    string
	Phone        string
	Name         string
	CustomFields map[string]string
}

// ---------------------------------------------------------------------------
// Store interface
// ---------------------------------------------------------------------------

type Store interface {
	// Template CRUD
	CreateTemplate(ctx context.Context, workspaceID, name, body, mediaURL, mediaType, createdBy string, variables []byte) (*TemplateRow, error)
	GetTemplate(ctx context.Context, id string) (*TemplateRow, error)
	ListTemplates(ctx context.Context, workspaceID, search string, page, pageSize int32) ([]*TemplateRow, int64, error)
	UpdateTemplate(ctx context.Context, id, name, body, mediaURL, mediaType string, variables []byte) (*TemplateRow, error)
	DeleteTemplate(ctx context.Context, id string) error
	TemplateUsedByRunningCampaign(ctx context.Context, templateID string) (bool, error)
	TemplateUsedByActiveCampaign(ctx context.Context, templateID string) (bool, error)

	// Campaign CRUD
	CreateCampaign(ctx context.Context, row *CampaignRow) (*CampaignRow, error)
	GetCampaign(ctx context.Context, id string) (*CampaignRow, error)
	ListCampaigns(ctx context.Context, workspaceID, status string, page, pageSize int32) ([]*CampaignRow, int64, error)
	UpdateCampaignStatus(ctx context.Context, id, status string, setStarted, setCompleted bool) (*CampaignRow, error)

	// Campaign Numbers
	AddCampaignNumbers(ctx context.Context, campaignID string, waNumberIDs []string) error
	RemoveCampaignNumbers(ctx context.Context, campaignID string, waNumberIDs []string) error
	ListCampaignNumbers(ctx context.Context, campaignID string, page, pageSize int32) ([]*CampaignNumberRow, int64, error)
	GetActiveCampaignNumbers(ctx context.Context, campaignID string) ([]*CampaignNumberRow, error)
	UpdateCampaignNumberStatus(ctx context.Context, campaignID, waNumberID, status string) error
	IncrementNumberSentCount(ctx context.Context, campaignID, waNumberID string) error

	// Campaign Contacts
	AddCampaignContacts(ctx context.Context, campaignID string, contactIDs []string) (int32, error)
	RemoveCampaignContacts(ctx context.Context, campaignID string, contactIDs []string) (int32, error)
	ListCampaignContacts(ctx context.Context, campaignID, status string, page, pageSize int32) ([]*CampaignContactJoinRow, int64, error)
	GetPendingContacts(ctx context.Context, campaignID string, limit int32) ([]*PendingContactRow, error)
	UpdateContactSent(ctx context.Context, campaignID, contactID, waNumberID string) error
	SkipPendingContacts(ctx context.Context, campaignID string) (int32, error)

	// Campaign stats
	IncrementSentCount(ctx context.Context, campaignID string) error
	IncrementFailedCount(ctx context.Context, campaignID string) error
	IncrementRepliedCount(ctx context.Context, campaignID string) error
	IncrementBannedCount(ctx context.Context, campaignID string) (int32, error)
	UpdateTotalContacts(ctx context.Context, campaignID string) error

	// Cross-service lookups (read-only from contacts/workspaces tables)
	GetWorkspaceTenantID(ctx context.Context, workspaceID string) (string, error)
	PopulateAllowlistFromCampaign(ctx context.Context, campaignID, workspaceID string) (int64, error)
	FindContactInActiveCampaigns(ctx context.Context, senderPhone string) ([]CampaignContactMatch, error)
	GetCampaignsUsingNumber(ctx context.Context, waNumberID string, statuses []string) ([]*CampaignRow, error)
	CountCampaignNumbers(ctx context.Context, campaignID string) (int32, error)
	CountCampaignContacts(ctx context.Context, campaignID string) (int32, error)
}

type CampaignContactMatch struct {
	CampaignID string
	ContactID  string
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

const templateCols = "id, workspace_id, name, body, media_url, media_type, variables, created_by, created_at"
const campaignCols = "id, workspace_id, template_id, name, status, schedule_at, daily_cap_per_num, ban_pause_threshold, rotation_strategy, delay_min_ms, delay_max_ms, total_contacts, sent_count, failed_count, replied_count, banned_count, created_by, created_at, started_at, completed_at"

func scanTemplate(row pgx.Row) (*TemplateRow, error) {
	t := &TemplateRow{}
	err := row.Scan(&t.ID, &t.WorkspaceID, &t.Name, &t.Body, &t.MediaURL, &t.MediaType, &t.Variables, &t.CreatedBy, &t.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return t, nil
}

func scanCampaign(row pgx.Row) (*CampaignRow, error) {
	c := &CampaignRow{}
	err := row.Scan(
		&c.ID, &c.WorkspaceID, &c.TemplateID, &c.Name, &c.Status,
		&c.ScheduleAt, &c.DailyCapPerNum, &c.BanPauseThreshold,
		&c.RotationStrategy, &c.DelayMinMs, &c.DelayMaxMs,
		&c.TotalContacts, &c.SentCount, &c.FailedCount,
		&c.RepliedCount, &c.BannedCount, &c.CreatedBy,
		&c.CreatedAt, &c.StartedAt, &c.CompletedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return c, nil
}

func scanCampaigns(rows pgx.Rows) ([]*CampaignRow, error) {
	defer rows.Close()
	var result []*CampaignRow
	for rows.Next() {
		c := &CampaignRow{}
		if err := rows.Scan(
			&c.ID, &c.WorkspaceID, &c.TemplateID, &c.Name, &c.Status,
			&c.ScheduleAt, &c.DailyCapPerNum, &c.BanPauseThreshold,
			&c.RotationStrategy, &c.DelayMinMs, &c.DelayMaxMs,
			&c.TotalContacts, &c.SentCount, &c.FailedCount,
			&c.RepliedCount, &c.BannedCount, &c.CreatedBy,
			&c.CreatedAt, &c.StartedAt, &c.CompletedAt,
		); err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

// ---------------------------------------------------------------------------
// Template operations
// ---------------------------------------------------------------------------

func (s *PgStore) CreateTemplate(ctx context.Context, workspaceID, name, body, mediaURL, mediaType, createdBy string, variables []byte) (*TemplateRow, error) {
	row := s.pool.QueryRow(ctx,
		"INSERT INTO templates (workspace_id, name, body, media_url, media_type, variables, created_by) VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING "+templateCols,
		workspaceID, name, body, mediaURL, mediaType, variables, createdBy,
	)
	t, err := scanTemplate(row)
	if err != nil {
		return nil, fmt.Errorf("inserting template: %w", err)
	}
	return t, nil
}

func (s *PgStore) GetTemplate(ctx context.Context, id string) (*TemplateRow, error) {
	return scanTemplate(s.pool.QueryRow(ctx, "SELECT "+templateCols+" FROM templates WHERE id=$1", id))
}

func (s *PgStore) ListTemplates(ctx context.Context, workspaceID, search string, page, pageSize int32) ([]*TemplateRow, int64, error) {
	where := "WHERE workspace_id = $1"
	args := []any{workspaceID}
	idx := 2

	if search != "" {
		where += fmt.Sprintf(" AND name ILIKE $%d", idx)
		args = append(args, "%"+search+"%")
		idx++
	}

	var total int64
	if err := s.pool.QueryRow(ctx, "SELECT COUNT(*) FROM templates "+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("counting templates: %w", err)
	}

	offset := (page - 1) * pageSize
	query := fmt.Sprintf("SELECT %s FROM templates %s ORDER BY created_at DESC LIMIT $%d OFFSET $%d", templateCols, where, idx, idx+1)
	args = append(args, pageSize, offset)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("querying templates: %w", err)
	}
	defer rows.Close()

	var list []*TemplateRow
	for rows.Next() {
		t := &TemplateRow{}
		if err := rows.Scan(&t.ID, &t.WorkspaceID, &t.Name, &t.Body, &t.MediaURL, &t.MediaType, &t.Variables, &t.CreatedBy, &t.CreatedAt); err != nil {
			return nil, 0, err
		}
		list = append(list, t)
	}
	return list, total, rows.Err()
}

func (s *PgStore) UpdateTemplate(ctx context.Context, id, name, body, mediaURL, mediaType string, variables []byte) (*TemplateRow, error) {
	var setClauses []string
	var args []any
	idx := 1

	add := func(col string, val any) {
		setClauses = append(setClauses, fmt.Sprintf("%s = $%d", col, idx))
		args = append(args, val)
		idx++
	}

	if name != "" {
		add("name", name)
	}
	if body != "" {
		add("body", body)
		add("variables", variables)
	}
	if mediaURL != "" {
		add("media_url", mediaURL)
	}
	if mediaType != "" {
		add("media_type", mediaType)
	}

	if len(setClauses) == 0 {
		return s.GetTemplate(ctx, id)
	}

	query := fmt.Sprintf("UPDATE templates SET %s WHERE id = $%d RETURNING %s",
		strings.Join(setClauses, ", "), idx, templateCols)
	args = append(args, id)

	t, err := scanTemplate(s.pool.QueryRow(ctx, query, args...))
	if err != nil {
		return nil, fmt.Errorf("updating template: %w", err)
	}
	return t, nil
}

func (s *PgStore) DeleteTemplate(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, "DELETE FROM templates WHERE id=$1", id)
	if err != nil {
		return fmt.Errorf("deleting template: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PgStore) TemplateUsedByRunningCampaign(ctx context.Context, templateID string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM campaigns WHERE template_id=$1 AND status='running')",
		templateID).Scan(&exists)
	return exists, err
}

func (s *PgStore) TemplateUsedByActiveCampaign(ctx context.Context, templateID string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM campaigns WHERE template_id=$1 AND status IN ('draft','scheduled','running','paused'))",
		templateID).Scan(&exists)
	return exists, err
}

// ---------------------------------------------------------------------------
// Campaign CRUD
// ---------------------------------------------------------------------------

func (s *PgStore) CreateCampaign(ctx context.Context, r *CampaignRow) (*CampaignRow, error) {
	row := s.pool.QueryRow(ctx,
		`INSERT INTO campaigns (workspace_id, template_id, name, schedule_at, daily_cap_per_num, ban_pause_threshold, rotation_strategy, delay_min_ms, delay_max_ms, created_by)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING `+campaignCols,
		r.WorkspaceID, r.TemplateID, r.Name, r.ScheduleAt,
		r.DailyCapPerNum, r.BanPauseThreshold, r.RotationStrategy,
		r.DelayMinMs, r.DelayMaxMs, r.CreatedBy,
	)
	c, err := scanCampaign(row)
	if err != nil {
		return nil, fmt.Errorf("inserting campaign: %w", err)
	}
	return c, nil
}

func (s *PgStore) GetCampaign(ctx context.Context, id string) (*CampaignRow, error) {
	return scanCampaign(s.pool.QueryRow(ctx, "SELECT "+campaignCols+" FROM campaigns WHERE id=$1", id))
}

func (s *PgStore) ListCampaigns(ctx context.Context, workspaceID, status string, page, pageSize int32) ([]*CampaignRow, int64, error) {
	where := "WHERE workspace_id = $1"
	args := []any{workspaceID}
	idx := 2

	if status != "" {
		where += fmt.Sprintf(" AND status = $%d", idx)
		args = append(args, status)
		idx++
	}

	var total int64
	if err := s.pool.QueryRow(ctx, "SELECT COUNT(*) FROM campaigns "+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("counting campaigns: %w", err)
	}

	offset := (page - 1) * pageSize
	query := fmt.Sprintf("SELECT %s FROM campaigns %s ORDER BY created_at DESC LIMIT $%d OFFSET $%d", campaignCols, where, idx, idx+1)
	args = append(args, pageSize, offset)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("querying campaigns: %w", err)
	}
	list, err := scanCampaigns(rows)
	if err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

func (s *PgStore) UpdateCampaignStatus(ctx context.Context, id, status string, setStarted, setCompleted bool) (*CampaignRow, error) {
	set := "status = $1"
	args := []any{status}
	idx := 2

	if setStarted {
		set += fmt.Sprintf(", started_at = now()")
	}
	if setCompleted {
		set += fmt.Sprintf(", completed_at = now()")
	}

	query := fmt.Sprintf("UPDATE campaigns SET %s WHERE id = $%d RETURNING %s", set, idx, campaignCols)
	args = append(args, id)

	c, err := scanCampaign(s.pool.QueryRow(ctx, query, args...))
	if err != nil {
		return nil, fmt.Errorf("updating campaign status: %w", err)
	}
	return c, nil
}

// ---------------------------------------------------------------------------
// Campaign Numbers
// ---------------------------------------------------------------------------

func (s *PgStore) AddCampaignNumbers(ctx context.Context, campaignID string, waNumberIDs []string) error {
	for _, nid := range waNumberIDs {
		_, err := s.pool.Exec(ctx,
			"INSERT INTO campaign_numbers (campaign_id, wa_number_id) VALUES ($1, $2) ON CONFLICT DO NOTHING",
			campaignID, nid)
		if err != nil {
			return fmt.Errorf("adding campaign number %s: %w", nid, err)
		}
	}
	return nil
}

func (s *PgStore) RemoveCampaignNumbers(ctx context.Context, campaignID string, waNumberIDs []string) error {
	for _, nid := range waNumberIDs {
		_, err := s.pool.Exec(ctx,
			"DELETE FROM campaign_numbers WHERE campaign_id=$1 AND wa_number_id=$2",
			campaignID, nid)
		if err != nil {
			return fmt.Errorf("removing campaign number %s: %w", nid, err)
		}
	}
	return nil
}

func (s *PgStore) ListCampaignNumbers(ctx context.Context, campaignID string, page, pageSize int32) ([]*CampaignNumberRow, int64, error) {
	var total int64
	if err := s.pool.QueryRow(ctx, "SELECT COUNT(*) FROM campaign_numbers WHERE campaign_id=$1", campaignID).Scan(&total); err != nil {
		return nil, 0, err
	}

	offset := (page - 1) * pageSize
	rows, err := s.pool.Query(ctx,
		"SELECT campaign_id, wa_number_id, status, sent_count, failed_count FROM campaign_numbers WHERE campaign_id=$1 ORDER BY wa_number_id LIMIT $2 OFFSET $3",
		campaignID, pageSize, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var list []*CampaignNumberRow
	for rows.Next() {
		n := &CampaignNumberRow{}
		if err := rows.Scan(&n.CampaignID, &n.WaNumberID, &n.Status, &n.SentCount, &n.FailedCount); err != nil {
			return nil, 0, err
		}
		list = append(list, n)
	}
	return list, total, rows.Err()
}

func (s *PgStore) GetActiveCampaignNumbers(ctx context.Context, campaignID string) ([]*CampaignNumberRow, error) {
	rows, err := s.pool.Query(ctx,
		"SELECT campaign_id, wa_number_id, status, sent_count, failed_count FROM campaign_numbers WHERE campaign_id=$1 AND status='active'",
		campaignID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*CampaignNumberRow
	for rows.Next() {
		n := &CampaignNumberRow{}
		if err := rows.Scan(&n.CampaignID, &n.WaNumberID, &n.Status, &n.SentCount, &n.FailedCount); err != nil {
			return nil, err
		}
		list = append(list, n)
	}
	return list, rows.Err()
}

func (s *PgStore) UpdateCampaignNumberStatus(ctx context.Context, campaignID, waNumberID, status string) error {
	_, err := s.pool.Exec(ctx,
		"UPDATE campaign_numbers SET status=$1 WHERE campaign_id=$2 AND wa_number_id=$3",
		status, campaignID, waNumberID)
	return err
}

func (s *PgStore) IncrementNumberSentCount(ctx context.Context, campaignID, waNumberID string) error {
	_, err := s.pool.Exec(ctx,
		"UPDATE campaign_numbers SET sent_count = sent_count + 1 WHERE campaign_id=$1 AND wa_number_id=$2",
		campaignID, waNumberID)
	return err
}

// ---------------------------------------------------------------------------
// Campaign Contacts
// ---------------------------------------------------------------------------

func (s *PgStore) AddCampaignContacts(ctx context.Context, campaignID string, contactIDs []string) (int32, error) {
	var added int32
	for _, cid := range contactIDs {
		tag, err := s.pool.Exec(ctx,
			"INSERT INTO campaign_contacts (campaign_id, contact_id) VALUES ($1, $2) ON CONFLICT DO NOTHING",
			campaignID, cid)
		if err != nil {
			return added, fmt.Errorf("adding campaign contact %s: %w", cid, err)
		}
		added += int32(tag.RowsAffected())
	}
	return added, nil
}

func (s *PgStore) RemoveCampaignContacts(ctx context.Context, campaignID string, contactIDs []string) (int32, error) {
	var removed int32
	for _, cid := range contactIDs {
		tag, err := s.pool.Exec(ctx,
			"DELETE FROM campaign_contacts WHERE campaign_id=$1 AND contact_id=$2",
			campaignID, cid)
		if err != nil {
			return removed, fmt.Errorf("removing campaign contact %s: %w", cid, err)
		}
		removed += int32(tag.RowsAffected())
	}
	return removed, nil
}

func (s *PgStore) ListCampaignContacts(ctx context.Context, campaignID, status string, page, pageSize int32) ([]*CampaignContactJoinRow, int64, error) {
	where := "WHERE cc.campaign_id = $1"
	args := []any{campaignID}
	idx := 2

	if status != "" {
		where += fmt.Sprintf(" AND cc.status = $%d", idx)
		args = append(args, status)
		idx++
	}

	var total int64
	if err := s.pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM campaign_contacts cc "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	offset := (page - 1) * pageSize
	query := fmt.Sprintf(
		`SELECT cc.campaign_id, cc.contact_id, cc.wa_number_id, cc.status, cc.sent_at, cc.delivered_at, cc.failed_at, cc.error,
		        COALESCE(c.name,'') AS contact_name, COALESCE(c.phone,'') AS contact_phone
		 FROM campaign_contacts cc
		 LEFT JOIN contacts c ON c.id = cc.contact_id
		 %s ORDER BY cc.contact_id LIMIT $%d OFFSET $%d`, where, idx, idx+1)
	args = append(args, pageSize, offset)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var list []*CampaignContactJoinRow
	for rows.Next() {
		r := &CampaignContactJoinRow{}
		if err := rows.Scan(&r.CampaignID, &r.ContactID, &r.WaNumberID, &r.Status, &r.SentAt, &r.DeliveredAt, &r.FailedAt, &r.Error, &r.ContactName, &r.ContactPhone); err != nil {
			return nil, 0, err
		}
		list = append(list, r)
	}
	return list, total, rows.Err()
}

func (s *PgStore) GetPendingContacts(ctx context.Context, campaignID string, limit int32) ([]*PendingContactRow, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT cc.contact_id, COALESCE(c.phone,''), COALESCE(c.name,''),
		        COALESCE(
		            (SELECT json_object_agg(cf.key, cf.value)
		             FROM contact_custom_fields cf
		             WHERE cf.contact_id = c.id),
		            '{}'
		        )::text
		 FROM campaign_contacts cc
		 JOIN contacts c ON c.id = cc.contact_id
		 WHERE cc.campaign_id = $1 AND cc.status = 'pending'
		 ORDER BY cc.contact_id
		 LIMIT $2`,
		campaignID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*PendingContactRow
	for rows.Next() {
		r := &PendingContactRow{}
		var cfRaw []byte
		if err := rows.Scan(&r.ContactID, &r.Phone, &r.Name, &cfRaw); err != nil {
			return nil, err
		}
		r.CustomFields = make(map[string]string)
		_ = json.Unmarshal(cfRaw, &r.CustomFields)
		list = append(list, r)
	}
	return list, rows.Err()
}

func (s *PgStore) UpdateContactSent(ctx context.Context, campaignID, contactID, waNumberID string) error {
	_, err := s.pool.Exec(ctx,
		"UPDATE campaign_contacts SET status='sent', wa_number_id=$1, sent_at=now() WHERE campaign_id=$2 AND contact_id=$3",
		waNumberID, campaignID, contactID)
	return err
}

func (s *PgStore) SkipPendingContacts(ctx context.Context, campaignID string) (int32, error) {
	tag, err := s.pool.Exec(ctx,
		"UPDATE campaign_contacts SET status='skipped' WHERE campaign_id=$1 AND status='pending'",
		campaignID)
	if err != nil {
		return 0, err
	}
	return int32(tag.RowsAffected()), nil
}

// ---------------------------------------------------------------------------
// Campaign stats
// ---------------------------------------------------------------------------

func (s *PgStore) IncrementSentCount(ctx context.Context, campaignID string) error {
	_, err := s.pool.Exec(ctx, "UPDATE campaigns SET sent_count = sent_count + 1 WHERE id=$1", campaignID)
	return err
}

func (s *PgStore) IncrementFailedCount(ctx context.Context, campaignID string) error {
	_, err := s.pool.Exec(ctx, "UPDATE campaigns SET failed_count = failed_count + 1 WHERE id=$1", campaignID)
	return err
}

func (s *PgStore) IncrementRepliedCount(ctx context.Context, campaignID string) error {
	_, err := s.pool.Exec(ctx, "UPDATE campaigns SET replied_count = replied_count + 1 WHERE id=$1", campaignID)
	return err
}

func (s *PgStore) IncrementBannedCount(ctx context.Context, campaignID string) (int32, error) {
	var count int32
	err := s.pool.QueryRow(ctx,
		"UPDATE campaigns SET banned_count = banned_count + 1 WHERE id=$1 RETURNING banned_count",
		campaignID).Scan(&count)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	return count, err
}

func (s *PgStore) UpdateTotalContacts(ctx context.Context, campaignID string) error {
	_, err := s.pool.Exec(ctx,
		"UPDATE campaigns SET total_contacts = (SELECT COUNT(*) FROM campaign_contacts WHERE campaign_id=$1) WHERE id=$1",
		campaignID)
	return err
}

// ---------------------------------------------------------------------------
// Cross-service lookups
// ---------------------------------------------------------------------------

func (s *PgStore) GetWorkspaceTenantID(ctx context.Context, workspaceID string) (string, error) {
	var tenantID string
	err := s.pool.QueryRow(ctx, "SELECT tenant_id FROM workspaces WHERE id=$1", workspaceID).Scan(&tenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return tenantID, err
}

func (s *PgStore) FindContactInActiveCampaigns(ctx context.Context, senderPhone string) ([]CampaignContactMatch, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT cc.campaign_id, cc.contact_id
		 FROM campaign_contacts cc
		 JOIN campaigns c ON c.id = cc.campaign_id
		 JOIN contacts ct ON ct.id = cc.contact_id
		 WHERE ct.phone = $1 AND c.status = 'running'`,
		senderPhone)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var matches []CampaignContactMatch
	for rows.Next() {
		var m CampaignContactMatch
		if err := rows.Scan(&m.CampaignID, &m.ContactID); err != nil {
			return nil, err
		}
		matches = append(matches, m)
	}
	return matches, rows.Err()
}

func (s *PgStore) GetCampaignsUsingNumber(ctx context.Context, waNumberID string, statuses []string) ([]*CampaignRow, error) {
	query := fmt.Sprintf(
		`SELECT DISTINCT %s FROM campaigns c
		 JOIN campaign_numbers cn ON cn.campaign_id = c.id
		 WHERE cn.wa_number_id = $1 AND c.status = ANY($2)`, campaignCols)

	rows, err := s.pool.Query(ctx, query, waNumberID, statuses)
	if err != nil {
		return nil, err
	}
	return scanCampaigns(rows)
}

func (s *PgStore) CountCampaignNumbers(ctx context.Context, campaignID string) (int32, error) {
	var count int32
	err := s.pool.QueryRow(ctx, "SELECT COUNT(*) FROM campaign_numbers WHERE campaign_id=$1", campaignID).Scan(&count)
	return count, err
}

func (s *PgStore) CountCampaignContacts(ctx context.Context, campaignID string) (int32, error) {
	var count int32
	err := s.pool.QueryRow(ctx, "SELECT COUNT(*) FROM campaign_contacts WHERE campaign_id=$1", campaignID).Scan(&count)
	return count, err
}

func (s *PgStore) PopulateAllowlistFromCampaign(ctx context.Context, campaignID, workspaceID string) (int64, error) {
	// Insert all campaign contact phones into the allowlist (with + stripped).
	// $1=workspace_id, $2=campaign_id (text for source_id), $3=campaign_id (UUID for WHERE)
	tag, err := s.pool.Exec(ctx,
		`INSERT INTO contact_allowlist (workspace_id, phone, source, source_id)
		 SELECT $1, LTRIM(c.phone, '+'), 'campaign', $2::text
		 FROM campaign_contacts cc
		 JOIN contacts c ON c.id = cc.contact_id
		 WHERE cc.campaign_id = $3
		 ON CONFLICT (workspace_id, phone) DO NOTHING`,
		workspaceID, campaignID, campaignID,
	)
	if err != nil {
		return 0, fmt.Errorf("populating allowlist: %w", err)
	}
	return tag.RowsAffected(), nil
}
