// Package rest provides a JSON-over-HTTP adapter for the HermesGateway gRPC service.
// It registers routes on an http.ServeMux and calls the gRPC handler directly (in-process).
// This avoids the need for grpc-gateway, envoy, or connect-go.
package rest

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/rs/zerolog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/hermes-waba/hermes/internal/gateway/handler"
	"github.com/hermes-waba/hermes/internal/gateway/middleware"
)

// GatewayStore is the subset of the gateway store needed by the REST adapter.
type GatewayStore interface {
	ClearAllConversations(ctx context.Context, workspaceID string) (int64, error)
	AddToAllowlist(ctx context.Context, workspaceID, phone, source string) error
	RemoveFromAllowlist(ctx context.Context, workspaceID, phone string) error
	ClearAllowlist(ctx context.Context, workspaceID string) (int64, error)
	ListAllowlist(ctx context.Context, workspaceID string, page, pageSize int32) ([]handler.AllowlistRow, int64, error)
}

// Adapter wraps a HermesGateway gRPC handler and exposes it as REST/JSON.
type Adapter struct {
	gw          hermesv1.HermesGatewayServer
	store       GatewayStore
	jwtSecret   []byte
	log         zerolog.Logger
	marshaler   protojson.MarshalOptions
	waHTTPAddr  string // WA service HTTP endpoint for phone pairing (e.g. "wa:9105")
}

// New creates a REST adapter for the given gRPC handler.
func New(gw hermesv1.HermesGatewayServer, store GatewayStore, jwtSecret []byte, log zerolog.Logger, waHTTPAddr string) *Adapter {
	return &Adapter{
		gw:         gw,
		store:      store,
		jwtSecret:  jwtSecret,
		log:        log.With().Str("component", "rest").Logger(),
		waHTTPAddr: waHTTPAddr,
		marshaler: protojson.MarshalOptions{
			EmitDefaultValues: true,
			UseProtoNames:     false, // camelCase for frontend
		},
	}
}

// Register mounts all REST routes on the given mux under /api/v1/.
func (a *Adapter) Register(mux *http.ServeMux) {
	// Auth (no middleware)
	mux.HandleFunc("POST /api/v1/auth/login", a.login)
	mux.HandleFunc("POST /api/v1/auth/refresh", a.refreshToken)

	// Auth (requires JWT)
	mux.HandleFunc("POST /api/v1/auth/logout", a.auth(a.logout))
	mux.HandleFunc("GET /api/v1/auth/me", a.auth(a.getMe))

	// Dashboard
	mux.HandleFunc("GET /api/v1/dashboard/stats", a.auth(a.getDashboardStats))

	// Tenants
	mux.HandleFunc("POST /api/v1/tenants", a.auth(a.createTenant))
	mux.HandleFunc("GET /api/v1/tenants", a.auth(a.listTenants))
	mux.HandleFunc("GET /api/v1/tenants/{id}", a.auth(a.getTenant))
	mux.HandleFunc("PUT /api/v1/tenants/{id}", a.auth(a.updateTenant))

	// Workspaces
	mux.HandleFunc("POST /api/v1/workspaces", a.auth(a.createWorkspace))
	mux.HandleFunc("GET /api/v1/workspaces", a.auth(a.listWorkspaces))
	mux.HandleFunc("GET /api/v1/workspaces/{id}", a.auth(a.getWorkspace))
	mux.HandleFunc("PUT /api/v1/workspaces/{id}", a.auth(a.updateWorkspace))
	mux.HandleFunc("DELETE /api/v1/workspaces/{id}", a.auth(a.deleteWorkspace))

	// Users
	mux.HandleFunc("POST /api/v1/users", a.auth(a.createUser))
	mux.HandleFunc("GET /api/v1/users", a.auth(a.listUsers))
	mux.HandleFunc("GET /api/v1/users/{id}", a.auth(a.getUser))
	mux.HandleFunc("PUT /api/v1/users/{id}", a.auth(a.updateUser))
	mux.HandleFunc("DELETE /api/v1/users/{id}", a.auth(a.deleteUser))

	// WA Numbers
	mux.HandleFunc("POST /api/v1/wa-numbers", a.auth(a.registerWaNumber))
	mux.HandleFunc("GET /api/v1/wa-numbers", a.auth(a.listWaNumbers))
	mux.HandleFunc("GET /api/v1/wa-numbers/{id}", a.auth(a.getWaNumber))
	mux.HandleFunc("GET /api/v1/wa-numbers/{id}/qr-code", a.auth(a.getQRCode))
	mux.HandleFunc("PUT /api/v1/wa-numbers/{id}", a.auth(a.updateWaNumber))
	mux.HandleFunc("POST /api/v1/wa-numbers/{id}/disconnect", a.auth(a.disconnectWaNumber))
	mux.HandleFunc("POST /api/v1/wa-numbers/{id}/reconnect", a.auth(a.reconnectWaNumber))
	mux.HandleFunc("DELETE /api/v1/wa-numbers/{id}", a.auth(a.deleteWaNumber))

	// Proxies
	mux.HandleFunc("POST /api/v1/proxies", a.auth(a.addProxies))
	mux.HandleFunc("GET /api/v1/proxies", a.auth(a.listProxies))
	mux.HandleFunc("GET /api/v1/proxies/best", a.auth(a.getBestProxy))
	mux.HandleFunc("GET /api/v1/proxies/{id}/health", a.auth(a.getProxyHealth))
	mux.HandleFunc("PUT /api/v1/proxies/{id}", a.auth(a.updateProxy))
	mux.HandleFunc("DELETE /api/v1/proxies/{id}", a.auth(a.deleteProxy))
	mux.HandleFunc("POST /api/v1/proxies/assign", a.auth(a.assignProxy))

	// Contacts
	mux.HandleFunc("POST /api/v1/contacts", a.auth(a.createContact))
	mux.HandleFunc("POST /api/v1/contacts/import", a.auth(a.importContacts))
	mux.HandleFunc("GET /api/v1/contacts", a.auth(a.listContacts))
	mux.HandleFunc("GET /api/v1/contacts/{id}", a.auth(a.getContact))
	mux.HandleFunc("PUT /api/v1/contacts/{id}", a.auth(a.updateContact))
	mux.HandleFunc("DELETE /api/v1/contacts/{id}", a.auth(a.deleteContact))
	mux.HandleFunc("GET /api/v1/contacts/{id}/campaigns", a.auth(a.getContactCampaignHistory))

	// Templates
	mux.HandleFunc("POST /api/v1/templates", a.auth(a.createTemplate))
	mux.HandleFunc("GET /api/v1/templates", a.auth(a.listTemplates))
	mux.HandleFunc("GET /api/v1/templates/{id}", a.auth(a.getTemplate))
	mux.HandleFunc("PUT /api/v1/templates/{id}", a.auth(a.updateTemplate))
	mux.HandleFunc("DELETE /api/v1/templates/{id}", a.auth(a.deleteTemplate))

	// Campaigns
	mux.HandleFunc("POST /api/v1/campaigns", a.auth(a.createCampaign))
	mux.HandleFunc("GET /api/v1/campaigns", a.auth(a.listCampaigns))
	mux.HandleFunc("GET /api/v1/campaigns/{id}", a.auth(a.getCampaign))
	mux.HandleFunc("POST /api/v1/campaigns/{id}/start", a.auth(a.startCampaign))
	mux.HandleFunc("POST /api/v1/campaigns/{id}/pause", a.auth(a.pauseCampaign))
	mux.HandleFunc("POST /api/v1/campaigns/{id}/resume", a.auth(a.resumeCampaign))
	mux.HandleFunc("POST /api/v1/campaigns/{id}/cancel", a.auth(a.cancelCampaign))
	mux.HandleFunc("PUT /api/v1/campaigns/{id}/numbers", a.auth(a.updateCampaignNumbers))
	mux.HandleFunc("PUT /api/v1/campaigns/{id}/contacts", a.auth(a.updateCampaignContacts))
	mux.HandleFunc("GET /api/v1/campaigns/{id}/contacts", a.auth(a.listCampaignContacts))
	mux.HandleFunc("GET /api/v1/campaigns/{id}/numbers", a.auth(a.listCampaignNumbers))

	// Conversations
	mux.HandleFunc("GET /api/v1/conversations", a.auth(a.listConversations))
	mux.HandleFunc("GET /api/v1/conversations/{id}", a.auth(a.getConversation))
	mux.HandleFunc("POST /api/v1/conversations/{id}/claim", a.auth(a.claimConversation))
	mux.HandleFunc("POST /api/v1/conversations/{id}/transfer", a.auth(a.transferConversation))
	mux.HandleFunc("POST /api/v1/conversations/{id}/close", a.auth(a.closeConversation))
	mux.HandleFunc("GET /api/v1/conversations/{id}/messages", a.auth(a.listMessages))
	mux.HandleFunc("POST /api/v1/conversations/{id}/messages", a.auth(a.sendMessage))
	mux.HandleFunc("POST /api/v1/conversations/{id}/typing", a.auth(a.sendTypingIndicator))
	mux.HandleFunc("POST /api/v1/messages/search", a.auth(a.searchMessages))

	// Agent Performance
	mux.HandleFunc("GET /api/v1/agent-performance", a.auth(a.getAgentPerformance))

	// Canned Responses
	mux.HandleFunc("POST /api/v1/canned-responses", a.auth(a.createCannedResponse))
	mux.HandleFunc("GET /api/v1/canned-responses", a.auth(a.listCannedResponses))
	mux.HandleFunc("PUT /api/v1/canned-responses/{id}", a.auth(a.updateCannedResponse))
	mux.HandleFunc("DELETE /api/v1/canned-responses/{id}", a.auth(a.deleteCannedResponse))

	// Phone pairing
	mux.HandleFunc("POST /api/v1/wa-numbers/{id}/pair-phone", a.auth(a.pairPhone))

	// Admin operations
	mux.HandleFunc("DELETE /api/v1/conversations/clear", a.auth(a.clearAllConversations))

	// Contact allowlist
	mux.HandleFunc("GET /api/v1/allowlist", a.auth(a.listAllowlist))
	mux.HandleFunc("POST /api/v1/allowlist", a.auth(a.addToAllowlist))
	mux.HandleFunc("DELETE /api/v1/allowlist", a.auth(a.removeFromAllowlist))
	mux.HandleFunc("DELETE /api/v1/allowlist/clear", a.auth(a.clearAllowlist))

	// Notifications
	mux.HandleFunc("POST /api/v1/notifications", a.auth(a.configureNotification))
	mux.HandleFunc("GET /api/v1/notifications", a.auth(a.listNotificationConfigs))
	mux.HandleFunc("POST /api/v1/notifications/{id}/test", a.auth(a.testNotification))
	mux.HandleFunc("DELETE /api/v1/notifications/{id}", a.auth(a.deleteNotificationConfig))
}

// ═══════════════════════════════════════════════════════════════
// Helpers
// ═══════════════════════════════════════════════════════════════

func (a *Adapter) writeProto(w http.ResponseWriter, msg proto.Message) {
	data, err := a.marshaler.Marshal(msg)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "MARSHAL_ERROR", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}

func (a *Adapter) writeError(w http.ResponseWriter, httpStatus int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatus)
	json.NewEncoder(w).Encode(map[string]string{"code": code, "message": message})
}

func (a *Adapter) grpcError(w http.ResponseWriter, err error) {
	st, ok := status.FromError(err)
	if !ok {
		a.writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	httpCode := grpcToHTTP(st.Code())
	a.writeError(w, httpCode, st.Code().String(), st.Message())
}

func grpcToHTTP(c codes.Code) int {
	switch c {
	case codes.OK:
		return 200
	case codes.InvalidArgument:
		return 400
	case codes.Unauthenticated:
		return 401
	case codes.PermissionDenied:
		return 403
	case codes.NotFound:
		return 404
	case codes.AlreadyExists:
		return 409
	case codes.ResourceExhausted:
		return 429
	case codes.Unimplemented:
		return 501
	case codes.Unavailable:
		return 503
	default:
		return 500
	}
}

func readJSON(r *http.Request, v any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1MB limit
	if err != nil {
		return err
	}
	if len(body) == 0 {
		return nil
	}
	return json.Unmarshal(body, v)
}

func readProto(r *http.Request, msg proto.Message) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	if len(body) == 0 {
		return nil
	}
	return protojson.Unmarshal(body, msg)
}

// auth wraps a handler with JWT authentication, injecting claims into context.
func (a *Adapter) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := ""
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			token = strings.TrimPrefix(h, "Bearer ")
		}
		if token == "" {
			a.writeError(w, 401, "UNAUTHENTICATED", "missing authorization token")
			return
		}

		// Reuse the gateway's auth interceptor logic via middleware package.
		claims, err := middleware.ParseJWT(token, a.jwtSecret)
		if err != nil {
			a.writeError(w, 401, "UNAUTHENTICATED", "invalid or expired token")
			return
		}

		// Build a context with the same keys the gRPC interceptor would set.
		ctx := r.Context()
		ctx = context.WithValue(ctx, middleware.CtxUserID, claims.UserID)
		ctx = context.WithValue(ctx, middleware.CtxTenantID, claims.TenantID)
		ctx = context.WithValue(ctx, middleware.CtxWorkspaceID, claims.WorkspaceID)
		ctx = context.WithValue(ctx, middleware.CtxRole, claims.Role)

		next(w, r.WithContext(ctx))
	}
}

// CORS middleware for dev.
func (a *Adapter) CORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == "OPTIONS" {
			w.WriteHeader(204)
			return
		}
		next.ServeHTTP(w, r)
	})
}
