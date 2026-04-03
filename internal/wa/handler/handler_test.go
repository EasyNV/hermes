package handler

import (
	"context"
	"fmt"
	"testing"
	"time"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/hermes-waba/hermes/internal/wa/sender"
	"github.com/hermes-waba/hermes/internal/wa/session"
	"github.com/rs/zerolog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ---------------------------------------------------------------------------
// Mock Store
// ---------------------------------------------------------------------------

type mockStore struct {
	getWaNumberFn              func(ctx context.Context, id string) (*WaNumberRow, error)
	listWaNumbersByPodFn       func(ctx context.Context, podID, statusFilter string, page, pageSize int32) ([]*WaNumberRow, int64, error)
	setWaNumberConnectedFn     func(ctx context.Context, id, jid, podID string) error
	setWaNumberDisconnectedFn  func(ctx context.Context, id string) error
	setWaNumberBannedFn        func(ctx context.Context, id string) error
	incrementSentCountFn       func(ctx context.Context, id string) error
	getTenantIDFn              func(ctx context.Context, waNumberID string) (string, error)
}

func (m *mockStore) GetWaNumber(ctx context.Context, id string) (*WaNumberRow, error) {
	if m.getWaNumberFn != nil {
		return m.getWaNumberFn(ctx, id)
	}
	return nil, fmt.Errorf("GetWaNumber not mocked")
}
func (m *mockStore) ListWaNumbersByPod(ctx context.Context, podID, statusFilter string, page, pageSize int32) ([]*WaNumberRow, int64, error) {
	if m.listWaNumbersByPodFn != nil {
		return m.listWaNumbersByPodFn(ctx, podID, statusFilter, page, pageSize)
	}
	return nil, 0, fmt.Errorf("ListWaNumbersByPod not mocked")
}
func (m *mockStore) SetWaNumberConnected(ctx context.Context, id, jid, podID string) error {
	if m.setWaNumberConnectedFn != nil {
		return m.setWaNumberConnectedFn(ctx, id, jid, podID)
	}
	return nil
}
func (m *mockStore) SetWaNumberDisconnected(ctx context.Context, id string) error {
	if m.setWaNumberDisconnectedFn != nil {
		return m.setWaNumberDisconnectedFn(ctx, id)
	}
	return nil
}
func (m *mockStore) SetWaNumberBanned(ctx context.Context, id string) error {
	if m.setWaNumberBannedFn != nil {
		return m.setWaNumberBannedFn(ctx, id)
	}
	return nil
}
func (m *mockStore) IncrementSentCount(ctx context.Context, id string) error {
	if m.incrementSentCountFn != nil {
		return m.incrementSentCountFn(ctx, id)
	}
	return nil
}
func (m *mockStore) GetTenantID(ctx context.Context, waNumberID string) (string, error) {
	if m.getTenantIDFn != nil {
		return m.getTenantIDFn(ctx, waNumberID)
	}
	return "tenant-1", nil
}

// ---------------------------------------------------------------------------
// Mock Session Manager
// ---------------------------------------------------------------------------

type mockManager struct {
	connectFn      func(ctx context.Context, waNumberID, phone, jid, proxyID string, proxy *session.ProxyConfig) (*session.Info, string, error)
	disconnectFn   func(waNumberID string) error
	getSessionFn   func(waNumberID string) (*session.Info, bool)
	getClientFn    func(waNumberID string) (sender.WaClient, bool)
	getQRCodeFn    func(waNumberID string) (string, time.Time, bool, error)
	listSessionsFn func() []*session.Info
	getPodStatsFn  func() session.PodStats
}

func (m *mockManager) Connect(ctx context.Context, waNumberID, phone, jid, proxyID string, proxy *session.ProxyConfig) (*session.Info, string, error) {
	if m.connectFn != nil {
		return m.connectFn(ctx, waNumberID, phone, jid, proxyID, proxy)
	}
	return nil, "", fmt.Errorf("Connect not mocked")
}
func (m *mockManager) Disconnect(waNumberID string) error {
	if m.disconnectFn != nil {
		return m.disconnectFn(waNumberID)
	}
	return nil
}
func (m *mockManager) GetSession(waNumberID string) (*session.Info, bool) {
	if m.getSessionFn != nil {
		return m.getSessionFn(waNumberID)
	}
	return nil, false
}
func (m *mockManager) GetClient(waNumberID string) (sender.WaClient, bool) {
	if m.getClientFn != nil {
		return m.getClientFn(waNumberID)
	}
	return nil, false
}
func (m *mockManager) GetQRCode(waNumberID string) (string, time.Time, bool, error) {
	if m.getQRCodeFn != nil {
		return m.getQRCodeFn(waNumberID)
	}
	return "", time.Time{}, false, fmt.Errorf("not mocked")
}
func (m *mockManager) ListSessions() []*session.Info {
	if m.listSessionsFn != nil {
		return m.listSessionsFn()
	}
	return nil
}
func (m *mockManager) GetPodStats() session.PodStats {
	if m.getPodStatsFn != nil {
		return m.getPodStatsFn()
	}
	return session.PodStats{}
}
func (m *mockManager) Close() {}

// ---------------------------------------------------------------------------
// Mock Sender
// ---------------------------------------------------------------------------

type mockSender struct {
	sendMessageFn        func(ctx context.Context, client sender.WaClient, recipientJID string, contentType hermesv1.ContentType, body, mediaURL, filename, caption string) (string, time.Time, error)
	sendTypingIndicatorFn func(ctx context.Context, client sender.WaClient, recipientJID string, durationMs int32) error
}

func (m *mockSender) SendMessage(ctx context.Context, client sender.WaClient, recipientJID string, contentType hermesv1.ContentType, body, mediaURL, filename, caption string) (string, time.Time, error) {
	if m.sendMessageFn != nil {
		return m.sendMessageFn(ctx, client, recipientJID, contentType, body, mediaURL, filename, caption)
	}
	return "msg-123", time.Now(), nil
}
func (m *mockSender) SendTypingIndicator(ctx context.Context, client sender.WaClient, recipientJID string, durationMs int32) error {
	if m.sendTypingIndicatorFn != nil {
		return m.sendTypingIndicatorFn(ctx, client, recipientJID, durationMs)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Mock WaClient
// ---------------------------------------------------------------------------

type mockWaClient struct{}

func (m *mockWaClient) SendMsg(_ context.Context, _ string, _ hermesv1.ContentType, _, _, _, _ string) (string, time.Time, error) {
	return "msg-123", time.Now(), nil
}
func (m *mockWaClient) SendPresence(_ string, _ bool) error { return nil }

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func newTestHandler(store Store, mgr session.Manager, snd sender.Sender) *Handler {
	return NewHandler(store, mgr, snd, nil, "hermes-wa-0", zerolog.Nop())
}

func makeWaNumberRow(id, tenantID, phone, jid, status, podID string) *WaNumberRow {
	return &WaNumberRow{
		ID: id, TenantID: tenantID, Phone: phone, JID: jid,
		Status: status, PodID: podID, HealthScore: 100,
		CreatedAt: time.Now(),
	}
}

// ---------------------------------------------------------------------------
// TestConnectSession
// ---------------------------------------------------------------------------

func TestConnectSession(t *testing.T) {
	connectedInfo := &session.Info{
		WaNumberID: "n1", JID: "628@s.whatsapp.net", Phone: "+628",
		State: hermesv1.SessionState_SESSION_STATE_CONNECTED, ConnectedAt: time.Now(),
	}
	qrInfo := &session.Info{
		WaNumberID: "n2", Phone: "+629",
		State: hermesv1.SessionState_SESSION_STATE_QR_PENDING,
	}

	tests := []struct {
		name     string
		req      *hermesv1.ConnectSessionRequest
		store    *mockStore
		mgr      *mockManager
		wantCode codes.Code
		wantQR   bool
	}{
		{
			name:     "missing wa_number_id",
			req:      &hermesv1.ConnectSessionRequest{},
			store:    &mockStore{},
			mgr:      &mockManager{},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "wa_number not found",
			req:  &hermesv1.ConnectSessionRequest{WaNumberId: "missing"},
			store: &mockStore{
				getWaNumberFn: func(_ context.Context, _ string) (*WaNumberRow, error) {
					return nil, ErrNotFound
				},
			},
			mgr:      &mockManager{},
			wantCode: codes.NotFound,
		},
		{
			name: "successful connect with existing session",
			req:  &hermesv1.ConnectSessionRequest{WaNumberId: "n1"},
			store: &mockStore{
				getWaNumberFn: func(_ context.Context, _ string) (*WaNumberRow, error) {
					return makeWaNumberRow("n1", "t1", "+628", "628@s.whatsapp.net", "disconnected", ""), nil
				},
			},
			mgr: &mockManager{
				connectFn: func(_ context.Context, _, _, _, _ string, _ *session.ProxyConfig) (*session.Info, string, error) {
					return connectedInfo, "", nil
				},
			},
		},
		{
			name: "connect returns QR code",
			req:  &hermesv1.ConnectSessionRequest{WaNumberId: "n2"},
			store: &mockStore{
				getWaNumberFn: func(_ context.Context, _ string) (*WaNumberRow, error) {
					return makeWaNumberRow("n2", "t1", "+629", "", "disconnected", ""), nil
				},
			},
			mgr: &mockManager{
				connectFn: func(_ context.Context, _, _, _, _ string, _ *session.ProxyConfig) (*session.Info, string, error) {
					return qrInfo, "qr-data-here", nil
				},
			},
			wantQR: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHandler(tt.store, tt.mgr, &mockSender{})
			resp, err := h.ConnectSession(context.Background(), tt.req)

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
			if resp.Session == nil {
				t.Fatal("expected session in response")
			}
			if tt.wantQR && resp.QrCode == "" {
				t.Error("expected QR code in response")
			}
			if !tt.wantQR && resp.QrCode != "" {
				t.Errorf("unexpected QR code: %q", resp.QrCode)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestDisconnectSession
// ---------------------------------------------------------------------------

func TestDisconnectSession(t *testing.T) {
	tests := []struct {
		name     string
		req      *hermesv1.DisconnectSessionRequest
		mgr      *mockManager
		wantCode codes.Code
	}{
		{
			name:     "missing wa_number_id",
			req:      &hermesv1.DisconnectSessionRequest{},
			mgr:      &mockManager{},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "session not found",
			req:  &hermesv1.DisconnectSessionRequest{WaNumberId: "missing"},
			mgr: &mockManager{
				getSessionFn: func(_ string) (*session.Info, bool) { return nil, false },
			},
			wantCode: codes.NotFound,
		},
		{
			name: "successful disconnect",
			req:  &hermesv1.DisconnectSessionRequest{WaNumberId: "n1"},
			mgr: &mockManager{
				getSessionFn: func(id string) (*session.Info, bool) {
					return &session.Info{WaNumberID: id, State: hermesv1.SessionState_SESSION_STATE_CONNECTED}, true
				},
				disconnectFn: func(_ string) error { return nil },
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHandler(&mockStore{}, tt.mgr, &mockSender{})
			resp, err := h.DisconnectSession(context.Background(), tt.req)

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
			if resp.Session == nil {
				t.Fatal("expected session in response")
			}
			if resp.Session.State != hermesv1.SessionState_SESSION_STATE_DISCONNECTED {
				t.Errorf("expected DISCONNECTED state, got %v", resp.Session.State)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestGetSessionStatus
// ---------------------------------------------------------------------------

func TestGetSessionStatus(t *testing.T) {
	tests := []struct {
		name     string
		req      *hermesv1.GetSessionStatusRequest
		mgr      *mockManager
		wantCode codes.Code
		wantState hermesv1.SessionState
	}{
		{
			name:     "missing wa_number_id",
			req:      &hermesv1.GetSessionStatusRequest{},
			mgr:      &mockManager{},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "session not found",
			req:  &hermesv1.GetSessionStatusRequest{WaNumberId: "missing"},
			mgr: &mockManager{
				getSessionFn: func(_ string) (*session.Info, bool) { return nil, false },
			},
			wantCode: codes.NotFound,
		},
		{
			name: "returns connected session",
			req:  &hermesv1.GetSessionStatusRequest{WaNumberId: "n1"},
			mgr: &mockManager{
				getSessionFn: func(_ string) (*session.Info, bool) {
					return &session.Info{WaNumberID: "n1", State: hermesv1.SessionState_SESSION_STATE_CONNECTED}, true
				},
			},
			wantState: hermesv1.SessionState_SESSION_STATE_CONNECTED,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHandler(&mockStore{}, tt.mgr, &mockSender{})
			resp, err := h.GetSessionStatus(context.Background(), tt.req)

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
			if resp.Session.State != tt.wantState {
				t.Errorf("expected state %v, got %v", tt.wantState, resp.Session.State)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestSendMessage
// ---------------------------------------------------------------------------

func TestSendMessage(t *testing.T) {
	tests := []struct {
		name     string
		req      *hermesv1.WaSendMessageRequest
		mgr      *mockManager
		snd      *mockSender
		wantCode codes.Code
		wantMsgID string
	}{
		{
			name:     "missing wa_number_id",
			req:      &hermesv1.WaSendMessageRequest{},
			mgr:      &mockManager{},
			snd:      &mockSender{},
			wantCode: codes.InvalidArgument,
		},
		{
			name:     "missing recipient_jid",
			req:      &hermesv1.WaSendMessageRequest{WaNumberId: "n1"},
			mgr:      &mockManager{},
			snd:      &mockSender{},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "session not connected",
			req:  &hermesv1.WaSendMessageRequest{WaNumberId: "n1", RecipientJid: "628@s.whatsapp.net"},
			mgr: &mockManager{
				getClientFn: func(_ string) (sender.WaClient, bool) { return nil, false },
			},
			snd:      &mockSender{},
			wantCode: codes.NotFound,
		},
		{
			name: "successful send",
			req: &hermesv1.WaSendMessageRequest{
				WaNumberId: "n1", RecipientJid: "628@s.whatsapp.net",
				ContentType: hermesv1.ContentType_CONTENT_TYPE_TEXT, Body: "Hello",
			},
			mgr: &mockManager{
				getClientFn: func(_ string) (sender.WaClient, bool) {
					return &mockWaClient{}, true
				},
			},
			snd: &mockSender{
				sendMessageFn: func(_ context.Context, _ sender.WaClient, _ string, _ hermesv1.ContentType, _, _, _, _ string) (string, time.Time, error) {
					return "wa-msg-456", time.Now(), nil
				},
			},
			wantMsgID: "wa-msg-456",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHandler(&mockStore{}, tt.mgr, tt.snd)
			resp, err := h.SendMessage(context.Background(), tt.req)

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
			if resp.WaMessageId != tt.wantMsgID {
				t.Errorf("message ID: got %q, want %q", resp.WaMessageId, tt.wantMsgID)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestSendTypingIndicator
// ---------------------------------------------------------------------------

func TestSendTypingIndicator(t *testing.T) {
	tests := []struct {
		name     string
		req      *hermesv1.WaSendTypingIndicatorRequest
		mgr      *mockManager
		snd      *mockSender
		wantCode codes.Code
	}{
		{
			name:     "missing wa_number_id",
			req:      &hermesv1.WaSendTypingIndicatorRequest{},
			mgr:      &mockManager{},
			snd:      &mockSender{},
			wantCode: codes.InvalidArgument,
		},
		{
			name:     "missing recipient_jid",
			req:      &hermesv1.WaSendTypingIndicatorRequest{WaNumberId: "n1"},
			mgr:      &mockManager{},
			snd:      &mockSender{},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "session not connected",
			req:  &hermesv1.WaSendTypingIndicatorRequest{WaNumberId: "n1", RecipientJid: "628@s.whatsapp.net"},
			mgr: &mockManager{
				getClientFn: func(_ string) (sender.WaClient, bool) { return nil, false },
			},
			snd:      &mockSender{},
			wantCode: codes.NotFound,
		},
		{
			name: "successful typing indicator",
			req:  &hermesv1.WaSendTypingIndicatorRequest{WaNumberId: "n1", RecipientJid: "628@s.whatsapp.net", DurationMs: 2000},
			mgr: &mockManager{
				getClientFn: func(_ string) (sender.WaClient, bool) { return &mockWaClient{}, true },
			},
			snd: &mockSender{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHandler(&mockStore{}, tt.mgr, tt.snd)
			_, err := h.SendTypingIndicator(context.Background(), tt.req)

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
		})
	}
}

// ---------------------------------------------------------------------------
// TestGetQRCode
// ---------------------------------------------------------------------------

func TestGetQRCode(t *testing.T) {
	tests := []struct {
		name       string
		req        *hermesv1.WaGetQRCodeRequest
		mgr        *mockManager
		wantCode   codes.Code
		wantLinked bool
		wantQR     string
	}{
		{
			name:     "missing wa_number_id",
			req:      &hermesv1.WaGetQRCodeRequest{},
			mgr:      &mockManager{},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "session already linked",
			req:  &hermesv1.WaGetQRCodeRequest{WaNumberId: "n1"},
			mgr: &mockManager{
				getQRCodeFn: func(_ string) (string, time.Time, bool, error) {
					return "", time.Time{}, true, nil
				},
			},
			wantLinked: true,
		},
		{
			name: "QR code available",
			req:  &hermesv1.WaGetQRCodeRequest{WaNumberId: "n2"},
			mgr: &mockManager{
				getQRCodeFn: func(_ string) (string, time.Time, bool, error) {
					return "qr-data-123", time.Now().Add(60 * time.Second), false, nil
				},
			},
			wantQR: "qr-data-123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHandler(&mockStore{}, tt.mgr, &mockSender{})
			resp, err := h.GetQRCode(context.Background(), tt.req)

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
			if resp.IsLinked != tt.wantLinked {
				t.Errorf("IsLinked: got %v, want %v", resp.IsLinked, tt.wantLinked)
			}
			if resp.QrCode != tt.wantQR {
				t.Errorf("QRCode: got %q, want %q", resp.QrCode, tt.wantQR)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestListSessions
// ---------------------------------------------------------------------------

func TestListSessions(t *testing.T) {
	allSessions := []*session.Info{
		{WaNumberID: "n1", State: hermesv1.SessionState_SESSION_STATE_CONNECTED},
		{WaNumberID: "n2", State: hermesv1.SessionState_SESSION_STATE_DISCONNECTED},
		{WaNumberID: "n3", State: hermesv1.SessionState_SESSION_STATE_QR_PENDING},
	}

	tests := []struct {
		name      string
		req       *hermesv1.ListSessionsRequest
		mgr       *mockManager
		wantCount int
		wantTotal int64
	}{
		{
			name: "list all sessions",
			req:  &hermesv1.ListSessionsRequest{},
			mgr: &mockManager{
				listSessionsFn: func() []*session.Info { return allSessions },
			},
			wantCount: 3,
			wantTotal: 3,
		},
		{
			name: "filter by connected state",
			req:  &hermesv1.ListSessionsRequest{State: hermesv1.SessionState_SESSION_STATE_CONNECTED},
			mgr: &mockManager{
				listSessionsFn: func() []*session.Info { return allSessions },
			},
			wantCount: 1,
			wantTotal: 1,
		},
		{
			name: "empty list",
			req:  &hermesv1.ListSessionsRequest{},
			mgr: &mockManager{
				listSessionsFn: func() []*session.Info { return nil },
			},
			wantCount: 0,
			wantTotal: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHandler(&mockStore{}, tt.mgr, &mockSender{})
			resp, err := h.ListSessions(context.Background(), tt.req)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := len(resp.Sessions); got != tt.wantCount {
				t.Errorf("session count: got %d, want %d", got, tt.wantCount)
			}
			if resp.Pagination.Total != tt.wantTotal {
				t.Errorf("total: got %d, want %d", resp.Pagination.Total, tt.wantTotal)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestGetPodHealth
// ---------------------------------------------------------------------------

func TestGetPodHealth(t *testing.T) {
	h := newTestHandler(&mockStore{}, &mockManager{
		getPodStatsFn: func() session.PodStats {
			return session.PodStats{TotalSessions: 5, ConnectedSessions: 3, MemoryBytes: 1024 * 1024}
		},
	}, &mockSender{})

	resp, err := h.GetPodHealth(context.Background(), &hermesv1.GetPodHealthRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.PodId != "hermes-wa-0" {
		t.Errorf("pod_id: got %q, want %q", resp.PodId, "hermes-wa-0")
	}
	if resp.TotalSessions != 5 {
		t.Errorf("total_sessions: got %d, want 5", resp.TotalSessions)
	}
	if resp.ConnectedSessions != 3 {
		t.Errorf("connected_sessions: got %d, want 3", resp.ConnectedSessions)
	}
	if resp.MemoryBytes != 1024*1024 {
		t.Errorf("memory_bytes: got %d, want %d", resp.MemoryBytes, 1024*1024)
	}
	if resp.StartedAt == nil {
		t.Error("expected started_at timestamp")
	}
}
