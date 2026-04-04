package handler

import (
	"context"
	"fmt"
	"testing"
	"time"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/hermes-waba/hermes/internal/gateway/middleware"
	"github.com/rs/zerolog"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ---------------------------------------------------------------------------
// Mock store
// ---------------------------------------------------------------------------

type mockStore struct {
	getUserByEmailFn        func(ctx context.Context, email string) (*UserRow, error)
	getUserByIDFn           func(ctx context.Context, id string) (*UserRow, error)
	createUserFn            func(ctx context.Context, tenantID, email, passwordHash, role string) (*UserRow, error)
	listUsersFn             func(ctx context.Context, workspaceID string, page, pageSize int32) ([]*UserRow, int64, error)
	updateUserFn            func(ctx context.Context, id, email, role, passwordHash string) (*UserRow, error)
	deleteUserFn            func(ctx context.Context, id string) error
	getUserWorkspaceIDsFn   func(ctx context.Context, userID string) ([]string, error)
	addWorkspaceMemberFn    func(ctx context.Context, userID, workspaceID, role string) error
	createTenantFn          func(ctx context.Context, name, settingsJSON string) (*TenantRow, error)
	getTenantFn             func(ctx context.Context, id string) (*TenantRow, error)
	listTenantsFn           func(ctx context.Context, page, pageSize int32) ([]*TenantRow, int64, error)
	updateTenantFn          func(ctx context.Context, id, name, settingsJSON string) (*TenantRow, error)
	createWorkspaceFn       func(ctx context.Context, tenantID, name, settingsJSON string, dailyCap int32) (*WorkspaceRow, error)
	getWorkspaceFn          func(ctx context.Context, id string) (*WorkspaceRow, error)
	listWorkspacesFn        func(ctx context.Context, tenantID string, page, pageSize int32) ([]*WorkspaceRow, int64, error)
	updateWorkspaceFn       func(ctx context.Context, id, name, settingsJSON string, dailyCap int32) (*WorkspaceRow, error)
	deleteWorkspaceFn       func(ctx context.Context, id string) error
	saveRefreshTokenFn      func(ctx context.Context, tokenID, userID string, expiresAt time.Time) error
	getRefreshTokenFn       func(ctx context.Context, tokenID string) (string, error)
	deleteRefreshTokenFn    func(ctx context.Context, tokenID string) error
	deleteUserRefreshTokensFn func(ctx context.Context, userID string) error
	getDashboardStatsFn     func(ctx context.Context, tenantID, workspaceID string) (*DashboardStatsRow, error)
}

func (m *mockStore) GetUserByEmail(ctx context.Context, email string) (*UserRow, error) {
	if m.getUserByEmailFn != nil {
		return m.getUserByEmailFn(ctx, email)
	}
	return nil, fmt.Errorf("not mocked")
}

func (m *mockStore) GetUserByID(ctx context.Context, id string) (*UserRow, error) {
	if m.getUserByIDFn != nil {
		return m.getUserByIDFn(ctx, id)
	}
	return nil, fmt.Errorf("not mocked")
}

func (m *mockStore) CreateUser(ctx context.Context, tenantID, email, passwordHash, role string) (*UserRow, error) {
	if m.createUserFn != nil {
		return m.createUserFn(ctx, tenantID, email, passwordHash, role)
	}
	return nil, fmt.Errorf("not mocked")
}

func (m *mockStore) ListUsers(ctx context.Context, workspaceID string, page, pageSize int32) ([]*UserRow, int64, error) {
	if m.listUsersFn != nil {
		return m.listUsersFn(ctx, workspaceID, page, pageSize)
	}
	return nil, 0, fmt.Errorf("not mocked")
}

func (m *mockStore) UpdateUser(ctx context.Context, id, email, role, passwordHash string) (*UserRow, error) {
	if m.updateUserFn != nil {
		return m.updateUserFn(ctx, id, email, role, passwordHash)
	}
	return nil, fmt.Errorf("not mocked")
}

func (m *mockStore) DeleteUser(ctx context.Context, id string) error {
	if m.deleteUserFn != nil {
		return m.deleteUserFn(ctx, id)
	}
	return fmt.Errorf("not mocked")
}

func (m *mockStore) GetUserWorkspaceIDs(ctx context.Context, userID string) ([]string, error) {
	if m.getUserWorkspaceIDsFn != nil {
		return m.getUserWorkspaceIDsFn(ctx, userID)
	}
	return nil, nil
}

func (m *mockStore) AddWorkspaceMember(ctx context.Context, userID, workspaceID, role string) error {
	if m.addWorkspaceMemberFn != nil {
		return m.addWorkspaceMemberFn(ctx, userID, workspaceID, role)
	}
	return fmt.Errorf("not mocked")
}

func (m *mockStore) CreateTenant(ctx context.Context, name, settingsJSON string) (*TenantRow, error) {
	if m.createTenantFn != nil {
		return m.createTenantFn(ctx, name, settingsJSON)
	}
	return nil, fmt.Errorf("not mocked")
}

func (m *mockStore) GetTenant(ctx context.Context, id string) (*TenantRow, error) {
	if m.getTenantFn != nil {
		return m.getTenantFn(ctx, id)
	}
	return nil, fmt.Errorf("not mocked")
}

func (m *mockStore) ListTenants(ctx context.Context, page, pageSize int32) ([]*TenantRow, int64, error) {
	if m.listTenantsFn != nil {
		return m.listTenantsFn(ctx, page, pageSize)
	}
	return nil, 0, fmt.Errorf("not mocked")
}

func (m *mockStore) UpdateTenant(ctx context.Context, id, name, settingsJSON string) (*TenantRow, error) {
	if m.updateTenantFn != nil {
		return m.updateTenantFn(ctx, id, name, settingsJSON)
	}
	return nil, fmt.Errorf("not mocked")
}

func (m *mockStore) CreateWorkspace(ctx context.Context, tenantID, name, settingsJSON string, dailyCap int32) (*WorkspaceRow, error) {
	if m.createWorkspaceFn != nil {
		return m.createWorkspaceFn(ctx, tenantID, name, settingsJSON, dailyCap)
	}
	return nil, fmt.Errorf("not mocked")
}

func (m *mockStore) GetWorkspace(ctx context.Context, id string) (*WorkspaceRow, error) {
	if m.getWorkspaceFn != nil {
		return m.getWorkspaceFn(ctx, id)
	}
	return nil, fmt.Errorf("not mocked")
}

func (m *mockStore) ListWorkspaces(ctx context.Context, tenantID string, page, pageSize int32) ([]*WorkspaceRow, int64, error) {
	if m.listWorkspacesFn != nil {
		return m.listWorkspacesFn(ctx, tenantID, page, pageSize)
	}
	return nil, 0, fmt.Errorf("not mocked")
}

func (m *mockStore) UpdateWorkspace(ctx context.Context, id, name, settingsJSON string, dailyCap int32) (*WorkspaceRow, error) {
	if m.updateWorkspaceFn != nil {
		return m.updateWorkspaceFn(ctx, id, name, settingsJSON, dailyCap)
	}
	return nil, fmt.Errorf("not mocked")
}

func (m *mockStore) DeleteWorkspace(ctx context.Context, id string) error {
	if m.deleteWorkspaceFn != nil {
		return m.deleteWorkspaceFn(ctx, id)
	}
	return fmt.Errorf("not mocked")
}

func (m *mockStore) SaveRefreshToken(ctx context.Context, tokenID, userID string, expiresAt time.Time) error {
	if m.saveRefreshTokenFn != nil {
		return m.saveRefreshTokenFn(ctx, tokenID, userID, expiresAt)
	}
	return nil
}

func (m *mockStore) GetRefreshToken(ctx context.Context, tokenID string) (string, error) {
	if m.getRefreshTokenFn != nil {
		return m.getRefreshTokenFn(ctx, tokenID)
	}
	return "", fmt.Errorf("not mocked")
}

func (m *mockStore) DeleteRefreshToken(ctx context.Context, tokenID string) error {
	if m.deleteRefreshTokenFn != nil {
		return m.deleteRefreshTokenFn(ctx, tokenID)
	}
	return nil
}

func (m *mockStore) DeleteUserRefreshTokens(ctx context.Context, userID string) error {
	if m.deleteUserRefreshTokensFn != nil {
		return m.deleteUserRefreshTokensFn(ctx, userID)
	}
	return nil
}

func (m *mockStore) GetDashboardStats(ctx context.Context, tenantID, workspaceID string) (*DashboardStatsRow, error) {
	if m.getDashboardStatsFn != nil {
		return m.getDashboardStatsFn(ctx, tenantID, workspaceID)
	}
	return nil, fmt.Errorf("not mocked")
}

// ---------------------------------------------------------------------------
// Mock inbox client
// ---------------------------------------------------------------------------

type mockInboxClient struct {
	hermesv1.HermesInboxClient // embed for default methods
	listConversationsFn func(ctx context.Context, req *hermesv1.InboxListConversationsRequest, opts ...grpc.CallOption) (*hermesv1.InboxListConversationsResponse, error)
}

func (m *mockInboxClient) ListConversations(ctx context.Context, req *hermesv1.InboxListConversationsRequest, opts ...grpc.CallOption) (*hermesv1.InboxListConversationsResponse, error) {
	if m.listConversationsFn != nil {
		return m.listConversationsFn(ctx, req, opts...)
	}
	return &hermesv1.InboxListConversationsResponse{}, nil
}

// ---------------------------------------------------------------------------
// Test helper
// ---------------------------------------------------------------------------

func newTestHandler(s Store) *Handler {
	return New(s, []byte("test-secret"), zerolog.Nop(), nil, nil, nil, nil, nil, nil)
}

func newTestHandlerWithInbox(s Store, inbox hermesv1.HermesInboxClient) *Handler {
	return New(s, []byte("test-secret"), zerolog.Nop(), nil, nil, nil, nil, inbox, nil)
}

// ---------------------------------------------------------------------------
// TestLogin
// ---------------------------------------------------------------------------

func TestLogin(t *testing.T) {
	hashedCorrect, err := bcrypt.GenerateFromPassword([]byte("correct"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}

	testUser := &UserRow{
		ID:           "u1",
		TenantID:     "t1",
		Email:        "test@example.com",
		PasswordHash: string(hashedCorrect),
		Role:         "workspace_admin",
		CreatedAt:    time.Now(),
	}

	tests := []struct {
		name     string
		req      *hermesv1.LoginRequest
		store    *mockStore
		wantCode codes.Code
		check    func(t *testing.T, resp *hermesv1.LoginResponse)
	}{
		{
			name:     "missing email",
			req:      &hermesv1.LoginRequest{Password: "pass"},
			store:    &mockStore{},
			wantCode: codes.InvalidArgument,
		},
		{
			name:     "missing password",
			req:      &hermesv1.LoginRequest{Email: "test@example.com"},
			store:    &mockStore{},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "user not found",
			req:  &hermesv1.LoginRequest{Email: "unknown@example.com", Password: "pass"},
			store: &mockStore{
				getUserByEmailFn: func(_ context.Context, _ string) (*UserRow, error) {
					return nil, ErrNotFound
				},
			},
			wantCode: codes.Unauthenticated,
		},
		{
			name: "wrong password",
			req:  &hermesv1.LoginRequest{Email: "test@example.com", Password: "wrong"},
			store: &mockStore{
				getUserByEmailFn: func(_ context.Context, _ string) (*UserRow, error) {
					return testUser, nil
				},
			},
			wantCode: codes.Unauthenticated,
		},
		{
			name: "success",
			req:  &hermesv1.LoginRequest{Email: "test@example.com", Password: "correct"},
			store: &mockStore{
				getUserByEmailFn: func(_ context.Context, _ string) (*UserRow, error) {
					return testUser, nil
				},
				getUserWorkspaceIDsFn: func(_ context.Context, _ string) ([]string, error) {
					return []string{"ws1"}, nil
				},
				saveRefreshTokenFn: func(_ context.Context, _, _ string, _ time.Time) error {
					return nil
				},
			},
			check: func(t *testing.T, resp *hermesv1.LoginResponse) {
				if resp.AccessToken == "" {
					t.Error("expected non-empty access token")
				}
				if resp.RefreshToken == "" {
					t.Error("expected non-empty refresh token")
				}
				if resp.ExpiresIn != 900 {
					t.Errorf("expected ExpiresIn=900, got %d", resp.ExpiresIn)
				}
				if resp.User == nil {
					t.Fatal("expected non-nil user")
				}
				if resp.User.Email != "test@example.com" {
					t.Errorf("expected email test@example.com, got %s", resp.User.Email)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHandler(tt.store)
			resp, err := h.Login(context.Background(), tt.req)

			if tt.wantCode != codes.OK {
				if err == nil {
					t.Fatalf("expected error code %v, got nil", tt.wantCode)
				}
				if got := status.Code(err); got != tt.wantCode {
					t.Fatalf("expected code %v, got %v: %v", tt.wantCode, got, err)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.check != nil {
				tt.check(t, resp)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestRefreshToken
// ---------------------------------------------------------------------------

func TestRefreshToken(t *testing.T) {
	testUser := &UserRow{
		ID:           "u1",
		TenantID:     "t1",
		Email:        "test@example.com",
		PasswordHash: "unused",
		Role:         "workspace_admin",
		CreatedAt:    time.Now(),
	}

	tests := []struct {
		name     string
		req      *hermesv1.RefreshTokenRequest
		store    *mockStore
		wantCode codes.Code
		check    func(t *testing.T, resp *hermesv1.RefreshTokenResponse)
	}{
		{
			name:     "missing token",
			req:      &hermesv1.RefreshTokenRequest{},
			store:    &mockStore{},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "invalid token",
			req:  &hermesv1.RefreshTokenRequest{RefreshToken: "bad-token"},
			store: &mockStore{
				getRefreshTokenFn: func(_ context.Context, _ string) (string, error) {
					return "", ErrNotFound
				},
			},
			wantCode: codes.Unauthenticated,
		},
		{
			name: "success",
			req:  &hermesv1.RefreshTokenRequest{RefreshToken: "valid-token-id"},
			store: &mockStore{
				getRefreshTokenFn: func(_ context.Context, _ string) (string, error) {
					return "u1", nil
				},
				deleteRefreshTokenFn: func(_ context.Context, _ string) error {
					return nil
				},
				getUserByIDFn: func(_ context.Context, _ string) (*UserRow, error) {
					return testUser, nil
				},
				getUserWorkspaceIDsFn: func(_ context.Context, _ string) ([]string, error) {
					return []string{"ws1"}, nil
				},
				saveRefreshTokenFn: func(_ context.Context, _, _ string, _ time.Time) error {
					return nil
				},
			},
			check: func(t *testing.T, resp *hermesv1.RefreshTokenResponse) {
				if resp.AccessToken == "" {
					t.Error("expected non-empty access token")
				}
				if resp.RefreshToken == "" {
					t.Error("expected non-empty refresh token")
				}
				if resp.ExpiresIn != 900 {
					t.Errorf("expected ExpiresIn=900, got %d", resp.ExpiresIn)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHandler(tt.store)
			resp, err := h.RefreshToken(context.Background(), tt.req)

			if tt.wantCode != codes.OK {
				if err == nil {
					t.Fatalf("expected error code %v, got nil", tt.wantCode)
				}
				if got := status.Code(err); got != tt.wantCode {
					t.Fatalf("expected code %v, got %v: %v", tt.wantCode, got, err)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.check != nil {
				tt.check(t, resp)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestListConversationsCSAgentFilter
// ---------------------------------------------------------------------------

func TestListConversationsCSAgentFilter(t *testing.T) {
	tests := []struct {
		name     string
		role     string
		userID   string
		req      *hermesv1.ListConversationsRequest
		wantCode codes.Code
		check    func(t *testing.T, captured *hermesv1.InboxListConversationsRequest)
	}{
		{
			name:   "cs_agent gets assigned_to injected",
			role:   "cs_agent",
			userID: "agent-1",
			req: &hermesv1.ListConversationsRequest{
				WorkspaceId: "ws1",
			},
			check: func(t *testing.T, captured *hermesv1.InboxListConversationsRequest) {
				if captured.AssignedTo != "agent-1" {
					t.Errorf("expected AssignedTo=agent-1, got %q", captured.AssignedTo)
				}
			},
		},
		{
			name:   "cs_agent cannot view other agent conversations",
			role:   "cs_agent",
			userID: "agent-1",
			req: &hermesv1.ListConversationsRequest{
				WorkspaceId: "ws1",
				AssignedTo:  "other-agent",
				Status:      hermesv1.ConversationStatus_CONVERSATION_STATUS_ASSIGNED,
			},
			wantCode: codes.PermissionDenied,
		},
		{
			name:   "admin sees all",
			role:   "workspace_admin",
			userID: "admin-1",
			req: &hermesv1.ListConversationsRequest{
				WorkspaceId: "ws1",
			},
			check: func(t *testing.T, captured *hermesv1.InboxListConversationsRequest) {
				if captured.AssignedTo != "" {
					t.Errorf("expected AssignedTo empty for admin, got %q", captured.AssignedTo)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var captured *hermesv1.InboxListConversationsRequest

			mockInbox := &mockInboxClient{
				listConversationsFn: func(_ context.Context, req *hermesv1.InboxListConversationsRequest, _ ...grpc.CallOption) (*hermesv1.InboxListConversationsResponse, error) {
					captured = req
					return &hermesv1.InboxListConversationsResponse{}, nil
				},
			}

			h := newTestHandlerWithInbox(&mockStore{}, mockInbox)

			ctx := context.WithValue(context.Background(), middleware.CtxRole, tt.role)
			ctx = context.WithValue(ctx, middleware.CtxUserID, tt.userID)

			_, err := h.ListConversations(ctx, tt.req)

			if tt.wantCode != codes.OK {
				if err == nil {
					t.Fatalf("expected error code %v, got nil", tt.wantCode)
				}
				if got := status.Code(err); got != tt.wantCode {
					t.Fatalf("expected code %v, got %v: %v", tt.wantCode, got, err)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.check != nil {
				if captured == nil {
					t.Fatal("inbox client was not called")
				}
				tt.check(t, captured)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestCreateTenant
// ---------------------------------------------------------------------------

func TestCreateTenant(t *testing.T) {
	tests := []struct {
		name     string
		req      *hermesv1.CreateTenantRequest
		store    *mockStore
		wantCode codes.Code
		check    func(t *testing.T, resp *hermesv1.CreateTenantResponse)
	}{
		{
			name:     "missing name",
			req:      &hermesv1.CreateTenantRequest{},
			store:    &mockStore{},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "success",
			req:  &hermesv1.CreateTenantRequest{Name: "Acme Corp"},
			store: &mockStore{
				createTenantFn: func(_ context.Context, name, _ string) (*TenantRow, error) {
					return &TenantRow{
						ID:           "tenant-1",
						Name:         name,
						SettingsJSON: "{}",
						CreatedAt:    time.Now(),
					}, nil
				},
			},
			check: func(t *testing.T, resp *hermesv1.CreateTenantResponse) {
				if resp.Tenant == nil {
					t.Fatal("expected non-nil tenant")
				}
				if resp.Tenant.Id != "tenant-1" {
					t.Errorf("expected tenant id=tenant-1, got %s", resp.Tenant.Id)
				}
				if resp.Tenant.Name != "Acme Corp" {
					t.Errorf("expected tenant name=Acme Corp, got %s", resp.Tenant.Name)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHandler(tt.store)
			resp, err := h.CreateTenant(context.Background(), tt.req)

			if tt.wantCode != codes.OK {
				if err == nil {
					t.Fatalf("expected error code %v, got nil", tt.wantCode)
				}
				if got := status.Code(err); got != tt.wantCode {
					t.Fatalf("expected code %v, got %v: %v", tt.wantCode, got, err)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.check != nil {
				tt.check(t, resp)
			}
		})
	}
}
