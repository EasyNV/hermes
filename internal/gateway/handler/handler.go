package handler

import (
	"context"
	"encoding/base64"
	"errors"

	"github.com/rs/zerolog"
	qrcode "github.com/skip2/go-qrcode"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/hermes-waba/hermes/internal/gateway/middleware"
)

// qrStringToPNGBase64 converts a whatsmeow QR string (e.g. "2@ABC...") into
// a base64-encoded PNG image suitable for <img src="data:image/png;base64,...">.
// The raw whatsmeow string is encoded directly — no wrapping, no modification.
// WhatsApp multi-device format: 2@<ref>,<noise_key>,<identity_key>,<adv_secret>
func qrStringToPNGBase64(qrStr string) string {
	if qrStr == "" {
		return ""
	}
	// Low recovery level to keep the QR simple and fast to scan.
	png, err := qrcode.Encode(qrStr, qrcode.Low, 256)
	if err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(png)
}

// ---------------------------------------------------------------------------
// Handler struct
// ---------------------------------------------------------------------------

// Handler implements the HermesGateway gRPC service. It holds references to
// the local Store (for gateway-owned entities), backend service gRPC clients
// (for routed RPCs), and auth configuration.
type Handler struct {
	hermesv1.UnimplementedHermesGatewayServer
	store     Store
	jwtSecret []byte
	log       zerolog.Logger

	// Backend service gRPC clients (nil-guarded for tests).
	waClient       hermesv1.HermesWaClient
	proxyClient    hermesv1.HermesProxyClient
	contactsClient hermesv1.HermesContactsClient
	campaignClient hermesv1.HermesCampaignClient
	inboxClient    hermesv1.HermesInboxClient
	notifyClient   hermesv1.HermesNotifyClient
	mbsClient      hermesv1.HermesMbsClient
}

// ---------------------------------------------------------------------------
// Constructor
// ---------------------------------------------------------------------------

// New creates a new Handler with all dependencies. Any backend client may be
// nil; the handler nil-guards every forwarded RPC call so that tests and
// partial deployments work without panics.
func New(
	store Store,
	jwtSecret []byte,
	log zerolog.Logger,
	waClient hermesv1.HermesWaClient,
	proxyClient hermesv1.HermesProxyClient,
	contactsClient hermesv1.HermesContactsClient,
	campaignClient hermesv1.HermesCampaignClient,
	inboxClient hermesv1.HermesInboxClient,
	notifyClient hermesv1.HermesNotifyClient,
	mbsClient hermesv1.HermesMbsClient,
) *Handler {
	return &Handler{
		store:          store,
		jwtSecret:      jwtSecret,
		log:            log,
		waClient:       waClient,
		proxyClient:    proxyClient,
		contactsClient: contactsClient,
		campaignClient: campaignClient,
		inboxClient:    inboxClient,
		notifyClient:   notifyClient,
		mbsClient:      mbsClient,
	}
}

// ---------------------------------------------------------------------------
// Pagination helpers
// ---------------------------------------------------------------------------

func clampPagination(p *hermesv1.PageRequest) (page, pageSize int32) {
	page = p.GetPage()
	pageSize = p.GetPageSize()
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 200 {
		pageSize = 200
	}
	return
}

func pageResponse(total int64, page, pageSize int32) *hermesv1.PageResponse {
	totalPages := int32(0)
	if total > 0 {
		totalPages = int32((total + int64(pageSize) - 1) / int64(pageSize))
	}
	return &hermesv1.PageResponse{
		Total:      total,
		Page:       page,
		PageSize:   pageSize,
		TotalPages: totalPages,
	}
}

// ===========================================================================
//
//  1. TENANT CRUD (gateway-owned, direct DB via store)
//
// ===========================================================================

func (h *Handler) CreateTenant(ctx context.Context, req *hermesv1.CreateTenantRequest) (*hermesv1.CreateTenantResponse, error) {
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}

	row, err := h.store.CreateTenant(ctx, req.GetName(), req.GetSettingsJson())
	if err != nil {
		h.log.Error().Err(err).Msg("failed to create tenant")
		return nil, status.Error(codes.Internal, "internal error")
	}
	return &hermesv1.CreateTenantResponse{Tenant: tenantToProto(row)}, nil
}

func (h *Handler) GetTenant(ctx context.Context, req *hermesv1.GetTenantRequest) (*hermesv1.GetTenantResponse, error) {
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	row, err := h.store.GetTenant(ctx, req.GetId())
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, status.Error(codes.NotFound, "tenant not found")
		}
		h.log.Error().Err(err).Str("id", req.GetId()).Msg("failed to get tenant")
		return nil, status.Error(codes.Internal, "internal error")
	}
	return &hermesv1.GetTenantResponse{Tenant: tenantToProto(row)}, nil
}

func (h *Handler) ListTenants(ctx context.Context, req *hermesv1.ListTenantsRequest) (*hermesv1.ListTenantsResponse, error) {
	page, pageSize := clampPagination(req.GetPagination())

	rows, total, err := h.store.ListTenants(ctx, page, pageSize)
	if err != nil {
		h.log.Error().Err(err).Msg("failed to list tenants")
		return nil, status.Error(codes.Internal, "internal error")
	}

	tenants := make([]*hermesv1.Tenant, 0, len(rows))
	for _, r := range rows {
		tenants = append(tenants, tenantToProto(r))
	}
	return &hermesv1.ListTenantsResponse{
		Tenants:    tenants,
		Pagination: pageResponse(total, page, pageSize),
	}, nil
}

func (h *Handler) UpdateTenant(ctx context.Context, req *hermesv1.UpdateTenantRequest) (*hermesv1.UpdateTenantResponse, error) {
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	row, err := h.store.UpdateTenant(ctx, req.GetId(), req.GetName(), req.GetSettingsJson())
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, status.Error(codes.NotFound, "tenant not found")
		}
		h.log.Error().Err(err).Str("id", req.GetId()).Msg("failed to update tenant")
		return nil, status.Error(codes.Internal, "internal error")
	}
	return &hermesv1.UpdateTenantResponse{Tenant: tenantToProto(row)}, nil
}

// ===========================================================================
//
//  2. WORKSPACE CRUD (gateway-owned)
//
// ===========================================================================

func (h *Handler) CreateWorkspace(ctx context.Context, req *hermesv1.CreateWorkspaceRequest) (*hermesv1.CreateWorkspaceResponse, error) {
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}

	row, err := h.store.CreateWorkspace(ctx, req.GetTenantId(), req.GetName(), req.GetSettingsJson(), req.GetDailyCap())
	if err != nil {
		h.log.Error().Err(err).Msg("failed to create workspace")
		return nil, status.Error(codes.Internal, "internal error")
	}
	return &hermesv1.CreateWorkspaceResponse{Workspace: workspaceToProto(row)}, nil
}

func (h *Handler) GetWorkspace(ctx context.Context, req *hermesv1.GetWorkspaceRequest) (*hermesv1.GetWorkspaceResponse, error) {
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	row, err := h.store.GetWorkspace(ctx, req.GetId())
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, status.Error(codes.NotFound, "workspace not found")
		}
		h.log.Error().Err(err).Str("id", req.GetId()).Msg("failed to get workspace")
		return nil, status.Error(codes.Internal, "internal error")
	}
	return &hermesv1.GetWorkspaceResponse{Workspace: workspaceToProto(row)}, nil
}

func (h *Handler) ListWorkspaces(ctx context.Context, req *hermesv1.ListWorkspacesRequest) (*hermesv1.ListWorkspacesResponse, error) {
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	page, pageSize := clampPagination(req.GetPagination())

	rows, total, err := h.store.ListWorkspaces(ctx, req.GetTenantId(), page, pageSize)
	if err != nil {
		h.log.Error().Err(err).Msg("failed to list workspaces")
		return nil, status.Error(codes.Internal, "internal error")
	}

	workspaces := make([]*hermesv1.Workspace, 0, len(rows))
	for _, r := range rows {
		workspaces = append(workspaces, workspaceToProto(r))
	}
	return &hermesv1.ListWorkspacesResponse{
		Workspaces: workspaces,
		Pagination: pageResponse(total, page, pageSize),
	}, nil
}

func (h *Handler) UpdateWorkspace(ctx context.Context, req *hermesv1.UpdateWorkspaceRequest) (*hermesv1.UpdateWorkspaceResponse, error) {
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	row, err := h.store.UpdateWorkspace(ctx, req.GetId(), req.GetName(), req.GetSettingsJson(), req.GetDailyCap())
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, status.Error(codes.NotFound, "workspace not found")
		}
		h.log.Error().Err(err).Str("id", req.GetId()).Msg("failed to update workspace")
		return nil, status.Error(codes.Internal, "internal error")
	}
	return &hermesv1.UpdateWorkspaceResponse{Workspace: workspaceToProto(row)}, nil
}

func (h *Handler) DeleteWorkspace(ctx context.Context, req *hermesv1.DeleteWorkspaceRequest) (*hermesv1.DeleteWorkspaceResponse, error) {
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	if err := h.store.DeleteWorkspace(ctx, req.GetId()); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, status.Error(codes.NotFound, "workspace not found")
		}
		h.log.Error().Err(err).Str("id", req.GetId()).Msg("failed to delete workspace")
		return nil, status.Error(codes.Internal, "internal error")
	}
	return &hermesv1.DeleteWorkspaceResponse{}, nil
}

// ===========================================================================
//
//  3. USER CRUD (gateway-owned)
//
// ===========================================================================

func (h *Handler) CreateUser(ctx context.Context, req *hermesv1.CreateUserRequest) (*hermesv1.CreateUserResponse, error) {
	if req.GetWorkspaceId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id is required")
	}
	if req.GetEmail() == "" {
		return nil, status.Error(codes.InvalidArgument, "email is required")
	}
	if req.GetPassword() == "" {
		return nil, status.Error(codes.InvalidArgument, "password is required")
	}
	if req.GetRole() == hermesv1.Role_ROLE_UNSPECIFIED {
		return nil, status.Error(codes.InvalidArgument, "role is required")
	}

	// Resolve tenant_id from workspace.
	ws, err := h.store.GetWorkspace(ctx, req.GetWorkspaceId())
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, status.Error(codes.NotFound, "workspace not found")
		}
		h.log.Error().Err(err).Msg("failed to get workspace for user creation")
		return nil, status.Error(codes.Internal, "internal error")
	}

	// Hash password.
	hash, err := bcrypt.GenerateFromPassword([]byte(req.GetPassword()), bcrypt.DefaultCost)
	if err != nil {
		h.log.Error().Err(err).Msg("failed to hash password")
		return nil, status.Error(codes.Internal, "internal error")
	}

	role := roleToStr(req.GetRole())
	user, err := h.store.CreateUser(ctx, ws.TenantID, req.GetEmail(), string(hash), role)
	if err != nil {
		if errors.Is(err, ErrAlreadyExists) {
			return nil, status.Error(codes.AlreadyExists, "user with this email already exists")
		}
		h.log.Error().Err(err).Msg("failed to create user")
		return nil, status.Error(codes.Internal, "internal error")
	}

	// Add workspace membership.
	if err := h.store.AddWorkspaceMember(ctx, user.ID, req.GetWorkspaceId(), role); err != nil {
		h.log.Error().Err(err).Msg("failed to add workspace member")
		return nil, status.Error(codes.Internal, "internal error")
	}

	return &hermesv1.CreateUserResponse{User: userToProto(user, req.GetWorkspaceId())}, nil
}

func (h *Handler) GetUser(ctx context.Context, req *hermesv1.GetUserRequest) (*hermesv1.GetUserResponse, error) {
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	user, err := h.store.GetUserByID(ctx, req.GetId())
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, status.Error(codes.NotFound, "user not found")
		}
		h.log.Error().Err(err).Str("id", req.GetId()).Msg("failed to get user")
		return nil, status.Error(codes.Internal, "internal error")
	}

	// Resolve workspace.
	wsIDs, _ := h.store.GetUserWorkspaceIDs(ctx, user.ID)
	var workspaceID string
	if len(wsIDs) > 0 {
		workspaceID = wsIDs[0]
	}

	return &hermesv1.GetUserResponse{User: userToProto(user, workspaceID)}, nil
}

func (h *Handler) ListUsers(ctx context.Context, req *hermesv1.ListUsersRequest) (*hermesv1.ListUsersResponse, error) {
	if req.GetWorkspaceId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id is required")
	}
	page, pageSize := clampPagination(req.GetPagination())

	rows, total, err := h.store.ListUsers(ctx, req.GetWorkspaceId(), page, pageSize)
	if err != nil {
		h.log.Error().Err(err).Msg("failed to list users")
		return nil, status.Error(codes.Internal, "internal error")
	}

	users := make([]*hermesv1.User, 0, len(rows))
	for _, r := range rows {
		users = append(users, userToProto(r, req.GetWorkspaceId()))
	}
	return &hermesv1.ListUsersResponse{
		Users:      users,
		Pagination: pageResponse(total, page, pageSize),
	}, nil
}

func (h *Handler) UpdateUser(ctx context.Context, req *hermesv1.UpdateUserRequest) (*hermesv1.UpdateUserResponse, error) {
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	var passwordHash string
	if req.GetPassword() != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(req.GetPassword()), bcrypt.DefaultCost)
		if err != nil {
			h.log.Error().Err(err).Msg("failed to hash password")
			return nil, status.Error(codes.Internal, "internal error")
		}
		passwordHash = string(hash)
	}

	role := roleToStr(req.GetRole())
	user, err := h.store.UpdateUser(ctx, req.GetId(), req.GetEmail(), role, passwordHash)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, status.Error(codes.NotFound, "user not found")
		}
		h.log.Error().Err(err).Str("id", req.GetId()).Msg("failed to update user")
		return nil, status.Error(codes.Internal, "internal error")
	}

	wsIDs, _ := h.store.GetUserWorkspaceIDs(ctx, user.ID)
	var workspaceID string
	if len(wsIDs) > 0 {
		workspaceID = wsIDs[0]
	}

	return &hermesv1.UpdateUserResponse{User: userToProto(user, workspaceID)}, nil
}

func (h *Handler) DeleteUser(ctx context.Context, req *hermesv1.DeleteUserRequest) (*hermesv1.DeleteUserResponse, error) {
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	if err := h.store.DeleteUser(ctx, req.GetId()); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, status.Error(codes.NotFound, "user not found")
		}
		h.log.Error().Err(err).Str("id", req.GetId()).Msg("failed to delete user")
		return nil, status.Error(codes.Internal, "internal error")
	}
	return &hermesv1.DeleteUserResponse{}, nil
}

// ===========================================================================
//
//  4. WHATSAPP NUMBER RPCs (forward to WA service)
//
// ===========================================================================

func (h *Handler) RegisterWaNumber(ctx context.Context, req *hermesv1.RegisterWaNumberRequest) (*hermesv1.RegisterWaNumberResponse, error) {
	if req.GetPhone() == "" {
		return nil, status.Error(codes.InvalidArgument, "phone is required")
	}

	// Resolve tenant from JWT context.
	tenantID := req.GetTenantId()
	if tenantID == "" {
		tenantID = middleware.TenantIDFromCtx(ctx)
	}
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	// 1. Create the wa_number DB record (or upsert if phone exists).
	waNumberID, err := h.store.CreateWaNumber(ctx, tenantID, req.GetPhone(), req.GetDisplayName(), req.GetProxyId())
	if err != nil {
		h.log.Error().Err(err).Str("phone", req.GetPhone()).Msg("failed to create wa_number")
		return nil, status.Errorf(codes.Internal, "failed to create number: %v", err)
	}

	// 2. Assign to workspaces.
	if len(req.GetWorkspaceIds()) > 0 {
		if err := h.store.AssignWaNumberWorkspaces(ctx, waNumberID, req.GetWorkspaceIds()); err != nil {
			h.log.Error().Err(err).Str("wa_number_id", waNumberID).Msg("failed to assign workspaces")
			// Non-fatal — number is created, workspace assignment can be retried.
		}
	}

	waNumber := &hermesv1.WaNumber{
		Id:           waNumberID,
		TenantId:     tenantID,
		Phone:        req.GetPhone(),
		DisplayName:  req.GetDisplayName(),
		ProxyId:      req.GetProxyId(),
		WorkspaceIds: req.GetWorkspaceIds(),
	}

	// 3. Optionally connect the session (triggers QR code).
	var qrCode string
	if h.waClient != nil {
		resp, err := h.waClient.ConnectSession(ctx, &hermesv1.ConnectSessionRequest{
			WaNumberId: waNumberID,
			ProxyId:    req.GetProxyId(),
		})
		if err != nil {
			h.log.Warn().Err(err).Str("wa_number_id", waNumberID).Msg("connect session failed (number registered but not connected)")
			// Non-fatal — return the number without QR. User can reconnect later.
		} else {
			qrCode = qrStringToPNGBase64(resp.GetQrCode())
			if resp.GetSession() != nil {
				waNumber.Jid = resp.GetSession().GetJid()
				waNumber.PodId = resp.GetSession().GetPodId()
				waNumber.ConnectedAt = resp.GetSession().GetConnectedAt()
			}
		}
	}

	return &hermesv1.RegisterWaNumberResponse{
		WaNumber: waNumber,
		QrCode:   qrCode,
	}, nil
}

func (h *Handler) GetQRCode(ctx context.Context, req *hermesv1.GetQRCodeRequest) (*hermesv1.GetQRCodeResponse, error) {
	if h.waClient == nil {
		return nil, status.Error(codes.Unavailable, "wa service not available")
	}
	if req.GetWaNumberId() == "" {
		return nil, status.Error(codes.InvalidArgument, "wa_number_id is required")
	}

	resp, err := h.waClient.GetQRCode(ctx, &hermesv1.WaGetQRCodeRequest{
		WaNumberId: req.GetWaNumberId(),
	})
	if err != nil {
		return nil, err
	}

	return &hermesv1.GetQRCodeResponse{
		QrCode:   qrStringToPNGBase64(resp.GetQrCode()),
		IsLinked: resp.GetIsLinked(),
	}, nil
}

func (h *Handler) ListWaNumbers(ctx context.Context, req *hermesv1.ListWaNumbersRequest) (*hermesv1.ListWaNumbersResponse, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		tenantID = middleware.TenantIDFromCtx(ctx)
	}

	// Map proto enum to DB status string.
	var statusFilter string
	switch req.GetStatus() {
	case hermesv1.WaNumberStatus_WA_NUMBER_STATUS_ACTIVE:
		statusFilter = "active"
	case hermesv1.WaNumberStatus_WA_NUMBER_STATUS_BANNED:
		statusFilter = "banned"
	case hermesv1.WaNumberStatus_WA_NUMBER_STATUS_DISCONNECTED:
		statusFilter = "disconnected"
	case hermesv1.WaNumberStatus_WA_NUMBER_STATUS_COOLDOWN:
		statusFilter = "cooldown"
	}

	page, pageSize := clampPagination(req.GetPagination())
	rows, total, err := h.store.ListWaNumbers(ctx, tenantID, req.GetWorkspaceId(), statusFilter, page, pageSize)
	if err != nil {
		h.log.Error().Err(err).Msg("failed to list wa_numbers")
		return nil, status.Error(codes.Internal, "internal error")
	}

	var numbers []*hermesv1.WaNumber
	for _, r := range rows {
		// Get workspace IDs for each number.
		wsIDs, _ := h.store.GetWaNumberWorkspaceIDs(ctx, r.ID)
		n := waNumberRowToProto(r)
		n.WorkspaceIds = wsIDs
		numbers = append(numbers, n)
	}

	return &hermesv1.ListWaNumbersResponse{
		WaNumbers:  numbers,
		Pagination: pageResponse(total, page, pageSize),
	}, nil
}

func (h *Handler) GetWaNumber(ctx context.Context, req *hermesv1.GetWaNumberRequest) (*hermesv1.GetWaNumberResponse, error) {
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	row, err := h.store.GetWaNumberByID(ctx, req.GetId())
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, status.Error(codes.NotFound, "wa number not found")
		}
		return nil, status.Error(codes.Internal, "internal error")
	}

	n := waNumberRowToProto(row)
	wsIDs, _ := h.store.GetWaNumberWorkspaceIDs(ctx, row.ID)
	n.WorkspaceIds = wsIDs

	return &hermesv1.GetWaNumberResponse{WaNumber: n}, nil
}

func (h *Handler) UpdateWaNumber(ctx context.Context, req *hermesv1.UpdateWaNumberRequest) (*hermesv1.UpdateWaNumberResponse, error) {
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	row, err := h.store.UpdateWaNumber(ctx, req.GetId(), req.GetDisplayName(), req.GetProxyId())
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, status.Error(codes.NotFound, "wa number not found")
		}
		h.log.Error().Err(err).Str("id", req.GetId()).Msg("failed to update wa_number")
		return nil, status.Error(codes.Internal, "internal error")
	}

	// Replace workspace assignments if provided.
	if len(req.GetWorkspaceIds()) > 0 {
		if err := h.store.ReplaceWaNumberWorkspaces(ctx, req.GetId(), req.GetWorkspaceIds()); err != nil {
			h.log.Error().Err(err).Str("id", req.GetId()).Msg("failed to update workspace assignments")
		}
	}

	n := waNumberRowToProto(row)
	wsIDs, _ := h.store.GetWaNumberWorkspaceIDs(ctx, row.ID)
	n.WorkspaceIds = wsIDs

	return &hermesv1.UpdateWaNumberResponse{WaNumber: n}, nil
}

func (h *Handler) DisconnectWaNumber(ctx context.Context, req *hermesv1.DisconnectWaNumberRequest) (*hermesv1.DisconnectWaNumberResponse, error) {
	if h.waClient == nil {
		return nil, status.Error(codes.Unavailable, "wa service not available")
	}
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	_, err := h.waClient.DisconnectSession(ctx, &hermesv1.DisconnectSessionRequest{
		WaNumberId: req.GetId(),
	})
	if err != nil {
		return nil, err
	}

	return &hermesv1.DisconnectWaNumberResponse{
		WaNumber: &hermesv1.WaNumber{
			Id:     req.GetId(),
			Status: hermesv1.WaNumberStatus_WA_NUMBER_STATUS_DISCONNECTED,
		},
	}, nil
}

func (h *Handler) ReconnectWaNumber(ctx context.Context, req *hermesv1.ReconnectWaNumberRequest) (*hermesv1.ReconnectWaNumberResponse, error) {
	if h.waClient == nil {
		return nil, status.Error(codes.Unavailable, "wa service not available")
	}
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	resp, err := h.waClient.ConnectSession(ctx, &hermesv1.ConnectSessionRequest{
		WaNumberId: req.GetId(),
	})
	if err != nil {
		return nil, err
	}

	var waNumber *hermesv1.WaNumber
	if resp.GetSession() != nil {
		s := resp.GetSession()
		waNumber = &hermesv1.WaNumber{
			Id:          s.GetWaNumberId(),
			Jid:         s.GetJid(),
			Phone:       s.GetPhone(),
			ProxyId:     s.GetProxyId(),
			PodId:       s.GetPodId(),
			ConnectedAt: s.GetConnectedAt(),
		}
	}

	return &hermesv1.ReconnectWaNumberResponse{
		WaNumber: waNumber,
		QrCode:   qrStringToPNGBase64(resp.GetQrCode()),
	}, nil
}

func (h *Handler) DeleteWaNumber(ctx context.Context, req *hermesv1.DeleteWaNumberRequest) (*hermesv1.DeleteWaNumberResponse, error) {
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	// 1. Try to disconnect the WA session (best-effort).
	if h.waClient != nil {
		_, err := h.waClient.DisconnectSession(ctx, &hermesv1.DisconnectSessionRequest{
			WaNumberId: req.GetId(),
		})
		if err != nil {
			h.log.Warn().Err(err).Str("id", req.GetId()).Msg("disconnect failed during delete, proceeding with DB cleanup")
		}
	}

	// 2. Delete the DB row (cascades to wa_number_workspaces).
	if err := h.store.DeleteWaNumber(ctx, req.GetId()); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, status.Error(codes.NotFound, "wa number not found")
		}
		h.log.Error().Err(err).Str("id", req.GetId()).Msg("failed to delete wa_number")
		return nil, status.Error(codes.Internal, "internal error")
	}

	return &hermesv1.DeleteWaNumberResponse{}, nil
}

// ===========================================================================
//
//  5. PROXY RPCs (forward to proxy service)
//
// ===========================================================================

func (h *Handler) AddProxies(ctx context.Context, req *hermesv1.AddProxiesRequest) (*hermesv1.AddProxiesResponse, error) {
	if h.proxyClient == nil {
		return nil, status.Error(codes.Unavailable, "proxy service not available")
	}
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if len(req.GetProxies()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "at least one proxy is required")
	}

	// Translate gateway ProxyInput -> backend ProxyAddInput.
	inputs := make([]*hermesv1.ProxyAddInput, 0, len(req.GetProxies()))
	for _, p := range req.GetProxies() {
		inputs = append(inputs, &hermesv1.ProxyAddInput{
			Host:     p.GetHost(),
			Port:     p.GetPort(),
			Username: p.GetUsername(),
			Password: p.GetPassword(),
			Type:     p.GetType(),
		})
	}

	resp, err := h.proxyClient.AddProxies(ctx, &hermesv1.ProxyAddRequest{
		TenantId: req.GetTenantId(),
		Proxies:  inputs,
	})
	if err != nil {
		return nil, err
	}

	return &hermesv1.AddProxiesResponse{
		Proxies:      resp.GetProxies(),
		SkippedCount: resp.GetSkippedCount(),
	}, nil
}

func (h *Handler) ListProxies(ctx context.Context, req *hermesv1.ListProxiesRequest) (*hermesv1.ListProxiesResponse, error) {
	if h.proxyClient == nil {
		return nil, status.Error(codes.Unavailable, "proxy service not available")
	}
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	resp, err := h.proxyClient.ListProxies(ctx, &hermesv1.ProxyListRequest{
		TenantId:   req.GetTenantId(),
		Status:     req.GetStatus(),
		Pagination: req.GetPagination(),
	})
	if err != nil {
		return nil, err
	}

	return &hermesv1.ListProxiesResponse{
		Proxies:    resp.GetProxies(),
		Pagination: resp.GetPagination(),
	}, nil
}

func (h *Handler) UpdateProxy(ctx context.Context, req *hermesv1.UpdateProxyRequest) (*hermesv1.UpdateProxyResponse, error) {
	if h.proxyClient == nil {
		return nil, status.Error(codes.Unavailable, "proxy service not available")
	}
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	resp, err := h.proxyClient.UpdateProxy(ctx, &hermesv1.ProxyUpdateRequest{
		Id:       req.GetId(),
		Host:     req.GetHost(),
		Port:     req.GetPort(),
		Username: req.GetUsername(),
		Password: req.GetPassword(),
		Type:     req.GetType(),
		Status:   req.GetStatus(),
	})
	if err != nil {
		return nil, err
	}

	return &hermesv1.UpdateProxyResponse{Proxy: resp.GetProxy()}, nil
}

func (h *Handler) DeleteProxy(ctx context.Context, req *hermesv1.DeleteProxyRequest) (*hermesv1.DeleteProxyResponse, error) {
	if h.proxyClient == nil {
		return nil, status.Error(codes.Unavailable, "proxy service not available")
	}
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	_, err := h.proxyClient.DeleteProxy(ctx, &hermesv1.ProxyDeleteRequest{
		Id: req.GetId(),
	})
	if err != nil {
		return nil, err
	}

	return &hermesv1.DeleteProxyResponse{}, nil
}

func (h *Handler) AssignProxy(ctx context.Context, req *hermesv1.AssignProxyRequest) (*hermesv1.AssignProxyResponse, error) {
	if h.proxyClient == nil {
		return nil, status.Error(codes.Unavailable, "proxy service not available")
	}
	if req.GetWaNumberId() == "" {
		return nil, status.Error(codes.InvalidArgument, "wa_number_id is required")
	}

	if _, err := h.proxyClient.AssignProxy(ctx, &hermesv1.ProxyAssignRequest{
		WaNumberId: req.GetWaNumberId(),
		ProxyId:    req.GetProxyId(),
	}); err != nil {
		return nil, err
	}

	// ProxyAssignResponse returns the proxy, not the WA number. Build a minimal
	// WaNumber with the assignment reflected.
	return &hermesv1.AssignProxyResponse{
		WaNumber: &hermesv1.WaNumber{
			Id:      req.GetWaNumberId(),
			ProxyId: req.GetProxyId(),
		},
	}, nil
}

func (h *Handler) GetProxyHealth(ctx context.Context, req *hermesv1.GetProxyHealthRequest) (*hermesv1.GetProxyHealthResponse, error) {
	if h.proxyClient == nil {
		return nil, status.Error(codes.Unavailable, "proxy service not available")
	}
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	resp, err := h.proxyClient.GetProxyHealth(ctx, &hermesv1.ProxyGetHealthRequest{
		Id: req.GetId(),
	})
	if err != nil {
		return nil, err
	}

	return &hermesv1.GetProxyHealthResponse{
		Proxy:     resp.GetProxy(),
		LatencyMs: resp.GetLatencyMs(),
		Reachable: resp.GetReachable(),
		CheckedAt: resp.GetCheckedAt(),
	}, nil
}

func (h *Handler) GetBestProxy(ctx context.Context, req *hermesv1.GetBestProxyRequest) (*hermesv1.GetBestProxyResponse, error) {
	if h.proxyClient == nil {
		return nil, status.Error(codes.Unavailable, "proxy service not available")
	}
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	resp, err := h.proxyClient.GetBestProxy(ctx, &hermesv1.ProxyGetBestRequest{
		TenantId: req.GetTenantId(),
		Type:     req.GetType(),
	})
	if err != nil {
		return nil, err
	}

	return &hermesv1.GetBestProxyResponse{
		Proxy:         resp.GetProxy(),
		PoolExhausted: resp.GetPoolExhausted(),
	}, nil
}

// ===========================================================================
//
//  6. CONTACT RPCs (forward to contacts service)
//
// ===========================================================================

func (h *Handler) CreateContact(ctx context.Context, req *hermesv1.CreateContactRequest) (*hermesv1.CreateContactResponse, error) {
	if h.contactsClient == nil {
		return nil, status.Error(codes.Unavailable, "contacts service not available")
	}
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if req.GetPhone() == "" {
		return nil, status.Error(codes.InvalidArgument, "phone is required")
	}

	resp, err := h.contactsClient.CreateContact(ctx, &hermesv1.ContactsCreateRequest{
		TenantId:     req.GetTenantId(),
		Phone:        req.GetPhone(),
		Name:         req.GetName(),
		Tags:         req.GetTags(),
		CustomFields: req.GetCustomFields(),
	})
	if err != nil {
		return nil, err
	}

	return &hermesv1.CreateContactResponse{
		Contact:        resp.GetContact(),
		AlreadyExisted: resp.GetAlreadyExisted(),
	}, nil
}

func (h *Handler) ImportContacts(ctx context.Context, req *hermesv1.ImportContactsRequest) (*hermesv1.ImportContactsResponse, error) {
	if h.contactsClient == nil {
		return nil, status.Error(codes.Unavailable, "contacts service not available")
	}
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	resp, err := h.contactsClient.ImportContacts(ctx, &hermesv1.ContactsImportRequest{
		TenantId:      req.GetTenantId(),
		CsvData:       req.GetCsvData(),
		Filename:      req.GetFilename(),
		ColumnMapping: req.GetColumnMapping(),
		DefaultTags:   req.GetDefaultTags(),
	})
	if err != nil {
		return nil, err
	}

	// Translate backend ImportError -> gateway ImportError.
	importErrors := make([]*hermesv1.ImportError, 0, len(resp.GetErrors()))
	for _, e := range resp.GetErrors() {
		importErrors = append(importErrors, &hermesv1.ImportError{
			Row:   e.GetRow(),
			Phone: e.GetPhone(),
			Error: e.GetError(),
		})
	}

	return &hermesv1.ImportContactsResponse{
		ImportedCount: resp.GetImportedCount(),
		SkippedCount:  resp.GetSkippedCount(),
		FailedCount:   resp.GetFailedCount(),
		Errors:        importErrors,
		BannedCount:   resp.GetBannedCount(),
	}, nil
}

func (h *Handler) ListContacts(ctx context.Context, req *hermesv1.ListContactsRequest) (*hermesv1.ListContactsResponse, error) {
	if h.contactsClient == nil {
		return nil, status.Error(codes.Unavailable, "contacts service not available")
	}
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	resp, err := h.contactsClient.ListContacts(ctx, &hermesv1.ContactsListRequest{
		TenantId:     req.GetTenantId(),
		Search:       req.GetSearch(),
		Tags:         req.GetTags(),
		IsBanned:     req.GetIsBanned(),
		FilterBanned: req.GetFilterBanned(),
		Pagination:   req.GetPagination(),
	})
	if err != nil {
		return nil, err
	}

	return &hermesv1.ListContactsResponse{
		Contacts:   resp.GetContacts(),
		Pagination: resp.GetPagination(),
	}, nil
}

func (h *Handler) GetContact(ctx context.Context, req *hermesv1.GetContactRequest) (*hermesv1.GetContactResponse, error) {
	if h.contactsClient == nil {
		return nil, status.Error(codes.Unavailable, "contacts service not available")
	}
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	resp, err := h.contactsClient.GetContact(ctx, &hermesv1.ContactsGetRequest{
		Id: req.GetId(),
	})
	if err != nil {
		return nil, err
	}

	return &hermesv1.GetContactResponse{Contact: resp.GetContact()}, nil
}

func (h *Handler) UpdateContact(ctx context.Context, req *hermesv1.UpdateContactRequest) (*hermesv1.UpdateContactResponse, error) {
	if h.contactsClient == nil {
		return nil, status.Error(codes.Unavailable, "contacts service not available")
	}
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	resp, err := h.contactsClient.UpdateContact(ctx, &hermesv1.ContactsUpdateRequest{
		Id:           req.GetId(),
		Name:         req.GetName(),
		Phone:        req.GetPhone(),
		Tags:         req.GetTags(),
		CustomFields: req.GetCustomFields(),
		IsBanned:     req.GetIsBanned(),
	})
	if err != nil {
		return nil, err
	}

	return &hermesv1.UpdateContactResponse{Contact: resp.GetContact()}, nil
}

func (h *Handler) DeleteContact(ctx context.Context, req *hermesv1.DeleteContactRequest) (*hermesv1.DeleteContactResponse, error) {
	if h.contactsClient == nil {
		return nil, status.Error(codes.Unavailable, "contacts service not available")
	}
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	_, err := h.contactsClient.DeleteContact(ctx, &hermesv1.ContactsDeleteRequest{
		Id: req.GetId(),
	})
	if err != nil {
		return nil, err
	}

	return &hermesv1.DeleteContactResponse{}, nil
}

// ===========================================================================
//
//  7. TEMPLATE RPCs (forward to campaign service)
//
// ===========================================================================

func (h *Handler) CreateTemplate(ctx context.Context, req *hermesv1.CreateTemplateRequest) (*hermesv1.CreateTemplateResponse, error) {
	if h.campaignClient == nil {
		return nil, status.Error(codes.Unavailable, "campaign service not available")
	}
	if req.GetWorkspaceId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id is required")
	}
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	if req.GetBody() == "" {
		return nil, status.Error(codes.InvalidArgument, "body is required")
	}

	userID := middleware.UserIDFromCtx(ctx)

	resp, err := h.campaignClient.CreateTemplate(ctx, &hermesv1.TemplateCreateRequest{
		WorkspaceId: req.GetWorkspaceId(),
		Name:        req.GetName(),
		Body:        req.GetBody(),
		MediaUrl:    req.GetMediaUrl(),
		MediaType:   req.GetMediaType(),
		CreatedBy:   userID,
	})
	if err != nil {
		return nil, err
	}

	return &hermesv1.CreateTemplateResponse{Template: resp.GetTemplate()}, nil
}

func (h *Handler) GetTemplate(ctx context.Context, req *hermesv1.GetTemplateRequest) (*hermesv1.GetTemplateResponse, error) {
	if h.campaignClient == nil {
		return nil, status.Error(codes.Unavailable, "campaign service not available")
	}
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	resp, err := h.campaignClient.GetTemplate(ctx, &hermesv1.TemplateGetRequest{
		Id: req.GetId(),
	})
	if err != nil {
		return nil, err
	}

	return &hermesv1.GetTemplateResponse{Template: resp.GetTemplate()}, nil
}

func (h *Handler) ListTemplates(ctx context.Context, req *hermesv1.ListTemplatesRequest) (*hermesv1.ListTemplatesResponse, error) {
	if h.campaignClient == nil {
		return nil, status.Error(codes.Unavailable, "campaign service not available")
	}
	if req.GetWorkspaceId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id is required")
	}

	resp, err := h.campaignClient.ListTemplates(ctx, &hermesv1.TemplateListRequest{
		WorkspaceId: req.GetWorkspaceId(),
		Search:      req.GetSearch(),
		Pagination:  req.GetPagination(),
	})
	if err != nil {
		return nil, err
	}

	return &hermesv1.ListTemplatesResponse{
		Templates:  resp.GetTemplates(),
		Pagination: resp.GetPagination(),
	}, nil
}

func (h *Handler) UpdateTemplate(ctx context.Context, req *hermesv1.UpdateTemplateRequest) (*hermesv1.UpdateTemplateResponse, error) {
	if h.campaignClient == nil {
		return nil, status.Error(codes.Unavailable, "campaign service not available")
	}
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	resp, err := h.campaignClient.UpdateTemplate(ctx, &hermesv1.TemplateUpdateRequest{
		Id:        req.GetId(),
		Name:      req.GetName(),
		Body:      req.GetBody(),
		MediaUrl:  req.GetMediaUrl(),
		MediaType: req.GetMediaType(),
	})
	if err != nil {
		return nil, err
	}

	return &hermesv1.UpdateTemplateResponse{Template: resp.GetTemplate()}, nil
}

func (h *Handler) DeleteTemplate(ctx context.Context, req *hermesv1.DeleteTemplateRequest) (*hermesv1.DeleteTemplateResponse, error) {
	if h.campaignClient == nil {
		return nil, status.Error(codes.Unavailable, "campaign service not available")
	}
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	_, err := h.campaignClient.DeleteTemplate(ctx, &hermesv1.TemplateDeleteRequest{
		Id: req.GetId(),
	})
	if err != nil {
		return nil, err
	}

	return &hermesv1.DeleteTemplateResponse{}, nil
}

// ===========================================================================
//
//  8. CAMPAIGN RPCs (forward to campaign service)
//
// ===========================================================================

func (h *Handler) CreateCampaign(ctx context.Context, req *hermesv1.CreateCampaignRequest) (*hermesv1.CreateCampaignResponse, error) {
	if h.campaignClient == nil {
		return nil, status.Error(codes.Unavailable, "campaign service not available")
	}
	if req.GetWorkspaceId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id is required")
	}
	if req.GetTemplateId() == "" {
		return nil, status.Error(codes.InvalidArgument, "template_id is required")
	}
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}

	userID := middleware.UserIDFromCtx(ctx)

	resp, err := h.campaignClient.CreateCampaign(ctx, &hermesv1.CampaignCreateRequest{
		WorkspaceId:       req.GetWorkspaceId(),
		TemplateId:        req.GetTemplateId(),
		Name:              req.GetName(),
		ScheduleAt:        req.GetScheduleAt(),
		DailyCapPerNum:    req.GetDailyCapPerNum(),
		BanPauseThreshold: req.GetBanPauseThreshold(),
		RotationStrategy:  req.GetRotationStrategy(),
		DelayMinMs:        req.GetDelayMinMs(),
		DelayMaxMs:        req.GetDelayMaxMs(),
		WaNumberIds:       req.GetWaNumberIds(),
		ContactIds:        req.GetContactIds(),
		CreatedBy:         userID,
		// Chunk 8: forward channel + mbs_session_uids. Campaign service
		// owns validation (mutual exclusion, allowed values).
		Channel:        req.GetChannel(),
		MbsSessionUids: req.GetMbsSessionUids(),
	})
	if err != nil {
		return nil, err
	}

	return &hermesv1.CreateCampaignResponse{Campaign: resp.GetCampaign()}, nil
}

func (h *Handler) GetCampaign(ctx context.Context, req *hermesv1.GetCampaignRequest) (*hermesv1.GetCampaignResponse, error) {
	if h.campaignClient == nil {
		return nil, status.Error(codes.Unavailable, "campaign service not available")
	}
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	resp, err := h.campaignClient.GetCampaign(ctx, &hermesv1.CampaignGetRequest{
		Id: req.GetId(),
	})
	if err != nil {
		return nil, err
	}

	return &hermesv1.GetCampaignResponse{
		Campaign: resp.GetCampaign(),
		Numbers:  resp.GetNumbers(),
		Template: resp.GetTemplate(),
	}, nil
}

func (h *Handler) ListCampaigns(ctx context.Context, req *hermesv1.ListCampaignsRequest) (*hermesv1.ListCampaignsResponse, error) {
	if h.campaignClient == nil {
		return nil, status.Error(codes.Unavailable, "campaign service not available")
	}
	if req.GetWorkspaceId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id is required")
	}

	resp, err := h.campaignClient.ListCampaigns(ctx, &hermesv1.CampaignListRequest{
		WorkspaceId: req.GetWorkspaceId(),
		Status:      req.GetStatus(),
		Pagination:  req.GetPagination(),
	})
	if err != nil {
		return nil, err
	}

	return &hermesv1.ListCampaignsResponse{
		Campaigns:  resp.GetCampaigns(),
		Pagination: resp.GetPagination(),
	}, nil
}

func (h *Handler) StartCampaign(ctx context.Context, req *hermesv1.StartCampaignRequest) (*hermesv1.StartCampaignResponse, error) {
	if h.campaignClient == nil {
		return nil, status.Error(codes.Unavailable, "campaign service not available")
	}
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	resp, err := h.campaignClient.StartCampaign(ctx, &hermesv1.CampaignStartRequest{
		Id: req.GetId(),
	})
	if err != nil {
		return nil, err
	}

	return &hermesv1.StartCampaignResponse{Campaign: resp.GetCampaign()}, nil
}

func (h *Handler) PauseCampaign(ctx context.Context, req *hermesv1.PauseCampaignRequest) (*hermesv1.PauseCampaignResponse, error) {
	if h.campaignClient == nil {
		return nil, status.Error(codes.Unavailable, "campaign service not available")
	}
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	resp, err := h.campaignClient.PauseCampaign(ctx, &hermesv1.CampaignPauseRequest{
		Id: req.GetId(),
	})
	if err != nil {
		return nil, err
	}

	return &hermesv1.PauseCampaignResponse{Campaign: resp.GetCampaign()}, nil
}

func (h *Handler) ResumeCampaign(ctx context.Context, req *hermesv1.ResumeCampaignRequest) (*hermesv1.ResumeCampaignResponse, error) {
	if h.campaignClient == nil {
		return nil, status.Error(codes.Unavailable, "campaign service not available")
	}
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	resp, err := h.campaignClient.ResumeCampaign(ctx, &hermesv1.CampaignResumeRequest{
		Id: req.GetId(),
	})
	if err != nil {
		return nil, err
	}

	return &hermesv1.ResumeCampaignResponse{Campaign: resp.GetCampaign()}, nil
}

func (h *Handler) CancelCampaign(ctx context.Context, req *hermesv1.CancelCampaignRequest) (*hermesv1.CancelCampaignResponse, error) {
	if h.campaignClient == nil {
		return nil, status.Error(codes.Unavailable, "campaign service not available")
	}
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	resp, err := h.campaignClient.CancelCampaign(ctx, &hermesv1.CampaignCancelRequest{
		Id: req.GetId(),
	})
	if err != nil {
		return nil, err
	}

	return &hermesv1.CancelCampaignResponse{Campaign: resp.GetCampaign()}, nil
}

func (h *Handler) UpdateCampaignNumbers(ctx context.Context, req *hermesv1.UpdateCampaignNumbersRequest) (*hermesv1.UpdateCampaignNumbersResponse, error) {
	if h.campaignClient == nil {
		return nil, status.Error(codes.Unavailable, "campaign service not available")
	}
	if req.GetCampaignId() == "" {
		return nil, status.Error(codes.InvalidArgument, "campaign_id is required")
	}

	resp, err := h.campaignClient.UpdateCampaignNumbers(ctx, &hermesv1.CampaignUpdateNumbersRequest{
		CampaignId:       req.GetCampaignId(),
		AddWaNumberIds:   req.GetAddWaNumberIds(),
		RemoveWaNumberIds: req.GetRemoveWaNumberIds(),
	})
	if err != nil {
		return nil, err
	}

	return &hermesv1.UpdateCampaignNumbersResponse{
		Campaign: resp.GetCampaign(),
		Numbers:  resp.GetNumbers(),
	}, nil
}

func (h *Handler) UpdateCampaignContacts(ctx context.Context, req *hermesv1.UpdateCampaignContactsRequest) (*hermesv1.UpdateCampaignContactsResponse, error) {
	if h.campaignClient == nil {
		return nil, status.Error(codes.Unavailable, "campaign service not available")
	}
	if req.GetCampaignId() == "" {
		return nil, status.Error(codes.InvalidArgument, "campaign_id is required")
	}

	resp, err := h.campaignClient.UpdateCampaignContacts(ctx, &hermesv1.CampaignUpdateContactsRequest{
		CampaignId:       req.GetCampaignId(),
		AddContactIds:    req.GetAddContactIds(),
		RemoveContactIds: req.GetRemoveContactIds(),
	})
	if err != nil {
		return nil, err
	}

	return &hermesv1.UpdateCampaignContactsResponse{Campaign: resp.GetCampaign()}, nil
}

func (h *Handler) ListCampaignContacts(ctx context.Context, req *hermesv1.ListCampaignContactsRequest) (*hermesv1.ListCampaignContactsResponse, error) {
	if h.campaignClient == nil {
		return nil, status.Error(codes.Unavailable, "campaign service not available")
	}
	if req.GetCampaignId() == "" {
		return nil, status.Error(codes.InvalidArgument, "campaign_id is required")
	}

	resp, err := h.campaignClient.ListCampaignContacts(ctx, &hermesv1.CampaignListContactsRequest{
		CampaignId: req.GetCampaignId(),
		Status:     req.GetStatus(),
		Pagination: req.GetPagination(),
	})
	if err != nil {
		return nil, err
	}

	// Translate CampaignContactRow -> gateway CampaignContactDetail.
	// The backend returns CampaignContactRow with denormalized contact info,
	// while the gateway proto uses CampaignContactDetail with a full Contact.
	contacts := make([]*hermesv1.CampaignContactDetail, 0, len(resp.GetContacts()))
	for _, c := range resp.GetContacts() {
		detail := &hermesv1.CampaignContactDetail{
			CampaignContact: c.GetCampaignContact(),
		}
		// Build a minimal Contact from denormalized fields.
		if c.GetContactName() != "" || c.GetContactPhone() != "" {
			detail.Contact = &hermesv1.Contact{
				Id:    c.GetCampaignContact().GetContactId(),
				Name:  c.GetContactName(),
				Phone: c.GetContactPhone(),
			}
		}
		contacts = append(contacts, detail)
	}

	return &hermesv1.ListCampaignContactsResponse{
		Contacts:   contacts,
		Pagination: resp.GetPagination(),
	}, nil
}

func (h *Handler) ListCampaignNumbers(ctx context.Context, req *hermesv1.ListCampaignNumbersRequest) (*hermesv1.ListCampaignNumbersResponse, error) {
	if h.campaignClient == nil {
		return nil, status.Error(codes.Unavailable, "campaign service not available")
	}
	if req.GetCampaignId() == "" {
		return nil, status.Error(codes.InvalidArgument, "campaign_id is required")
	}

	resp, err := h.campaignClient.ListCampaignNumbers(ctx, &hermesv1.CampaignListNumbersRequest{
		CampaignId: req.GetCampaignId(),
		Pagination: req.GetPagination(),
	})
	if err != nil {
		return nil, err
	}

	// Translate CampaignNumber -> gateway CampaignNumberDetail.
	// Enrich with phone/name from wa_numbers via shared DB.
	numbers := make([]*hermesv1.CampaignNumberDetail, 0, len(resp.GetNumbers()))
	for _, n := range resp.GetNumbers() {
		detail := &hermesv1.CampaignNumberDetail{
			CampaignNumber: n,
			CurrentStatus:  n.GetStatus(),
		}
		if row, err := h.store.GetWaNumberByID(ctx, n.GetWaNumberId()); err == nil {
			detail.Phone = row.Phone
			detail.DisplayName = row.DisplayName
			switch row.Status {
			case "active":
				detail.CurrentStatus = hermesv1.WaNumberStatus_WA_NUMBER_STATUS_ACTIVE
			case "banned":
				detail.CurrentStatus = hermesv1.WaNumberStatus_WA_NUMBER_STATUS_BANNED
			case "disconnected":
				detail.CurrentStatus = hermesv1.WaNumberStatus_WA_NUMBER_STATUS_DISCONNECTED
			case "cooldown":
				detail.CurrentStatus = hermesv1.WaNumberStatus_WA_NUMBER_STATUS_COOLDOWN
			}
		}
		numbers = append(numbers, detail)
	}

	return &hermesv1.ListCampaignNumbersResponse{
		Numbers:    numbers,
		Pagination: resp.GetPagination(),
	}, nil
}

// ===========================================================================
//
//  9. INBOX -- CONVERSATION RPCs (forward to inbox service)
//
//     CRITICAL: conversation read/write is scoped per CS agent.
//     Ownership model (operator-defined):
//       - unassigned conversation  -> any cs_agent may read + claim
//       - assigned to a cs_agent    -> only that agent + higher roles
//
//     Enforcement lives HERE in the gateway handler (not the gRPC interceptor)
//     so it applies on BOTH transports: gRPC (interceptor chain) AND the REST
//     adapter, which dispatches in-process to these same handler methods and
//     bypasses the interceptor.
//
// ===========================================================================

// canAccessConversation reports whether a caller with the given role and user ID
// may read/act on a conversation with the given assigned_to value.
//
//	privileged (role != cs_agent)        -> true   (admins see everything)
//	cs_agent, assignedTo == ""           -> true   (unassigned: open to all CS)
//	cs_agent, assignedTo == callerUserID -> true   (own)
//	cs_agent, otherwise                  -> false  (someone else's)
func canAccessConversation(role, callerUserID, assignedTo string) bool {
	if role != "cs_agent" {
		return true
	}
	return assignedTo == "" || assignedTo == callerUserID
}

// authorizeConversationAccess resolves a conversation's assigned_to via the inbox
// service and enforces canAccessConversation for cs_agent callers. Privileged
// roles skip the round trip entirely.
//
// Returns nil when access is allowed, codes.PermissionDenied when a cs_agent
// lacks access, or a propagated inbox error (NotFound / Unavailable / Internal).
func (h *Handler) authorizeConversationAccess(ctx context.Context, convID string) error {
	role := middleware.RoleFromCtx(ctx)
	if role != "cs_agent" {
		return nil // privileged: full access, no extra fetch
	}
	if h.inboxClient == nil {
		return status.Error(codes.Unavailable, "inbox service not available")
	}
	resp, err := h.inboxClient.GetConversation(ctx, &hermesv1.InboxGetConversationRequest{Id: convID})
	if err != nil {
		return err // propagate NotFound etc. unchanged
	}
	userID := middleware.UserIDFromCtx(ctx)
	if !canAccessConversation(role, userID, resp.GetConversation().GetAssignedTo()) {
		return status.Error(codes.PermissionDenied, "conversation assigned to another agent")
	}
	return nil
}

// CanAccessConversation reports whether (role, userID) may read a conversation,
// resolving its assigned_to via the inbox service. Exported so the WebSocket hub
// can authorize per-conversation subscriptions with the SAME ownership model the
// REST/gRPC conversation endpoints enforce. Returns false on any resolution
// error (fail-closed) — the hub treats inability to verify as deny.
func (h *Handler) CanAccessConversation(ctx context.Context, role, userID, conversationID string) bool {
	if role != "cs_agent" {
		return true // privileged: full access, no fetch
	}
	if h.inboxClient == nil {
		return false
	}
	resp, err := h.inboxClient.GetConversation(ctx, &hermesv1.InboxGetConversationRequest{Id: conversationID})
	if err != nil {
		return false // fail-closed: can't verify ownership -> deny
	}
	return canAccessConversation(role, userID, resp.GetConversation().GetAssignedTo())
}

func (h *Handler) ListConversations(ctx context.Context, req *hermesv1.ListConversationsRequest) (*hermesv1.ListConversationsResponse, error) {
	if h.inboxClient == nil {
		return nil, status.Error(codes.Unavailable, "inbox service not available")
	}
	if req.GetWorkspaceId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id is required")
	}

	role := middleware.RoleFromCtx(ctx)
	userID := middleware.UserIDFromCtx(ctx)

	// RBAC filter injection: CS agents may only see UNASSIGNED conversations
	// or conversations assigned to themselves. Admins see everything.
	inboxReq := &hermesv1.InboxListConversationsRequest{
		WorkspaceId: req.GetWorkspaceId(),
		Status:      req.GetStatus(),
		AssignedTo:  req.GetAssignedTo(),
		WaNumberId:  req.GetWaNumberId(),
		Search:      req.GetSearch(),
		Pagination:  req.GetPagination(),
		Channel:     req.GetChannel(),
	}

	if role == "cs_agent" {
		// RBAC scope: a cs_agent sees their own conversations OR any unassigned
		// one. We set assigned_to to the agent's own user ID and flip
		// include_unassigned so the inbox store emits
		// (assigned_to = self OR assigned_to IS NULL).
		if inboxReq.AssignedTo == "" {
			inboxReq.AssignedTo = userID
			inboxReq.IncludeUnassigned = true
		}
		// If the agent explicitly requested a specific status that is not
		// UNASSIGNED and a different agent's conversations, block it.
		if inboxReq.AssignedTo != userID &&
			inboxReq.Status != hermesv1.ConversationStatus_CONVERSATION_STATUS_UNASSIGNED {
			return nil, status.Error(codes.PermissionDenied, "cs_agent can only view own or unassigned conversations")
		}
	}

	resp, err := h.inboxClient.ListConversations(ctx, inboxReq)
	if err != nil {
		return nil, err
	}

	return &hermesv1.ListConversationsResponse{
		Conversations: resp.GetConversations(),
		Pagination:    resp.GetPagination(),
	}, nil
}

func (h *Handler) GetConversation(ctx context.Context, req *hermesv1.GetConversationRequest) (*hermesv1.GetConversationResponse, error) {
	if h.inboxClient == nil {
		return nil, status.Error(codes.Unavailable, "inbox service not available")
	}
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	resp, err := h.inboxClient.GetConversation(ctx, &hermesv1.InboxGetConversationRequest{
		Id: req.GetId(),
	})
	if err != nil {
		return nil, err
	}

	// RBAC scope: cs_agent may only read own or unassigned conversations.
	// Inline predicate on the already-fetched row (no extra round trip).
	role := middleware.RoleFromCtx(ctx)
	userID := middleware.UserIDFromCtx(ctx)
	if !canAccessConversation(role, userID, resp.GetConversation().GetAssignedTo()) {
		return nil, status.Error(codes.PermissionDenied, "conversation assigned to another agent")
	}

	return &hermesv1.GetConversationResponse{
		Conversation: resp.GetConversation(),
		Contact:      resp.GetContact(),
		WaNumber:     resp.GetWaNumber(),
	}, nil
}

func (h *Handler) ClaimConversation(ctx context.Context, req *hermesv1.ClaimConversationRequest) (*hermesv1.ClaimConversationResponse, error) {
	if h.inboxClient == nil {
		return nil, status.Error(codes.Unavailable, "inbox service not available")
	}
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	userID := middleware.UserIDFromCtx(ctx)

	resp, err := h.inboxClient.ClaimConversation(ctx, &hermesv1.InboxClaimConversationRequest{
		Id:     req.GetId(),
		UserId: userID,
	})
	if err != nil {
		return nil, err
	}

	return &hermesv1.ClaimConversationResponse{Conversation: resp.GetConversation()}, nil
}

func (h *Handler) TransferConversation(ctx context.Context, req *hermesv1.TransferConversationRequest) (*hermesv1.TransferConversationResponse, error) {
	if h.inboxClient == nil {
		return nil, status.Error(codes.Unavailable, "inbox service not available")
	}
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	if req.GetTargetUserId() == "" {
		return nil, status.Error(codes.InvalidArgument, "target_user_id is required")
	}

	// RBAC scope: cs_agent may only transfer own or unassigned conversations.
	if err := h.authorizeConversationAccess(ctx, req.GetId()); err != nil {
		return nil, err
	}

	userID := middleware.UserIDFromCtx(ctx)

	resp, err := h.inboxClient.TransferConversation(ctx, &hermesv1.InboxTransferConversationRequest{
		Id:         req.GetId(),
		FromUserId: userID,
		ToUserId:   req.GetTargetUserId(),
	})
	if err != nil {
		return nil, err
	}

	return &hermesv1.TransferConversationResponse{Conversation: resp.GetConversation()}, nil
}

func (h *Handler) CloseConversation(ctx context.Context, req *hermesv1.CloseConversationRequest) (*hermesv1.CloseConversationResponse, error) {
	if h.inboxClient == nil {
		return nil, status.Error(codes.Unavailable, "inbox service not available")
	}
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	// RBAC scope: cs_agent may only close own or unassigned conversations.
	if err := h.authorizeConversationAccess(ctx, req.GetId()); err != nil {
		return nil, err
	}

	userID := middleware.UserIDFromCtx(ctx)

	resp, err := h.inboxClient.CloseConversation(ctx, &hermesv1.InboxCloseConversationRequest{
		Id:     req.GetId(),
		UserId: userID,
	})
	if err != nil {
		return nil, err
	}

	return &hermesv1.CloseConversationResponse{Conversation: resp.GetConversation()}, nil
}

// ===========================================================================
//
//  10. INBOX -- MESSAGE RPCs (forward to inbox service + WA service)
//
// ===========================================================================

func (h *Handler) ListMessages(ctx context.Context, req *hermesv1.ListMessagesRequest) (*hermesv1.ListMessagesResponse, error) {
	if h.inboxClient == nil {
		return nil, status.Error(codes.Unavailable, "inbox service not available")
	}
	if req.GetConversationId() == "" {
		return nil, status.Error(codes.InvalidArgument, "conversation_id is required")
	}

	// RBAC scope: cs_agent may only read messages of own or unassigned conversations.
	if err := h.authorizeConversationAccess(ctx, req.GetConversationId()); err != nil {
		return nil, err
	}

	resp, err := h.inboxClient.ListMessages(ctx, &hermesv1.InboxListMessagesRequest{
		ConversationId: req.GetConversationId(),
		Pagination:     req.GetPagination(),
	})
	if err != nil {
		return nil, err
	}

	return &hermesv1.ListMessagesResponse{
		Messages:   resp.GetMessages(),
		Pagination: resp.GetPagination(),
	}, nil
}

func (h *Handler) SendMessage(ctx context.Context, req *hermesv1.SendMessageRequest) (*hermesv1.SendMessageResponse, error) {
	if h.inboxClient == nil {
		return nil, status.Error(codes.Unavailable, "inbox service not available")
	}
	if req.GetConversationId() == "" {
		return nil, status.Error(codes.InvalidArgument, "conversation_id is required")
	}

	// RBAC scope: cs_agent may only send into own or unassigned conversations.
	if err := h.authorizeConversationAccess(ctx, req.GetConversationId()); err != nil {
		return nil, err
	}

	userID := middleware.UserIDFromCtx(ctx)

	resp, err := h.inboxClient.SendMessage(ctx, &hermesv1.InboxSendMessageRequest{
		ConversationId: req.GetConversationId(),
		ContentType:    req.GetContentType(),
		Body:           req.GetBody(),
		MediaUrl:       req.GetMediaUrl(),
		SenderUserId:   userID,
	})
	if err != nil {
		return nil, err
	}

	return &hermesv1.SendMessageResponse{Message: resp.GetMessage()}, nil
}

func (h *Handler) SearchMessages(ctx context.Context, req *hermesv1.SearchMessagesRequest) (*hermesv1.SearchMessagesResponse, error) {
	if h.inboxClient == nil {
		return nil, status.Error(codes.Unavailable, "inbox service not available")
	}
	if req.GetWorkspaceId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id is required")
	}
	if req.GetQuery() == "" {
		return nil, status.Error(codes.InvalidArgument, "query is required")
	}

	inboxReq := &hermesv1.InboxSearchMessagesRequest{
		WorkspaceId:    req.GetWorkspaceId(),
		Query:          req.GetQuery(),
		ConversationId: req.GetConversationId(),
		Pagination:     req.GetPagination(),
	}
	// RBAC scope: a cs_agent's search is restricted to conversations they may
	// read (own + unassigned). Admins search the whole workspace.
	if middleware.RoleFromCtx(ctx) == "cs_agent" {
		inboxReq.RequesterUserId = middleware.UserIDFromCtx(ctx)
		inboxReq.IncludeUnassigned = true
	}

	resp, err := h.inboxClient.SearchMessages(ctx, inboxReq)
	if err != nil {
		return nil, err
	}

	// Translate InboxSearchHit -> gateway SearchMessageHit.
	hits := make([]*hermesv1.SearchMessageHit, 0, len(resp.GetHits()))
	for _, hit := range resp.GetHits() {
		hits = append(hits, &hermesv1.SearchMessageHit{
			Message:        hit.GetMessage(),
			ConversationId: hit.GetConversationId(),
			ContactName:    hit.GetContactName(),
			Highlight:      hit.GetHighlight(),
		})
	}

	return &hermesv1.SearchMessagesResponse{
		Hits:       hits,
		Pagination: resp.GetPagination(),
	}, nil
}

// SendTypingIndicator forwards to the WA service (not inbox).
func (h *Handler) SendTypingIndicator(ctx context.Context, req *hermesv1.SendTypingIndicatorRequest) (*hermesv1.SendTypingIndicatorResponse, error) {
	if h.waClient == nil {
		return nil, status.Error(codes.Unavailable, "wa service not available")
	}
	if req.GetConversationId() == "" {
		return nil, status.Error(codes.InvalidArgument, "conversation_id is required")
	}

	// To send a typing indicator we need the wa_number_id and recipient JID.
	// Fetch conversation details from inbox to resolve these.
	if h.inboxClient == nil {
		return nil, status.Error(codes.Unavailable, "inbox service not available")
	}

	convResp, err := h.inboxClient.GetConversation(ctx, &hermesv1.InboxGetConversationRequest{
		Id: req.GetConversationId(),
	})
	if err != nil {
		return nil, err
	}

	conv := convResp.GetConversation()
	contact := convResp.GetContact()
	if conv == nil {
		return nil, status.Error(codes.NotFound, "conversation not found")
	}

	// Resolve recipient JID from contact phone.
	recipientJID := ""
	if contact != nil && contact.GetPhone() != "" {
		// Build JID from phone (strip leading + if present).
		phone := contact.GetPhone()
		if len(phone) > 0 && phone[0] == '+' {
			phone = phone[1:]
		}
		recipientJID = phone + "@s.whatsapp.net"
	}

	_, err = h.waClient.SendTypingIndicator(ctx, &hermesv1.WaSendTypingIndicatorRequest{
		WaNumberId:   conv.GetWaNumberId(),
		RecipientJid: recipientJID,
	})
	if err != nil {
		return nil, err
	}

	return &hermesv1.SendTypingIndicatorResponse{}, nil
}

// ===========================================================================
//
//  11. INBOX -- CAMPAIGN HISTORY + AGENT PERFORMANCE
//
// ===========================================================================

func (h *Handler) GetContactCampaignHistory(ctx context.Context, req *hermesv1.GetContactCampaignHistoryRequest) (*hermesv1.GetContactCampaignHistoryResponse, error) {
	if h.inboxClient == nil {
		return nil, status.Error(codes.Unavailable, "inbox service not available")
	}
	if req.GetContactId() == "" {
		return nil, status.Error(codes.InvalidArgument, "contact_id is required")
	}

	resp, err := h.inboxClient.GetContactCampaignHistory(ctx, &hermesv1.InboxGetContactCampaignHistoryRequest{
		ContactId:  req.GetContactId(),
		Pagination: req.GetPagination(),
	})
	if err != nil {
		return nil, err
	}

	// Translate InboxContactCampaignSummary -> gateway ContactCampaignSummary.
	campaigns := make([]*hermesv1.ContactCampaignSummary, 0, len(resp.GetCampaigns()))
	for _, c := range resp.GetCampaigns() {
		campaigns = append(campaigns, &hermesv1.ContactCampaignSummary{
			CampaignId:   c.GetCampaignId(),
			CampaignName: c.GetCampaignName(),
			TemplateId:   c.GetTemplateId(),
			TemplateName: c.GetTemplateName(),
			ResolvedBody: c.GetResolvedBody(),
			Status:       c.GetStatus(),
			SentAt:       c.GetSentAt(),
			DeliveredAt:  c.GetDeliveredAt(),
		})
	}

	return &hermesv1.GetContactCampaignHistoryResponse{
		Campaigns:  campaigns,
		Pagination: resp.GetPagination(),
	}, nil
}

// GetAgentPerformance is defined in the gateway proto but the inbox service
// does not expose a dedicated RPC for it. This is implemented via shared-DB
// queries (cross-service read pattern). For now, return Unimplemented; the
// store will be extended with an AgentPerformance query in a follow-up.
func (h *Handler) GetAgentPerformance(_ context.Context, _ *hermesv1.GetAgentPerformanceRequest) (*hermesv1.GetAgentPerformanceResponse, error) {
	// TODO: implement via shared-DB store method querying conversations and messages
	// tables for avg/median first_response_time_secs and message counts per agent.
	return nil, status.Error(codes.Unimplemented, "GetAgentPerformance not yet implemented")
}

// ===========================================================================
//
//  12. CANNED RESPONSE RPCs (forward to inbox service)
//
// ===========================================================================

func (h *Handler) CreateCannedResponse(ctx context.Context, req *hermesv1.CreateCannedResponseRequest) (*hermesv1.CreateCannedResponseResponse, error) {
	if h.inboxClient == nil {
		return nil, status.Error(codes.Unavailable, "inbox service not available")
	}
	if req.GetWorkspaceId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id is required")
	}
	if req.GetShortcut() == "" {
		return nil, status.Error(codes.InvalidArgument, "shortcut is required")
	}
	if req.GetBody() == "" {
		return nil, status.Error(codes.InvalidArgument, "body is required")
	}

	userID := middleware.UserIDFromCtx(ctx)

	resp, err := h.inboxClient.CreateCannedResponse(ctx, &hermesv1.InboxCreateCannedResponseRequest{
		WorkspaceId: req.GetWorkspaceId(),
		Shortcut:    req.GetShortcut(),
		Body:        req.GetBody(),
		CreatedBy:   userID,
	})
	if err != nil {
		return nil, err
	}

	return &hermesv1.CreateCannedResponseResponse{CannedResponse: resp.GetCannedResponse()}, nil
}

func (h *Handler) ListCannedResponses(ctx context.Context, req *hermesv1.ListCannedResponsesRequest) (*hermesv1.ListCannedResponsesResponse, error) {
	if h.inboxClient == nil {
		return nil, status.Error(codes.Unavailable, "inbox service not available")
	}
	if req.GetWorkspaceId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id is required")
	}

	resp, err := h.inboxClient.ListCannedResponses(ctx, &hermesv1.InboxListCannedResponsesRequest{
		WorkspaceId: req.GetWorkspaceId(),
		Search:      req.GetSearch(),
		Pagination:  req.GetPagination(),
	})
	if err != nil {
		return nil, err
	}

	return &hermesv1.ListCannedResponsesResponse{
		CannedResponses: resp.GetCannedResponses(),
		Pagination:      resp.GetPagination(),
	}, nil
}

func (h *Handler) UpdateCannedResponse(ctx context.Context, req *hermesv1.UpdateCannedResponseRequest) (*hermesv1.UpdateCannedResponseResponse, error) {
	if h.inboxClient == nil {
		return nil, status.Error(codes.Unavailable, "inbox service not available")
	}
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	resp, err := h.inboxClient.UpdateCannedResponse(ctx, &hermesv1.InboxUpdateCannedResponseRequest{
		Id:       req.GetId(),
		Shortcut: req.GetShortcut(),
		Body:     req.GetBody(),
	})
	if err != nil {
		return nil, err
	}

	return &hermesv1.UpdateCannedResponseResponse{CannedResponse: resp.GetCannedResponse()}, nil
}

func (h *Handler) DeleteCannedResponse(ctx context.Context, req *hermesv1.DeleteCannedResponseRequest) (*hermesv1.DeleteCannedResponseResponse, error) {
	if h.inboxClient == nil {
		return nil, status.Error(codes.Unavailable, "inbox service not available")
	}
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	_, err := h.inboxClient.DeleteCannedResponse(ctx, &hermesv1.InboxDeleteCannedResponseRequest{
		Id: req.GetId(),
	})
	if err != nil {
		return nil, err
	}

	return &hermesv1.DeleteCannedResponseResponse{}, nil
}

// ===========================================================================
//
//  13. NOTIFICATION RPCs (forward to notify service)
//
// ===========================================================================

func (h *Handler) ConfigureNotification(ctx context.Context, req *hermesv1.ConfigureNotificationRequest) (*hermesv1.ConfigureNotificationResponse, error) {
	if h.notifyClient == nil {
		return nil, status.Error(codes.Unavailable, "notify service not available")
	}
	if req.GetWorkspaceId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id is required")
	}

	resp, err := h.notifyClient.ConfigureNotification(ctx, &hermesv1.NotifyConfigureRequest{
		WorkspaceId: req.GetWorkspaceId(),
		Type:        req.GetType(),
		WebhookUrl:  req.GetWebhookUrl(),
		WebhookType: req.GetWebhookType(),
		Enabled:     req.GetEnabled(),
	})
	if err != nil {
		return nil, err
	}

	return &hermesv1.ConfigureNotificationResponse{Config: resp.GetConfig()}, nil
}

func (h *Handler) ListNotificationConfigs(ctx context.Context, req *hermesv1.ListNotificationConfigsRequest) (*hermesv1.ListNotificationConfigsResponse, error) {
	if h.notifyClient == nil {
		return nil, status.Error(codes.Unavailable, "notify service not available")
	}
	if req.GetWorkspaceId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id is required")
	}

	resp, err := h.notifyClient.ListNotificationConfigs(ctx, &hermesv1.NotifyListConfigsRequest{
		WorkspaceId: req.GetWorkspaceId(),
	})
	if err != nil {
		return nil, err
	}

	return &hermesv1.ListNotificationConfigsResponse{Configs: resp.GetConfigs()}, nil
}

func (h *Handler) TestNotification(ctx context.Context, req *hermesv1.TestNotificationRequest) (*hermesv1.TestNotificationResponse, error) {
	if h.notifyClient == nil {
		return nil, status.Error(codes.Unavailable, "notify service not available")
	}
	if req.GetConfigId() == "" {
		return nil, status.Error(codes.InvalidArgument, "config_id is required")
	}

	resp, err := h.notifyClient.TestNotification(ctx, &hermesv1.NotifyTestRequest{
		ConfigId: req.GetConfigId(),
	})
	if err != nil {
		return nil, err
	}

	return &hermesv1.TestNotificationResponse{
		Success: resp.GetSuccess(),
		Error:   resp.GetError(),
	}, nil
}

func (h *Handler) DeleteNotificationConfig(ctx context.Context, req *hermesv1.DeleteNotificationConfigRequest) (*hermesv1.DeleteNotificationConfigResponse, error) {
	if h.notifyClient == nil {
		return nil, status.Error(codes.Unavailable, "notify service not available")
	}
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	_, err := h.notifyClient.DeleteNotificationConfig(ctx, &hermesv1.NotifyDeleteConfigRequest{
		Id: req.GetId(),
	})
	if err != nil {
		return nil, err
	}

	return &hermesv1.DeleteNotificationConfigResponse{}, nil
}

// ===========================================================================
//
//  14. DASHBOARD (gateway-owned, shared-DB read)
//
// ===========================================================================

func (h *Handler) GetDashboardStats(ctx context.Context, req *hermesv1.GetDashboardStatsRequest) (*hermesv1.GetDashboardStatsResponse, error) {
	tenantID := req.GetTenantId()
	if tenantID == "" {
		// Fall back to the tenant from the auth context.
		tenantID = middleware.TenantIDFromCtx(ctx)
	}
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	stats, err := h.store.GetDashboardStats(ctx, tenantID, req.GetWorkspaceId())
	if err != nil {
		h.log.Error().Err(err).Msg("failed to get dashboard stats")
		return nil, status.Error(codes.Internal, "internal error")
	}

	return &hermesv1.GetDashboardStatsResponse{
		ActiveNumbers:            stats.ActiveNumbers,
		TotalNumbers:             stats.TotalNumbers,
		MessagesSentToday:        stats.MessagesSentToday,
		MessagesReceivedToday:    stats.MessagesReceivedToday,
		ActiveCampaigns:          stats.ActiveCampaigns,
		UnassignedConversations:  stats.UnassignedConversations,
		ActiveProxies:            stats.ActiveProxies,
		TotalProxies:             stats.TotalProxies,
		BansToday:                stats.BansToday,
		TotalContacts:            stats.TotalContacts,
	}, nil
}
