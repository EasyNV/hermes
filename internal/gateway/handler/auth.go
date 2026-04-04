package handler

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/hermes-waba/hermes/internal/gateway/middleware"
)

const (
	accessTokenExpiry  = 15 * time.Minute
	refreshTokenExpiry = 7 * 24 * time.Hour
	accessExpiresInSec = 900 // 15 minutes in seconds
)

// ---------------------------------------------------------------------------
// JWT Claims
// ---------------------------------------------------------------------------

// Claims is the JWT payload embedded in access tokens.
type Claims struct {
	jwt.RegisteredClaims
	UserID      string `json:"uid"`
	TenantID    string `json:"tid"`
	WorkspaceID string `json:"wid"`
	Role        string `json:"role"`
}

// ---------------------------------------------------------------------------
// Token helpers (methods on Handler — they use h.jwtSecret)
// ---------------------------------------------------------------------------

// generateAccessToken creates a signed JWT access token with the given claims.
func (h *Handler) generateAccessToken(c Claims) (string, error) {
	now := time.Now()
	c.RegisteredClaims = jwt.RegisteredClaims{
		ExpiresAt: jwt.NewNumericDate(now.Add(accessTokenExpiry)),
		IssuedAt:  jwt.NewNumericDate(now),
		ID:        uuid.NewString(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, c)
	return token.SignedString(h.jwtSecret)
}

// parseAccessToken validates and parses a JWT access token string.
func (h *Handler) parseAccessToken(tokenStr string) (*Claims, error) {
	c := &Claims{}
	token, err := jwt.ParseWithClaims(tokenStr, c, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return h.jwtSecret, nil
	})
	if err != nil {
		return nil, err
	}
	if !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}
	return c, nil
}

// generateRefreshToken returns a new UUID to be used as a refresh token ID.
func generateRefreshToken() string {
	return uuid.NewString()
}

// ---------------------------------------------------------------------------
// Role conversion helpers
// ---------------------------------------------------------------------------

func roleToProto(r string) hermesv1.Role {
	switch r {
	case "superadmin":
		return hermesv1.Role_ROLE_SUPERADMIN
	case "tenant_admin":
		return hermesv1.Role_ROLE_TENANT_ADMIN
	case "workspace_admin":
		return hermesv1.Role_ROLE_WORKSPACE_ADMIN
	case "cs_agent":
		return hermesv1.Role_ROLE_CS_AGENT
	default:
		return hermesv1.Role_ROLE_UNSPECIFIED
	}
}

func roleToStr(r hermesv1.Role) string {
	switch r {
	case hermesv1.Role_ROLE_SUPERADMIN:
		return "superadmin"
	case hermesv1.Role_ROLE_TENANT_ADMIN:
		return "tenant_admin"
	case hermesv1.Role_ROLE_WORKSPACE_ADMIN:
		return "workspace_admin"
	case hermesv1.Role_ROLE_CS_AGENT:
		return "cs_agent"
	default:
		return ""
	}
}

// ---------------------------------------------------------------------------
// Row → Proto converters
// ---------------------------------------------------------------------------

func userToProto(u *UserRow, workspaceID string) *hermesv1.User {
	return &hermesv1.User{
		Id:          u.ID,
		WorkspaceId: workspaceID,
		Email:       u.Email,
		Role:        roleToProto(u.Role),
		CreatedAt:   timestamppb.New(u.CreatedAt),
	}
}

func workspaceToProto(w *WorkspaceRow) *hermesv1.Workspace {
	return &hermesv1.Workspace{
		Id:           w.ID,
		TenantId:     w.TenantID,
		Name:         w.Name,
		SettingsJson: w.SettingsJSON,
		DailyCap:     w.DailyCap,
		CreatedAt:    timestamppb.New(w.CreatedAt),
	}
}

func tenantToProto(t *TenantRow) *hermesv1.Tenant {
	return &hermesv1.Tenant{
		Id:                 t.ID,
		Name:               t.Name,
		SettingsJson:       t.SettingsJSON,
		MaxNumbersPerProxy: t.MaxNumbersPerProxy,
		CreatedAt:          timestamppb.New(t.CreatedAt),
	}
}

// ---------------------------------------------------------------------------
// Auth RPCs
// ---------------------------------------------------------------------------

// Login authenticates a user with email + password and returns JWT tokens.
func (h *Handler) Login(ctx context.Context, req *hermesv1.LoginRequest) (*hermesv1.LoginResponse, error) {
	// Validate required fields.
	if req.GetEmail() == "" {
		return nil, status.Error(codes.InvalidArgument, "email is required")
	}
	if req.GetPassword() == "" {
		return nil, status.Error(codes.InvalidArgument, "password is required")
	}

	// Look up user by email.
	user, err := h.store.GetUserByEmail(ctx, req.GetEmail())
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, status.Error(codes.Unauthenticated, "invalid email or password")
		}
		h.log.Error().Err(err).Str("email", req.GetEmail()).Msg("failed to get user by email")
		return nil, status.Error(codes.Internal, "internal error")
	}

	// Verify password.
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.GetPassword())); err != nil {
		return nil, status.Error(codes.Unauthenticated, "invalid email or password")
	}

	// Get the user's workspace memberships. Use first workspace as the default.
	wsIDs, err := h.store.GetUserWorkspaceIDs(ctx, user.ID)
	if err != nil {
		h.log.Error().Err(err).Str("user_id", user.ID).Msg("failed to get workspace IDs")
		return nil, status.Error(codes.Internal, "internal error")
	}
	var workspaceID string
	if len(wsIDs) > 0 {
		workspaceID = wsIDs[0]
	}

	// Generate access token.
	claims := Claims{
		UserID:      user.ID,
		TenantID:    user.TenantID,
		WorkspaceID: workspaceID,
		Role:        user.Role,
	}
	accessToken, err := h.generateAccessToken(claims)
	if err != nil {
		h.log.Error().Err(err).Msg("failed to generate access token")
		return nil, status.Error(codes.Internal, "internal error")
	}

	// Generate and persist refresh token.
	refreshTokenID := generateRefreshToken()
	if err := h.store.SaveRefreshToken(ctx, refreshTokenID, user.ID, time.Now().Add(refreshTokenExpiry)); err != nil {
		h.log.Error().Err(err).Msg("failed to save refresh token")
		return nil, status.Error(codes.Internal, "internal error")
	}

	return &hermesv1.LoginResponse{
		AccessToken:  accessToken,
		RefreshToken: refreshTokenID,
		ExpiresIn:    accessExpiresInSec,
		User:         userToProto(user, workspaceID),
	}, nil
}

// RefreshToken exchanges a valid refresh token for a new access + refresh token pair.
func (h *Handler) RefreshToken(ctx context.Context, req *hermesv1.RefreshTokenRequest) (*hermesv1.RefreshTokenResponse, error) {
	if req.GetRefreshToken() == "" {
		return nil, status.Error(codes.InvalidArgument, "refresh_token is required")
	}

	// Validate the refresh token and get the associated user ID.
	userID, err := h.store.GetRefreshToken(ctx, req.GetRefreshToken())
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, status.Error(codes.Unauthenticated, "invalid or expired refresh token")
		}
		h.log.Error().Err(err).Msg("failed to get refresh token")
		return nil, status.Error(codes.Internal, "internal error")
	}

	// Delete the old refresh token (single-use rotation).
	if err := h.store.DeleteRefreshToken(ctx, req.GetRefreshToken()); err != nil {
		h.log.Error().Err(err).Msg("failed to delete old refresh token")
		// Non-fatal — continue issuing new tokens.
	}

	// Load the user to build fresh claims.
	user, err := h.store.GetUserByID(ctx, userID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, status.Error(codes.Unauthenticated, "user no longer exists")
		}
		h.log.Error().Err(err).Str("user_id", userID).Msg("failed to get user by ID")
		return nil, status.Error(codes.Internal, "internal error")
	}

	// Resolve workspace.
	wsIDs, err := h.store.GetUserWorkspaceIDs(ctx, user.ID)
	if err != nil {
		h.log.Error().Err(err).Str("user_id", user.ID).Msg("failed to get workspace IDs")
		return nil, status.Error(codes.Internal, "internal error")
	}
	var workspaceID string
	if len(wsIDs) > 0 {
		workspaceID = wsIDs[0]
	}

	// Issue new access token.
	claims := Claims{
		UserID:      user.ID,
		TenantID:    user.TenantID,
		WorkspaceID: workspaceID,
		Role:        user.Role,
	}
	accessToken, err := h.generateAccessToken(claims)
	if err != nil {
		h.log.Error().Err(err).Msg("failed to generate access token")
		return nil, status.Error(codes.Internal, "internal error")
	}

	// Issue new refresh token.
	newRefreshID := generateRefreshToken()
	if err := h.store.SaveRefreshToken(ctx, newRefreshID, user.ID, time.Now().Add(refreshTokenExpiry)); err != nil {
		h.log.Error().Err(err).Msg("failed to save new refresh token")
		return nil, status.Error(codes.Internal, "internal error")
	}

	return &hermesv1.RefreshTokenResponse{
		AccessToken:  accessToken,
		RefreshToken: newRefreshID,
		ExpiresIn:    accessExpiresInSec,
	}, nil
}

// Logout invalidates all refresh tokens for the authenticated user.
func (h *Handler) Logout(ctx context.Context, _ *hermesv1.LogoutRequest) (*hermesv1.LogoutResponse, error) {
	userID, ok := ctx.Value(middleware.CtxUserID).(string)
	if !ok || userID == "" {
		return nil, status.Error(codes.Unauthenticated, "not authenticated")
	}

	if err := h.store.DeleteUserRefreshTokens(ctx, userID); err != nil {
		h.log.Error().Err(err).Str("user_id", userID).Msg("failed to delete refresh tokens")
		return nil, status.Error(codes.Internal, "internal error")
	}

	return &hermesv1.LogoutResponse{}, nil
}

// GetMe returns the authenticated user's profile, workspace, and tenant.
func (h *Handler) GetMe(ctx context.Context, _ *hermesv1.GetMeRequest) (*hermesv1.GetMeResponse, error) {
	userID, _ := ctx.Value(middleware.CtxUserID).(string)
	if userID == "" {
		return nil, status.Error(codes.Unauthenticated, "not authenticated")
	}

	user, err := h.store.GetUserByID(ctx, userID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, status.Error(codes.NotFound, "user not found")
		}
		h.log.Error().Err(err).Str("user_id", userID).Msg("failed to get user")
		return nil, status.Error(codes.Internal, "internal error")
	}

	// Resolve workspace ID from context (set by auth middleware).
	workspaceID, _ := ctx.Value(middleware.CtxWorkspaceID).(string)

	resp := &hermesv1.GetMeResponse{
		User: userToProto(user, workspaceID),
	}

	// Load workspace if available.
	if workspaceID != "" {
		ws, err := h.store.GetWorkspace(ctx, workspaceID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			h.log.Error().Err(err).Str("workspace_id", workspaceID).Msg("failed to get workspace")
			return nil, status.Error(codes.Internal, "internal error")
		}
		if ws != nil {
			resp.Workspace = workspaceToProto(ws)
		}
	}

	// Load tenant.
	tenant, err := h.store.GetTenant(ctx, user.TenantID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		h.log.Error().Err(err).Str("tenant_id", user.TenantID).Msg("failed to get tenant")
		return nil, status.Error(codes.Internal, "internal error")
	}
	if tenant != nil {
		resp.Tenant = tenantToProto(tenant)
	}

	return resp, nil
}
