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
	ErrNotFound         = errors.New("not found")
	ErrAlreadyAssigned  = errors.New("already assigned")
	ErrDuplicateShortcut = errors.New("duplicate shortcut")
)

// ---------------------------------------------------------------------------
// Row types
// ---------------------------------------------------------------------------

type ConversationRow struct {
	ID                    string
	WorkspaceID           string
	ContactID             string
	WaNumberID            string
	AssignedTo            *string
	Status                string
	LastMessageAt         time.Time
	CampaignID            *string
	FirstResponseTimeSecs int32
	CreatedAt             time.Time
	// Denormalized fields (populated by list queries via JOIN).
	ContactName        string
	ContactPhone       string
	LastMessagePreview string
	UnreadCount        int32
	// E3 chunk 2: channel discriminator + MBS-specific keys.
	// Channel is the textual form: "wa" or "mbs" (matches DB CHECK
	// constraint). Empty defaults to "wa" via COALESCE on read.
	Channel       string
	MbsSessionUID string // populated when Channel == "mbs"
	MbsThreadID   string
	MbsPageID     string
}

type MessageRow struct {
	ID               string
	ConversationID   string
	Direction        string
	ContentType      string
	Body             *string
	MediaURL         *string
	TemplateID       *string
	ResolvedVarsJSON *string
	WaMessageID      string
	Status           string
	CreatedAt        time.Time
	// E3 chunk 2: Meta MID for MBS messages (empty for WA).
	MbsMID string
}

type CannedResponseRow struct {
	ID          string
	WorkspaceID string
	Shortcut    string
	Body        string
	CreatedBy   *string
	CreatedAt   time.Time
}

type ContactRow struct {
	ID    string
	Phone string
	Name  string
}

type WaNumberRow struct {
	ID          string
	TenantID    string
	Phone       string
	DisplayName string
	JID         string
}

type SearchHitRow struct {
	MessageRow
	ConversationID string
	ContactName    string
	ContactPhone   string
	Highlight      string
}

type CampaignHistoryRow struct {
	CampaignID   string
	CampaignName string
	TemplateID   *string
	TemplateName string
	ResolvedBody string
	Status       string
	SentAt       *time.Time
	DeliveredAt  *time.Time
}

type AgentPerfRow struct {
	UserID                 string
	Email                  string
	AvgResponseTimeSecs    float32
	MedianResponseTimeSecs float32
	TotalConversations     int32
	ActiveConversations    int32
	MessagesSent           int32
}

// ---------------------------------------------------------------------------
// Store interface
// ---------------------------------------------------------------------------

type Store interface {
	// Conversations
	// E3 chunk 2: ListConversations gains `channel` filter ("" = both).
	ListConversations(ctx context.Context, workspaceID, status, assignedTo, waNumberID, search, channel string, sortOrder int32, page, pageSize int32) ([]*ConversationRow, int64, error)
	GetConversation(ctx context.Context, id string) (*ConversationRow, error)
	GetConversationContact(ctx context.Context, contactID string) (*ContactRow, error)
	GetConversationWaNumber(ctx context.Context, waNumberID string) (*WaNumberRow, error)
	ClaimConversation(ctx context.Context, id, userID string) (*ConversationRow, error)
	TransferConversation(ctx context.Context, id, toUserID string) (*ConversationRow, error)
	CloseConversation(ctx context.Context, id string) (*ConversationRow, error)

	// Messages
	ListMessages(ctx context.Context, conversationID string, beforeMessageID string, page, pageSize int32) ([]*MessageRow, bool, int64, error)
	CreateMessage(ctx context.Context, conversationID, direction, contentType string, body, mediaURL *string, waMessageID string) (*MessageRow, error)
	SearchMessages(ctx context.Context, workspaceID, query, conversationID string, fromDate, toDate *time.Time, page, pageSize int32) ([]*SearchHitRow, int64, error)
	UpdateMessageStatus(ctx context.Context, waMessageID, newStatus string) error
	GetMessageByWaMessageID(ctx context.Context, waMessageID string) (*MessageRow, error)

	// Conversation upsert for inbound messages
	FindOrCreateConversation(ctx context.Context, workspaceID, contactID, waNumberID string, campaignID *string) (*ConversationRow, bool, error)
	ReopenConversation(ctx context.Context, id string) error
	UpdateLastMessage(ctx context.Context, conversationID string, preview string) error
	SetFirstResponseTime(ctx context.Context, conversationID string, secs int32) error

	// E3 chunk 2: MBS channel parallels for the inbox-service MBS
	// inbound consumer (E3.3), send routing (E3.4), and outbound
	// status reconciliation (E3.4). All additive — WA paths untouched.
	FindOrCreateMbsConversation(ctx context.Context, workspaceID, contactID, mbsSessionUID, mbsThreadID, mbsPageID string) (*ConversationRow, bool, error)
	CreateMbsMessage(ctx context.Context, conversationID, direction, body, mbsMID string) (*MessageRow, error)
	GetMessageByMbsMID(ctx context.Context, mbsMID string) (*MessageRow, error)
	UpdateMbsMessageStatus(ctx context.Context, mbsMID, newStatus string) error

	// Contact lookup (cross-service read)
	FindContactByPhone(ctx context.Context, phone string) (*ContactRow, string, error) // returns contact + tenantID
	GetWorkspaceIDForWaNumber(ctx context.Context, waNumberID string) (string, string, error) // returns workspaceID, tenantID
	GetWorkspaceIDForMbsUid(ctx context.Context, uid int64) (string, string, error) // returns workspaceID, tenantID for an MBS session
	AutoCreateContact(ctx context.Context, tenantID, phone, name string) (*ContactRow, error) // auto-create from inbound message
	ClearAllConversations(ctx context.Context, workspaceID string) (int64, error) // delete all conversations + messages in workspace

	// Contact allowlist
	IsPhoneAllowlisted(ctx context.Context, workspaceID, phone string) (bool, error)
	AddToAllowlist(ctx context.Context, workspaceID, phone, source, sourceID string) error
	BulkAddToAllowlist(ctx context.Context, workspaceID string, phones []string, source, sourceID string) (int64, error)
	RemoveFromAllowlist(ctx context.Context, workspaceID, phone string) error
	ListAllowlist(ctx context.Context, workspaceID string, page, pageSize int32) ([]AllowlistEntry, int64, error)

	// Canned responses
	CreateCannedResponse(ctx context.Context, workspaceID, shortcut, body string, createdBy *string) (*CannedResponseRow, error)
	GetCannedResponse(ctx context.Context, id string) (*CannedResponseRow, error)
	ListCannedResponses(ctx context.Context, workspaceID, search string, page, pageSize int32) ([]*CannedResponseRow, int64, error)
	UpdateCannedResponse(ctx context.Context, id, shortcut, body string) (*CannedResponseRow, error)
	DeleteCannedResponse(ctx context.Context, id string) error

	// Campaign history (cross-service read)
	GetContactCampaignHistory(ctx context.Context, contactID string, page, pageSize int32) ([]*CampaignHistoryRow, int64, error)

	// Agent performance
	GetAgentPerformance(ctx context.Context, workspaceID, userID string, fromDate, toDate *time.Time) ([]*AgentPerfRow, error)
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
// Conversations
// ---------------------------------------------------------------------------

func (s *PgStore) ListConversations(ctx context.Context, workspaceID, statusFilter, assignedTo, waNumberID, search, channel string, sortOrder int32, page, pageSize int32) ([]*ConversationRow, int64, error) {
	where := "WHERE c.workspace_id = $1"
	args := []any{workspaceID}
	idx := 2

	if statusFilter != "" {
		where += fmt.Sprintf(" AND c.status = $%d", idx)
		args = append(args, statusFilter)
		idx++
	}
	if assignedTo != "" {
		where += fmt.Sprintf(" AND c.assigned_to = $%d", idx)
		args = append(args, assignedTo)
		idx++
	}
	if waNumberID != "" {
		where += fmt.Sprintf(" AND c.wa_number_id = $%d", idx)
		args = append(args, waNumberID)
		idx++
	}
	if search != "" {
		where += fmt.Sprintf(" AND (ct.name ILIKE $%d OR ct.phone ILIKE $%d)", idx, idx)
		args = append(args, "%"+search+"%")
		idx++
	}
	// E3 chunk 2: optional channel filter ("wa" | "mbs"; "" = both).
	if channel != "" {
		where += fmt.Sprintf(" AND c.channel = $%d", idx)
		args = append(args, channel)
		idx++
	}

	// Count query.
	countQuery := fmt.Sprintf(`
		SELECT COUNT(*)
		FROM conversations c
		LEFT JOIN contacts ct ON ct.id = c.contact_id
		%s`, where)

	var total int64
	if err := s.pool.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("counting conversations: %w", err)
	}

	// Sort order.
	orderBy := "c.last_message_at DESC"
	switch sortOrder {
	case 1: // LAST_MESSAGE_DESC
		orderBy = "c.last_message_at DESC"
	case 2: // LAST_MESSAGE_ASC
		orderBy = "c.last_message_at ASC"
	case 3: // CREATED_DESC
		orderBy = "c.created_at DESC"
	}

	offset := (page - 1) * pageSize
	query := fmt.Sprintf(`
		SELECT c.id, c.workspace_id, c.contact_id, COALESCE(c.wa_number_id, ''), c.assigned_to,
		       c.status, c.last_message_at, c.campaign_id, c.first_response_time_secs, c.created_at,
		       COALESCE(ct.name, ''), COALESCE(ct.phone, ''),
		       COALESCE((SELECT LEFT(body, 100) FROM messages WHERE conversation_id = c.id ORDER BY created_at DESC LIMIT 1), ''),
		       COALESCE((SELECT COUNT(*)::int FROM messages WHERE conversation_id = c.id AND direction = 'inbound' AND status = 'pending'), 0),
		       COALESCE(c.channel, 'wa'),
		       COALESCE(c.mbs_session_uid, ''),
		       COALESCE(c.mbs_thread_id, ''),
		       COALESCE(c.mbs_page_id, '')
		FROM conversations c
		LEFT JOIN contacts ct ON ct.id = c.contact_id
		%s
		ORDER BY %s
		LIMIT $%d OFFSET $%d`, where, orderBy, idx, idx+1)
	args = append(args, pageSize, offset)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("listing conversations: %w", err)
	}
	defer rows.Close()

	var result []*ConversationRow
	for rows.Next() {
		r := &ConversationRow{}
		if err := rows.Scan(
			&r.ID, &r.WorkspaceID, &r.ContactID, &r.WaNumberID, &r.AssignedTo,
			&r.Status, &r.LastMessageAt, &r.CampaignID, &r.FirstResponseTimeSecs, &r.CreatedAt,
			&r.ContactName, &r.ContactPhone,
			&r.LastMessagePreview, &r.UnreadCount,
			&r.Channel, &r.MbsSessionUID, &r.MbsThreadID, &r.MbsPageID,
		); err != nil {
			return nil, 0, fmt.Errorf("scanning conversation: %w", err)
		}
		result = append(result, r)
	}
	return result, total, rows.Err()
}

func (s *PgStore) GetConversation(ctx context.Context, id string) (*ConversationRow, error) {
	r := &ConversationRow{}
	err := s.pool.QueryRow(ctx, `
		SELECT c.id, c.workspace_id, c.contact_id, COALESCE(c.wa_number_id, ''), c.assigned_to,
		       c.status, c.last_message_at, c.campaign_id, c.first_response_time_secs, c.created_at,
		       COALESCE(ct.name, ''), COALESCE(ct.phone, ''), '', 0,
		       COALESCE(c.channel, 'wa'),
		       COALESCE(c.mbs_session_uid, ''),
		       COALESCE(c.mbs_thread_id, ''),
		       COALESCE(c.mbs_page_id, '')
		FROM conversations c
		LEFT JOIN contacts ct ON ct.id = c.contact_id
		WHERE c.id = $1`, id).Scan(
		&r.ID, &r.WorkspaceID, &r.ContactID, &r.WaNumberID, &r.AssignedTo,
		&r.Status, &r.LastMessageAt, &r.CampaignID, &r.FirstResponseTimeSecs, &r.CreatedAt,
		&r.ContactName, &r.ContactPhone,
		&r.LastMessagePreview, &r.UnreadCount,
		&r.Channel, &r.MbsSessionUID, &r.MbsThreadID, &r.MbsPageID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("getting conversation: %w", err)
	}
	return r, nil
}

func (s *PgStore) GetConversationContact(ctx context.Context, contactID string) (*ContactRow, error) {
	c := &ContactRow{}
	err := s.pool.QueryRow(ctx,
		"SELECT id, phone, COALESCE(name, '') FROM contacts WHERE id = $1", contactID,
	).Scan(&c.ID, &c.Phone, &c.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

func (s *PgStore) GetConversationWaNumber(ctx context.Context, waNumberID string) (*WaNumberRow, error) {
	w := &WaNumberRow{}
	err := s.pool.QueryRow(ctx,
		"SELECT id, tenant_id, phone, COALESCE(display_name, ''), jid FROM wa_numbers WHERE id = $1", waNumberID,
	).Scan(&w.ID, &w.TenantID, &w.Phone, &w.DisplayName, &w.JID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return w, err
}

func (s *PgStore) ClaimConversation(ctx context.Context, id, userID string) (*ConversationRow, error) {
	r := &ConversationRow{}
	err := s.pool.QueryRow(ctx, `
		UPDATE conversations
		SET status = 'assigned', assigned_to = $2
		WHERE id = $1 AND status = 'unassigned'
		RETURNING id, workspace_id, contact_id, wa_number_id, assigned_to,
		          status, last_message_at, campaign_id, first_response_time_secs, created_at`,
		id, userID,
	).Scan(
		&r.ID, &r.WorkspaceID, &r.ContactID, &r.WaNumberID, &r.AssignedTo,
		&r.Status, &r.LastMessageAt, &r.CampaignID, &r.FirstResponseTimeSecs, &r.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		// Check if it exists at all to distinguish not-found from already-assigned.
		var status string
		checkErr := s.pool.QueryRow(ctx, "SELECT status FROM conversations WHERE id = $1", id).Scan(&status)
		if errors.Is(checkErr, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, ErrAlreadyAssigned
	}
	if err != nil {
		return nil, fmt.Errorf("claiming conversation: %w", err)
	}
	return r, nil
}

func (s *PgStore) TransferConversation(ctx context.Context, id, toUserID string) (*ConversationRow, error) {
	r := &ConversationRow{}
	err := s.pool.QueryRow(ctx, `
		UPDATE conversations SET assigned_to = $2
		WHERE id = $1
		RETURNING id, workspace_id, contact_id, wa_number_id, assigned_to,
		          status, last_message_at, campaign_id, first_response_time_secs, created_at`,
		id, toUserID,
	).Scan(
		&r.ID, &r.WorkspaceID, &r.ContactID, &r.WaNumberID, &r.AssignedTo,
		&r.Status, &r.LastMessageAt, &r.CampaignID, &r.FirstResponseTimeSecs, &r.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("transferring conversation: %w", err)
	}
	return r, nil
}

func (s *PgStore) CloseConversation(ctx context.Context, id string) (*ConversationRow, error) {
	r := &ConversationRow{}
	err := s.pool.QueryRow(ctx, `
		UPDATE conversations SET status = 'closed', assigned_to = NULL
		WHERE id = $1
		RETURNING id, workspace_id, contact_id, wa_number_id, assigned_to,
		          status, last_message_at, campaign_id, first_response_time_secs, created_at`,
		id,
	).Scan(
		&r.ID, &r.WorkspaceID, &r.ContactID, &r.WaNumberID, &r.AssignedTo,
		&r.Status, &r.LastMessageAt, &r.CampaignID, &r.FirstResponseTimeSecs, &r.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("closing conversation: %w", err)
	}
	return r, nil
}

// ---------------------------------------------------------------------------
// Messages
// ---------------------------------------------------------------------------

func (s *PgStore) ListMessages(ctx context.Context, conversationID, beforeMessageID string, page, pageSize int32) ([]*MessageRow, bool, int64, error) {
	args := []any{conversationID}
	idx := 2

	where := "WHERE conversation_id = $1"

	if beforeMessageID != "" {
		where += fmt.Sprintf(" AND created_at < (SELECT created_at FROM messages WHERE id = $%d)", idx)
		args = append(args, beforeMessageID)
		idx++
	}

	// Count total for pagination.
	var total int64
	if err := s.pool.QueryRow(ctx, "SELECT COUNT(*) FROM messages "+where, args...).Scan(&total); err != nil {
		return nil, false, 0, fmt.Errorf("counting messages: %w", err)
	}

	// Fetch one extra to determine has_more.
	limit := pageSize + 1
	if beforeMessageID == "" {
		offset := (page - 1) * pageSize
		args = append(args, limit, offset)
	} else {
		args = append(args, limit)
	}

	var query string
	if beforeMessageID == "" {
		query = fmt.Sprintf(`
			SELECT id, conversation_id, direction, content_type, body, media_url,
			       template_id, resolved_vars_json, wa_message_id, status, created_at,
			       COALESCE(mbs_mid, '')
			FROM messages %s
			ORDER BY created_at DESC
			LIMIT $%d OFFSET $%d`, where, idx, idx+1)
	} else {
		query = fmt.Sprintf(`
			SELECT id, conversation_id, direction, content_type, body, media_url,
			       template_id, resolved_vars_json, wa_message_id, status, created_at,
			       COALESCE(mbs_mid, '')
			FROM messages %s
			ORDER BY created_at DESC
			LIMIT $%d`, where, idx)
	}

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, false, 0, fmt.Errorf("listing messages: %w", err)
	}
	defer rows.Close()

	var result []*MessageRow
	for rows.Next() {
		m := &MessageRow{}
		if err := rows.Scan(
			&m.ID, &m.ConversationID, &m.Direction, &m.ContentType, &m.Body, &m.MediaURL,
			&m.TemplateID, &m.ResolvedVarsJSON, &m.WaMessageID, &m.Status, &m.CreatedAt,
			&m.MbsMID,
		); err != nil {
			return nil, false, 0, fmt.Errorf("scanning message: %w", err)
		}
		result = append(result, m)
	}

	hasMore := len(result) > int(pageSize)
	if hasMore {
		result = result[:pageSize]
	}

	return result, hasMore, total, rows.Err()
}

func (s *PgStore) CreateMessage(ctx context.Context, conversationID, direction, contentType string, body, mediaURL *string, waMessageID string) (*MessageRow, error) {
	m := &MessageRow{}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO messages (conversation_id, direction, content_type, body, media_url, wa_message_id, status)
		VALUES ($1, $2, $3, $4, $5, $6, 'pending')
		RETURNING id, conversation_id, direction, content_type, body, media_url,
		          template_id, resolved_vars_json, wa_message_id, status, created_at,
		          COALESCE(mbs_mid, '')`,
		conversationID, direction, contentType, body, mediaURL, waMessageID,
	).Scan(
		&m.ID, &m.ConversationID, &m.Direction, &m.ContentType, &m.Body, &m.MediaURL,
		&m.TemplateID, &m.ResolvedVarsJSON, &m.WaMessageID, &m.Status, &m.CreatedAt,
		&m.MbsMID,
	)
	if err != nil {
		return nil, fmt.Errorf("creating message: %w", err)
	}
	return m, nil
}

func (s *PgStore) SearchMessages(ctx context.Context, workspaceID, query, conversationID string, fromDate, toDate *time.Time, page, pageSize int32) ([]*SearchHitRow, int64, error) {
	where := "WHERE c.workspace_id = $1 AND m.body IS NOT NULL AND to_tsvector('simple', m.body) @@ plainto_tsquery('simple', $2)"
	args := []any{workspaceID, query}
	idx := 3

	if conversationID != "" {
		where += fmt.Sprintf(" AND m.conversation_id = $%d", idx)
		args = append(args, conversationID)
		idx++
	}
	if fromDate != nil {
		where += fmt.Sprintf(" AND m.created_at >= $%d", idx)
		args = append(args, *fromDate)
		idx++
	}
	if toDate != nil {
		where += fmt.Sprintf(" AND m.created_at <= $%d", idx)
		args = append(args, *toDate)
		idx++
	}

	var total int64
	countQuery := fmt.Sprintf(`
		SELECT COUNT(*)
		FROM messages m
		JOIN conversations c ON c.id = m.conversation_id
		LEFT JOIN contacts ct ON ct.id = c.contact_id
		%s`, where)
	if err := s.pool.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("counting search results: %w", err)
	}

	offset := (page - 1) * pageSize
	searchQuery := fmt.Sprintf(`
		SELECT m.id, m.conversation_id, m.direction, m.content_type, m.body, m.media_url,
		       m.template_id, m.resolved_vars_json, m.wa_message_id, m.status, m.created_at,
		       COALESCE(m.mbs_mid, ''),
		       c.id, COALESCE(ct.name, ''), COALESCE(ct.phone, ''),
		       ts_headline('simple', COALESCE(m.body, ''), plainto_tsquery('simple', $2),
		           'StartSel=<mark>, StopSel=</mark>, MaxWords=35, MinWords=15')
		FROM messages m
		JOIN conversations c ON c.id = m.conversation_id
		LEFT JOIN contacts ct ON ct.id = c.contact_id
		%s
		ORDER BY m.created_at DESC
		LIMIT $%d OFFSET $%d`, where, idx, idx+1)
	args = append(args, pageSize, offset)

	rows, err := s.pool.Query(ctx, searchQuery, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("searching messages: %w", err)
	}
	defer rows.Close()

	var result []*SearchHitRow
	for rows.Next() {
		h := &SearchHitRow{}
		if err := rows.Scan(
			&h.ID, &h.MessageRow.ConversationID, &h.Direction, &h.ContentType, &h.Body, &h.MediaURL,
			&h.TemplateID, &h.ResolvedVarsJSON, &h.WaMessageID, &h.Status, &h.MessageRow.CreatedAt,
			&h.MbsMID,
			&h.ConversationID, &h.ContactName, &h.ContactPhone, &h.Highlight,
		); err != nil {
			return nil, 0, fmt.Errorf("scanning search hit: %w", err)
		}
		result = append(result, h)
	}
	return result, total, rows.Err()
}

func (s *PgStore) UpdateMessageStatus(ctx context.Context, waMessageID, newStatus string) error {
	tag, err := s.pool.Exec(ctx,
		"UPDATE messages SET status = $2 WHERE wa_message_id = $1 AND wa_message_id != ''",
		waMessageID, newStatus,
	)
	if err != nil {
		return fmt.Errorf("updating message status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PgStore) GetMessageByWaMessageID(ctx context.Context, waMessageID string) (*MessageRow, error) {
	m := &MessageRow{}
	err := s.pool.QueryRow(ctx, `
		SELECT id, conversation_id, direction, content_type, body, media_url,
		       template_id, resolved_vars_json, wa_message_id, status, created_at,
		       COALESCE(mbs_mid, '')
		FROM messages WHERE wa_message_id = $1 AND wa_message_id != ''`, waMessageID,
	).Scan(
		&m.ID, &m.ConversationID, &m.Direction, &m.ContentType, &m.Body, &m.MediaURL,
		&m.TemplateID, &m.ResolvedVarsJSON, &m.WaMessageID, &m.Status, &m.CreatedAt,
		&m.MbsMID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("getting message by wa_message_id: %w", err)
	}
	return m, nil
}

// ---------------------------------------------------------------------------
// Conversation upsert (for inbound message processing)
// ---------------------------------------------------------------------------

func (s *PgStore) FindOrCreateConversation(ctx context.Context, workspaceID, contactID, waNumberID string, campaignID *string) (*ConversationRow, bool, error) {
	r := &ConversationRow{}
	// E3 chunk 2: ON CONFLICT target now references the partial unique
	// index uq_conversations_wa (workspace_id, contact_id, wa_number_id)
	// WHERE channel = 'wa'. Postgres requires the same WHERE clause on
	// the conflict_target for partial-index inference. INSERT explicitly
	// sets channel='wa' for clarity (default would do it too).
	err := s.pool.QueryRow(ctx, `
		INSERT INTO conversations (workspace_id, contact_id, wa_number_id, campaign_id, channel, status, last_message_at)
		VALUES ($1, $2, $3, $4, 'wa', 'unassigned', now())
		ON CONFLICT (workspace_id, contact_id, wa_number_id) WHERE channel = 'wa'
		DO UPDATE SET last_message_at = now()
		RETURNING id, workspace_id, contact_id, COALESCE(wa_number_id, ''), assigned_to,
		          status, last_message_at, campaign_id, first_response_time_secs, created_at,
		          COALESCE(channel, 'wa'),
		          COALESCE(mbs_session_uid, ''),
		          COALESCE(mbs_thread_id, ''),
		          COALESCE(mbs_page_id, '')`,
		workspaceID, contactID, waNumberID, campaignID,
	).Scan(
		&r.ID, &r.WorkspaceID, &r.ContactID, &r.WaNumberID, &r.AssignedTo,
		&r.Status, &r.LastMessageAt, &r.CampaignID, &r.FirstResponseTimeSecs, &r.CreatedAt,
		&r.Channel, &r.MbsSessionUID, &r.MbsThreadID, &r.MbsPageID,
	)
	if err != nil {
		return nil, false, fmt.Errorf("upserting conversation: %w", err)
	}

	// The ON CONFLICT path doesn't update created_at, so a freshly
	// inserted row has created_at very close to now(). For our NATS
	// consumer, "isNew" matters for deciding whether to send a
	// notification.
	isNew := r.CreatedAt.After(time.Now().Add(-2 * time.Second))

	return r, isNew, nil
}

func (s *PgStore) ReopenConversation(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx,
		"UPDATE conversations SET status = 'unassigned', assigned_to = NULL WHERE id = $1 AND status = 'closed'", id)
	return err
}

func (s *PgStore) UpdateLastMessage(ctx context.Context, conversationID, preview string) error {
	_, err := s.pool.Exec(ctx,
		"UPDATE conversations SET last_message_at = now() WHERE id = $1", conversationID)
	return err
}

func (s *PgStore) SetFirstResponseTime(ctx context.Context, conversationID string, secs int32) error {
	_, err := s.pool.Exec(ctx,
		"UPDATE conversations SET first_response_time_secs = $2 WHERE id = $1 AND first_response_time_secs = 0",
		conversationID, secs)
	return err
}

// ---------------------------------------------------------------------------
// E3 chunk 2: MBS channel parallels
// ---------------------------------------------------------------------------

// FindOrCreateMbsConversation upserts a Conversation row for an MBS
// thread keyed by (workspace_id, mbs_session_uid, mbs_thread_id) via
// the partial unique index uq_conversations_mbs.
func (s *PgStore) FindOrCreateMbsConversation(
	ctx context.Context,
	workspaceID, contactID, mbsSessionUID, mbsThreadID, mbsPageID string,
) (*ConversationRow, bool, error) {
	if mbsSessionUID == "" || mbsThreadID == "" {
		return nil, false, fmt.Errorf("FindOrCreateMbsConversation: mbsSessionUID and mbsThreadID required")
	}
	r := &ConversationRow{}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO conversations
		  (workspace_id, contact_id, channel,
		   mbs_session_uid, mbs_thread_id, mbs_page_id,
		   status, last_message_at)
		VALUES ($1, $2, 'mbs', $3, $4, $5, 'unassigned', now())
		ON CONFLICT (workspace_id, mbs_session_uid, mbs_thread_id) WHERE channel = 'mbs'
		DO UPDATE SET last_message_at = now(),
		              mbs_page_id = COALESCE(NULLIF(EXCLUDED.mbs_page_id, ''), conversations.mbs_page_id)
		RETURNING id, workspace_id, contact_id,
		          COALESCE(wa_number_id, ''), assigned_to,
		          status, last_message_at, campaign_id,
		          first_response_time_secs, created_at,
		          COALESCE(channel, 'mbs'),
		          COALESCE(mbs_session_uid, ''),
		          COALESCE(mbs_thread_id, ''),
		          COALESCE(mbs_page_id, '')`,
		workspaceID, contactID, mbsSessionUID, mbsThreadID, mbsPageID,
	).Scan(
		&r.ID, &r.WorkspaceID, &r.ContactID, &r.WaNumberID, &r.AssignedTo,
		&r.Status, &r.LastMessageAt, &r.CampaignID, &r.FirstResponseTimeSecs,
		&r.CreatedAt,
		&r.Channel, &r.MbsSessionUID, &r.MbsThreadID, &r.MbsPageID,
	)
	if err != nil {
		return nil, false, fmt.Errorf("upserting mbs conversation: %w", err)
	}
	isNew := r.CreatedAt.After(time.Now().Add(-2 * time.Second))
	return r, isNew, nil
}

// CreateMbsMessage inserts a TEXT message for the given conversation,
// keyed on mbs_mid (Meta MID). Inbound messages start at 'delivered'
// (Meta wouldn't have delivered them otherwise); outbound start at
// 'pending' and reconcile via the chunk-4 outbound consumer.
func (s *PgStore) CreateMbsMessage(
	ctx context.Context,
	conversationID, direction, body, mbsMID string,
) (*MessageRow, error) {
	if mbsMID == "" {
		return nil, fmt.Errorf("CreateMbsMessage: mbsMID required")
	}
	var bodyPtr *string
	if body != "" {
		bodyPtr = &body
	}
	initial := "pending"
	if direction == "inbound" {
		initial = "delivered"
	}
	// ON CONFLICT (mbs_mid) handles Meta retransmits / consumer redelivery.
	// The partial unique index uq_messages_mbs_mid lives on (mbs_mid) WHERE mbs_mid != ''
	// so we can target it with ON CONFLICT (mbs_mid) WHERE mbs_mid != ''.
	// If a row already exists, INSERT returns no rows and we SELECT the existing one.
	m := &MessageRow{}
	err := s.pool.QueryRow(ctx, `
		WITH ins AS (
		  INSERT INTO messages
		    (conversation_id, direction, content_type, body, mbs_mid, status)
		  VALUES ($1, $2, 'text', $3, $4, $5)
		  ON CONFLICT (mbs_mid) WHERE mbs_mid != ''
		  DO NOTHING
		  RETURNING id, conversation_id, direction, content_type, body, media_url,
		            template_id, resolved_vars_json, wa_message_id, status, created_at,
		            COALESCE(mbs_mid, '') AS mbs_mid
		)
		SELECT * FROM ins
		UNION ALL
		SELECT id, conversation_id, direction, content_type, body, media_url,
		       template_id, resolved_vars_json, wa_message_id, status, created_at,
		       COALESCE(mbs_mid, '')
		FROM messages WHERE mbs_mid = $4 AND mbs_mid != ''
		LIMIT 1`,
		conversationID, direction, bodyPtr, mbsMID, initial,
	).Scan(
		&m.ID, &m.ConversationID, &m.Direction, &m.ContentType, &m.Body, &m.MediaURL,
		&m.TemplateID, &m.ResolvedVarsJSON, &m.WaMessageID, &m.Status, &m.CreatedAt,
		&m.MbsMID,
	)
	if err != nil {
		return nil, fmt.Errorf("creating mbs message: %w", err)
	}
	return m, nil
}

// GetMessageByMbsMID looks up a message by its Meta MID. Skips rows
// with empty mbs_mid via the WHERE guard so the partial index is hit.
func (s *PgStore) GetMessageByMbsMID(ctx context.Context, mbsMID string) (*MessageRow, error) {
	if mbsMID == "" {
		return nil, ErrNotFound
	}
	m := &MessageRow{}
	err := s.pool.QueryRow(ctx, `
		SELECT id, conversation_id, direction, content_type, body, media_url,
		       template_id, resolved_vars_json, wa_message_id, status, created_at,
		       COALESCE(mbs_mid, '')
		FROM messages WHERE mbs_mid = $1 AND mbs_mid != ''`, mbsMID,
	).Scan(
		&m.ID, &m.ConversationID, &m.Direction, &m.ContentType, &m.Body, &m.MediaURL,
		&m.TemplateID, &m.ResolvedVarsJSON, &m.WaMessageID, &m.Status, &m.CreatedAt,
		&m.MbsMID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("getting message by mbs_mid: %w", err)
	}
	return m, nil
}

// UpdateMbsMessageStatus transitions a message identified by Meta MID
// to newStatus. Returns ErrNotFound if no row matches (caller decides
// whether that's a real error or expected — e.g., outbound consumer
// for a manual-send message that was never persisted).
func (s *PgStore) UpdateMbsMessageStatus(ctx context.Context, mbsMID, newStatus string) error {
	if mbsMID == "" {
		return ErrNotFound
	}
	tag, err := s.pool.Exec(ctx,
		"UPDATE messages SET status = $2 WHERE mbs_mid = $1 AND mbs_mid != ''",
		mbsMID, newStatus,
	)
	if err != nil {
		return fmt.Errorf("updating mbs message status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------------------------------------------------------------------
// Contact lookup (cross-service read)
// ---------------------------------------------------------------------------

func (s *PgStore) FindContactByPhone(ctx context.Context, phone string) (*ContactRow, string, error) {
	c := &ContactRow{}
	var tenantID string
	err := s.pool.QueryRow(ctx,
		"SELECT id, phone, COALESCE(name, ''), tenant_id FROM contacts WHERE phone = $1 LIMIT 1", phone,
	).Scan(&c.ID, &c.Phone, &c.Name, &tenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", ErrNotFound
	}
	if err != nil {
		return nil, "", fmt.Errorf("finding contact by phone: %w", err)
	}
	return c, tenantID, nil
}

func (s *PgStore) GetWorkspaceIDForWaNumber(ctx context.Context, waNumberID string) (string, string, error) {
	var workspaceID, tenantID string
	err := s.pool.QueryRow(ctx, `
		SELECT wnw.workspace_id, wn.tenant_id
		FROM wa_number_workspaces wnw
		JOIN wa_numbers wn ON wn.id = wnw.wa_number_id
		WHERE wnw.wa_number_id = $1
		LIMIT 1`, waNumberID,
	).Scan(&workspaceID, &tenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrNotFound
	}
	if err != nil {
		return "", "", fmt.Errorf("getting workspace for wa_number: %w", err)
	}
	return workspaceID, tenantID, nil
}

// GetWorkspaceIDForMbsUid resolves (workspaceID, tenantID) for an MBS
// session uid by joining mbs_sessions ↔ workspaces. Both tables live
// in the same hermes DB so this is in-process — no cross-service RPC.
//
// Multi-workspace tenants: returns the workspace with the smallest
// created_at. Today, gateway seeds one workspace per tenant. If multi-
// workspace tenants land later, we'd add an explicit mbs_session_workspaces
// mapping table — carrying gap E3.3-G2.
func (s *PgStore) GetWorkspaceIDForMbsUid(ctx context.Context, uid int64) (string, string, error) {
	var workspaceID, tenantID string
	err := s.pool.QueryRow(ctx, `
		SELECT w.id, w.tenant_id
		FROM mbs_sessions s
		JOIN workspaces  w ON w.tenant_id = s.tenant_id
		WHERE s.uid = $1
		ORDER BY w.created_at ASC
		LIMIT 1`, uid,
	).Scan(&workspaceID, &tenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrNotFound
	}
	if err != nil {
		return "", "", fmt.Errorf("getting workspace for mbs uid: %w", err)
	}
	return workspaceID, tenantID, nil
}

// ---------------------------------------------------------------------------
// Canned Responses
// ---------------------------------------------------------------------------

func (s *PgStore) CreateCannedResponse(ctx context.Context, workspaceID, shortcut, body string, createdBy *string) (*CannedResponseRow, error) {
	r := &CannedResponseRow{}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO canned_responses (workspace_id, shortcut, body, created_by)
		VALUES ($1, $2, $3, $4)
		RETURNING id, workspace_id, shortcut, body, created_by, created_at`,
		workspaceID, shortcut, body, createdBy,
	).Scan(&r.ID, &r.WorkspaceID, &r.Shortcut, &r.Body, &r.CreatedBy, &r.CreatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") || strings.Contains(err.Error(), "unique constraint") {
			return nil, ErrDuplicateShortcut
		}
		return nil, fmt.Errorf("creating canned response: %w", err)
	}
	return r, nil
}

func (s *PgStore) GetCannedResponse(ctx context.Context, id string) (*CannedResponseRow, error) {
	r := &CannedResponseRow{}
	err := s.pool.QueryRow(ctx,
		"SELECT id, workspace_id, shortcut, body, created_by, created_at FROM canned_responses WHERE id = $1", id,
	).Scan(&r.ID, &r.WorkspaceID, &r.Shortcut, &r.Body, &r.CreatedBy, &r.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

func (s *PgStore) ListCannedResponses(ctx context.Context, workspaceID, search string, page, pageSize int32) ([]*CannedResponseRow, int64, error) {
	where := "WHERE workspace_id = $1"
	args := []any{workspaceID}
	idx := 2

	if search != "" {
		where += fmt.Sprintf(" AND (shortcut ILIKE $%d OR body ILIKE $%d)", idx, idx)
		args = append(args, "%"+search+"%")
		idx++
	}

	var total int64
	if err := s.pool.QueryRow(ctx, "SELECT COUNT(*) FROM canned_responses "+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("counting canned responses: %w", err)
	}

	offset := (page - 1) * pageSize
	query := fmt.Sprintf(`
		SELECT id, workspace_id, shortcut, body, created_by, created_at
		FROM canned_responses %s
		ORDER BY shortcut
		LIMIT $%d OFFSET $%d`, where, idx, idx+1)
	args = append(args, pageSize, offset)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("listing canned responses: %w", err)
	}
	defer rows.Close()

	var result []*CannedResponseRow
	for rows.Next() {
		r := &CannedResponseRow{}
		if err := rows.Scan(&r.ID, &r.WorkspaceID, &r.Shortcut, &r.Body, &r.CreatedBy, &r.CreatedAt); err != nil {
			return nil, 0, fmt.Errorf("scanning canned response: %w", err)
		}
		result = append(result, r)
	}
	return result, total, rows.Err()
}

func (s *PgStore) UpdateCannedResponse(ctx context.Context, id, shortcut, body string) (*CannedResponseRow, error) {
	var setClauses []string
	var args []any
	idx := 1

	if shortcut != "" {
		setClauses = append(setClauses, fmt.Sprintf("shortcut = $%d", idx))
		args = append(args, shortcut)
		idx++
	}
	if body != "" {
		setClauses = append(setClauses, fmt.Sprintf("body = $%d", idx))
		args = append(args, body)
		idx++
	}

	if len(setClauses) == 0 {
		return s.GetCannedResponse(ctx, id)
	}

	query := fmt.Sprintf("UPDATE canned_responses SET %s WHERE id = $%d RETURNING id, workspace_id, shortcut, body, created_by, created_at",
		strings.Join(setClauses, ", "), idx)
	args = append(args, id)

	r := &CannedResponseRow{}
	err := s.pool.QueryRow(ctx, query, args...).Scan(
		&r.ID, &r.WorkspaceID, &r.Shortcut, &r.Body, &r.CreatedBy, &r.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") || strings.Contains(err.Error(), "unique constraint") {
			return nil, ErrDuplicateShortcut
		}
		return nil, fmt.Errorf("updating canned response: %w", err)
	}
	return r, nil
}

func (s *PgStore) DeleteCannedResponse(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, "DELETE FROM canned_responses WHERE id = $1", id)
	if err != nil {
		return fmt.Errorf("deleting canned response: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------------------------------------------------------------------
// Campaign history (cross-service read)
// ---------------------------------------------------------------------------

func (s *PgStore) GetContactCampaignHistory(ctx context.Context, contactID string, page, pageSize int32) ([]*CampaignHistoryRow, int64, error) {
	var total int64
	if err := s.pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM campaign_contacts WHERE contact_id = $1", contactID,
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("counting campaign history: %w", err)
	}

	offset := (page - 1) * pageSize
	rows, err := s.pool.Query(ctx, `
		SELECT c.id, c.name, cc.wa_number_id, COALESCE(t.name, ''),
		       COALESCE(cc.resolved_body, ''), cc.status, cc.sent_at, cc.delivered_at
		FROM campaign_contacts cc
		JOIN campaigns c ON c.id = cc.campaign_id
		LEFT JOIN templates t ON t.id = c.template_id
		WHERE cc.contact_id = $1
		ORDER BY cc.sent_at DESC NULLS LAST
		LIMIT $2 OFFSET $3`, contactID, pageSize, offset,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("querying campaign history: %w", err)
	}
	defer rows.Close()

	var result []*CampaignHistoryRow
	for rows.Next() {
		h := &CampaignHistoryRow{}
		if err := rows.Scan(
			&h.CampaignID, &h.CampaignName, &h.TemplateID, &h.TemplateName,
			&h.ResolvedBody, &h.Status, &h.SentAt, &h.DeliveredAt,
		); err != nil {
			return nil, 0, fmt.Errorf("scanning campaign history: %w", err)
		}
		result = append(result, h)
	}
	return result, total, rows.Err()
}

// ---------------------------------------------------------------------------
// Agent performance
// ---------------------------------------------------------------------------

func (s *PgStore) GetAgentPerformance(ctx context.Context, workspaceID, userID string, fromDate, toDate *time.Time) ([]*AgentPerfRow, error) {
	where := "WHERE c.workspace_id = $1 AND c.assigned_to IS NOT NULL"
	args := []any{workspaceID}
	idx := 2

	if userID != "" {
		where += fmt.Sprintf(" AND c.assigned_to = $%d", idx)
		args = append(args, userID)
		idx++
	}
	if fromDate != nil {
		where += fmt.Sprintf(" AND c.created_at >= $%d", idx)
		args = append(args, *fromDate)
		idx++
	}
	if toDate != nil {
		where += fmt.Sprintf(" AND c.created_at <= $%d", idx)
		args = append(args, *toDate)
		idx++
	}

	query := fmt.Sprintf(`
		SELECT
			c.assigned_to,
			COALESCE(u.email, ''),
			COALESCE(AVG(CASE WHEN c.first_response_time_secs > 0 THEN c.first_response_time_secs END), 0)::float4,
			COALESCE(PERCENTILE_CONT(0.5) WITHIN GROUP (ORDER BY c.first_response_time_secs)
				FILTER (WHERE c.first_response_time_secs > 0), 0)::float4,
			COUNT(*)::int,
			COUNT(*) FILTER (WHERE c.status = 'assigned')::int,
			COALESCE((SELECT COUNT(*)::int FROM messages m
				WHERE m.conversation_id = ANY(ARRAY_AGG(c.id))
				AND m.direction = 'outbound'), 0)
		FROM conversations c
		LEFT JOIN users u ON u.id = c.assigned_to
		%s
		GROUP BY c.assigned_to, u.email`, where)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying agent performance: %w", err)
	}
	defer rows.Close()

	var result []*AgentPerfRow
	for rows.Next() {
		a := &AgentPerfRow{}
		if err := rows.Scan(
			&a.UserID, &a.Email,
			&a.AvgResponseTimeSecs, &a.MedianResponseTimeSecs,
			&a.TotalConversations, &a.ActiveConversations, &a.MessagesSent,
		); err != nil {
			return nil, fmt.Errorf("scanning agent perf: %w", err)
		}
		result = append(result, a)
	}
	return result, rows.Err()
}

type AllowlistEntry struct {
	WorkspaceID string
	Phone       string
	Source      string
	SourceID    string
	AddedAt     time.Time
}

func (s *PgStore) IsPhoneAllowlisted(ctx context.Context, workspaceID, phone string) (bool, error) {
	var exists bool
	// Check both with and without '+' prefix.
	err := s.pool.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM contact_allowlist WHERE workspace_id=$1 AND (phone=$2 OR phone=$3))",
		workspaceID, phone, "+"+phone,
	).Scan(&exists)
	return exists, err
}

func (s *PgStore) AddToAllowlist(ctx context.Context, workspaceID, phone, source, sourceID string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO contact_allowlist (workspace_id, phone, source, source_id)
		 VALUES ($1, $2, $3, $4) ON CONFLICT (workspace_id, phone) DO NOTHING`,
		workspaceID, phone, source, sourceID,
	)
	return err
}

func (s *PgStore) BulkAddToAllowlist(ctx context.Context, workspaceID string, phones []string, source, sourceID string) (int64, error) {
	var count int64
	for _, phone := range phones {
		tag, err := s.pool.Exec(ctx,
			`INSERT INTO contact_allowlist (workspace_id, phone, source, source_id)
			 VALUES ($1, $2, $3, $4) ON CONFLICT (workspace_id, phone) DO NOTHING`,
			workspaceID, phone, source, sourceID,
		)
		if err != nil {
			return count, err
		}
		count += tag.RowsAffected()
	}
	return count, nil
}

func (s *PgStore) RemoveFromAllowlist(ctx context.Context, workspaceID, phone string) error {
	_, err := s.pool.Exec(ctx,
		"DELETE FROM contact_allowlist WHERE workspace_id=$1 AND phone=$2",
		workspaceID, phone,
	)
	return err
}

func (s *PgStore) ListAllowlist(ctx context.Context, workspaceID string, page, pageSize int32) ([]AllowlistEntry, int64, error) {
	var total int64
	if err := s.pool.QueryRow(ctx, "SELECT COUNT(*) FROM contact_allowlist WHERE workspace_id=$1", workspaceID).Scan(&total); err != nil {
		return nil, 0, err
	}
	offset := (page - 1) * pageSize
	rows, err := s.pool.Query(ctx,
		"SELECT workspace_id, phone, source, source_id, added_at FROM contact_allowlist WHERE workspace_id=$1 ORDER BY added_at DESC LIMIT $2 OFFSET $3",
		workspaceID, pageSize, offset,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var list []AllowlistEntry
	for rows.Next() {
		e := AllowlistEntry{}
		if err := rows.Scan(&e.WorkspaceID, &e.Phone, &e.Source, &e.SourceID, &e.AddedAt); err != nil {
			return nil, 0, err
		}
		list = append(list, e)
	}
	return list, total, rows.Err()
}

func (s *PgStore) ClearAllConversations(ctx context.Context, workspaceID string) (int64, error) {
	// Messages cascade-delete via FK on conversations.
	tag, err := s.pool.Exec(ctx, "DELETE FROM conversations WHERE workspace_id=$1", workspaceID)
	if err != nil {
		return 0, fmt.Errorf("clearing conversations: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (s *PgStore) AutoCreateContact(ctx context.Context, tenantID, phone, name string) (*ContactRow, error) {
	if name == "" {
		name = phone
	}
	c := &ContactRow{}
	err := s.pool.QueryRow(ctx,
		`INSERT INTO contacts (tenant_id, phone, name) VALUES ($1, $2, $3)
		 ON CONFLICT (tenant_id, phone) DO UPDATE SET name = EXCLUDED.name
		 RETURNING id, phone, name`,
		tenantID, phone, name,
	).Scan(&c.ID, &c.Phone, &c.Name)
	if err != nil {
		return nil, fmt.Errorf("auto-creating contact: %w", err)
	}
	return c, nil
}
