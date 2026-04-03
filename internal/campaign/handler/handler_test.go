package handler

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/rs/zerolog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ---------------------------------------------------------------------------
// Mock Engine
// ---------------------------------------------------------------------------

type mockEngine struct {
	startFn    func(campaignID, workspaceID, tenantID string) error
	stopFn     func(campaignID string)
	isRunning  bool
}

func (m *mockEngine) Start(campaignID, workspaceID, tenantID string) error {
	if m.startFn != nil {
		return m.startFn(campaignID, workspaceID, tenantID)
	}
	return nil
}

func (m *mockEngine) Stop(campaignID string) {
	if m.stopFn != nil {
		m.stopFn(campaignID)
	}
}

func (m *mockEngine) IsRunning(campaignID string) bool {
	return m.isRunning
}

// ---------------------------------------------------------------------------
// Mock Store
// ---------------------------------------------------------------------------

type mockStore struct {
	createTemplateFn             func(ctx context.Context, workspaceID, name, body, mediaURL, mediaType, createdBy string, variables []byte) (*TemplateRow, error)
	getTemplateFn                func(ctx context.Context, id string) (*TemplateRow, error)
	listTemplatesFn              func(ctx context.Context, workspaceID, search string, page, pageSize int32) ([]*TemplateRow, int64, error)
	updateTemplateFn             func(ctx context.Context, id, name, body, mediaURL, mediaType string, variables []byte) (*TemplateRow, error)
	deleteTemplateFn             func(ctx context.Context, id string) error
	templateUsedByRunningFn      func(ctx context.Context, templateID string) (bool, error)
	templateUsedByActiveFn       func(ctx context.Context, templateID string) (bool, error)
	createCampaignFn             func(ctx context.Context, row *CampaignRow) (*CampaignRow, error)
	getCampaignFn                func(ctx context.Context, id string) (*CampaignRow, error)
	listCampaignsFn              func(ctx context.Context, workspaceID, status string, page, pageSize int32) ([]*CampaignRow, int64, error)
	updateCampaignStatusFn       func(ctx context.Context, id, status string, setStarted, setCompleted bool) (*CampaignRow, error)
	addCampaignNumbersFn         func(ctx context.Context, campaignID string, waNumberIDs []string) error
	removeCampaignNumbersFn      func(ctx context.Context, campaignID string, waNumberIDs []string) error
	listCampaignNumbersFn        func(ctx context.Context, campaignID string, page, pageSize int32) ([]*CampaignNumberRow, int64, error)
	getActiveCampaignNumbersFn   func(ctx context.Context, campaignID string) ([]*CampaignNumberRow, error)
	updateCampaignNumberStatusFn func(ctx context.Context, campaignID, waNumberID, status string) error
	incrementNumberSentCountFn   func(ctx context.Context, campaignID, waNumberID string) error
	addCampaignContactsFn        func(ctx context.Context, campaignID string, contactIDs []string) (int32, error)
	removeCampaignContactsFn     func(ctx context.Context, campaignID string, contactIDs []string) (int32, error)
	listCampaignContactsFn       func(ctx context.Context, campaignID, status string, page, pageSize int32) ([]*CampaignContactJoinRow, int64, error)
	getPendingContactsFn         func(ctx context.Context, campaignID string, limit int32) ([]*PendingContactRow, error)
	updateContactSentFn          func(ctx context.Context, campaignID, contactID, waNumberID string) error
	skipPendingContactsFn        func(ctx context.Context, campaignID string) (int32, error)
	incrementSentCountFn         func(ctx context.Context, campaignID string) error
	incrementFailedCountFn       func(ctx context.Context, campaignID string) error
	incrementRepliedCountFn      func(ctx context.Context, campaignID string) error
	incrementBannedCountFn       func(ctx context.Context, campaignID string) (int32, error)
	updateTotalContactsFn        func(ctx context.Context, campaignID string) error
	getWorkspaceTenantIDFn       func(ctx context.Context, workspaceID string) (string, error)
	findContactInActiveFn        func(ctx context.Context, senderPhone string) ([]CampaignContactMatch, error)
	getCampaignsUsingNumberFn    func(ctx context.Context, waNumberID string, statuses []string) ([]*CampaignRow, error)
	countCampaignNumbersFn       func(ctx context.Context, campaignID string) (int32, error)
	countCampaignContactsFn      func(ctx context.Context, campaignID string) (int32, error)
}

func (m *mockStore) CreateTemplate(ctx context.Context, workspaceID, name, body, mediaURL, mediaType, createdBy string, variables []byte) (*TemplateRow, error) {
	if m.createTemplateFn != nil {
		return m.createTemplateFn(ctx, workspaceID, name, body, mediaURL, mediaType, createdBy, variables)
	}
	return nil, nil
}

func (m *mockStore) GetTemplate(ctx context.Context, id string) (*TemplateRow, error) {
	if m.getTemplateFn != nil {
		return m.getTemplateFn(ctx, id)
	}
	return nil, nil
}

func (m *mockStore) ListTemplates(ctx context.Context, workspaceID, search string, page, pageSize int32) ([]*TemplateRow, int64, error) {
	if m.listTemplatesFn != nil {
		return m.listTemplatesFn(ctx, workspaceID, search, page, pageSize)
	}
	return nil, 0, nil
}

func (m *mockStore) UpdateTemplate(ctx context.Context, id, name, body, mediaURL, mediaType string, variables []byte) (*TemplateRow, error) {
	if m.updateTemplateFn != nil {
		return m.updateTemplateFn(ctx, id, name, body, mediaURL, mediaType, variables)
	}
	return nil, nil
}

func (m *mockStore) DeleteTemplate(ctx context.Context, id string) error {
	if m.deleteTemplateFn != nil {
		return m.deleteTemplateFn(ctx, id)
	}
	return nil
}

func (m *mockStore) TemplateUsedByRunningCampaign(ctx context.Context, templateID string) (bool, error) {
	if m.templateUsedByRunningFn != nil {
		return m.templateUsedByRunningFn(ctx, templateID)
	}
	return false, nil
}

func (m *mockStore) TemplateUsedByActiveCampaign(ctx context.Context, templateID string) (bool, error) {
	if m.templateUsedByActiveFn != nil {
		return m.templateUsedByActiveFn(ctx, templateID)
	}
	return false, nil
}

func (m *mockStore) CreateCampaign(ctx context.Context, row *CampaignRow) (*CampaignRow, error) {
	if m.createCampaignFn != nil {
		return m.createCampaignFn(ctx, row)
	}
	return nil, nil
}

func (m *mockStore) GetCampaign(ctx context.Context, id string) (*CampaignRow, error) {
	if m.getCampaignFn != nil {
		return m.getCampaignFn(ctx, id)
	}
	return nil, nil
}

func (m *mockStore) ListCampaigns(ctx context.Context, workspaceID, st string, page, pageSize int32) ([]*CampaignRow, int64, error) {
	if m.listCampaignsFn != nil {
		return m.listCampaignsFn(ctx, workspaceID, st, page, pageSize)
	}
	return nil, 0, nil
}

func (m *mockStore) UpdateCampaignStatus(ctx context.Context, id, st string, setStarted, setCompleted bool) (*CampaignRow, error) {
	if m.updateCampaignStatusFn != nil {
		return m.updateCampaignStatusFn(ctx, id, st, setStarted, setCompleted)
	}
	return nil, nil
}

func (m *mockStore) AddCampaignNumbers(ctx context.Context, campaignID string, waNumberIDs []string) error {
	if m.addCampaignNumbersFn != nil {
		return m.addCampaignNumbersFn(ctx, campaignID, waNumberIDs)
	}
	return nil
}

func (m *mockStore) RemoveCampaignNumbers(ctx context.Context, campaignID string, waNumberIDs []string) error {
	if m.removeCampaignNumbersFn != nil {
		return m.removeCampaignNumbersFn(ctx, campaignID, waNumberIDs)
	}
	return nil
}

func (m *mockStore) ListCampaignNumbers(ctx context.Context, campaignID string, page, pageSize int32) ([]*CampaignNumberRow, int64, error) {
	if m.listCampaignNumbersFn != nil {
		return m.listCampaignNumbersFn(ctx, campaignID, page, pageSize)
	}
	return nil, 0, nil
}

func (m *mockStore) GetActiveCampaignNumbers(ctx context.Context, campaignID string) ([]*CampaignNumberRow, error) {
	if m.getActiveCampaignNumbersFn != nil {
		return m.getActiveCampaignNumbersFn(ctx, campaignID)
	}
	return nil, nil
}

func (m *mockStore) UpdateCampaignNumberStatus(ctx context.Context, campaignID, waNumberID, st string) error {
	if m.updateCampaignNumberStatusFn != nil {
		return m.updateCampaignNumberStatusFn(ctx, campaignID, waNumberID, st)
	}
	return nil
}

func (m *mockStore) IncrementNumberSentCount(ctx context.Context, campaignID, waNumberID string) error {
	if m.incrementNumberSentCountFn != nil {
		return m.incrementNumberSentCountFn(ctx, campaignID, waNumberID)
	}
	return nil
}

func (m *mockStore) AddCampaignContacts(ctx context.Context, campaignID string, contactIDs []string) (int32, error) {
	if m.addCampaignContactsFn != nil {
		return m.addCampaignContactsFn(ctx, campaignID, contactIDs)
	}
	return int32(len(contactIDs)), nil
}

func (m *mockStore) RemoveCampaignContacts(ctx context.Context, campaignID string, contactIDs []string) (int32, error) {
	if m.removeCampaignContactsFn != nil {
		return m.removeCampaignContactsFn(ctx, campaignID, contactIDs)
	}
	return int32(len(contactIDs)), nil
}

func (m *mockStore) ListCampaignContacts(ctx context.Context, campaignID, st string, page, pageSize int32) ([]*CampaignContactJoinRow, int64, error) {
	if m.listCampaignContactsFn != nil {
		return m.listCampaignContactsFn(ctx, campaignID, st, page, pageSize)
	}
	return nil, 0, nil
}

func (m *mockStore) GetPendingContacts(ctx context.Context, campaignID string, limit int32) ([]*PendingContactRow, error) {
	if m.getPendingContactsFn != nil {
		return m.getPendingContactsFn(ctx, campaignID, limit)
	}
	return nil, nil
}

func (m *mockStore) UpdateContactSent(ctx context.Context, campaignID, contactID, waNumberID string) error {
	if m.updateContactSentFn != nil {
		return m.updateContactSentFn(ctx, campaignID, contactID, waNumberID)
	}
	return nil
}

func (m *mockStore) SkipPendingContacts(ctx context.Context, campaignID string) (int32, error) {
	if m.skipPendingContactsFn != nil {
		return m.skipPendingContactsFn(ctx, campaignID)
	}
	return 0, nil
}

func (m *mockStore) IncrementSentCount(ctx context.Context, campaignID string) error {
	if m.incrementSentCountFn != nil {
		return m.incrementSentCountFn(ctx, campaignID)
	}
	return nil
}

func (m *mockStore) IncrementFailedCount(ctx context.Context, campaignID string) error {
	if m.incrementFailedCountFn != nil {
		return m.incrementFailedCountFn(ctx, campaignID)
	}
	return nil
}

func (m *mockStore) IncrementRepliedCount(ctx context.Context, campaignID string) error {
	if m.incrementRepliedCountFn != nil {
		return m.incrementRepliedCountFn(ctx, campaignID)
	}
	return nil
}

func (m *mockStore) IncrementBannedCount(ctx context.Context, campaignID string) (int32, error) {
	if m.incrementBannedCountFn != nil {
		return m.incrementBannedCountFn(ctx, campaignID)
	}
	return 0, nil
}

func (m *mockStore) UpdateTotalContacts(ctx context.Context, campaignID string) error {
	if m.updateTotalContactsFn != nil {
		return m.updateTotalContactsFn(ctx, campaignID)
	}
	return nil
}

func (m *mockStore) GetWorkspaceTenantID(ctx context.Context, workspaceID string) (string, error) {
	if m.getWorkspaceTenantIDFn != nil {
		return m.getWorkspaceTenantIDFn(ctx, workspaceID)
	}
	return "tenant-1", nil
}

func (m *mockStore) FindContactInActiveCampaigns(ctx context.Context, senderPhone string) ([]CampaignContactMatch, error) {
	if m.findContactInActiveFn != nil {
		return m.findContactInActiveFn(ctx, senderPhone)
	}
	return nil, nil
}

func (m *mockStore) GetCampaignsUsingNumber(ctx context.Context, waNumberID string, statuses []string) ([]*CampaignRow, error) {
	if m.getCampaignsUsingNumberFn != nil {
		return m.getCampaignsUsingNumberFn(ctx, waNumberID, statuses)
	}
	return nil, nil
}

func (m *mockStore) CountCampaignNumbers(ctx context.Context, campaignID string) (int32, error) {
	if m.countCampaignNumbersFn != nil {
		return m.countCampaignNumbersFn(ctx, campaignID)
	}
	return 0, nil
}

func (m *mockStore) CountCampaignContacts(ctx context.Context, campaignID string) (int32, error) {
	if m.countCampaignContactsFn != nil {
		return m.countCampaignContactsFn(ctx, campaignID)
	}
	return 0, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newTestHandler(s Store) *Handler {
	return New(s, &mockEngine{}, zerolog.Nop())
}

func makeTemplate(id, workspaceID, name, body string) *TemplateRow {
	vars, _ := json.Marshal([]string{"name"})
	return &TemplateRow{
		ID:          id,
		WorkspaceID: workspaceID,
		Name:        name,
		Body:        body,
		Variables:   vars,
		CreatedAt:   time.Now(),
	}
}

func makeCampaign(id, workspaceID, templateID, name, st string) *CampaignRow {
	return &CampaignRow{
		ID:               id,
		WorkspaceID:      workspaceID,
		TemplateID:       templateID,
		Name:             name,
		Status:           st,
		RotationStrategy: "round_robin",
		DelayMinMs:       3000,
		DelayMaxMs:       15000,
		CreatedAt:        time.Now(),
	}
}

func assertCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %v", err)
	}
	if st.Code() != want {
		t.Errorf("got code %v, want %v (msg: %s)", st.Code(), want, st.Message())
	}
}

// ---------------------------------------------------------------------------
// Template Tests
// ---------------------------------------------------------------------------

func TestCreateTemplate(t *testing.T) {
	tests := []struct {
		name     string
		req      *hermesv1.TemplateCreateRequest
		store    *mockStore
		wantCode codes.Code
		wantName string
	}{
		{
			name: "success",
			req: &hermesv1.TemplateCreateRequest{
				WorkspaceId: "ws-1", Name: "greeting", Body: "{Hi|Hello} {{name}}", CreatedBy: "user-1",
			},
			store: &mockStore{
				createTemplateFn: func(_ context.Context, workspaceID, name, body, _, _, _ string, vars []byte) (*TemplateRow, error) {
					return makeTemplate("t-1", workspaceID, name, body), nil
				},
			},
			wantName: "greeting",
		},
		{
			name:     "missing workspace_id",
			req:      &hermesv1.TemplateCreateRequest{Name: "x", Body: "y"},
			store:    &mockStore{},
			wantCode: codes.InvalidArgument,
		},
		{
			name:     "missing name",
			req:      &hermesv1.TemplateCreateRequest{WorkspaceId: "ws-1", Body: "y"},
			store:    &mockStore{},
			wantCode: codes.InvalidArgument,
		},
		{
			name:     "missing body",
			req:      &hermesv1.TemplateCreateRequest{WorkspaceId: "ws-1", Name: "x"},
			store:    &mockStore{},
			wantCode: codes.InvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHandler(tt.store)
			resp, err := h.CreateTemplate(context.Background(), tt.req)
			if tt.wantCode != 0 {
				assertCode(t, err, tt.wantCode)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if resp.Template.Name != tt.wantName {
				t.Errorf("got name %q, want %q", resp.Template.Name, tt.wantName)
			}
		})
	}
}

func TestGetTemplate(t *testing.T) {
	tests := []struct {
		name     string
		req      *hermesv1.TemplateGetRequest
		store    *mockStore
		wantCode codes.Code
	}{
		{
			name: "success",
			req:  &hermesv1.TemplateGetRequest{Id: "t-1"},
			store: &mockStore{
				getTemplateFn: func(_ context.Context, id string) (*TemplateRow, error) {
					return makeTemplate(id, "ws-1", "test", "body"), nil
				},
			},
		},
		{
			name: "not found",
			req:  &hermesv1.TemplateGetRequest{Id: "t-missing"},
			store: &mockStore{
				getTemplateFn: func(_ context.Context, _ string) (*TemplateRow, error) {
					return nil, nil
				},
			},
			wantCode: codes.NotFound,
		},
		{
			name:     "missing id",
			req:      &hermesv1.TemplateGetRequest{},
			store:    &mockStore{},
			wantCode: codes.InvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHandler(tt.store)
			resp, err := h.GetTemplate(context.Background(), tt.req)
			if tt.wantCode != 0 {
				assertCode(t, err, tt.wantCode)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if resp.Template.Id != tt.req.Id {
				t.Errorf("got id %q, want %q", resp.Template.Id, tt.req.Id)
			}
		})
	}
}

func TestDeleteTemplate(t *testing.T) {
	tests := []struct {
		name     string
		req      *hermesv1.TemplateDeleteRequest
		store    *mockStore
		wantCode codes.Code
	}{
		{
			name: "success",
			req:  &hermesv1.TemplateDeleteRequest{Id: "t-1"},
			store: &mockStore{
				templateUsedByActiveFn: func(_ context.Context, _ string) (bool, error) { return false, nil },
				deleteTemplateFn:       func(_ context.Context, _ string) error { return nil },
			},
		},
		{
			name: "used by active campaign",
			req:  &hermesv1.TemplateDeleteRequest{Id: "t-1"},
			store: &mockStore{
				templateUsedByActiveFn: func(_ context.Context, _ string) (bool, error) { return true, nil },
			},
			wantCode: codes.FailedPrecondition,
		},
		{
			name: "not found",
			req:  &hermesv1.TemplateDeleteRequest{Id: "t-missing"},
			store: &mockStore{
				templateUsedByActiveFn: func(_ context.Context, _ string) (bool, error) { return false, nil },
				deleteTemplateFn:       func(_ context.Context, _ string) error { return ErrNotFound },
			},
			wantCode: codes.NotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHandler(tt.store)
			_, err := h.DeleteTemplate(context.Background(), tt.req)
			if tt.wantCode != 0 {
				assertCode(t, err, tt.wantCode)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestPreviewTemplate(t *testing.T) {
	store := &mockStore{
		getTemplateFn: func(_ context.Context, _ string) (*TemplateRow, error) {
			return makeTemplate("t-1", "ws-1", "test", "{Hi|Hello} {{name}}, welcome!"), nil
		},
	}
	h := newTestHandler(store)

	resp, err := h.PreviewTemplate(context.Background(), &hermesv1.TemplatePreviewRequest{
		Id:        "t-1",
		Variables: map[string]string{"name": "Alice"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.ResolvedBody != "Hi Alice, welcome!" && resp.ResolvedBody != "Hello Alice, welcome!" {
		t.Errorf("unexpected resolved body: %q", resp.ResolvedBody)
	}
}

// ---------------------------------------------------------------------------
// Campaign CRUD Tests
// ---------------------------------------------------------------------------

func TestCreateCampaign(t *testing.T) {
	tests := []struct {
		name     string
		req      *hermesv1.CampaignCreateRequest
		store    *mockStore
		wantCode codes.Code
	}{
		{
			name: "success",
			req: &hermesv1.CampaignCreateRequest{
				WorkspaceId: "ws-1", TemplateId: "t-1", Name: "promo",
				RotationStrategy: hermesv1.RotationStrategy_ROTATION_STRATEGY_ROUND_ROBIN,
			},
			store: &mockStore{
				getTemplateFn: func(_ context.Context, _ string) (*TemplateRow, error) {
					return makeTemplate("t-1", "ws-1", "tmpl", "body"), nil
				},
				createCampaignFn: func(_ context.Context, row *CampaignRow) (*CampaignRow, error) {
					return makeCampaign("c-1", row.WorkspaceID, row.TemplateID, row.Name, "draft"), nil
				},
				getCampaignFn: func(_ context.Context, _ string) (*CampaignRow, error) {
					return makeCampaign("c-1", "ws-1", "t-1", "promo", "draft"), nil
				},
			},
		},
		{
			name: "missing workspace_id",
			req:  &hermesv1.CampaignCreateRequest{TemplateId: "t-1", Name: "promo"},
			store: &mockStore{},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "template not found",
			req:  &hermesv1.CampaignCreateRequest{WorkspaceId: "ws-1", TemplateId: "t-missing", Name: "promo"},
			store: &mockStore{
				getTemplateFn: func(_ context.Context, _ string) (*TemplateRow, error) { return nil, nil },
			},
			wantCode: codes.NotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHandler(tt.store)
			resp, err := h.CreateCampaign(context.Background(), tt.req)
			if tt.wantCode != 0 {
				assertCode(t, err, tt.wantCode)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if resp.Campaign.Status != hermesv1.CampaignStatus_CAMPAIGN_STATUS_DRAFT {
				t.Errorf("got status %v, want DRAFT", resp.Campaign.Status)
			}
		})
	}
}

func TestGetCampaign(t *testing.T) {
	tests := []struct {
		name     string
		req      *hermesv1.CampaignGetRequest
		store    *mockStore
		wantCode codes.Code
	}{
		{
			name: "success",
			req:  &hermesv1.CampaignGetRequest{Id: "c-1"},
			store: &mockStore{
				getCampaignFn: func(_ context.Context, _ string) (*CampaignRow, error) {
					return makeCampaign("c-1", "ws-1", "t-1", "promo", "draft"), nil
				},
				listCampaignNumbersFn: func(_ context.Context, _ string, _, _ int32) ([]*CampaignNumberRow, int64, error) {
					return nil, 0, nil
				},
				getTemplateFn: func(_ context.Context, _ string) (*TemplateRow, error) {
					return makeTemplate("t-1", "ws-1", "tmpl", "body"), nil
				},
			},
		},
		{
			name: "not found",
			req:  &hermesv1.CampaignGetRequest{Id: "c-missing"},
			store: &mockStore{
				getCampaignFn: func(_ context.Context, _ string) (*CampaignRow, error) { return nil, nil },
			},
			wantCode: codes.NotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHandler(tt.store)
			_, err := h.GetCampaign(context.Background(), tt.req)
			if tt.wantCode != 0 {
				assertCode(t, err, tt.wantCode)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Lifecycle Tests
// ---------------------------------------------------------------------------

func TestStartCampaign(t *testing.T) {
	tests := []struct {
		name     string
		req      *hermesv1.CampaignStartRequest
		store    *mockStore
		wantCode codes.Code
	}{
		{
			name: "success from draft",
			req:  &hermesv1.CampaignStartRequest{Id: "c-1"},
			store: &mockStore{
				getCampaignFn: func(_ context.Context, _ string) (*CampaignRow, error) {
					return makeCampaign("c-1", "ws-1", "t-1", "promo", "draft"), nil
				},
				countCampaignNumbersFn:  func(_ context.Context, _ string) (int32, error) { return 2, nil },
				countCampaignContactsFn: func(_ context.Context, _ string) (int32, error) { return 100, nil },
				updateCampaignStatusFn: func(_ context.Context, id, st string, _, _ bool) (*CampaignRow, error) {
					return makeCampaign(id, "ws-1", "t-1", "promo", st), nil
				},
			},
		},
		{
			name: "no numbers",
			req:  &hermesv1.CampaignStartRequest{Id: "c-1"},
			store: &mockStore{
				getCampaignFn: func(_ context.Context, _ string) (*CampaignRow, error) {
					return makeCampaign("c-1", "ws-1", "t-1", "promo", "draft"), nil
				},
				countCampaignNumbersFn:  func(_ context.Context, _ string) (int32, error) { return 0, nil },
			},
			wantCode: codes.FailedPrecondition,
		},
		{
			name: "wrong status",
			req:  &hermesv1.CampaignStartRequest{Id: "c-1"},
			store: &mockStore{
				getCampaignFn: func(_ context.Context, _ string) (*CampaignRow, error) {
					return makeCampaign("c-1", "ws-1", "t-1", "promo", "completed"), nil
				},
			},
			wantCode: codes.FailedPrecondition,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHandler(tt.store)
			resp, err := h.StartCampaign(context.Background(), tt.req)
			if tt.wantCode != 0 {
				assertCode(t, err, tt.wantCode)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if resp.Campaign.Status != hermesv1.CampaignStatus_CAMPAIGN_STATUS_RUNNING {
				t.Errorf("got status %v, want RUNNING", resp.Campaign.Status)
			}
		})
	}
}

func TestPauseCampaign(t *testing.T) {
	tests := []struct {
		name     string
		status   string
		wantCode codes.Code
	}{
		{name: "success", status: "running"},
		{name: "not running", status: "draft", wantCode: codes.FailedPrecondition},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &mockStore{
				getCampaignFn: func(_ context.Context, _ string) (*CampaignRow, error) {
					return makeCampaign("c-1", "ws-1", "t-1", "promo", tt.status), nil
				},
				updateCampaignStatusFn: func(_ context.Context, id, st string, _, _ bool) (*CampaignRow, error) {
					return makeCampaign(id, "ws-1", "t-1", "promo", st), nil
				},
			}
			h := newTestHandler(store)
			_, err := h.PauseCampaign(context.Background(), &hermesv1.CampaignPauseRequest{Id: "c-1"})
			if tt.wantCode != 0 {
				assertCode(t, err, tt.wantCode)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestResumeCampaign(t *testing.T) {
	tests := []struct {
		name     string
		status   string
		wantCode codes.Code
	}{
		{name: "success", status: "paused"},
		{name: "not paused", status: "draft", wantCode: codes.FailedPrecondition},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &mockStore{
				getCampaignFn: func(_ context.Context, _ string) (*CampaignRow, error) {
					return makeCampaign("c-1", "ws-1", "t-1", "promo", tt.status), nil
				},
				updateCampaignStatusFn: func(_ context.Context, id, st string, _, _ bool) (*CampaignRow, error) {
					return makeCampaign(id, "ws-1", "t-1", "promo", st), nil
				},
			}
			h := newTestHandler(store)
			_, err := h.ResumeCampaign(context.Background(), &hermesv1.CampaignResumeRequest{Id: "c-1"})
			if tt.wantCode != 0 {
				assertCode(t, err, tt.wantCode)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestCancelCampaign(t *testing.T) {
	tests := []struct {
		name     string
		status   string
		wantCode codes.Code
	}{
		{name: "cancel draft", status: "draft"},
		{name: "cancel running", status: "running"},
		{name: "already completed", status: "completed", wantCode: codes.FailedPrecondition},
		{name: "already cancelled", status: "cancelled", wantCode: codes.FailedPrecondition},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &mockStore{
				getCampaignFn: func(_ context.Context, _ string) (*CampaignRow, error) {
					return makeCampaign("c-1", "ws-1", "t-1", "promo", tt.status), nil
				},
				updateCampaignStatusFn: func(_ context.Context, id, st string, _, _ bool) (*CampaignRow, error) {
					return makeCampaign(id, "ws-1", "t-1", "promo", st), nil
				},
			}
			h := newTestHandler(store)
			_, err := h.CancelCampaign(context.Background(), &hermesv1.CampaignCancelRequest{Id: "c-1"})
			if tt.wantCode != 0 {
				assertCode(t, err, tt.wantCode)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Campaign Numbers & Contacts Tests
// ---------------------------------------------------------------------------

func TestUpdateCampaignNumbers(t *testing.T) {
	tests := []struct {
		name     string
		status   string
		wantCode codes.Code
	}{
		{name: "allowed in draft", status: "draft"},
		{name: "allowed in paused", status: "paused"},
		{name: "rejected in running", status: "running", wantCode: codes.FailedPrecondition},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &mockStore{
				getCampaignFn: func(_ context.Context, _ string) (*CampaignRow, error) {
					return makeCampaign("c-1", "ws-1", "t-1", "promo", tt.status), nil
				},
				listCampaignNumbersFn: func(_ context.Context, _ string, _, _ int32) ([]*CampaignNumberRow, int64, error) {
					return nil, 0, nil
				},
			}
			h := newTestHandler(store)
			_, err := h.UpdateCampaignNumbers(context.Background(), &hermesv1.CampaignUpdateNumbersRequest{
				CampaignId: "c-1", AddWaNumberIds: []string{"n-1"},
			})
			if tt.wantCode != 0 {
				assertCode(t, err, tt.wantCode)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestUpdateCampaignContacts(t *testing.T) {
	tests := []struct {
		name     string
		status   string
		wantCode codes.Code
	}{
		{name: "allowed in draft", status: "draft"},
		{name: "rejected in running", status: "running", wantCode: codes.FailedPrecondition},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &mockStore{
				getCampaignFn: func(_ context.Context, _ string) (*CampaignRow, error) {
					return makeCampaign("c-1", "ws-1", "t-1", "promo", tt.status), nil
				},
			}
			h := newTestHandler(store)
			_, err := h.UpdateCampaignContacts(context.Background(), &hermesv1.CampaignUpdateContactsRequest{
				CampaignId: "c-1", AddContactIds: []string{"ct-1"},
			})
			if tt.wantCode != 0 {
				assertCode(t, err, tt.wantCode)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestListCampaignContacts(t *testing.T) {
	store := &mockStore{
		listCampaignContactsFn: func(_ context.Context, _ string, _ string, _, _ int32) ([]*CampaignContactJoinRow, int64, error) {
			return []*CampaignContactJoinRow{
				{CampaignID: "c-1", ContactID: "ct-1", Status: "sent", ContactName: "Alice", ContactPhone: "+628123"},
			}, 1, nil
		},
	}
	h := newTestHandler(store)

	resp, err := h.ListCampaignContacts(context.Background(), &hermesv1.CampaignListContactsRequest{CampaignId: "c-1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Contacts) != 1 {
		t.Fatalf("expected 1 contact, got %d", len(resp.Contacts))
	}
	if resp.Contacts[0].ContactName != "Alice" {
		t.Errorf("got name %q, want Alice", resp.Contacts[0].ContactName)
	}
}

// ---------------------------------------------------------------------------
// Conversion helpers tests
// ---------------------------------------------------------------------------

func TestCampaignStatusConversion(t *testing.T) {
	statuses := map[string]hermesv1.CampaignStatus{
		"draft":     hermesv1.CampaignStatus_CAMPAIGN_STATUS_DRAFT,
		"scheduled": hermesv1.CampaignStatus_CAMPAIGN_STATUS_SCHEDULED,
		"running":   hermesv1.CampaignStatus_CAMPAIGN_STATUS_RUNNING,
		"paused":    hermesv1.CampaignStatus_CAMPAIGN_STATUS_PAUSED,
		"completed": hermesv1.CampaignStatus_CAMPAIGN_STATUS_COMPLETED,
		"cancelled": hermesv1.CampaignStatus_CAMPAIGN_STATUS_CANCELLED,
	}

	for str, enum := range statuses {
		if got := strToCampaignStatus(str); got != enum {
			t.Errorf("strToCampaignStatus(%q) = %v, want %v", str, got, enum)
		}
		if got := campaignStatusToStr(enum); got != str {
			t.Errorf("campaignStatusToStr(%v) = %q, want %q", enum, got, str)
		}
	}
}

func TestRotationStrategyConversion(t *testing.T) {
	if got := rotationStrategyToStr(hermesv1.RotationStrategy_ROTATION_STRATEGY_ROUND_ROBIN); got != "round_robin" {
		t.Errorf("got %q, want round_robin", got)
	}
	if got := rotationStrategyToStr(hermesv1.RotationStrategy_ROTATION_STRATEGY_LEAST_USED); got != "least_used" {
		t.Errorf("got %q, want least_used", got)
	}
	if got := strToRotationStrategy("round_robin"); got != hermesv1.RotationStrategy_ROTATION_STRATEGY_ROUND_ROBIN {
		t.Errorf("got %v, want ROUND_ROBIN", got)
	}
}
