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

// ErrNotFound is returned when a requested contact does not exist.
var ErrNotFound = errors.New("contact not found")

// contactRow is the DB-level representation of a contact.
type contactRow struct {
	ID        string
	TenantID  string
	Phone     string
	Name      string
	IsBanned  bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// tagCountRow holds a tag and its usage count.
type tagCountRow struct {
	Tag   string
	Count int32
}

// ListFilter holds query parameters for listing contacts.
type ListFilter struct {
	TenantID     string
	Search       string
	Tags         []string
	FilterBanned bool
	IsBanned     bool
	Page         int32
	PageSize     int32
}

// Store abstracts all database operations for the contacts service.
type Store interface {
	// CreateContact inserts a new contact. If the phone already exists in the
	// tenant, returns the existing row with alreadyExisted=true.
	CreateContact(ctx context.Context, tenantID, phone, name string) (contactRow, bool, error)
	GetContactByID(ctx context.Context, id string) (contactRow, error)
	GetContactByPhone(ctx context.Context, tenantID, phone string) (contactRow, error)
	UpdateContact(ctx context.Context, id, name, phone string, isBanned bool) (contactRow, error)
	UpdateContactImport(ctx context.Context, id, name string) error
	DeleteContact(ctx context.Context, id string) error
	BulkDelete(ctx context.Context, ids []string) (int64, error)
	List(ctx context.Context, f ListFilter) ([]contactRow, int64, error)

	ReplaceTags(ctx context.Context, contactID string, tags []string) error
	GetTags(ctx context.Context, contactID string) ([]string, error)
	ListTags(ctx context.Context, tenantID, prefix string) ([]tagCountRow, error)

	MergeCustomFields(ctx context.Context, contactID string, fields map[string]string) error
	GetCustomFields(ctx context.Context, contactID string) (map[string]string, error)

	CheckBan(ctx context.Context, tenantID, phone string) (bool, error)
	BulkCheckBan(ctx context.Context, tenantID string, phones []string) (map[string]bool, error)
}

// ---------------------------------------------------------------------------
// pgxStore — PostgreSQL implementation of Store
// ---------------------------------------------------------------------------

type pgxStore struct {
	pool *pgxpool.Pool
}

// NewPgxStore creates a Store backed by a pgxpool.
func NewPgxStore(pool *pgxpool.Pool) Store {
	return &pgxStore{pool: pool}
}

func (s *pgxStore) CreateContact(ctx context.Context, tenantID, phone, name string) (contactRow, bool, error) {
	var c contactRow
	err := s.pool.QueryRow(ctx, `
		INSERT INTO contacts (tenant_id, phone, name)
		VALUES ($1, $2, $3)
		ON CONFLICT (tenant_id, phone) DO NOTHING
		RETURNING id, tenant_id, phone, name, is_banned, created_at, updated_at
	`, tenantID, phone, name).Scan(
		&c.ID, &c.TenantID, &c.Phone, &c.Name, &c.IsBanned, &c.CreatedAt, &c.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		existing, err2 := s.GetContactByPhone(ctx, tenantID, phone)
		if err2 != nil {
			return contactRow{}, false, fmt.Errorf("fetching existing contact: %w", err2)
		}
		return existing, true, nil
	}
	if err != nil {
		return contactRow{}, false, fmt.Errorf("inserting contact: %w", err)
	}
	return c, false, nil
}

func (s *pgxStore) GetContactByID(ctx context.Context, id string) (contactRow, error) {
	var c contactRow
	err := s.pool.QueryRow(ctx, `
		SELECT id, tenant_id, phone, name, is_banned, created_at, updated_at
		FROM contacts WHERE id = $1
	`, id).Scan(&c.ID, &c.TenantID, &c.Phone, &c.Name, &c.IsBanned, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return contactRow{}, ErrNotFound
	}
	return c, err
}

func (s *pgxStore) GetContactByPhone(ctx context.Context, tenantID, phone string) (contactRow, error) {
	var c contactRow
	err := s.pool.QueryRow(ctx, `
		SELECT id, tenant_id, phone, name, is_banned, created_at, updated_at
		FROM contacts WHERE tenant_id = $1 AND phone = $2
	`, tenantID, phone).Scan(&c.ID, &c.TenantID, &c.Phone, &c.Name, &c.IsBanned, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return contactRow{}, ErrNotFound
	}
	return c, err
}

func (s *pgxStore) UpdateContact(ctx context.Context, id, name, phone string, isBanned bool) (contactRow, error) {
	var c contactRow
	err := s.pool.QueryRow(ctx, `
		UPDATE contacts SET name = $2, phone = $3, is_banned = $4, updated_at = now()
		WHERE id = $1
		RETURNING id, tenant_id, phone, name, is_banned, created_at, updated_at
	`, id, name, phone, isBanned).Scan(
		&c.ID, &c.TenantID, &c.Phone, &c.Name, &c.IsBanned, &c.CreatedAt, &c.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return contactRow{}, ErrNotFound
	}
	return c, err
}

func (s *pgxStore) UpdateContactImport(ctx context.Context, id, name string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE contacts SET name = $2, updated_at = now() WHERE id = $1
	`, id, name)
	return err
}

func (s *pgxStore) DeleteContact(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM contacts WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *pgxStore) BulkDelete(ctx context.Context, ids []string) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM contacts WHERE id = ANY($1)`, ids)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func (s *pgxStore) List(ctx context.Context, f ListFilter) ([]contactRow, int64, error) {
	var (
		conditions []string
		args       []interface{}
		argIdx     = 1
	)

	conditions = append(conditions, fmt.Sprintf("c.tenant_id = $%d", argIdx))
	args = append(args, f.TenantID)
	argIdx++

	if f.Search != "" {
		conditions = append(conditions, fmt.Sprintf(
			"to_tsvector('simple', coalesce(c.name, '') || ' ' || coalesce(c.phone, '')) @@ plainto_tsquery('simple', $%d)",
			argIdx))
		args = append(args, f.Search)
		argIdx++
	}

	if len(f.Tags) > 0 {
		conditions = append(conditions, fmt.Sprintf(
			"(SELECT COUNT(DISTINCT tag) FROM contact_tags WHERE contact_id = c.id AND tag = ANY($%d)) = $%d",
			argIdx, argIdx+1))
		args = append(args, f.Tags, len(f.Tags))
		argIdx += 2
	}

	if f.FilterBanned {
		conditions = append(conditions, fmt.Sprintf("c.is_banned = $%d", argIdx))
		args = append(args, f.IsBanned)
		argIdx++
	}

	where := strings.Join(conditions, " AND ")

	// Count total matching rows.
	var total int64
	err := s.pool.QueryRow(ctx,
		fmt.Sprintf("SELECT COUNT(*) FROM contacts c WHERE %s", where),
		args...,
	).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("counting contacts: %w", err)
	}

	// Fetch the requested page.
	offset := (f.Page - 1) * f.PageSize
	query := fmt.Sprintf(`
		SELECT c.id, c.tenant_id, c.phone, c.name, c.is_banned, c.created_at, c.updated_at
		FROM contacts c
		WHERE %s
		ORDER BY c.created_at DESC
		LIMIT $%d OFFSET $%d
	`, where, argIdx, argIdx+1)
	args = append(args, f.PageSize, offset)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("listing contacts: %w", err)
	}
	defer rows.Close()

	var contacts []contactRow
	for rows.Next() {
		var c contactRow
		if err := rows.Scan(&c.ID, &c.TenantID, &c.Phone, &c.Name, &c.IsBanned, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, 0, fmt.Errorf("scanning contact: %w", err)
		}
		contacts = append(contacts, c)
	}
	return contacts, total, nil
}

// ---------------------------------------------------------------------------
// Tags
// ---------------------------------------------------------------------------

func (s *pgxStore) ReplaceTags(ctx context.Context, contactID string, tags []string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, "DELETE FROM contact_tags WHERE contact_id = $1", contactID); err != nil {
		return err
	}
	for _, tag := range tags {
		if _, err := tx.Exec(ctx,
			"INSERT INTO contact_tags (contact_id, tag) VALUES ($1, $2) ON CONFLICT DO NOTHING",
			contactID, tag,
		); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *pgxStore) GetTags(ctx context.Context, contactID string) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		"SELECT tag FROM contact_tags WHERE contact_id = $1 ORDER BY tag", contactID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tags []string
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			return nil, err
		}
		tags = append(tags, tag)
	}
	return tags, nil
}

func (s *pgxStore) ListTags(ctx context.Context, tenantID, prefix string) ([]tagCountRow, error) {
	var (
		query string
		args  []interface{}
	)
	if prefix != "" {
		query = `
			SELECT ct.tag, COUNT(*)::int AS cnt
			FROM contact_tags ct
			JOIN contacts c ON ct.contact_id = c.id
			WHERE c.tenant_id = $1 AND ct.tag LIKE $2
			GROUP BY ct.tag
			ORDER BY ct.tag`
		args = []interface{}{tenantID, prefix + "%"}
	} else {
		query = `
			SELECT ct.tag, COUNT(*)::int AS cnt
			FROM contact_tags ct
			JOIN contacts c ON ct.contact_id = c.id
			WHERE c.tenant_id = $1
			GROUP BY ct.tag
			ORDER BY ct.tag`
		args = []interface{}{tenantID}
	}

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tags []tagCountRow
	for rows.Next() {
		var tc tagCountRow
		if err := rows.Scan(&tc.Tag, &tc.Count); err != nil {
			return nil, err
		}
		tags = append(tags, tc)
	}
	return tags, nil
}

// ---------------------------------------------------------------------------
// Custom Fields
// ---------------------------------------------------------------------------

func (s *pgxStore) MergeCustomFields(ctx context.Context, contactID string, fields map[string]string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	for key, value := range fields {
		if value == "" {
			_, err = tx.Exec(ctx,
				"DELETE FROM contact_custom_fields WHERE contact_id = $1 AND key = $2",
				contactID, key)
		} else {
			_, err = tx.Exec(ctx, `
				INSERT INTO contact_custom_fields (contact_id, key, value) VALUES ($1, $2, $3)
				ON CONFLICT (contact_id, key) DO UPDATE SET value = $3
			`, contactID, key, value)
		}
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *pgxStore) GetCustomFields(ctx context.Context, contactID string) (map[string]string, error) {
	rows, err := s.pool.Query(ctx,
		"SELECT key, value FROM contact_custom_fields WHERE contact_id = $1", contactID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	fields := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		fields[k] = v
	}
	return fields, nil
}

// ---------------------------------------------------------------------------
// Ban Check
// ---------------------------------------------------------------------------

func (s *pgxStore) CheckBan(ctx context.Context, tenantID, phone string) (bool, error) {
	var banned bool
	err := s.pool.QueryRow(ctx,
		"SELECT is_banned FROM contacts WHERE tenant_id = $1 AND phone = $2",
		tenantID, phone).Scan(&banned)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return banned, err
}

func (s *pgxStore) BulkCheckBan(ctx context.Context, tenantID string, phones []string) (map[string]bool, error) {
	rows, err := s.pool.Query(ctx,
		"SELECT phone, is_banned FROM contacts WHERE tenant_id = $1 AND phone = ANY($2)",
		tenantID, phones)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]bool, len(phones))
	for rows.Next() {
		var phone string
		var banned bool
		if err := rows.Scan(&phone, &banned); err != nil {
			return nil, err
		}
		result[phone] = banned
	}
	return result, nil
}
