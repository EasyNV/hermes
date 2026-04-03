package handler

import (
	"context"
	"errors"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/hermes-waba/hermes/internal/wa/sender"
	"github.com/hermes-waba/hermes/internal/wa/session"
	"github.com/rs/zerolog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Handler implements the HermesWaServer gRPC interface.
type Handler struct {
	hermesv1.UnimplementedHermesWaServer
	store   Store
	mgr     session.Manager
	sender  sender.Sender
	proxy   hermesv1.HermesProxyClient
	log     zerolog.Logger
	podID   string
	startAt *timestamppb.Timestamp
}

func NewHandler(store Store, mgr session.Manager, s sender.Sender, proxy hermesv1.HermesProxyClient, podID string, log zerolog.Logger) *Handler {
	return &Handler{
		store:   store,
		mgr:     mgr,
		sender:  s,
		proxy:   proxy,
		log:     log,
		podID:   podID,
		startAt: timestamppb.Now(),
	}
}

// ---------------------------------------------------------------------------
// Proto conversion helpers
// ---------------------------------------------------------------------------

func sessionInfoToProto(info *session.Info) *hermesv1.SessionInfo {
	si := &hermesv1.SessionInfo{
		WaNumberId:       info.WaNumberID,
		Jid:              info.JID,
		Phone:            info.Phone,
		State:            info.State,
		ProxyId:          info.ProxyID,
		MessagesSent:     info.MessagesSent,
		MessagesReceived: info.MessagesRecvd,
		MemoryBytes:      info.MemoryBytes,
	}
	if !info.ConnectedAt.IsZero() {
		si.ConnectedAt = timestamppb.New(info.ConnectedAt)
	}
	return si
}

func sessionStateToDBStatus(state hermesv1.SessionState) string {
	switch state {
	case hermesv1.SessionState_SESSION_STATE_CONNECTED:
		return "active"
	case hermesv1.SessionState_SESSION_STATE_BANNED:
		return "banned"
	case hermesv1.SessionState_SESSION_STATE_DISCONNECTED:
		return "disconnected"
	default:
		return ""
	}
}

func dbStatusToSessionState(s string) hermesv1.SessionState {
	switch s {
	case "active":
		return hermesv1.SessionState_SESSION_STATE_CONNECTED
	case "banned":
		return hermesv1.SessionState_SESSION_STATE_BANNED
	case "disconnected":
		return hermesv1.SessionState_SESSION_STATE_DISCONNECTED
	case "cooldown":
		return hermesv1.SessionState_SESSION_STATE_DISCONNECTED
	default:
		return hermesv1.SessionState_SESSION_STATE_UNSPECIFIED
	}
}

// ---------------------------------------------------------------------------
// RPC 1: ConnectSession
// ---------------------------------------------------------------------------

func (h *Handler) ConnectSession(ctx context.Context, req *hermesv1.ConnectSessionRequest) (*hermesv1.ConnectSessionResponse, error) {
	if req.WaNumberId == "" {
		return nil, status.Error(codes.InvalidArgument, "wa_number_id is required")
	}

	// Get wa_number from DB.
	row, err := h.store.GetWaNumber(ctx, req.WaNumberId)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, status.Error(codes.NotFound, "wa_number not found")
		}
		return nil, status.Errorf(codes.Internal, "getting wa_number: %v", err)
	}

	// Determine proxy.
	proxyID := req.ProxyId
	if proxyID == "" && row.ProxyID != nil {
		proxyID = *row.ProxyID
	}

	var proxyCfg *session.ProxyConfig
	if h.proxy != nil && proxyID != "" {
		resp, err := h.proxy.GetProxy(ctx, &hermesv1.ProxyGetRequest{Id: proxyID})
		if err == nil && resp.Proxy != nil {
			proxyCfg = &session.ProxyConfig{
				Host:     resp.Proxy.Host,
				Port:     resp.Proxy.Port,
				Username: resp.Proxy.Username,
				Password: resp.Proxy.Password,
				Type:     proxyTypeToStr(resp.Proxy.Type),
			}
		}
	}
	if h.proxy != nil && proxyCfg == nil {
		// Auto-assign best proxy.
		resp, err := h.proxy.GetBestProxy(ctx, &hermesv1.ProxyGetBestRequest{TenantId: row.TenantID})
		if err == nil && resp.Proxy != nil {
			proxyID = resp.Proxy.Id
			proxyCfg = &session.ProxyConfig{
				Host:     resp.Proxy.Host,
				Port:     resp.Proxy.Port,
				Username: resp.Proxy.Username,
				Password: resp.Proxy.Password,
				Type:     proxyTypeToStr(resp.Proxy.Type),
			}
		}
	}

	info, qrCode, err := h.mgr.Connect(ctx, req.WaNumberId, row.Phone, row.JID, proxyID, proxyCfg)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "connecting session: %v", err)
	}

	return &hermesv1.ConnectSessionResponse{
		Session: sessionInfoToProto(info),
		QrCode:  qrCode,
	}, nil
}

// ---------------------------------------------------------------------------
// RPC 2: DisconnectSession
// ---------------------------------------------------------------------------

func (h *Handler) DisconnectSession(ctx context.Context, req *hermesv1.DisconnectSessionRequest) (*hermesv1.DisconnectSessionResponse, error) {
	if req.WaNumberId == "" {
		return nil, status.Error(codes.InvalidArgument, "wa_number_id is required")
	}

	// Get session info before disconnect for response.
	info, ok := h.mgr.GetSession(req.WaNumberId)
	if !ok {
		return nil, status.Error(codes.NotFound, "session not found")
	}

	if err := h.mgr.Disconnect(req.WaNumberId); err != nil {
		return nil, status.Errorf(codes.Internal, "disconnecting session: %v", err)
	}

	info.State = hermesv1.SessionState_SESSION_STATE_DISCONNECTED
	return &hermesv1.DisconnectSessionResponse{
		Session: sessionInfoToProto(info),
	}, nil
}

// ---------------------------------------------------------------------------
// RPC 3: GetSessionStatus
// ---------------------------------------------------------------------------

func (h *Handler) GetSessionStatus(_ context.Context, req *hermesv1.GetSessionStatusRequest) (*hermesv1.GetSessionStatusResponse, error) {
	if req.WaNumberId == "" {
		return nil, status.Error(codes.InvalidArgument, "wa_number_id is required")
	}

	info, ok := h.mgr.GetSession(req.WaNumberId)
	if !ok {
		return nil, status.Error(codes.NotFound, "session not found on this pod")
	}

	return &hermesv1.GetSessionStatusResponse{
		Session: sessionInfoToProto(info),
	}, nil
}

// ---------------------------------------------------------------------------
// RPC 4: SendMessage
// ---------------------------------------------------------------------------

func (h *Handler) SendMessage(ctx context.Context, req *hermesv1.WaSendMessageRequest) (*hermesv1.WaSendMessageResponse, error) {
	if req.WaNumberId == "" {
		return nil, status.Error(codes.InvalidArgument, "wa_number_id is required")
	}
	if req.RecipientJid == "" {
		return nil, status.Error(codes.InvalidArgument, "recipient_jid is required")
	}

	client, ok := h.mgr.GetClient(req.WaNumberId)
	if !ok {
		return nil, status.Error(codes.NotFound, "session not connected")
	}

	msgID, sentAt, err := h.sender.SendMessage(ctx, client, req.RecipientJid, req.ContentType, req.Body, req.MediaUrl, req.Filename, req.Caption)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "sending message: %v", err)
	}

	// Increment sent count in DB.
	if err := h.store.IncrementSentCount(ctx, req.WaNumberId); err != nil {
		h.log.Error().Err(err).Str("wa_number_id", req.WaNumberId).Msg("failed to increment sent count")
	}

	return &hermesv1.WaSendMessageResponse{
		WaMessageId: msgID,
		SentAt:      timestamppb.New(sentAt),
	}, nil
}

// ---------------------------------------------------------------------------
// RPC 5: SendTypingIndicator
// ---------------------------------------------------------------------------

func (h *Handler) SendTypingIndicator(ctx context.Context, req *hermesv1.WaSendTypingIndicatorRequest) (*hermesv1.WaSendTypingIndicatorResponse, error) {
	if req.WaNumberId == "" {
		return nil, status.Error(codes.InvalidArgument, "wa_number_id is required")
	}
	if req.RecipientJid == "" {
		return nil, status.Error(codes.InvalidArgument, "recipient_jid is required")
	}

	client, ok := h.mgr.GetClient(req.WaNumberId)
	if !ok {
		return nil, status.Error(codes.NotFound, "session not connected")
	}

	if err := h.sender.SendTypingIndicator(ctx, client, req.RecipientJid, req.DurationMs); err != nil {
		return nil, status.Errorf(codes.Internal, "sending typing indicator: %v", err)
	}

	return &hermesv1.WaSendTypingIndicatorResponse{}, nil
}

// ---------------------------------------------------------------------------
// RPC 6: GetQRCode
// ---------------------------------------------------------------------------

func (h *Handler) GetQRCode(_ context.Context, req *hermesv1.WaGetQRCodeRequest) (*hermesv1.WaGetQRCodeResponse, error) {
	if req.WaNumberId == "" {
		return nil, status.Error(codes.InvalidArgument, "wa_number_id is required")
	}

	qr, expiresAt, isLinked, err := h.mgr.GetQRCode(req.WaNumberId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "getting QR code: %v", err)
	}

	resp := &hermesv1.WaGetQRCodeResponse{
		QrCode:   qr,
		IsLinked: isLinked,
	}
	if !expiresAt.IsZero() {
		resp.ExpiresAt = timestamppb.New(expiresAt)
	}
	return resp, nil
}

// ---------------------------------------------------------------------------
// RPC 7: ListSessions
// ---------------------------------------------------------------------------

func (h *Handler) ListSessions(_ context.Context, req *hermesv1.ListSessionsRequest) (*hermesv1.ListSessionsResponse, error) {
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

	allSessions := h.mgr.ListSessions()

	// Filter by state if specified.
	var filtered []*session.Info
	for _, s := range allSessions {
		if req.State != hermesv1.SessionState_SESSION_STATE_UNSPECIFIED && s.State != req.State {
			continue
		}
		filtered = append(filtered, s)
	}

	total := int64(len(filtered))
	totalPages := int32(0)
	if total > 0 {
		totalPages = int32((total + int64(pageSize) - 1) / int64(pageSize))
	}

	// Paginate.
	start := (page - 1) * pageSize
	if int64(start) >= total {
		start = 0
		filtered = nil
	} else {
		end := start + pageSize
		if int64(end) > total {
			end = int32(total)
		}
		filtered = filtered[start:end]
	}

	sessions := make([]*hermesv1.SessionInfo, 0, len(filtered))
	for _, info := range filtered {
		sessions = append(sessions, sessionInfoToProto(info))
	}

	return &hermesv1.ListSessionsResponse{
		Sessions: sessions,
		Pagination: &hermesv1.PageResponse{
			Total:      total,
			Page:       page,
			PageSize:   pageSize,
			TotalPages: totalPages,
		},
	}, nil
}

// ---------------------------------------------------------------------------
// RPC 8: GetPodHealth
// ---------------------------------------------------------------------------

func (h *Handler) GetPodHealth(_ context.Context, _ *hermesv1.GetPodHealthRequest) (*hermesv1.GetPodHealthResponse, error) {
	stats := h.mgr.GetPodStats()
	return &hermesv1.GetPodHealthResponse{
		PodId:             h.podID,
		TotalSessions:     stats.TotalSessions,
		ConnectedSessions: stats.ConnectedSessions,
		MemoryBytes:       stats.MemoryBytes,
		CpuPercent:        stats.CPUPercent,
		StartedAt:         h.startAt,
		NatsConsumerLag:   stats.NatsConsumerLag,
	}, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func proxyTypeToStr(t hermesv1.ProxyType) string {
	switch t {
	case hermesv1.ProxyType_PROXY_TYPE_SOCKS5:
		return "socks5"
	case hermesv1.ProxyType_PROXY_TYPE_HTTP:
		return "http"
	default:
		return "socks5"
	}
}
