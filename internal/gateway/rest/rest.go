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

// MbsRouter is the subset of the gateway handler that the MBS REST
// handlers (handlers_mbs.go) dispatch through. These methods are
// defined on *handler.Handler in chunk 1 but NOT part of
// HermesGatewayServer (the proto doesn't expose them — gateway.proto
// only declares Hermes core entities). The interface is local so the
// REST adapter doesn't have a hard dependency on the concrete handler
// type, and tests can substitute a fake.
type MbsRouter interface {
	ListMbsSessions(ctx context.Context, req *hermesv1.ListMbsSessionsRequest) (*hermesv1.ListMbsSessionsResponse, error)
	GetMbsSessionStatus(ctx context.Context, req *hermesv1.GetMbsSessionStatusRequest) (*hermesv1.GetMbsSessionStatusResponse, error)
	ListSessionAssets(ctx context.Context, req *hermesv1.ListSessionAssetsRequest) (*hermesv1.ListSessionAssetsResponse, error)
	BurnMbsSession(ctx context.Context, req *hermesv1.BurnMbsSessionRequest) (*hermesv1.BurnMbsSessionResponse, error)
	RemoveMbsSession(ctx context.Context, req *hermesv1.RemoveMbsSessionRequest) (*hermesv1.RemoveMbsSessionResponse, error)
	ResolveMbsPhone(ctx context.Context, req *hermesv1.ResolvePhoneRequest) (*hermesv1.ResolvePhoneResponse, error)
	SendMbsMessage(ctx context.Context, req *hermesv1.MbsSendMessageRequest) (*hermesv1.MbsSendMessageResponse, error)
}

// Adapter wraps a HermesGateway gRPC handler and exposes it as REST/JSON.
type Adapter struct {
	gw          hermesv1.HermesGatewayServer
	mbs         MbsRouter
	store       GatewayStore
	mbsClient   hermesv1.HermesMbsClient // direct client for bidi BridgeLogin WS bridge
	jwtSecret   []byte
	log         zerolog.Logger
	marshaler   protojson.MarshalOptions
	waHTTPAddr  string // WA service HTTP endpoint for phone pairing (e.g. "wa:9105")
}

// New creates a REST adapter for the given gRPC handler.
//
// mbs is the MBS router (typically the same *handler.Handler that
// satisfies HermesGatewayServer — see internal/gateway/handler/mbs.go).
//
// mbsClient is the same HermesMbsClient the gateway gRPC handler holds.
// It's passed directly so the BridgeLogin WS bridge can open a
// bidirectional gRPC stream without round-tripping through the gateway
// gRPC server (a bidi stream can't reasonably be unary-proxied).
//
// mbsClient may be nil — the WS bridge surfaces that as HTTP 503 before
// upgrading. REST routes don't touch mbsClient directly; they call
// through `mbs` and Handler's unary proxy methods (chunk 1).
func New(gw hermesv1.HermesGatewayServer, mbs MbsRouter, store GatewayStore, mbsClient hermesv1.HermesMbsClient, jwtSecret []byte, log zerolog.Logger, waHTTPAddr string) *Adapter {
	return &Adapter{
		gw:         gw,
		mbs:        mbs,
		store:      store,
		mbsClient:  mbsClient,
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

	// Auth (requires JWT, any authenticated role)
	mux.HandleFunc("POST /api/v1/auth/logout", a.authz("/hermes.v1.HermesGateway/Logout", a.logout))
	mux.HandleFunc("GET /api/v1/auth/me", a.authz("/hermes.v1.HermesGateway/GetMe", a.getMe))

	// Dashboard
	mux.HandleFunc("GET /api/v1/dashboard/stats", a.authz("/hermes.v1.HermesGateway/GetDashboardStats", a.getDashboardStats))

	// Tenants
	mux.HandleFunc("POST /api/v1/tenants", a.authz("/hermes.v1.HermesGateway/CreateTenant", a.createTenant))
	mux.HandleFunc("GET /api/v1/tenants", a.authz("/hermes.v1.HermesGateway/ListTenants", a.listTenants))
	mux.HandleFunc("GET /api/v1/tenants/{id}", a.authz("/hermes.v1.HermesGateway/GetTenant", a.getTenant))
	mux.HandleFunc("PUT /api/v1/tenants/{id}", a.authz("/hermes.v1.HermesGateway/UpdateTenant", a.updateTenant))

	// Workspaces
	mux.HandleFunc("POST /api/v1/workspaces", a.authz("/hermes.v1.HermesGateway/CreateWorkspace", a.createWorkspace))
	mux.HandleFunc("GET /api/v1/workspaces", a.authz("/hermes.v1.HermesGateway/ListWorkspaces", a.listWorkspaces))
	mux.HandleFunc("GET /api/v1/workspaces/{id}", a.authz("/hermes.v1.HermesGateway/GetWorkspace", a.getWorkspace))
	mux.HandleFunc("PUT /api/v1/workspaces/{id}", a.authz("/hermes.v1.HermesGateway/UpdateWorkspace", a.updateWorkspace))
	mux.HandleFunc("DELETE /api/v1/workspaces/{id}", a.authz("/hermes.v1.HermesGateway/DeleteWorkspace", a.deleteWorkspace))

	// Users
	mux.HandleFunc("POST /api/v1/users", a.authz("/hermes.v1.HermesGateway/CreateUser", a.createUser))
	mux.HandleFunc("GET /api/v1/users", a.authz("/hermes.v1.HermesGateway/ListUsers", a.listUsers))
	mux.HandleFunc("GET /api/v1/users/{id}", a.authz("/hermes.v1.HermesGateway/GetUser", a.getUser))
	mux.HandleFunc("PUT /api/v1/users/{id}", a.authz("/hermes.v1.HermesGateway/UpdateUser", a.updateUser))
	mux.HandleFunc("DELETE /api/v1/users/{id}", a.authz("/hermes.v1.HermesGateway/DeleteUser", a.deleteUser))

	// WA Numbers
	mux.HandleFunc("POST /api/v1/wa-numbers", a.authz("/hermes.v1.HermesGateway/RegisterWaNumber", a.registerWaNumber))
	mux.HandleFunc("GET /api/v1/wa-numbers", a.authz("/hermes.v1.HermesGateway/ListWaNumbers", a.listWaNumbers))
	mux.HandleFunc("GET /api/v1/wa-numbers/{id}", a.authz("/hermes.v1.HermesGateway/GetWaNumber", a.getWaNumber))
	mux.HandleFunc("GET /api/v1/wa-numbers/{id}/qr-code", a.authz("/hermes.v1.HermesGateway/GetQRCode", a.getQRCode))
	mux.HandleFunc("PUT /api/v1/wa-numbers/{id}", a.authz("/hermes.v1.HermesGateway/UpdateWaNumber", a.updateWaNumber))
	mux.HandleFunc("POST /api/v1/wa-numbers/{id}/disconnect", a.authz("/hermes.v1.HermesGateway/DisconnectWaNumber", a.disconnectWaNumber))
	mux.HandleFunc("POST /api/v1/wa-numbers/{id}/reconnect", a.authz("/hermes.v1.HermesGateway/ReconnectWaNumber", a.reconnectWaNumber))
	mux.HandleFunc("DELETE /api/v1/wa-numbers/{id}", a.authz("/hermes.v1.HermesGateway/DeleteWaNumber", a.deleteWaNumber))

	// Proxies
	mux.HandleFunc("POST /api/v1/proxies", a.authz("/hermes.v1.HermesGateway/AddProxies", a.addProxies))
	mux.HandleFunc("GET /api/v1/proxies", a.authz("/hermes.v1.HermesGateway/ListProxies", a.listProxies))
	mux.HandleFunc("GET /api/v1/proxies/best", a.authz("/hermes.v1.HermesGateway/GetBestProxy", a.getBestProxy))
	mux.HandleFunc("GET /api/v1/proxies/{id}/health", a.authz("/hermes.v1.HermesGateway/GetProxyHealth", a.getProxyHealth))
	mux.HandleFunc("PUT /api/v1/proxies/{id}", a.authz("/hermes.v1.HermesGateway/UpdateProxy", a.updateProxy))
	mux.HandleFunc("DELETE /api/v1/proxies/{id}", a.authz("/hermes.v1.HermesGateway/DeleteProxy", a.deleteProxy))
	mux.HandleFunc("POST /api/v1/proxies/assign", a.authz("/hermes.v1.HermesGateway/AssignProxy", a.assignProxy))

	// Contacts
	mux.HandleFunc("POST /api/v1/contacts", a.authz("/hermes.v1.HermesGateway/CreateContact", a.createContact))
	mux.HandleFunc("POST /api/v1/contacts/import", a.authz("/hermes.v1.HermesGateway/ImportContacts", a.importContacts))
	mux.HandleFunc("GET /api/v1/contacts", a.authz("/hermes.v1.HermesGateway/ListContacts", a.listContacts))
	mux.HandleFunc("GET /api/v1/contacts/{id}", a.authz("/hermes.v1.HermesGateway/GetContact", a.getContact))
	mux.HandleFunc("PUT /api/v1/contacts/{id}", a.authz("/hermes.v1.HermesGateway/UpdateContact", a.updateContact))
	mux.HandleFunc("DELETE /api/v1/contacts/{id}", a.authz("/hermes.v1.HermesGateway/DeleteContact", a.deleteContact))
	mux.HandleFunc("GET /api/v1/contacts/{id}/campaigns", a.authz("/hermes.v1.HermesGateway/GetContactCampaignHistory", a.getContactCampaignHistory))

	// Templates
	mux.HandleFunc("POST /api/v1/templates", a.authz("/hermes.v1.HermesGateway/CreateTemplate", a.createTemplate))
	mux.HandleFunc("GET /api/v1/templates", a.authz("/hermes.v1.HermesGateway/ListTemplates", a.listTemplates))
	mux.HandleFunc("GET /api/v1/templates/{id}", a.authz("/hermes.v1.HermesGateway/GetTemplate", a.getTemplate))
	mux.HandleFunc("PUT /api/v1/templates/{id}", a.authz("/hermes.v1.HermesGateway/UpdateTemplate", a.updateTemplate))
	mux.HandleFunc("DELETE /api/v1/templates/{id}", a.authz("/hermes.v1.HermesGateway/DeleteTemplate", a.deleteTemplate))

	// Campaigns
	mux.HandleFunc("POST /api/v1/campaigns", a.authz("/hermes.v1.HermesGateway/CreateCampaign", a.createCampaign))
	mux.HandleFunc("GET /api/v1/campaigns", a.authz("/hermes.v1.HermesGateway/ListCampaigns", a.listCampaigns))
	mux.HandleFunc("GET /api/v1/campaigns/{id}", a.authz("/hermes.v1.HermesGateway/GetCampaign", a.getCampaign))
	mux.HandleFunc("POST /api/v1/campaigns/{id}/start", a.authz("/hermes.v1.HermesGateway/StartCampaign", a.startCampaign))
	mux.HandleFunc("POST /api/v1/campaigns/{id}/pause", a.authz("/hermes.v1.HermesGateway/PauseCampaign", a.pauseCampaign))
	mux.HandleFunc("POST /api/v1/campaigns/{id}/resume", a.authz("/hermes.v1.HermesGateway/ResumeCampaign", a.resumeCampaign))
	mux.HandleFunc("POST /api/v1/campaigns/{id}/cancel", a.authz("/hermes.v1.HermesGateway/CancelCampaign", a.cancelCampaign))
	mux.HandleFunc("PUT /api/v1/campaigns/{id}/numbers", a.authz("/hermes.v1.HermesGateway/UpdateCampaignNumbers", a.updateCampaignNumbers))
	mux.HandleFunc("PUT /api/v1/campaigns/{id}/contacts", a.authz("/hermes.v1.HermesGateway/UpdateCampaignContacts", a.updateCampaignContacts))
	mux.HandleFunc("GET /api/v1/campaigns/{id}/contacts", a.authz("/hermes.v1.HermesGateway/ListCampaignContacts", a.listCampaignContacts))
	mux.HandleFunc("GET /api/v1/campaigns/{id}/numbers", a.authz("/hermes.v1.HermesGateway/ListCampaignNumbers", a.listCampaignNumbers))

	// Conversations
	mux.HandleFunc("GET /api/v1/conversations", a.authz("/hermes.v1.HermesGateway/ListConversations", a.listConversations))
	mux.HandleFunc("GET /api/v1/conversations/{id}", a.authz("/hermes.v1.HermesGateway/GetConversation", a.getConversation))
	mux.HandleFunc("POST /api/v1/conversations/{id}/claim", a.authz("/hermes.v1.HermesGateway/ClaimConversation", a.claimConversation))
	mux.HandleFunc("POST /api/v1/conversations/{id}/transfer", a.authz("/hermes.v1.HermesGateway/TransferConversation", a.transferConversation))
	mux.HandleFunc("POST /api/v1/conversations/{id}/close", a.authz("/hermes.v1.HermesGateway/CloseConversation", a.closeConversation))
	mux.HandleFunc("GET /api/v1/conversations/{id}/messages", a.authz("/hermes.v1.HermesGateway/ListMessages", a.listMessages))
	mux.HandleFunc("POST /api/v1/conversations/{id}/messages", a.authz("/hermes.v1.HermesGateway/SendMessage", a.sendMessage))
	mux.HandleFunc("POST /api/v1/conversations/{id}/typing", a.authz("REST:POST /api/v1/conversations/{id}/typing", a.sendTypingIndicator))
	mux.HandleFunc("POST /api/v1/messages/search", a.authz("/hermes.v1.HermesGateway/SearchMessages", a.searchMessages))

	// Agent Performance
	mux.HandleFunc("GET /api/v1/agent-performance", a.authz("/hermes.v1.HermesGateway/GetAgentPerformance", a.getAgentPerformance))

	// Canned Responses
	mux.HandleFunc("POST /api/v1/canned-responses", a.authz("/hermes.v1.HermesGateway/CreateCannedResponse", a.createCannedResponse))
	mux.HandleFunc("GET /api/v1/canned-responses", a.authz("/hermes.v1.HermesGateway/ListCannedResponses", a.listCannedResponses))
	mux.HandleFunc("PUT /api/v1/canned-responses/{id}", a.authz("/hermes.v1.HermesGateway/UpdateCannedResponse", a.updateCannedResponse))
	mux.HandleFunc("DELETE /api/v1/canned-responses/{id}", a.authz("/hermes.v1.HermesGateway/DeleteCannedResponse", a.deleteCannedResponse))

	// MBS sessions (chunk E2.2)
	mux.HandleFunc("GET /api/v1/mbs-sessions", a.authz("/hermes.v1.HermesMbs/ListSessions", a.listMbsSessions))
	mux.HandleFunc("GET /api/v1/mbs-sessions/{uid}", a.authz("/hermes.v1.HermesMbs/GetSessionStatus", a.getMbsSession))
	mux.HandleFunc("GET /api/v1/mbs-sessions/{uid}/assets", a.authz("/hermes.v1.HermesMbs/ListSessionAssets", a.listMbsSessionAssets))
	mux.HandleFunc("POST /api/v1/mbs-sessions/{uid}/burn", a.authz("/hermes.v1.HermesMbs/BurnSession", a.burnMbsSession))
	mux.HandleFunc("DELETE /api/v1/mbs-sessions/{uid}", a.authz("/hermes.v1.HermesMbs/RemoveSession", a.removeMbsSession))
	mux.HandleFunc("POST /api/v1/mbs-sessions/{uid}/resolve-phone", a.authz("/hermes.v1.HermesMbs/ResolvePhone", a.resolveMbsPhone))
	mux.HandleFunc("POST /api/v1/mbs-sessions/{uid}/messages", a.authz("/hermes.v1.HermesMbs/SendMessage", a.sendMbsMessage))

	// MBS bridge-login WebSocket (chunk E2.2).
	// Mounted OUTSIDE a.auth — the upgrade can't propagate ctx values
	// from HTTP middleware; the bridge validates JWT inline (matches
	// the existing /ws hub pattern).
	mux.HandleFunc("/ws/mbs/bridge-login", a.bridgeLoginWS)

	// Phone pairing
	mux.HandleFunc("POST /api/v1/wa-numbers/{id}/pair-phone", a.authz("REST:POST /api/v1/wa-numbers/{id}/pair-phone", a.pairPhone))

	// Admin operations
	mux.HandleFunc("DELETE /api/v1/conversations/clear", a.authz("REST:DELETE /api/v1/conversations/clear", a.clearAllConversations))

	// Contact allowlist
	mux.HandleFunc("GET /api/v1/allowlist", a.authz("REST:GET /api/v1/allowlist", a.listAllowlist))
	mux.HandleFunc("POST /api/v1/allowlist", a.authz("REST:POST /api/v1/allowlist", a.addToAllowlist))
	mux.HandleFunc("DELETE /api/v1/allowlist", a.authz("REST:DELETE /api/v1/allowlist", a.removeFromAllowlist))
	mux.HandleFunc("DELETE /api/v1/allowlist/clear", a.authz("REST:DELETE /api/v1/allowlist/clear", a.clearAllowlist))

	// Notifications
	mux.HandleFunc("POST /api/v1/notifications", a.authz("/hermes.v1.HermesGateway/ConfigureNotification", a.configureNotification))
	mux.HandleFunc("GET /api/v1/notifications", a.authz("/hermes.v1.HermesGateway/ListNotificationConfigs", a.listNotificationConfigs))
	mux.HandleFunc("POST /api/v1/notifications/{id}/test", a.authz("/hermes.v1.HermesGateway/TestNotification", a.testNotification))
	mux.HandleFunc("DELETE /api/v1/notifications/{id}", a.authz("/hermes.v1.HermesGateway/DeleteNotificationConfig", a.deleteNotificationConfig))
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

// authz wraps a handler with JWT authentication (via auth) PLUS role-tier RBAC
// for the given logical method key. It delegates to middleware.AuthorizeMethod
// — the SAME rpcRoles policy the gRPC RBACInterceptor enforces — so REST and
// gRPC apply identical authorization from one source of truth.
//
// method is either a HermesGateway/HermesMbs full-method path (e.g.
// "/hermes.v1.HermesGateway/CreateUser") or a synthetic "REST:METHOD /path"
// key for REST-only routes with no gRPC equivalent. Every authenticated route
// MUST pass a key present in rpcRoles or it fails closed (403).
func (a *Adapter) authz(method string, next http.HandlerFunc) http.HandlerFunc {
	return a.auth(func(w http.ResponseWriter, r *http.Request) {
		role, _ := r.Context().Value(middleware.CtxRole).(string)
		if err := middleware.AuthorizeMethod(role, method); err != nil {
			a.grpcError(w, err) // PermissionDenied -> 403 via grpcToHTTP
			return
		}
		next(w, r)
	})
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
