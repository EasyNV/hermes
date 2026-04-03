package handler

import (
	"context"
	"errors"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/hermes-waba/hermes/internal/proxy/health"
	"github.com/rs/zerolog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Handler implements the HermesProxyServer gRPC interface.
type Handler struct {
	hermesv1.UnimplementedHermesProxyServer
	store   Store
	checker health.Checker
	log     zerolog.Logger
}

func NewHandler(store Store, checker health.Checker, log zerolog.Logger) *Handler {
	return &Handler{store: store, checker: checker, log: log}
}

// ---------------------------------------------------------------------------
// Proto ↔ DB conversion helpers
// ---------------------------------------------------------------------------

func proxyTypeToStr(t hermesv1.ProxyType) string {
	switch t {
	case hermesv1.ProxyType_PROXY_TYPE_SOCKS5:
		return "socks5"
	case hermesv1.ProxyType_PROXY_TYPE_HTTP:
		return "http"
	default:
		return ""
	}
}

func strToProxyType(s string) hermesv1.ProxyType {
	switch s {
	case "socks5":
		return hermesv1.ProxyType_PROXY_TYPE_SOCKS5
	case "http":
		return hermesv1.ProxyType_PROXY_TYPE_HTTP
	default:
		return hermesv1.ProxyType_PROXY_TYPE_UNSPECIFIED
	}
}

func proxyStatusToStr(s hermesv1.ProxyStatus) string {
	switch s {
	case hermesv1.ProxyStatus_PROXY_STATUS_ACTIVE:
		return "active"
	case hermesv1.ProxyStatus_PROXY_STATUS_DEAD:
		return "dead"
	case hermesv1.ProxyStatus_PROXY_STATUS_FLAGGED:
		return "flagged"
	default:
		return ""
	}
}

func strToProxyStatus(s string) hermesv1.ProxyStatus {
	switch s {
	case "active":
		return hermesv1.ProxyStatus_PROXY_STATUS_ACTIVE
	case "dead":
		return hermesv1.ProxyStatus_PROXY_STATUS_DEAD
	case "flagged":
		return hermesv1.ProxyStatus_PROXY_STATUS_FLAGGED
	default:
		return hermesv1.ProxyStatus_PROXY_STATUS_UNSPECIFIED
	}
}

func strToWaNumberStatus(s string) hermesv1.WaNumberStatus {
	switch s {
	case "active":
		return hermesv1.WaNumberStatus_WA_NUMBER_STATUS_ACTIVE
	case "banned":
		return hermesv1.WaNumberStatus_WA_NUMBER_STATUS_BANNED
	case "disconnected":
		return hermesv1.WaNumberStatus_WA_NUMBER_STATUS_DISCONNECTED
	case "cooldown":
		return hermesv1.WaNumberStatus_WA_NUMBER_STATUS_COOLDOWN
	default:
		return hermesv1.WaNumberStatus_WA_NUMBER_STATUS_UNSPECIFIED
	}
}

func rowToProto(r *ProxyRow) *hermesv1.Proxy {
	p := &hermesv1.Proxy{
		Id:            r.ID,
		TenantId:      r.TenantID,
		Host:          r.Host,
		Port:          r.Port,
		Username:      r.Username,
		Password:      r.Password,
		Type:          strToProxyType(r.Type),
		Status:        strToProxyStatus(r.Status),
		BanCount:      r.BanCount,
		AssignedCount: r.AssignedCount,
		CreatedAt:     timestamppb.New(r.CreatedAt),
	}
	if r.LastHealthCheck != nil {
		p.LastHealthCheck = timestamppb.New(*r.LastHealthCheck)
	}
	return p
}

// ---------------------------------------------------------------------------
// RPC 1: AddProxies
// ---------------------------------------------------------------------------

func (h *Handler) AddProxies(ctx context.Context, req *hermesv1.ProxyAddRequest) (*hermesv1.ProxyAddResponse, error) {
	if req.TenantId == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if len(req.Proxies) == 0 {
		return nil, status.Error(codes.InvalidArgument, "at least one proxy is required")
	}

	var (
		added   []*hermesv1.Proxy
		skipped []*hermesv1.ProxySkipDetail
	)

	for _, input := range req.Proxies {
		if input.Host == "" || input.Port == 0 {
			skipped = append(skipped, &hermesv1.ProxySkipDetail{
				Host: input.Host, Port: input.Port,
				Reason: "host and port are required",
			})
			continue
		}

		exists, err := h.store.ProxyExistsByHostPort(ctx, req.TenantId, input.Host, input.Port)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "checking proxy existence: %v", err)
		}
		if exists {
			skipped = append(skipped, &hermesv1.ProxySkipDetail{
				Host: input.Host, Port: input.Port,
				Reason: "duplicate: proxy already exists for this tenant",
			})
			continue
		}

		pt := proxyTypeToStr(input.Type)
		if pt == "" {
			pt = "socks5"
		}

		row, err := h.store.CreateProxy(ctx, req.TenantId, input.Host, input.Port, input.Username, input.Password, pt)
		if err != nil {
			skipped = append(skipped, &hermesv1.ProxySkipDetail{
				Host: input.Host, Port: input.Port,
				Reason: "failed to create: " + err.Error(),
			})
			continue
		}
		added = append(added, rowToProto(row))
	}

	return &hermesv1.ProxyAddResponse{
		Proxies:      added,
		SkippedCount: int32(len(skipped)),
		Skipped:      skipped,
	}, nil
}

// ---------------------------------------------------------------------------
// RPC 2: ListProxies
// ---------------------------------------------------------------------------

func (h *Handler) ListProxies(ctx context.Context, req *hermesv1.ProxyListRequest) (*hermesv1.ProxyListResponse, error) {
	if req.TenantId == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	page := req.GetPagination().GetPage()
	pageSize := req.GetPagination().GetPageSize()
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 200 {
		pageSize = 200
	}

	rows, total, err := h.store.ListProxies(ctx, req.TenantId, proxyStatusToStr(req.Status), proxyTypeToStr(req.Type), page, pageSize)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "listing proxies: %v", err)
	}

	proxies := make([]*hermesv1.Proxy, 0, len(rows))
	for _, r := range rows {
		proxies = append(proxies, rowToProto(r))
	}

	totalPages := int32(0)
	if total > 0 {
		totalPages = int32((total + int64(pageSize) - 1) / int64(pageSize))
	}

	return &hermesv1.ProxyListResponse{
		Proxies: proxies,
		Pagination: &hermesv1.PageResponse{
			Total: total, Page: page, PageSize: pageSize, TotalPages: totalPages,
		},
	}, nil
}

// ---------------------------------------------------------------------------
// RPC 3: GetProxy
// ---------------------------------------------------------------------------

func (h *Handler) GetProxy(ctx context.Context, req *hermesv1.ProxyGetRequest) (*hermesv1.ProxyGetResponse, error) {
	if req.Id == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	row, err := h.store.GetProxy(ctx, req.Id)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "getting proxy: %v", err)
	}
	if row == nil {
		return nil, status.Error(codes.NotFound, "proxy not found")
	}

	numbers, err := h.store.GetAssignedNumbers(ctx, req.Id)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "getting assigned numbers: %v", err)
	}

	summaries := make([]*hermesv1.WaNumberSummary, 0, len(numbers))
	for _, n := range numbers {
		summaries = append(summaries, &hermesv1.WaNumberSummary{
			Id: n.ID, Phone: n.Phone, DisplayName: n.DisplayName,
			Status: strToWaNumberStatus(n.Status),
		})
	}

	return &hermesv1.ProxyGetResponse{Proxy: rowToProto(row), AssignedNumbers: summaries}, nil
}

// ---------------------------------------------------------------------------
// RPC 4: UpdateProxy
// ---------------------------------------------------------------------------

func (h *Handler) UpdateProxy(ctx context.Context, req *hermesv1.ProxyUpdateRequest) (*hermesv1.ProxyUpdateResponse, error) {
	if req.Id == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	row, err := h.store.UpdateProxy(ctx, req.Id, req.Host, req.Port, req.Username, req.Password,
		proxyTypeToStr(req.Type), proxyStatusToStr(req.Status))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "updating proxy: %v", err)
	}
	if row == nil {
		return nil, status.Error(codes.NotFound, "proxy not found")
	}

	return &hermesv1.ProxyUpdateResponse{Proxy: rowToProto(row)}, nil
}

// ---------------------------------------------------------------------------
// RPC 5: DeleteProxy
// ---------------------------------------------------------------------------

func (h *Handler) DeleteProxy(ctx context.Context, req *hermesv1.ProxyDeleteRequest) (*hermesv1.ProxyDeleteResponse, error) {
	if req.Id == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	unassigned, err := h.store.DeleteProxy(ctx, req.Id, req.Force)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, status.Error(codes.NotFound, "proxy not found")
		}
		if errors.Is(err, ErrHasAssignments) {
			return nil, status.Error(codes.FailedPrecondition, "proxy has assigned numbers; use force=true to unassign and delete")
		}
		return nil, status.Errorf(codes.Internal, "deleting proxy: %v", err)
	}

	return &hermesv1.ProxyDeleteResponse{UnassignedCount: unassigned}, nil
}

// ---------------------------------------------------------------------------
// RPC 6: AssignProxy
// ---------------------------------------------------------------------------

func (h *Handler) AssignProxy(ctx context.Context, req *hermesv1.ProxyAssignRequest) (*hermesv1.ProxyAssignResponse, error) {
	if req.WaNumberId == "" || req.ProxyId == "" {
		return nil, status.Error(codes.InvalidArgument, "wa_number_id and proxy_id are required")
	}

	row, err := h.store.AssignProxy(ctx, req.WaNumberId, req.ProxyId)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, status.Error(codes.NotFound, "proxy or WA number not found")
		}
		return nil, status.Errorf(codes.Internal, "assigning proxy: %v", err)
	}

	return &hermesv1.ProxyAssignResponse{Proxy: rowToProto(row), AssignedCount: row.AssignedCount}, nil
}

// ---------------------------------------------------------------------------
// RPC 7: UnassignProxy
// ---------------------------------------------------------------------------

func (h *Handler) UnassignProxy(ctx context.Context, req *hermesv1.ProxyUnassignRequest) (*hermesv1.ProxyUnassignResponse, error) {
	if req.WaNumberId == "" {
		return nil, status.Error(codes.InvalidArgument, "wa_number_id is required")
	}

	if err := h.store.UnassignProxy(ctx, req.WaNumberId); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, status.Error(codes.NotFound, "WA number not found or not assigned to any proxy")
		}
		return nil, status.Errorf(codes.Internal, "unassigning proxy: %v", err)
	}

	return &hermesv1.ProxyUnassignResponse{}, nil
}

// ---------------------------------------------------------------------------
// RPC 8: GetProxyHealth
// ---------------------------------------------------------------------------

func (h *Handler) GetProxyHealth(ctx context.Context, req *hermesv1.ProxyGetHealthRequest) (*hermesv1.ProxyGetHealthResponse, error) {
	if req.Id == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	row, err := h.store.GetProxy(ctx, req.Id)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "getting proxy: %v", err)
	}
	if row == nil {
		return nil, status.Error(codes.NotFound, "proxy not found")
	}

	result := h.checker.Check(ctx, row.Host, row.Port, row.Type, row.Username, row.Password)

	newStatus := "active"
	if !result.Reachable {
		newStatus = "dead"
	}
	if row.Status == "flagged" {
		newStatus = "flagged"
	}

	if err := h.store.UpdateProxyHealth(ctx, req.Id, newStatus); err != nil {
		h.log.Error().Err(err).Str("proxy_id", req.Id).Msg("failed to update proxy health")
	}

	return &hermesv1.ProxyGetHealthResponse{
		Proxy:      rowToProto(row),
		LatencyMs:  result.LatencyMs,
		Reachable:  result.Reachable,
		CanRoute:   result.CanRoute,
		ExternalIp: result.ExternalIP,
		CheckedAt:  timestamppb.Now(),
	}, nil
}

// ---------------------------------------------------------------------------
// RPC 9: CheckAllProxies
// ---------------------------------------------------------------------------

func (h *Handler) CheckAllProxies(ctx context.Context, req *hermesv1.ProxyCheckAllRequest) (*hermesv1.ProxyCheckAllResponse, error) {
	if req.TenantId == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	rows, err := h.store.GetAllProxiesForTenant(ctx, req.TenantId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "listing proxies: %v", err)
	}

	var healthy, dead, flagged int32
	for _, row := range rows {
		result := h.checker.Check(ctx, row.Host, row.Port, row.Type, row.Username, row.Password)

		newStatus := row.Status
		if row.Status != "flagged" {
			if result.Reachable {
				newStatus = "active"
			} else {
				newStatus = "dead"
			}
		}

		if err := h.store.UpdateProxyHealth(ctx, row.ID, newStatus); err != nil {
			h.log.Error().Err(err).Str("proxy_id", row.ID).Msg("failed to update proxy health")
		}

		switch newStatus {
		case "active":
			healthy++
		case "dead":
			dead++
		case "flagged":
			flagged++
		}
	}

	return &hermesv1.ProxyCheckAllResponse{
		Total: int32(len(rows)), Healthy: healthy, Dead: dead, Flagged: flagged,
	}, nil
}

// ---------------------------------------------------------------------------
// RPC 10: FlagProxy
// ---------------------------------------------------------------------------

func (h *Handler) FlagProxy(ctx context.Context, req *hermesv1.ProxyFlagRequest) (*hermesv1.ProxyFlagResponse, error) {
	if req.Id == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	row, err := h.store.FlagProxy(ctx, req.Id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, status.Error(codes.NotFound, "proxy not found")
		}
		return nil, status.Errorf(codes.Internal, "flagging proxy: %v", err)
	}

	h.log.Info().Str("proxy_id", req.Id).Str("reason", req.Reason).Msg("proxy flagged")
	return &hermesv1.ProxyFlagResponse{Proxy: rowToProto(row)}, nil
}

// ---------------------------------------------------------------------------
// RPC 11: GetBestProxy
// ---------------------------------------------------------------------------

func (h *Handler) GetBestProxy(ctx context.Context, req *hermesv1.ProxyGetBestRequest) (*hermesv1.ProxyGetBestResponse, error) {
	if req.TenantId == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	row, poolExhausted, err := h.store.GetBestProxy(ctx, req.TenantId, proxyTypeToStr(req.Type))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "getting best proxy: %v", err)
	}

	resp := &hermesv1.ProxyGetBestResponse{PoolExhausted: poolExhausted}
	if row != nil {
		resp.Proxy = rowToProto(row)
	}
	return resp, nil
}
