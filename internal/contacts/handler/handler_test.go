package handler

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
)

// ---------------------------------------------------------------------------
// mockStore — in-memory Store for unit testing
// ---------------------------------------------------------------------------

type mockStore struct {
	contacts     map[string]*contactRow
	phoneIndex   map[string]string                 // "tenant:phone" → contact ID
	tags         map[string][]string               // contact_id → tags
	customFields map[string]map[string]string       // contact_id → {key: value}
	nextID       int
}

func newMockStore() *mockStore {
	return &mockStore{
		contacts:     make(map[string]*contactRow),
		phoneIndex:   make(map[string]string),
		tags:         make(map[string][]string),
		customFields: make(map[string]map[string]string),
	}
}

func (m *mockStore) seedContact(tenantID, phone, name string, banned bool, contactTags []string) string {
	m.nextID++
	id := fmt.Sprintf("seed-%d", m.nextID)
	now := time.Now()
	m.contacts[id] = &contactRow{
		ID: id, TenantID: tenantID, Phone: phone, Name: name,
		IsBanned: banned, CreatedAt: now, UpdatedAt: now,
	}
	m.phoneIndex[tenantID+":"+phone] = id
	if len(contactTags) > 0 {
		m.tags[id] = contactTags
	}
	return id
}

func (m *mockStore) CreateContact(_ context.Context, tenantID, phone, name string) (contactRow, bool, error) {
	key := tenantID + ":" + phone
	if id, ok := m.phoneIndex[key]; ok {
		return *m.contacts[id], true, nil
	}
	m.nextID++
	id := fmt.Sprintf("uuid-%d", m.nextID)
	now := time.Now()
	c := &contactRow{ID: id, TenantID: tenantID, Phone: phone, Name: name, CreatedAt: now, UpdatedAt: now}
	m.contacts[id] = c
	m.phoneIndex[key] = id
	return *c, false, nil
}

func (m *mockStore) GetContactByID(_ context.Context, id string) (contactRow, error) {
	c, ok := m.contacts[id]
	if !ok {
		return contactRow{}, ErrNotFound
	}
	return *c, nil
}

func (m *mockStore) GetContactByPhone(_ context.Context, tenantID, phone string) (contactRow, error) {
	id, ok := m.phoneIndex[tenantID+":"+phone]
	if !ok {
		return contactRow{}, ErrNotFound
	}
	return *m.contacts[id], nil
}

func (m *mockStore) UpdateContact(_ context.Context, id, name, phone string, isBanned bool) (contactRow, error) {
	c, ok := m.contacts[id]
	if !ok {
		return contactRow{}, ErrNotFound
	}
	// Update phone index if phone changed.
	oldKey := c.TenantID + ":" + c.Phone
	newKey := c.TenantID + ":" + phone
	if oldKey != newKey {
		delete(m.phoneIndex, oldKey)
		m.phoneIndex[newKey] = id
	}
	c.Name = name
	c.Phone = phone
	c.IsBanned = isBanned
	c.UpdatedAt = time.Now()
	return *c, nil
}

func (m *mockStore) UpdateContactImport(_ context.Context, id, name string) error {
	c, ok := m.contacts[id]
	if !ok {
		return ErrNotFound
	}
	c.Name = name
	c.UpdatedAt = time.Now()
	return nil
}

func (m *mockStore) DeleteContact(_ context.Context, id string) error {
	c, ok := m.contacts[id]
	if !ok {
		return ErrNotFound
	}
	delete(m.phoneIndex, c.TenantID+":"+c.Phone)
	delete(m.contacts, id)
	delete(m.tags, id)
	delete(m.customFields, id)
	return nil
}

func (m *mockStore) BulkDelete(_ context.Context, ids []string) (int64, error) {
	var count int64
	for _, id := range ids {
		if c, ok := m.contacts[id]; ok {
			delete(m.phoneIndex, c.TenantID+":"+c.Phone)
			delete(m.contacts, id)
			delete(m.tags, id)
			delete(m.customFields, id)
			count++
		}
	}
	return count, nil
}

func (m *mockStore) List(_ context.Context, f ListFilter) ([]contactRow, int64, error) {
	var all []contactRow
	for _, c := range m.contacts {
		if c.TenantID != f.TenantID {
			continue
		}
		if f.Search != "" {
			q := strings.ToLower(f.Search)
			if !strings.Contains(strings.ToLower(c.Name), q) &&
				!strings.Contains(strings.ToLower(c.Phone), q) {
				continue
			}
		}
		if f.FilterBanned && c.IsBanned != f.IsBanned {
			continue
		}
		if len(f.Tags) > 0 {
			cTags := m.tags[c.ID]
			if !hasAllTags(cTags, f.Tags) {
				continue
			}
		}
		all = append(all, *c)
	}

	sort.Slice(all, func(i, j int) bool {
		return all[i].CreatedAt.After(all[j].CreatedAt)
	})

	total := int64(len(all))
	start := int((f.Page - 1) * f.PageSize)
	if start >= len(all) {
		return nil, total, nil
	}
	end := start + int(f.PageSize)
	if end > len(all) {
		end = len(all)
	}
	return all[start:end], total, nil
}

func hasAllTags(have, want []string) bool {
	set := make(map[string]struct{}, len(have))
	for _, t := range have {
		set[t] = struct{}{}
	}
	for _, t := range want {
		if _, ok := set[t]; !ok {
			return false
		}
	}
	return true
}

func (m *mockStore) ReplaceTags(_ context.Context, contactID string, tags []string) error {
	m.tags[contactID] = tags
	return nil
}

func (m *mockStore) GetTags(_ context.Context, contactID string) ([]string, error) {
	return m.tags[contactID], nil
}

func (m *mockStore) ListTags(_ context.Context, tenantID, prefix string) ([]tagCountRow, error) {
	counts := make(map[string]int32)
	for id, tags := range m.tags {
		c, ok := m.contacts[id]
		if !ok || c.TenantID != tenantID {
			continue
		}
		for _, t := range tags {
			if prefix == "" || strings.HasPrefix(t, prefix) {
				counts[t]++
			}
		}
	}
	result := make([]tagCountRow, 0, len(counts))
	for tag, cnt := range counts {
		result = append(result, tagCountRow{Tag: tag, Count: cnt})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Tag < result[j].Tag })
	return result, nil
}

func (m *mockStore) MergeCustomFields(_ context.Context, contactID string, fields map[string]string) error {
	if m.customFields[contactID] == nil {
		m.customFields[contactID] = make(map[string]string)
	}
	for k, v := range fields {
		if v == "" {
			delete(m.customFields[contactID], k)
		} else {
			m.customFields[contactID][k] = v
		}
	}
	return nil
}

func (m *mockStore) GetCustomFields(_ context.Context, contactID string) (map[string]string, error) {
	f := m.customFields[contactID]
	if f == nil {
		return map[string]string{}, nil
	}
	out := make(map[string]string, len(f))
	for k, v := range f {
		out[k] = v
	}
	return out, nil
}

func (m *mockStore) CheckBan(_ context.Context, tenantID, phone string) (bool, error) {
	id, ok := m.phoneIndex[tenantID+":"+phone]
	if !ok {
		return false, nil
	}
	return m.contacts[id].IsBanned, nil
}

func (m *mockStore) BulkCheckBan(_ context.Context, tenantID string, phones []string) (map[string]bool, error) {
	result := make(map[string]bool, len(phones))
	for _, phone := range phones {
		id, ok := m.phoneIndex[tenantID+":"+phone]
		if ok {
			result[phone] = m.contacts[id].IsBanned
		}
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func newTestHandler(store *mockStore) *Handler {
	return New(store, nil, zerolog.Nop())
}

// ---------------------------------------------------------------------------
// TestImportContacts — CSV import with dedup strategies
// ---------------------------------------------------------------------------

func TestImportContacts(t *testing.T) {
	const tenant = "tenant-1"

	tests := []struct {
		name         string
		csv          string
		mapping      map[string]string
		defaultTags  []string
		strategy     hermesv1.ImportDuplicateStrategy
		seed         func(s *mockStore)
		wantImported int32
		wantSkipped  int32
		wantUpdated  int32
		wantFailed   int32
		wantBanned   int32
	}{
		{
			name:    "all new contacts",
			csv:     "phone,name\n+628111,Alice\n+628222,Bob\n+628333,Charlie\n",
			mapping: map[string]string{"phone": "phone", "name": "name"},
			wantImported: 3,
		},
		{
			name:    "duplicates with skip strategy",
			csv:     "phone,name\n+628111,Alice\n+628222,Bob\n",
			mapping: map[string]string{"phone": "phone", "name": "name"},
			strategy: hermesv1.ImportDuplicateStrategy_IMPORT_DUPLICATE_STRATEGY_SKIP,
			seed: func(s *mockStore) {
				s.seedContact(tenant, "+628111", "ExistingAlice", false, nil)
			},
			wantImported: 1,
			wantSkipped:  1,
		},
		{
			name:    "duplicates with update strategy",
			csv:     "phone,name\n+628111,NewAlice\n+628222,Bob\n",
			mapping: map[string]string{"phone": "phone", "name": "name"},
			strategy: hermesv1.ImportDuplicateStrategy_IMPORT_DUPLICATE_STRATEGY_UPDATE,
			seed: func(s *mockStore) {
				s.seedContact(tenant, "+628111", "OldAlice", false, nil)
			},
			wantImported: 1,
			wantUpdated:  1,
		},
		{
			name:    "rows with missing phone are failed",
			csv:     "phone,name\n,Alice\n+628222,Bob\n",
			mapping: map[string]string{"phone": "phone", "name": "name"},
			wantImported: 1,
			wantFailed:   1,
		},
		{
			name:    "banned contacts counted",
			csv:     "phone,name\n+628111,Alice\n+628222,Bob\n",
			mapping: map[string]string{"phone": "phone", "name": "name"},
			strategy: hermesv1.ImportDuplicateStrategy_IMPORT_DUPLICATE_STRATEGY_SKIP,
			seed: func(s *mockStore) {
				s.seedContact(tenant, "+628111", "BannedAlice", true, nil)
			},
			wantImported: 1,
			wantSkipped:  1,
			wantBanned:   1,
		},
		{
			name:        "default tags applied to new contacts",
			csv:         "phone,name\n+628111,Alice\n",
			mapping:     map[string]string{"phone": "phone", "name": "name"},
			defaultTags: []string{"imported", "batch-1"},
			wantImported: 1,
		},
		{
			name:    "custom fields extracted",
			csv:     "phone,name,city\n+628111,Alice,Jakarta\n",
			mapping: map[string]string{"phone": "phone", "name": "name", "city": "city"},
			wantImported: 1,
		},
		{
			name:    "unspecified strategy defaults to skip",
			csv:     "phone,name\n+628111,Alice\n",
			mapping: map[string]string{"phone": "phone", "name": "name"},
			strategy: hermesv1.ImportDuplicateStrategy_IMPORT_DUPLICATE_STRATEGY_UNSPECIFIED,
			seed: func(s *mockStore) {
				s.seedContact(tenant, "+628111", "ExistingAlice", false, nil)
			},
			wantSkipped: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newMockStore()
			if tt.seed != nil {
				tt.seed(store)
			}
			h := newTestHandler(store)

			resp, err := h.ImportContacts(context.Background(), &hermesv1.ContactsImportRequest{
				TenantId:          tenant,
				CsvData:           []byte(tt.csv),
				ColumnMapping:     tt.mapping,
				DefaultTags:       tt.defaultTags,
				DuplicateStrategy: tt.strategy,
			})
			if err != nil {
				t.Fatalf("ImportContacts() error = %v", err)
			}

			if resp.ImportedCount != tt.wantImported {
				t.Errorf("ImportedCount = %d, want %d", resp.ImportedCount, tt.wantImported)
			}
			if resp.SkippedCount != tt.wantSkipped {
				t.Errorf("SkippedCount = %d, want %d", resp.SkippedCount, tt.wantSkipped)
			}
			if resp.UpdatedCount != tt.wantUpdated {
				t.Errorf("UpdatedCount = %d, want %d", resp.UpdatedCount, tt.wantUpdated)
			}
			if resp.FailedCount != tt.wantFailed {
				t.Errorf("FailedCount = %d, want %d", resp.FailedCount, tt.wantFailed)
			}
			if resp.BannedCount != tt.wantBanned {
				t.Errorf("BannedCount = %d, want %d", resp.BannedCount, tt.wantBanned)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestBulkBanCheck
// ---------------------------------------------------------------------------

func TestBulkBanCheck(t *testing.T) {
	const tenant = "tenant-1"

	tests := []struct {
		name       string
		phones     []string
		seed       func(s *mockStore)
		wantBanned map[string]bool
	}{
		{
			name:   "mixed banned and not banned",
			phones: []string{"+628111", "+628222", "+628333"},
			seed: func(s *mockStore) {
				s.seedContact(tenant, "+628111", "Alice", true, nil)
				s.seedContact(tenant, "+628222", "Bob", false, nil)
				s.seedContact(tenant, "+628333", "Charlie", true, nil)
			},
			wantBanned: map[string]bool{
				"+628111": true,
				"+628222": false,
				"+628333": true,
			},
		},
		{
			name:   "unknown phones return false",
			phones: []string{"+628999", "+628888"},
			seed:   func(s *mockStore) {},
			wantBanned: map[string]bool{
				"+628999": false,
				"+628888": false,
			},
		},
		{
			name:   "all not banned",
			phones: []string{"+628111", "+628222"},
			seed: func(s *mockStore) {
				s.seedContact(tenant, "+628111", "Alice", false, nil)
				s.seedContact(tenant, "+628222", "Bob", false, nil)
			},
			wantBanned: map[string]bool{
				"+628111": false,
				"+628222": false,
			},
		},
		{
			name:   "all banned",
			phones: []string{"+628111", "+628222"},
			seed: func(s *mockStore) {
				s.seedContact(tenant, "+628111", "Alice", true, nil)
				s.seedContact(tenant, "+628222", "Bob", true, nil)
			},
			wantBanned: map[string]bool{
				"+628111": true,
				"+628222": true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newMockStore()
			tt.seed(store)
			h := newTestHandler(store)

			resp, err := h.BulkBanCheck(context.Background(), &hermesv1.ContactsBulkBanCheckRequest{
				TenantId: tenant,
				Phones:   tt.phones,
			})
			if err != nil {
				t.Fatalf("BulkBanCheck() error = %v", err)
			}

			if len(resp.Results) != len(tt.phones) {
				t.Fatalf("got %d results, want %d", len(resp.Results), len(tt.phones))
			}

			for _, r := range resp.Results {
				want, ok := tt.wantBanned[r.Phone]
				if !ok {
					t.Errorf("unexpected phone in results: %s", r.Phone)
					continue
				}
				if r.IsBanned != want {
					t.Errorf("phone %s: IsBanned = %v, want %v", r.Phone, r.IsBanned, want)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestListTags — with prefix filter
// ---------------------------------------------------------------------------

func TestListTags(t *testing.T) {
	const tenant = "tenant-1"

	tests := []struct {
		name     string
		prefix   string
		seed     func(s *mockStore)
		wantTags []tagCountRow
	}{
		{
			name: "all tags no prefix",
			seed: func(s *mockStore) {
				s.seedContact(tenant, "+628111", "Alice", false, []string{"vip", "region-jakarta"})
				s.seedContact(tenant, "+628222", "Bob", false, []string{"vip", "region-bali"})
				s.seedContact(tenant, "+628333", "Charlie", false, []string{"region-jakarta"})
			},
			wantTags: []tagCountRow{
				{Tag: "region-bali", Count: 1},
				{Tag: "region-jakarta", Count: 2},
				{Tag: "vip", Count: 2},
			},
		},
		{
			name:   "prefix filter region-",
			prefix: "region-",
			seed: func(s *mockStore) {
				s.seedContact(tenant, "+628111", "Alice", false, []string{"vip", "region-jakarta"})
				s.seedContact(tenant, "+628222", "Bob", false, []string{"vip", "region-bali"})
				s.seedContact(tenant, "+628333", "Charlie", false, []string{"region-jakarta"})
			},
			wantTags: []tagCountRow{
				{Tag: "region-bali", Count: 1},
				{Tag: "region-jakarta", Count: 2},
			},
		},
		{
			name:   "prefix filter with no matches",
			prefix: "nonexistent-",
			seed: func(s *mockStore) {
				s.seedContact(tenant, "+628111", "Alice", false, []string{"vip", "region-jakarta"})
			},
			wantTags: []tagCountRow{},
		},
		{
			name:     "empty store returns empty",
			seed:     func(s *mockStore) {},
			wantTags: []tagCountRow{},
		},
		{
			name:   "ignores contacts from other tenants",
			prefix: "",
			seed: func(s *mockStore) {
				s.seedContact(tenant, "+628111", "Alice", false, []string{"vip"})
				s.seedContact("other-tenant", "+628222", "Bob", false, []string{"vip", "premium"})
			},
			wantTags: []tagCountRow{
				{Tag: "vip", Count: 1},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newMockStore()
			tt.seed(store)
			h := newTestHandler(store)

			resp, err := h.ListTags(context.Background(), &hermesv1.ContactsListTagsRequest{
				TenantId: tenant,
				Prefix:   tt.prefix,
			})
			if err != nil {
				t.Fatalf("ListTags() error = %v", err)
			}

			got := make([]tagCountRow, len(resp.Tags))
			for i, t := range resp.Tags {
				got[i] = tagCountRow{Tag: t.Tag, Count: t.Count}
			}

			if len(got) != len(tt.wantTags) {
				t.Fatalf("got %d tags, want %d: %+v", len(got), len(tt.wantTags), got)
			}

			for i := range got {
				if got[i].Tag != tt.wantTags[i].Tag || got[i].Count != tt.wantTags[i].Count {
					t.Errorf("tag[%d] = %+v, want %+v", i, got[i], tt.wantTags[i])
				}
			}
		})
	}
}
