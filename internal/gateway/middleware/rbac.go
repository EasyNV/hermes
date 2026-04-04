package middleware

import (
	"context"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// rpcRoles maps each gRPC full method path to the list of roles that are
// allowed to call it. The superadmin role is always allowed regardless of this
// map. Methods absent from the map are denied by default.
var rpcRoles = map[string][]string{
	// --- Any authenticated user ---
	"/hermes.v1.HermesGateway/Logout": {"superadmin", "tenant_admin", "workspace_admin", "cs_agent"},
	"/hermes.v1.HermesGateway/GetMe":  {"superadmin", "tenant_admin", "workspace_admin", "cs_agent"},

	// --- superadmin only ---
	"/hermes.v1.HermesGateway/CreateTenant": {"superadmin"},

	// --- superadmin, tenant_admin ---
	"/hermes.v1.HermesGateway/GetTenant":          {"superadmin", "tenant_admin"},
	"/hermes.v1.HermesGateway/ListTenants":         {"superadmin", "tenant_admin"},
	"/hermes.v1.HermesGateway/UpdateTenant":        {"superadmin", "tenant_admin"},
	"/hermes.v1.HermesGateway/CreateWorkspace":     {"superadmin", "tenant_admin"},
	"/hermes.v1.HermesGateway/DeleteWorkspace":     {"superadmin", "tenant_admin"},
	"/hermes.v1.HermesGateway/RegisterWaNumber":    {"superadmin", "tenant_admin"},
	"/hermes.v1.HermesGateway/GetQRCode":           {"superadmin", "tenant_admin"},
	"/hermes.v1.HermesGateway/UpdateWaNumber":      {"superadmin", "tenant_admin"},
	"/hermes.v1.HermesGateway/DisconnectWaNumber":  {"superadmin", "tenant_admin"},
	"/hermes.v1.HermesGateway/ReconnectWaNumber":   {"superadmin", "tenant_admin"},
	"/hermes.v1.HermesGateway/DeleteWaNumber":      {"superadmin", "tenant_admin"},
	"/hermes.v1.HermesGateway/AddProxies":          {"superadmin", "tenant_admin"},
	"/hermes.v1.HermesGateway/ListProxies":         {"superadmin", "tenant_admin"},
	"/hermes.v1.HermesGateway/UpdateProxy":         {"superadmin", "tenant_admin"},
	"/hermes.v1.HermesGateway/DeleteProxy":         {"superadmin", "tenant_admin"},
	"/hermes.v1.HermesGateway/AssignProxy":         {"superadmin", "tenant_admin"},
	"/hermes.v1.HermesGateway/GetProxyHealth":      {"superadmin", "tenant_admin"},
	"/hermes.v1.HermesGateway/GetBestProxy":        {"superadmin", "tenant_admin"},

	// --- WA number reads (all authenticated roles) ---
	"/hermes.v1.HermesGateway/ListWaNumbers": {"superadmin", "tenant_admin", "workspace_admin", "cs_agent"},
	"/hermes.v1.HermesGateway/GetWaNumber":   {"superadmin", "tenant_admin", "workspace_admin", "cs_agent"},

	// --- superadmin, tenant_admin, workspace_admin, cs_agent ---
	"/hermes.v1.HermesGateway/GetWorkspace":              {"superadmin", "tenant_admin", "workspace_admin", "cs_agent"},
	"/hermes.v1.HermesGateway/ListWorkspaces":             {"superadmin", "tenant_admin", "workspace_admin", "cs_agent"},
	"/hermes.v1.HermesGateway/ListContacts":               {"superadmin", "tenant_admin", "workspace_admin", "cs_agent"},
	"/hermes.v1.HermesGateway/GetContact":                 {"superadmin", "tenant_admin", "workspace_admin", "cs_agent"},
	"/hermes.v1.HermesGateway/GetTemplate":                {"superadmin", "tenant_admin", "workspace_admin", "cs_agent"},
	"/hermes.v1.HermesGateway/ListTemplates":              {"superadmin", "tenant_admin", "workspace_admin", "cs_agent"},
	"/hermes.v1.HermesGateway/GetCampaign":                {"superadmin", "tenant_admin", "workspace_admin", "cs_agent"},
	"/hermes.v1.HermesGateway/ListCampaigns":              {"superadmin", "tenant_admin", "workspace_admin", "cs_agent"},
	"/hermes.v1.HermesGateway/ListCampaignContacts":       {"superadmin", "tenant_admin", "workspace_admin", "cs_agent"},
	"/hermes.v1.HermesGateway/ListCampaignNumbers":        {"superadmin", "tenant_admin", "workspace_admin", "cs_agent"},
	"/hermes.v1.HermesGateway/ListConversations":          {"superadmin", "tenant_admin", "workspace_admin", "cs_agent"},
	"/hermes.v1.HermesGateway/GetConversation":            {"superadmin", "tenant_admin", "workspace_admin", "cs_agent"},
	"/hermes.v1.HermesGateway/ListMessages":               {"superadmin", "tenant_admin", "workspace_admin", "cs_agent"},
	"/hermes.v1.HermesGateway/SearchMessages":             {"superadmin", "tenant_admin", "workspace_admin", "cs_agent"},
	"/hermes.v1.HermesGateway/GetContactCampaignHistory":  {"superadmin", "tenant_admin", "workspace_admin", "cs_agent"},
	"/hermes.v1.HermesGateway/ListCannedResponses":        {"superadmin", "tenant_admin", "workspace_admin", "cs_agent"},
	"/hermes.v1.HermesGateway/GetDashboardStats":          {"superadmin", "tenant_admin", "workspace_admin", "cs_agent"},
	"/hermes.v1.HermesGateway/SendTypingIndicator":        {"superadmin", "tenant_admin", "workspace_admin", "cs_agent"},

	// --- superadmin, tenant_admin, workspace_admin ---
	"/hermes.v1.HermesGateway/UpdateWorkspace":         {"superadmin", "tenant_admin", "workspace_admin"},
	"/hermes.v1.HermesGateway/CreateUser":              {"superadmin", "tenant_admin", "workspace_admin"},
	"/hermes.v1.HermesGateway/GetUser":                 {"superadmin", "tenant_admin", "workspace_admin"},
	"/hermes.v1.HermesGateway/ListUsers":               {"superadmin", "tenant_admin", "workspace_admin"},
	"/hermes.v1.HermesGateway/UpdateUser":              {"superadmin", "tenant_admin", "workspace_admin"},
	"/hermes.v1.HermesGateway/DeleteUser":              {"superadmin", "tenant_admin", "workspace_admin"},
	"/hermes.v1.HermesGateway/CreateContact":           {"superadmin", "tenant_admin", "workspace_admin"},
	"/hermes.v1.HermesGateway/ImportContacts":          {"superadmin", "tenant_admin", "workspace_admin"},
	"/hermes.v1.HermesGateway/UpdateContact":           {"superadmin", "tenant_admin", "workspace_admin"},
	"/hermes.v1.HermesGateway/DeleteContact":           {"superadmin", "tenant_admin", "workspace_admin"},
	"/hermes.v1.HermesGateway/CreateTemplate":          {"superadmin", "tenant_admin", "workspace_admin"},
	"/hermes.v1.HermesGateway/UpdateTemplate":          {"superadmin", "tenant_admin", "workspace_admin"},
	"/hermes.v1.HermesGateway/DeleteTemplate":          {"superadmin", "tenant_admin", "workspace_admin"},
	"/hermes.v1.HermesGateway/CreateCampaign":          {"superadmin", "tenant_admin", "workspace_admin"},
	"/hermes.v1.HermesGateway/StartCampaign":           {"superadmin", "tenant_admin", "workspace_admin"},
	"/hermes.v1.HermesGateway/PauseCampaign":           {"superadmin", "tenant_admin", "workspace_admin"},
	"/hermes.v1.HermesGateway/ResumeCampaign":          {"superadmin", "tenant_admin", "workspace_admin"},
	"/hermes.v1.HermesGateway/CancelCampaign":          {"superadmin", "tenant_admin", "workspace_admin"},
	"/hermes.v1.HermesGateway/UpdateCampaignNumbers":   {"superadmin", "tenant_admin", "workspace_admin"},
	"/hermes.v1.HermesGateway/UpdateCampaignContacts":  {"superadmin", "tenant_admin", "workspace_admin"},
	"/hermes.v1.HermesGateway/CreateCannedResponse":    {"superadmin", "tenant_admin", "workspace_admin"},
	"/hermes.v1.HermesGateway/UpdateCannedResponse":    {"superadmin", "tenant_admin", "workspace_admin"},
	"/hermes.v1.HermesGateway/DeleteCannedResponse":    {"superadmin", "tenant_admin", "workspace_admin"},
	"/hermes.v1.HermesGateway/ConfigureNotification":   {"superadmin", "tenant_admin", "workspace_admin"},
	"/hermes.v1.HermesGateway/ListNotificationConfigs": {"superadmin", "tenant_admin", "workspace_admin"},
	"/hermes.v1.HermesGateway/TestNotification":        {"superadmin", "tenant_admin", "workspace_admin"},
	"/hermes.v1.HermesGateway/DeleteNotificationConfig": {"superadmin", "tenant_admin", "workspace_admin"},
	"/hermes.v1.HermesGateway/GetAgentPerformance":     {"superadmin", "tenant_admin", "workspace_admin"},

	// --- workspace_admin, cs_agent ---
	"/hermes.v1.HermesGateway/ClaimConversation":    {"workspace_admin", "cs_agent"},
	"/hermes.v1.HermesGateway/TransferConversation": {"workspace_admin", "cs_agent"},
	"/hermes.v1.HermesGateway/CloseConversation":    {"workspace_admin", "cs_agent"},
	"/hermes.v1.HermesGateway/SendMessage":          {"workspace_admin", "cs_agent"},
}

// RBACInterceptor returns a gRPC unary server interceptor that enforces
// role-based access control. It reads the caller's role from the context
// (set by AuthInterceptor) and checks it against the allowed roles for the
// requested RPC method. The superadmin role always bypasses RBAC checks.
func RBACInterceptor() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		// Skip RBAC for unauthenticated RPCs.
		if strings.Contains(info.FullMethod, "/Login") ||
			strings.Contains(info.FullMethod, "/RefreshToken") {
			return handler(ctx, req)
		}

		role := RoleFromCtx(ctx)
		if role == "" {
			return nil, status.Error(codes.PermissionDenied, "no role in context")
		}

		// Superadmin bypasses all RBAC checks.
		if role == "superadmin" {
			return handler(ctx, req)
		}

		allowed, ok := rpcRoles[info.FullMethod]
		if !ok {
			return nil, status.Errorf(codes.PermissionDenied, "no RBAC rule for %s", info.FullMethod)
		}

		for _, r := range allowed {
			if r == role {
				return handler(ctx, req)
			}
		}

		return nil, status.Errorf(codes.PermissionDenied, "role %q not allowed for %s", role, info.FullMethod)
	}
}
