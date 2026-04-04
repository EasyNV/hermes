package rest

import (
	"net/http"
	"strconv"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/hermes-waba/hermes/internal/gateway/middleware"
)

// helper: extract pagination from query params.
func pagination(r *http.Request) *hermesv1.PageRequest {
	p := &hermesv1.PageRequest{}
	if v := r.URL.Query().Get("page"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			p.Page = int32(n)
		}
	}
	if v := r.URL.Query().Get("pageSize"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			p.PageSize = int32(n)
		}
	}
	return p
}

// ═══════════════════════════════════════════════════════════════
// Auth
// ═══════════════════════════════════════════════════════════════

func (a *Adapter) login(w http.ResponseWriter, r *http.Request) {
	req := &hermesv1.LoginRequest{}
	if err := readProto(r, req); err != nil {
		a.writeError(w, 400, "BAD_REQUEST", err.Error())
		return
	}
	resp, err := a.gw.Login(r.Context(), req)
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) refreshToken(w http.ResponseWriter, r *http.Request) {
	req := &hermesv1.RefreshTokenRequest{}
	if err := readProto(r, req); err != nil {
		a.writeError(w, 400, "BAD_REQUEST", err.Error())
		return
	}
	resp, err := a.gw.RefreshToken(r.Context(), req)
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) logout(w http.ResponseWriter, r *http.Request) {
	resp, err := a.gw.Logout(r.Context(), &hermesv1.LogoutRequest{})
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) getMe(w http.ResponseWriter, r *http.Request) {
	resp, err := a.gw.GetMe(r.Context(), &hermesv1.GetMeRequest{})
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

// ═══════════════════════════════════════════════════════════════
// Dashboard
// ═══════════════════════════════════════════════════════════════

func (a *Adapter) getDashboardStats(w http.ResponseWriter, r *http.Request) {
	req := &hermesv1.GetDashboardStatsRequest{
		WorkspaceId: r.URL.Query().Get("workspaceId"),
		TenantId:    r.URL.Query().Get("tenantId"),
	}
	resp, err := a.gw.GetDashboardStats(r.Context(), req)
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

// ═══════════════════════════════════════════════════════════════
// Tenants
// ═══════════════════════════════════════════════════════════════

func (a *Adapter) createTenant(w http.ResponseWriter, r *http.Request) {
	req := &hermesv1.CreateTenantRequest{}
	if err := readProto(r, req); err != nil {
		a.writeError(w, 400, "BAD_REQUEST", err.Error())
		return
	}
	resp, err := a.gw.CreateTenant(r.Context(), req)
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) getTenant(w http.ResponseWriter, r *http.Request) {
	resp, err := a.gw.GetTenant(r.Context(), &hermesv1.GetTenantRequest{Id: r.PathValue("id")})
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) listTenants(w http.ResponseWriter, r *http.Request) {
	resp, err := a.gw.ListTenants(r.Context(), &hermesv1.ListTenantsRequest{Pagination: pagination(r)})
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) updateTenant(w http.ResponseWriter, r *http.Request) {
	req := &hermesv1.UpdateTenantRequest{Id: r.PathValue("id")}
	if err := readProto(r, req); err != nil {
		a.writeError(w, 400, "BAD_REQUEST", err.Error())
		return
	}
	req.Id = r.PathValue("id")
	resp, err := a.gw.UpdateTenant(r.Context(), req)
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

// ═══════════════════════════════════════════════════════════════
// Workspaces
// ═══════════════════════════════════════════════════════════════

func (a *Adapter) createWorkspace(w http.ResponseWriter, r *http.Request) {
	req := &hermesv1.CreateWorkspaceRequest{}
	if err := readProto(r, req); err != nil {
		a.writeError(w, 400, "BAD_REQUEST", err.Error())
		return
	}
	resp, err := a.gw.CreateWorkspace(r.Context(), req)
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) getWorkspace(w http.ResponseWriter, r *http.Request) {
	resp, err := a.gw.GetWorkspace(r.Context(), &hermesv1.GetWorkspaceRequest{Id: r.PathValue("id")})
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) listWorkspaces(w http.ResponseWriter, r *http.Request) {
	resp, err := a.gw.ListWorkspaces(r.Context(), &hermesv1.ListWorkspacesRequest{
		TenantId:   r.URL.Query().Get("tenantId"),
		Pagination: pagination(r),
	})
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) updateWorkspace(w http.ResponseWriter, r *http.Request) {
	req := &hermesv1.UpdateWorkspaceRequest{Id: r.PathValue("id")}
	if err := readProto(r, req); err != nil {
		a.writeError(w, 400, "BAD_REQUEST", err.Error())
		return
	}
	req.Id = r.PathValue("id")
	resp, err := a.gw.UpdateWorkspace(r.Context(), req)
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) deleteWorkspace(w http.ResponseWriter, r *http.Request) {
	resp, err := a.gw.DeleteWorkspace(r.Context(), &hermesv1.DeleteWorkspaceRequest{Id: r.PathValue("id")})
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

// ═══════════════════════════════════════════════════════════════
// Users
// ═══════════════════════════════════════════════════════════════

func (a *Adapter) createUser(w http.ResponseWriter, r *http.Request) {
	req := &hermesv1.CreateUserRequest{}
	if err := readProto(r, req); err != nil {
		a.writeError(w, 400, "BAD_REQUEST", err.Error())
		return
	}
	resp, err := a.gw.CreateUser(r.Context(), req)
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) getUser(w http.ResponseWriter, r *http.Request) {
	resp, err := a.gw.GetUser(r.Context(), &hermesv1.GetUserRequest{Id: r.PathValue("id")})
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) listUsers(w http.ResponseWriter, r *http.Request) {
	resp, err := a.gw.ListUsers(r.Context(), &hermesv1.ListUsersRequest{
		WorkspaceId: r.URL.Query().Get("workspaceId"),
		Pagination:  pagination(r),
	})
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) updateUser(w http.ResponseWriter, r *http.Request) {
	req := &hermesv1.UpdateUserRequest{Id: r.PathValue("id")}
	if err := readProto(r, req); err != nil {
		a.writeError(w, 400, "BAD_REQUEST", err.Error())
		return
	}
	req.Id = r.PathValue("id")
	resp, err := a.gw.UpdateUser(r.Context(), req)
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) deleteUser(w http.ResponseWriter, r *http.Request) {
	resp, err := a.gw.DeleteUser(r.Context(), &hermesv1.DeleteUserRequest{Id: r.PathValue("id")})
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

// ═══════════════════════════════════════════════════════════════
// WA Numbers
// ═══════════════════════════════════════════════════════════════

func (a *Adapter) registerWaNumber(w http.ResponseWriter, r *http.Request) {
	req := &hermesv1.RegisterWaNumberRequest{}
	if err := readProto(r, req); err != nil {
		a.writeError(w, 400, "BAD_REQUEST", err.Error())
		return
	}
	resp, err := a.gw.RegisterWaNumber(r.Context(), req)
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) listWaNumbers(w http.ResponseWriter, r *http.Request) {
	req := &hermesv1.ListWaNumbersRequest{
		TenantId:    r.URL.Query().Get("tenantId"),
		WorkspaceId: r.URL.Query().Get("workspaceId"),
		Pagination:  pagination(r),
	}
	resp, err := a.gw.ListWaNumbers(r.Context(), req)
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) getWaNumber(w http.ResponseWriter, r *http.Request) {
	resp, err := a.gw.GetWaNumber(r.Context(), &hermesv1.GetWaNumberRequest{Id: r.PathValue("id")})
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) getQRCode(w http.ResponseWriter, r *http.Request) {
	resp, err := a.gw.GetQRCode(r.Context(), &hermesv1.GetQRCodeRequest{WaNumberId: r.PathValue("id")})
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) updateWaNumber(w http.ResponseWriter, r *http.Request) {
	req := &hermesv1.UpdateWaNumberRequest{Id: r.PathValue("id")}
	if err := readProto(r, req); err != nil {
		a.writeError(w, 400, "BAD_REQUEST", err.Error())
		return
	}
	req.Id = r.PathValue("id")
	resp, err := a.gw.UpdateWaNumber(r.Context(), req)
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) disconnectWaNumber(w http.ResponseWriter, r *http.Request) {
	resp, err := a.gw.DisconnectWaNumber(r.Context(), &hermesv1.DisconnectWaNumberRequest{Id: r.PathValue("id")})
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) reconnectWaNumber(w http.ResponseWriter, r *http.Request) {
	resp, err := a.gw.ReconnectWaNumber(r.Context(), &hermesv1.ReconnectWaNumberRequest{Id: r.PathValue("id")})
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) deleteWaNumber(w http.ResponseWriter, r *http.Request) {
	resp, err := a.gw.DeleteWaNumber(r.Context(), &hermesv1.DeleteWaNumberRequest{Id: r.PathValue("id")})
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

// ═══════════════════════════════════════════════════════════════
// Proxies
// ═══════════════════════════════════════════════════════════════

func (a *Adapter) addProxies(w http.ResponseWriter, r *http.Request) {
	req := &hermesv1.AddProxiesRequest{}
	if err := readProto(r, req); err != nil {
		a.writeError(w, 400, "BAD_REQUEST", err.Error())
		return
	}
	resp, err := a.gw.AddProxies(r.Context(), req)
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) listProxies(w http.ResponseWriter, r *http.Request) {
	req := &hermesv1.ListProxiesRequest{
		TenantId:   r.URL.Query().Get("tenantId"),
		Pagination: pagination(r),
	}
	resp, err := a.gw.ListProxies(r.Context(), req)
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) updateProxy(w http.ResponseWriter, r *http.Request) {
	req := &hermesv1.UpdateProxyRequest{Id: r.PathValue("id")}
	if err := readProto(r, req); err != nil {
		a.writeError(w, 400, "BAD_REQUEST", err.Error())
		return
	}
	req.Id = r.PathValue("id")
	resp, err := a.gw.UpdateProxy(r.Context(), req)
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) deleteProxy(w http.ResponseWriter, r *http.Request) {
	resp, err := a.gw.DeleteProxy(r.Context(), &hermesv1.DeleteProxyRequest{Id: r.PathValue("id")})
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) assignProxy(w http.ResponseWriter, r *http.Request) {
	req := &hermesv1.AssignProxyRequest{}
	if err := readProto(r, req); err != nil {
		a.writeError(w, 400, "BAD_REQUEST", err.Error())
		return
	}
	resp, err := a.gw.AssignProxy(r.Context(), req)
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) getProxyHealth(w http.ResponseWriter, r *http.Request) {
	resp, err := a.gw.GetProxyHealth(r.Context(), &hermesv1.GetProxyHealthRequest{Id: r.PathValue("id")})
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) getBestProxy(w http.ResponseWriter, r *http.Request) {
	req := &hermesv1.GetBestProxyRequest{
		TenantId: r.URL.Query().Get("tenantId"),
	}
	resp, err := a.gw.GetBestProxy(r.Context(), req)
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

// ═══════════════════════════════════════════════════════════════
// Contacts
// ═══════════════════════════════════════════════════════════════

func (a *Adapter) createContact(w http.ResponseWriter, r *http.Request) {
	req := &hermesv1.CreateContactRequest{}
	if err := readProto(r, req); err != nil {
		a.writeError(w, 400, "BAD_REQUEST", err.Error())
		return
	}
	resp, err := a.gw.CreateContact(r.Context(), req)
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) importContacts(w http.ResponseWriter, r *http.Request) {
	req := &hermesv1.ImportContactsRequest{}
	if err := readProto(r, req); err != nil {
		a.writeError(w, 400, "BAD_REQUEST", err.Error())
		return
	}
	resp, err := a.gw.ImportContacts(r.Context(), req)
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) listContacts(w http.ResponseWriter, r *http.Request) {
	req := &hermesv1.ListContactsRequest{
		TenantId:   r.URL.Query().Get("tenantId"),
		Search:     r.URL.Query().Get("search"),
		Pagination: pagination(r),
	}
	if v := r.URL.Query().Get("isBanned"); v == "true" {
		req.IsBanned = true
		req.FilterBanned = true
	}
	for _, t := range r.URL.Query()["tags"] {
		req.Tags = append(req.Tags, t)
	}
	resp, err := a.gw.ListContacts(r.Context(), req)
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) getContact(w http.ResponseWriter, r *http.Request) {
	resp, err := a.gw.GetContact(r.Context(), &hermesv1.GetContactRequest{Id: r.PathValue("id")})
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) updateContact(w http.ResponseWriter, r *http.Request) {
	req := &hermesv1.UpdateContactRequest{Id: r.PathValue("id")}
	if err := readProto(r, req); err != nil {
		a.writeError(w, 400, "BAD_REQUEST", err.Error())
		return
	}
	req.Id = r.PathValue("id")
	resp, err := a.gw.UpdateContact(r.Context(), req)
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) deleteContact(w http.ResponseWriter, r *http.Request) {
	resp, err := a.gw.DeleteContact(r.Context(), &hermesv1.DeleteContactRequest{Id: r.PathValue("id")})
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) getContactCampaignHistory(w http.ResponseWriter, r *http.Request) {
	resp, err := a.gw.GetContactCampaignHistory(r.Context(), &hermesv1.GetContactCampaignHistoryRequest{
		ContactId:  r.PathValue("id"),
		Pagination: pagination(r),
	})
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

// ═══════════════════════════════════════════════════════════════
// Templates
// ═══════════════════════════════════════════════════════════════

func (a *Adapter) createTemplate(w http.ResponseWriter, r *http.Request) {
	req := &hermesv1.CreateTemplateRequest{}
	if err := readProto(r, req); err != nil {
		a.writeError(w, 400, "BAD_REQUEST", err.Error())
		return
	}
	resp, err := a.gw.CreateTemplate(r.Context(), req)
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) getTemplate(w http.ResponseWriter, r *http.Request) {
	resp, err := a.gw.GetTemplate(r.Context(), &hermesv1.GetTemplateRequest{Id: r.PathValue("id")})
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) listTemplates(w http.ResponseWriter, r *http.Request) {
	resp, err := a.gw.ListTemplates(r.Context(), &hermesv1.ListTemplatesRequest{
		WorkspaceId: r.URL.Query().Get("workspaceId"),
		Search:      r.URL.Query().Get("search"),
		Pagination:  pagination(r),
	})
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) updateTemplate(w http.ResponseWriter, r *http.Request) {
	req := &hermesv1.UpdateTemplateRequest{Id: r.PathValue("id")}
	if err := readProto(r, req); err != nil {
		a.writeError(w, 400, "BAD_REQUEST", err.Error())
		return
	}
	req.Id = r.PathValue("id")
	resp, err := a.gw.UpdateTemplate(r.Context(), req)
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) deleteTemplate(w http.ResponseWriter, r *http.Request) {
	resp, err := a.gw.DeleteTemplate(r.Context(), &hermesv1.DeleteTemplateRequest{Id: r.PathValue("id")})
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

// ═══════════════════════════════════════════════════════════════
// Campaigns
// ═══════════════════════════════════════════════════════════════

func (a *Adapter) createCampaign(w http.ResponseWriter, r *http.Request) {
	req := &hermesv1.CreateCampaignRequest{}
	if err := readProto(r, req); err != nil {
		a.writeError(w, 400, "BAD_REQUEST", err.Error())
		return
	}
	resp, err := a.gw.CreateCampaign(r.Context(), req)
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) getCampaign(w http.ResponseWriter, r *http.Request) {
	resp, err := a.gw.GetCampaign(r.Context(), &hermesv1.GetCampaignRequest{Id: r.PathValue("id")})
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) listCampaigns(w http.ResponseWriter, r *http.Request) {
	resp, err := a.gw.ListCampaigns(r.Context(), &hermesv1.ListCampaignsRequest{
		WorkspaceId: r.URL.Query().Get("workspaceId"),
		Pagination:  pagination(r),
	})
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) startCampaign(w http.ResponseWriter, r *http.Request) {
	resp, err := a.gw.StartCampaign(r.Context(), &hermesv1.StartCampaignRequest{Id: r.PathValue("id")})
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) pauseCampaign(w http.ResponseWriter, r *http.Request) {
	resp, err := a.gw.PauseCampaign(r.Context(), &hermesv1.PauseCampaignRequest{Id: r.PathValue("id")})
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) resumeCampaign(w http.ResponseWriter, r *http.Request) {
	resp, err := a.gw.ResumeCampaign(r.Context(), &hermesv1.ResumeCampaignRequest{Id: r.PathValue("id")})
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) cancelCampaign(w http.ResponseWriter, r *http.Request) {
	resp, err := a.gw.CancelCampaign(r.Context(), &hermesv1.CancelCampaignRequest{Id: r.PathValue("id")})
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) updateCampaignNumbers(w http.ResponseWriter, r *http.Request) {
	req := &hermesv1.UpdateCampaignNumbersRequest{CampaignId: r.PathValue("id")}
	if err := readProto(r, req); err != nil {
		a.writeError(w, 400, "BAD_REQUEST", err.Error())
		return
	}
	req.CampaignId = r.PathValue("id")
	resp, err := a.gw.UpdateCampaignNumbers(r.Context(), req)
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) updateCampaignContacts(w http.ResponseWriter, r *http.Request) {
	req := &hermesv1.UpdateCampaignContactsRequest{CampaignId: r.PathValue("id")}
	if err := readProto(r, req); err != nil {
		a.writeError(w, 400, "BAD_REQUEST", err.Error())
		return
	}
	req.CampaignId = r.PathValue("id")
	resp, err := a.gw.UpdateCampaignContacts(r.Context(), req)
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) listCampaignContacts(w http.ResponseWriter, r *http.Request) {
	resp, err := a.gw.ListCampaignContacts(r.Context(), &hermesv1.ListCampaignContactsRequest{
		CampaignId: r.PathValue("id"),
		Pagination: pagination(r),
	})
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) listCampaignNumbers(w http.ResponseWriter, r *http.Request) {
	resp, err := a.gw.ListCampaignNumbers(r.Context(), &hermesv1.ListCampaignNumbersRequest{
		CampaignId: r.PathValue("id"),
		Pagination: pagination(r),
	})
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

// ═══════════════════════════════════════════════════════════════
// Conversations & Messages
// ═══════════════════════════════════════════════════════════════

func (a *Adapter) listConversations(w http.ResponseWriter, r *http.Request) {
	resp, err := a.gw.ListConversations(r.Context(), &hermesv1.ListConversationsRequest{
		WorkspaceId: r.URL.Query().Get("workspaceId"),
		AssignedTo:  r.URL.Query().Get("assignedTo"),
		WaNumberId:  r.URL.Query().Get("waNumberId"),
		Search:      r.URL.Query().Get("search"),
		Pagination:  pagination(r),
	})
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) getConversation(w http.ResponseWriter, r *http.Request) {
	resp, err := a.gw.GetConversation(r.Context(), &hermesv1.GetConversationRequest{Id: r.PathValue("id")})
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) claimConversation(w http.ResponseWriter, r *http.Request) {
	resp, err := a.gw.ClaimConversation(r.Context(), &hermesv1.ClaimConversationRequest{Id: r.PathValue("id")})
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) transferConversation(w http.ResponseWriter, r *http.Request) {
	req := &hermesv1.TransferConversationRequest{Id: r.PathValue("id")}
	if err := readProto(r, req); err != nil {
		a.writeError(w, 400, "BAD_REQUEST", err.Error())
		return
	}
	req.Id = r.PathValue("id")
	resp, err := a.gw.TransferConversation(r.Context(), req)
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) closeConversation(w http.ResponseWriter, r *http.Request) {
	resp, err := a.gw.CloseConversation(r.Context(), &hermesv1.CloseConversationRequest{Id: r.PathValue("id")})
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) listMessages(w http.ResponseWriter, r *http.Request) {
	resp, err := a.gw.ListMessages(r.Context(), &hermesv1.ListMessagesRequest{
		ConversationId: r.PathValue("id"),
		Pagination:     pagination(r),
	})
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) sendMessage(w http.ResponseWriter, r *http.Request) {
	req := &hermesv1.SendMessageRequest{ConversationId: r.PathValue("id")}
	if err := readProto(r, req); err != nil {
		a.writeError(w, 400, "BAD_REQUEST", err.Error())
		return
	}
	req.ConversationId = r.PathValue("id")
	resp, err := a.gw.SendMessage(r.Context(), req)
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) searchMessages(w http.ResponseWriter, r *http.Request) {
	req := &hermesv1.SearchMessagesRequest{}
	if err := readProto(r, req); err != nil {
		a.writeError(w, 400, "BAD_REQUEST", err.Error())
		return
	}
	resp, err := a.gw.SearchMessages(r.Context(), req)
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) sendTypingIndicator(w http.ResponseWriter, r *http.Request) {
	resp, err := a.gw.SendTypingIndicator(r.Context(), &hermesv1.SendTypingIndicatorRequest{
		ConversationId: r.PathValue("id"),
	})
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

// ═══════════════════════════════════════════════════════════════
// Agent Performance
// ═══════════════════════════════════════════════════════════════

func (a *Adapter) getAgentPerformance(w http.ResponseWriter, r *http.Request) {
	resp, err := a.gw.GetAgentPerformance(r.Context(), &hermesv1.GetAgentPerformanceRequest{
		WorkspaceId: r.URL.Query().Get("workspaceId"),
		UserId:      r.URL.Query().Get("userId"),
	})
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

// ═══════════════════════════════════════════════════════════════
// Canned Responses
// ═══════════════════════════════════════════════════════════════

func (a *Adapter) createCannedResponse(w http.ResponseWriter, r *http.Request) {
	req := &hermesv1.CreateCannedResponseRequest{}
	if err := readProto(r, req); err != nil {
		a.writeError(w, 400, "BAD_REQUEST", err.Error())
		return
	}
	resp, err := a.gw.CreateCannedResponse(r.Context(), req)
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) listCannedResponses(w http.ResponseWriter, r *http.Request) {
	resp, err := a.gw.ListCannedResponses(r.Context(), &hermesv1.ListCannedResponsesRequest{
		WorkspaceId: r.URL.Query().Get("workspaceId"),
		Search:      r.URL.Query().Get("search"),
		Pagination:  pagination(r),
	})
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) updateCannedResponse(w http.ResponseWriter, r *http.Request) {
	req := &hermesv1.UpdateCannedResponseRequest{Id: r.PathValue("id")}
	if err := readProto(r, req); err != nil {
		a.writeError(w, 400, "BAD_REQUEST", err.Error())
		return
	}
	req.Id = r.PathValue("id")
	resp, err := a.gw.UpdateCannedResponse(r.Context(), req)
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) deleteCannedResponse(w http.ResponseWriter, r *http.Request) {
	resp, err := a.gw.DeleteCannedResponse(r.Context(), &hermesv1.DeleteCannedResponseRequest{Id: r.PathValue("id")})
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

// ═══════════════════════════════════════════════════════════════
// Notifications
// ═══════════════════════════════════════════════════════════════

func (a *Adapter) configureNotification(w http.ResponseWriter, r *http.Request) {
	req := &hermesv1.ConfigureNotificationRequest{}
	if err := readProto(r, req); err != nil {
		a.writeError(w, 400, "BAD_REQUEST", err.Error())
		return
	}
	resp, err := a.gw.ConfigureNotification(r.Context(), req)
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) listNotificationConfigs(w http.ResponseWriter, r *http.Request) {
	resp, err := a.gw.ListNotificationConfigs(r.Context(), &hermesv1.ListNotificationConfigsRequest{
		WorkspaceId: r.URL.Query().Get("workspaceId"),
	})
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) testNotification(w http.ResponseWriter, r *http.Request) {
	resp, err := a.gw.TestNotification(r.Context(), &hermesv1.TestNotificationRequest{ConfigId: r.PathValue("id")})
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

func (a *Adapter) deleteNotificationConfig(w http.ResponseWriter, r *http.Request) {
	resp, err := a.gw.DeleteNotificationConfig(r.Context(), &hermesv1.DeleteNotificationConfigRequest{Id: r.PathValue("id")})
	if err != nil {
		a.grpcError(w, err)
		return
	}
	a.writeProto(w, resp)
}

// Unused import guard.
var _ = middleware.CtxUserID
