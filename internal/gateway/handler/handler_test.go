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
	createWaNumberFn           func(ctx context.Context, tenantID, phone, displayName, proxyID string) (string, error)
	assignWaNumberWorkspacesFn func(ctx context.Context, waNumberID string, workspaceIDs []string) error
	listWaNumbersFn              func(ctx context.Context, tenantID, workspaceID, statusFilter string, page, pageSize int32) ([]*WaNumberRow, int64, error)
	getWaNumberByIDFn            func(ctx context.Context, id string) (*WaNumberRow, error)
	getWaNumberWorkspaceIDsFn    func(ctx context.Context, waNumberID string) ([]string, error)
	deleteWaNumberFn             func(ctx context.Context, id string) error
	updateWaNumberFn             func(ctx context.Context, id, displayName, proxyID string) (*WaNumberRow, error)
	replaceWaNumberWorkspacesFn  func(ctx context.Context, waNumberID string, workspaceIDs []string) error
	clearAllConversationsFn      func(ctx context.Context, workspaceID string) (int64, error)
	addToAllowlistFn             func(ctx context.Context, workspaceID, phone, source string) error
	removeFromAllowlistFn        func(ctx context.Context, workspaceID, phone string) error
	clearAllowlistFn             func(ctx context.Context, workspaceID string) (int64, error)
	listAllowlistFn              func(ctx context.Context, workspaceID string, page, pageSize int32) ([]AllowlistRow, int64, error)
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

func (m *mockStore) CreateWaNumber(ctx context.Context, tenantID, phone, displayName, proxyID string) (string, error) {
	if m.createWaNumberFn != nil {
		return m.createWaNumberFn(ctx, tenantID, phone, displayName, proxyID)
	}
	return "mock-wa-number-id", nil
}

func (m *mockStore) AssignWaNumberWorkspaces(ctx context.Context, waNumberID string, workspaceIDs []string) error {
	if m.assignWaNumberWorkspacesFn != nil {
		return m.assignWaNumberWorkspacesFn(ctx, waNumberID, workspaceIDs)
	}
	return nil
}

func (m *mockStore) ListWaNumbers(ctx context.Context, tenantID, workspaceID, statusFilter string, page, pageSize int32) ([]*WaNumberRow, int64, error) {
	if m.listWaNumbersFn != nil {
		return m.listWaNumbersFn(ctx, tenantID, workspaceID, statusFilter, page, pageSize)
	}
	return nil, 0, nil
}

func (m *mockStore) GetWaNumberByID(ctx context.Context, id string) (*WaNumberRow, error) {
	if m.getWaNumberByIDFn != nil {
		return m.getWaNumberByIDFn(ctx, id)
	}
	return nil, ErrNotFound
}

func (m *mockStore) GetWaNumberWorkspaceIDs(ctx context.Context, waNumberID string) ([]string, error) {
	if m.getWaNumberWorkspaceIDsFn != nil {
		return m.getWaNumberWorkspaceIDsFn(ctx, waNumberID)
	}
	return nil, nil
}

func (m *mockStore) DeleteWaNumber(ctx context.Context, id string) error {
	if m.deleteWaNumberFn != nil {
		return m.deleteWaNumberFn(ctx, id)
	}
	return nil
}

func (m *mockStore) UpdateWaNumber(ctx context.Context, id, displayName, proxyID string) (*WaNumberRow, error) {
	if m.updateWaNumberFn != nil {
		return m.updateWaNumberFn(ctx, id, displayName, proxyID)
	}
	return &WaNumberRow{ID: id, DisplayName: displayName, Status: "disconnected"}, nil
}

func (m *mockStore) ReplaceWaNumberWorkspaces(ctx context.Context, waNumberID string, workspaceIDs []string) error {
	if m.replaceWaNumberWorkspacesFn != nil {
		return m.replaceWaNumberWorkspacesFn(ctx, waNumberID, workspaceIDs)
	}
	return nil
}

func (m *mockStore) ClearAllConversations(ctx context.Context, workspaceID string) (int64, error) {
	if m.clearAllConversationsFn != nil {
		return m.clearAllConversationsFn(ctx, workspaceID)
	}
	return 0, nil
}

func (m *mockStore) AddToAllowlist(ctx context.Context, workspaceID, phone, source string) error {
	if m.addToAllowlistFn != nil {
		return m.addToAllowlistFn(ctx, workspaceID, phone, source)
	}
	return nil
}

func (m *mockStore) ClearAllowlist(ctx context.Context, workspaceID string) (int64, error) {
	if m.clearAllowlistFn != nil {
		return m.clearAllowlistFn(ctx, workspaceID)
	}
	return 0, nil
}

func (m *mockStore) RemoveFromAllowlist(ctx context.Context, workspaceID, phone string) error {
	if m.removeFromAllowlistFn != nil {
		return m.removeFromAllowlistFn(ctx, workspaceID, phone)
	}
	return nil
}

func (m *mockStore) ListAllowlist(ctx context.Context, workspaceID string, page, pageSize int32) ([]AllowlistRow, int64, error) {
	if m.listAllowlistFn != nil {
		return m.listAllowlistFn(ctx, workspaceID, page, pageSize)
	}
	return nil, 0, nil
}

// ---------------------------------------------------------------------------
// Mock inbox client
// ---------------------------------------------------------------------------

type mockInboxClient struct {
	hermesv1.HermesInboxClient // embed for default methods
	listConversationsFn func(ctx context.Context, req *hermesv1.InboxListConversationsRequest, opts ...grpc.CallOption) (*hermesv1.InboxListConversationsResponse, error)
	getConversationFn   func(ctx context.Context, req *hermesv1.InboxGetConversationRequest, opts ...grpc.CallOption) (*hermesv1.InboxGetConversationResponse, error)
	listMessagesFn      func(ctx context.Context, req *hermesv1.InboxListMessagesRequest, opts ...grpc.CallOption) (*hermesv1.InboxListMessagesResponse, error)
	sendMessageFn       func(ctx context.Context, req *hermesv1.InboxSendMessageRequest, opts ...grpc.CallOption) (*hermesv1.InboxSendMessageResponse, error)
	transferFn          func(ctx context.Context, req *hermesv1.InboxTransferConversationRequest, opts ...grpc.CallOption) (*hermesv1.InboxTransferConversationResponse, error)
	closeFn             func(ctx context.Context, req *hermesv1.InboxCloseConversationRequest, opts ...grpc.CallOption) (*hermesv1.InboxCloseConversationResponse, error)
	searchMessagesFn    func(ctx context.Context, req *hermesv1.InboxSearchMessagesRequest, opts ...grpc.CallOption) (*hermesv1.InboxSearchMessagesResponse, error)
}

func (m *mockInboxClient) ListConversations(ctx context.Context, req *hermesv1.InboxListConversationsRequest, opts ...grpc.CallOption) (*hermesv1.InboxListConversationsResponse, error) {
	if m.listConversationsFn != nil {
		return m.listConversationsFn(ctx, req, opts...)
	}
	return &hermesv1.InboxListConversationsResponse{}, nil
}

func (m *mockInboxClient) GetConversation(ctx context.Context, req *hermesv1.InboxGetConversationRequest, opts ...grpc.CallOption) (*hermesv1.InboxGetConversationResponse, error) {
	if m.getConversationFn != nil {
		return m.getConversationFn(ctx, req, opts...)
	}
	return &hermesv1.InboxGetConversationResponse{}, nil
}

func (m *mockInboxClient) ListMessages(ctx context.Context, req *hermesv1.InboxListMessagesRequest, opts ...grpc.CallOption) (*hermesv1.InboxListMessagesResponse, error) {
	if m.listMessagesFn != nil {
		return m.listMessagesFn(ctx, req, opts...)
	}
	return &hermesv1.InboxListMessagesResponse{}, nil
}

func (m *mockInboxClient) SendMessage(ctx context.Context, req *hermesv1.InboxSendMessageRequest, opts ...grpc.CallOption) (*hermesv1.InboxSendMessageResponse, error) {
	if m.sendMessageFn != nil {
		return m.sendMessageFn(ctx, req, opts...)
	}
	return &hermesv1.InboxSendMessageResponse{}, nil
}

func (m *mockInboxClient) TransferConversation(ctx context.Context, req *hermesv1.InboxTransferConversationRequest, opts ...grpc.CallOption) (*hermesv1.InboxTransferConversationResponse, error) {
	if m.transferFn != nil {
		return m.transferFn(ctx, req, opts...)
	}
	return &hermesv1.InboxTransferConversationResponse{}, nil
}

func (m *mockInboxClient) CloseConversation(ctx context.Context, req *hermesv1.InboxCloseConversationRequest, opts ...grpc.CallOption) (*hermesv1.InboxCloseConversationResponse, error) {
	if m.closeFn != nil {
		return m.closeFn(ctx, req, opts...)
	}
	return &hermesv1.InboxCloseConversationResponse{}, nil
}

func (m *mockInboxClient) SearchMessages(ctx context.Context, req *hermesv1.InboxSearchMessagesRequest, opts ...grpc.CallOption) (*hermesv1.InboxSearchMessagesResponse, error) {
	if m.searchMessagesFn != nil {
		return m.searchMessagesFn(ctx, req, opts...)
	}
	return &hermesv1.InboxSearchMessagesResponse{}, nil
}

// ---------------------------------------------------------------------------
// Test helper
// ---------------------------------------------------------------------------

func newTestHandler(s Store) *Handler {
	return New(s, []byte("test-secret"), zerolog.Nop(), nil, nil, nil, nil, nil, nil, nil)
}

func newTestHandlerWithInbox(s Store, inbox hermesv1.HermesInboxClient) *Handler {
	return New(s, []byte("test-secret"), zerolog.Nop(), nil, nil, nil, nil, inbox, nil, nil)
}

func newTestHandlerWithMbs(s Store, mbs hermesv1.HermesMbsClient) *Handler {
	return New(s, []byte("test-secret"), zerolog.Nop(), nil, nil, nil, nil, nil, nil, mbs)
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
			name:   "cs_agent gets assigned_to injected with include_unassigned",
			role:   "cs_agent",
			userID: "agent-1",
			req: &hermesv1.ListConversationsRequest{
				WorkspaceId: "ws1",
			},
			check: func(t *testing.T, captured *hermesv1.InboxListConversationsRequest) {
				if captured.AssignedTo != "agent-1" {
					t.Errorf("expected AssignedTo=agent-1, got %q", captured.AssignedTo)
				}
				if !captured.IncludeUnassigned {
					t.Errorf("expected IncludeUnassigned=true for cs_agent, got false")
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
				if captured.IncludeUnassigned {
					t.Errorf("expected IncludeUnassigned=false for admin, got true")
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

// ---------------------------------------------------------------------------
// TestConversationAccessRBAC — per-conversation read/write ownership for cs_agent
//
// Ownership model:
//   - unassigned conversation  -> any cs_agent may read + act
//   - assigned to a cs_agent    -> only that agent + higher roles
// ---------------------------------------------------------------------------

func TestCanAccessConversation(t *testing.T) {
	cases := []struct {
		name       string
		role       string
		callerID   string
		assignedTo string
		want       bool
	}{
		{"cs unassigned", "cs_agent", "agent-1", "", true},
		{"cs own", "cs_agent", "agent-1", "agent-1", true},
		{"cs other", "cs_agent", "agent-1", "agent-2", false},
		{"workspace_admin other", "workspace_admin", "admin-1", "agent-2", true},
		{"tenant_admin other", "tenant_admin", "ta-1", "agent-2", true},
		{"superadmin other", "superadmin", "su-1", "agent-2", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := canAccessConversation(c.role, c.callerID, c.assignedTo); got != c.want {
				t.Errorf("canAccessConversation(%q,%q,%q) = %v, want %v",
					c.role, c.callerID, c.assignedTo, got, c.want)
			}
		})
	}
}

// ctxFor builds a request context with role + user id like AuthInterceptor would.
func ctxFor(role, userID string) context.Context {
	ctx := context.WithValue(context.Background(), middleware.CtxRole, role)
	return context.WithValue(ctx, middleware.CtxUserID, userID)
}

type matrixCase struct {
	name        string
	role        string
	callerID    string
	assignedTo  string
	wantCode    codes.Code // codes.OK = allowed
	wantNoFetch bool       // privileged path must skip the authorize round-trip
}

func accessMatrix() []matrixCase {
	return []matrixCase{
		{"cs unassigned -> ok", "cs_agent", "agent-1", "", codes.OK, false},
		{"cs own -> ok", "cs_agent", "agent-1", "agent-1", codes.OK, false},
		{"cs other -> denied", "cs_agent", "agent-1", "agent-2", codes.PermissionDenied, false},
		{"workspace_admin other -> ok", "workspace_admin", "admin-1", "agent-2", codes.OK, true},
		{"tenant_admin other -> ok", "tenant_admin", "ta-1", "agent-2", codes.OK, true},
		{"superadmin other -> ok", "superadmin", "su-1", "agent-2", codes.OK, true},
	}
}

func TestConversationAccessRBAC(t *testing.T) {
	endpoints := []struct {
		name   string
		invoke func(h *Handler, ctx context.Context) error
	}{
		{
			name: "GetConversation",
			invoke: func(h *Handler, ctx context.Context) error {
				_, err := h.GetConversation(ctx, &hermesv1.GetConversationRequest{Id: "conv-1"})
				return err
			},
		},
		{
			name: "ListMessages",
			invoke: func(h *Handler, ctx context.Context) error {
				_, err := h.ListMessages(ctx, &hermesv1.ListMessagesRequest{ConversationId: "conv-1"})
				return err
			},
		},
		{
			name: "SendMessage",
			invoke: func(h *Handler, ctx context.Context) error {
				_, err := h.SendMessage(ctx, &hermesv1.SendMessageRequest{ConversationId: "conv-1", Body: "hi"})
				return err
			},
		},
		{
			name: "TransferConversation",
			invoke: func(h *Handler, ctx context.Context) error {
				_, err := h.TransferConversation(ctx, &hermesv1.TransferConversationRequest{Id: "conv-1", TargetUserId: "agent-9"})
				return err
			},
		},
		{
			name: "CloseConversation",
			invoke: func(h *Handler, ctx context.Context) error {
				_, err := h.CloseConversation(ctx, &hermesv1.CloseConversationRequest{Id: "conv-1"})
				return err
			},
		},
	}

	for _, ep := range endpoints {
		for _, mc := range accessMatrix() {
			t.Run(ep.name+"/"+mc.name, func(t *testing.T) {
				fetched := false
				mockInbox := &mockInboxClient{
					getConversationFn: func(_ context.Context, _ *hermesv1.InboxGetConversationRequest, _ ...grpc.CallOption) (*hermesv1.InboxGetConversationResponse, error) {
						fetched = true
						return &hermesv1.InboxGetConversationResponse{
							Conversation: &hermesv1.Conversation{Id: "conv-1", AssignedTo: mc.assignedTo},
						}, nil
					},
				}
				h := newTestHandlerWithInbox(&mockStore{}, mockInbox)
				err := ep.invoke(h, ctxFor(mc.role, mc.callerID))

				if mc.wantCode == codes.OK {
					if err != nil {
						t.Fatalf("expected OK, got %v", err)
					}
				} else {
					if status.Code(err) != mc.wantCode {
						t.Fatalf("expected %v, got %v (%v)", mc.wantCode, status.Code(err), err)
					}
				}

				// GetConversation always fetches (it IS the data call). The no-fetch
				// invariant only applies to the authorize-wrapper endpoints.
				if ep.name != "GetConversation" && mc.wantNoFetch && fetched {
					t.Errorf("privileged role should skip authorize fetch, but GetConversation was called")
				}
			})
		}
	}
}

// TestSearchMessagesRBACScope asserts the gateway sets requester scope for
// cs_agent and leaves it empty for admins.
func TestSearchMessagesRBACScope(t *testing.T) {
	cases := []struct {
		name              string
		role              string
		userID            string
		wantRequester     string
		wantIncludeUnassd bool
	}{
		{"cs_agent scoped", "cs_agent", "agent-1", "agent-1", true},
		{"admin unscoped", "workspace_admin", "admin-1", "", false},
		{"superadmin unscoped", "superadmin", "su-1", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var captured *hermesv1.InboxSearchMessagesRequest
			mockInbox := &mockInboxClient{
				searchMessagesFn: func(_ context.Context, req *hermesv1.InboxSearchMessagesRequest, _ ...grpc.CallOption) (*hermesv1.InboxSearchMessagesResponse, error) {
					captured = req
					return &hermesv1.InboxSearchMessagesResponse{}, nil
				},
			}
			h := newTestHandlerWithInbox(&mockStore{}, mockInbox)
			_, err := h.SearchMessages(ctxFor(c.role, c.userID), &hermesv1.SearchMessagesRequest{
				WorkspaceId: "ws1", Query: "hello",
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if captured == nil {
				t.Fatal("inbox SearchMessages not called")
			}
			if captured.RequesterUserId != c.wantRequester {
				t.Errorf("RequesterUserId = %q, want %q", captured.RequesterUserId, c.wantRequester)
			}
			if captured.IncludeUnassigned != c.wantIncludeUnassd {
				t.Errorf("IncludeUnassigned = %v, want %v", captured.IncludeUnassigned, c.wantIncludeUnassd)
			}
		})
	}
}
