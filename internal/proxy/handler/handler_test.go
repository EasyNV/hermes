package handler

import (
	"context"
	"fmt"
	"testing"
	"time"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/hermes-waba/hermes/internal/proxy/health"
	"github.com/rs/zerolog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ---------------------------------------------------------------------------
// Mock helpers
// ---------------------------------------------------------------------------

type mockStore struct {
	createProxyFn           func(ctx context.Context, tenantID, host string, port int32, username, password, proxyType string) (*ProxyRow, error)
	proxyExistsByHostPortFn func(ctx context.Context, tenantID, host string, port int32) (bool, error)
	getProxyFn              func(ctx context.Context, id string) (*ProxyRow, error)
	listProxiesFn           func(ctx context.Context, tenantID, status, proxyType string, page, pageSize int32) ([]*ProxyRow, int64, error)
	updateProxyFn           func(ctx context.Context, id, host string, port int32, username, password, proxyType, status string) (*ProxyRow, error)
	deleteProxyFn           func(ctx context.Context, id string, force bool) (int32, error)
	getAssignedNumbersFn    func(ctx context.Context, proxyID string) ([]*WaNumberRow, error)
	assignProxyFn           func(ctx context.Context, waNumberID, proxyID string) (*ProxyRow, error)
	assignProxyTargetFn     func(ctx context.Context, kind ProxyTargetKind, targetID, proxyID string) (*ProxyRow, error)
	unassignProxyFn         func(ctx context.Context, waNumberID string) error
	unassignProxyTargetFn   func(ctx context.Context, kind ProxyTargetKind, targetID string) error
	getBestProxyFn          func(ctx context.Context, tenantID, proxyType string) (*ProxyRow, bool, error)
	flagProxyFn             func(ctx context.Context, id string) (*ProxyRow, error)
	incrementBanCountFn     func(ctx context.Context, proxyID string) (int32, error)
	updateProxyHealthFn     func(ctx context.Context, id, status string) error
	getAllProxiesForTenantFn func(ctx context.Context, tenantID string) ([]*ProxyRow, error)
}

func (m *mockStore) CreateProxy(ctx context.Context, tenantID, host string, port int32, username, password, proxyType string) (*ProxyRow, error) {
	if m.createProxyFn != nil {
		return m.createProxyFn(ctx, tenantID, host, port, username, password, proxyType)
	}
	return nil, fmt.Errorf("CreateProxy not mocked")
}
func (m *mockStore) ProxyExistsByHostPort(ctx context.Context, tenantID, host string, port int32) (bool, error) {
	if m.proxyExistsByHostPortFn != nil {
		return m.proxyExistsByHostPortFn(ctx, tenantID, host, port)
	}
	return false, fmt.Errorf("ProxyExistsByHostPort not mocked")
}
func (m *mockStore) GetProxy(ctx context.Context, id string) (*ProxyRow, error) {
	if m.getProxyFn != nil {
		return m.getProxyFn(ctx, id)
	}
	return nil, fmt.Errorf("GetProxy not mocked")
}
func (m *mockStore) ListProxies(ctx context.Context, tenantID, st, proxyType string, page, pageSize int32) ([]*ProxyRow, int64, error) {
	if m.listProxiesFn != nil {
		return m.listProxiesFn(ctx, tenantID, st, proxyType, page, pageSize)
	}
	return nil, 0, fmt.Errorf("ListProxies not mocked")
}
func (m *mockStore) UpdateProxy(ctx context.Context, id, host string, port int32, username, password, proxyType, st string) (*ProxyRow, error) {
	if m.updateProxyFn != nil {
		return m.updateProxyFn(ctx, id, host, port, username, password, proxyType, st)
	}
	return nil, fmt.Errorf("UpdateProxy not mocked")
}
func (m *mockStore) DeleteProxy(ctx context.Context, id string, force bool) (int32, error) {
	if m.deleteProxyFn != nil {
		return m.deleteProxyFn(ctx, id, force)
	}
	return 0, fmt.Errorf("DeleteProxy not mocked")
}
func (m *mockStore) GetAssignedNumbers(ctx context.Context, proxyID string) ([]*WaNumberRow, error) {
	if m.getAssignedNumbersFn != nil {
		return m.getAssignedNumbersFn(ctx, proxyID)
	}
	return nil, fmt.Errorf("GetAssignedNumbers not mocked")
}
func (m *mockStore) AssignProxy(ctx context.Context, waNumberID, proxyID string) (*ProxyRow, error) {
	if m.assignProxyFn != nil {
		return m.assignProxyFn(ctx, waNumberID, proxyID)
	}
	return nil, fmt.Errorf("AssignProxy not mocked")
}
func (m *mockStore) AssignProxyTarget(ctx context.Context, kind ProxyTargetKind, targetID, proxyID string) (*ProxyRow, error) {
	if m.assignProxyTargetFn != nil {
		return m.assignProxyTargetFn(ctx, kind, targetID, proxyID)
	}
	// Fallback: WA targets delegate to the legacy mock so existing tests pass.
	if kind == TargetWA && m.assignProxyFn != nil {
		return m.assignProxyFn(ctx, targetID, proxyID)
	}
	return nil, fmt.Errorf("AssignProxyTarget not mocked")
}
func (m *mockStore) UnassignProxy(ctx context.Context, waNumberID string) error {
	if m.unassignProxyFn != nil {
		return m.unassignProxyFn(ctx, waNumberID)
	}
	return fmt.Errorf("UnassignProxy not mocked")
}
func (m *mockStore) UnassignProxyTarget(ctx context.Context, kind ProxyTargetKind, targetID string) error {
	if m.unassignProxyTargetFn != nil {
		return m.unassignProxyTargetFn(ctx, kind, targetID)
	}
	if kind == TargetWA && m.unassignProxyFn != nil {
		return m.unassignProxyFn(ctx, targetID)
	}
	return fmt.Errorf("UnassignProxyTarget not mocked")
}
func (m *mockStore) GetBestProxy(ctx context.Context, tenantID, proxyType string) (*ProxyRow, bool, error) {
	if m.getBestProxyFn != nil {
		return m.getBestProxyFn(ctx, tenantID, proxyType)
	}
	return nil, false, fmt.Errorf("GetBestProxy not mocked")
}
func (m *mockStore) FlagProxy(ctx context.Context, id string) (*ProxyRow, error) {
	if m.flagProxyFn != nil {
		return m.flagProxyFn(ctx, id)
	}
	return nil, fmt.Errorf("FlagProxy not mocked")
}
func (m *mockStore) IncrementBanCount(ctx context.Context, proxyID string) (int32, error) {
	if m.incrementBanCountFn != nil {
		return m.incrementBanCountFn(ctx, proxyID)
	}
	return 0, fmt.Errorf("IncrementBanCount not mocked")
}
func (m *mockStore) UpdateProxyHealth(ctx context.Context, id, st string) error {
	if m.updateProxyHealthFn != nil {
		return m.updateProxyHealthFn(ctx, id, st)
	}
	return fmt.Errorf("UpdateProxyHealth not mocked")
}
func (m *mockStore) GetAllProxiesForTenant(ctx context.Context, tenantID string) ([]*ProxyRow, error) {
	if m.getAllProxiesForTenantFn != nil {
		return m.getAllProxiesForTenantFn(ctx, tenantID)
	}
	return nil, fmt.Errorf("GetAllProxiesForTenant not mocked")
}

type mockChecker struct {
	result *health.Result
}

func (m *mockChecker) Check(_ context.Context, _ string, _ int32, _, _, _ string) *health.Result {
	if m.result != nil {
		return m.result
	}
	return &health.Result{}
}

func newTestHandler(s Store, c health.Checker) *Handler {
	return NewHandler(s, c, zerolog.Nop())
}

func makeRow(id, tenantID, host string, port int32, typ, st string, banCount, assignedCount int32) *ProxyRow {
	return &ProxyRow{
		ID: id, TenantID: tenantID, Host: host, Port: port,
		Type: typ, Status: st, BanCount: banCount, AssignedCount: assignedCount,
		CreatedAt: time.Now(),
	}
}

// ---------------------------------------------------------------------------
// TestAddProxies
// ---------------------------------------------------------------------------

func TestAddProxies(t *testing.T) {
	tests := []struct {
		name          string
		req           *hermesv1.ProxyAddRequest
		store         *mockStore
		wantAdded     int
		wantSkipped   int32
		wantCode      codes.Code
	}{
		{
			name:     "missing tenant_id",
			req:      &hermesv1.ProxyAddRequest{},
			store:    &mockStore{},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "empty proxy list",
			req:  &hermesv1.ProxyAddRequest{TenantId: "t1"},
			store: &mockStore{},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "all new proxies added",
			req: &hermesv1.ProxyAddRequest{
				TenantId: "t1",
				Proxies: []*hermesv1.ProxyAddInput{
					{Host: "1.1.1.1", Port: 1080, Type: hermesv1.ProxyType_PROXY_TYPE_SOCKS5},
					{Host: "2.2.2.2", Port: 8080, Type: hermesv1.ProxyType_PROXY_TYPE_HTTP},
				},
			},
			store: &mockStore{
				proxyExistsByHostPortFn: func(_ context.Context, _, _ string, _ int32) (bool, error) {
					return false, nil
				},
				createProxyFn: func(_ context.Context, tenantID, host string, port int32, _, _, pt string) (*ProxyRow, error) {
					return makeRow("new-id", tenantID, host, port, pt, "active", 0, 0), nil
				},
			},
			wantAdded:   2,
			wantSkipped: 0,
		},
		{
			name: "dedup skips existing proxy",
			req: &hermesv1.ProxyAddRequest{
				TenantId: "t1",
				Proxies: []*hermesv1.ProxyAddInput{
					{Host: "1.1.1.1", Port: 1080, Type: hermesv1.ProxyType_PROXY_TYPE_SOCKS5},
					{Host: "dup.host", Port: 9999, Type: hermesv1.ProxyType_PROXY_TYPE_SOCKS5},
					{Host: "3.3.3.3", Port: 1080, Type: hermesv1.ProxyType_PROXY_TYPE_HTTP},
				},
			},
			store: &mockStore{
				proxyExistsByHostPortFn: func(_ context.Context, _ string, host string, port int32) (bool, error) {
					return host == "dup.host" && port == 9999, nil
				},
				createProxyFn: func(_ context.Context, tenantID, host string, port int32, _, _, pt string) (*ProxyRow, error) {
					return makeRow("new-id", tenantID, host, port, pt, "active", 0, 0), nil
				},
			},
			wantAdded:   2,
			wantSkipped: 1,
		},
		{
			name: "invalid input skipped",
			req: &hermesv1.ProxyAddRequest{
				TenantId: "t1",
				Proxies: []*hermesv1.ProxyAddInput{
					{Host: "", Port: 0}, // invalid
					{Host: "ok.host", Port: 1080, Type: hermesv1.ProxyType_PROXY_TYPE_SOCKS5},
				},
			},
			store: &mockStore{
				proxyExistsByHostPortFn: func(_ context.Context, _, _ string, _ int32) (bool, error) {
					return false, nil
				},
				createProxyFn: func(_ context.Context, tenantID, host string, port int32, _, _, pt string) (*ProxyRow, error) {
					return makeRow("new-id", tenantID, host, port, pt, "active", 0, 0), nil
				},
			},
			wantAdded:   1,
			wantSkipped: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHandler(tt.store, &mockChecker{})
			resp, err := h.AddProxies(context.Background(), tt.req)

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
			if got := len(resp.Proxies); got != tt.wantAdded {
				t.Errorf("added count: got %d, want %d", got, tt.wantAdded)
			}
			if resp.SkippedCount != tt.wantSkipped {
				t.Errorf("skipped count: got %d, want %d", resp.SkippedCount, tt.wantSkipped)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestGetBestProxy
// ---------------------------------------------------------------------------

func TestGetBestProxy(t *testing.T) {
	cleanProxy := makeRow("p-clean", "t1", "1.1.1.1", 1080, "socks5", "active", 0, 1)
	loadedProxy := makeRow("p-loaded", "t1", "2.2.2.2", 1080, "socks5", "active", 0, 5)
	bannedProxy := makeRow("p-banned", "t1", "3.3.3.3", 1080, "socks5", "active", 3, 0)

	tests := []struct {
		name           string
		req            *hermesv1.ProxyGetBestRequest
		store          *mockStore
		wantProxyID    string
		wantExhausted  bool
		wantCode       codes.Code
	}{
		{
			name:     "missing tenant_id",
			req:      &hermesv1.ProxyGetBestRequest{},
			store:    &mockStore{},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "returns cleanest proxy (lowest ban count, then lowest assigned count)",
			req:  &hermesv1.ProxyGetBestRequest{TenantId: "t1"},
			store: &mockStore{
				getBestProxyFn: func(_ context.Context, _, _ string) (*ProxyRow, bool, error) {
					// Simulate the DB sorting: cleanProxy has ban_count=0, assigned_count=1 (best)
					// loadedProxy: ban_count=0, assigned_count=5; bannedProxy: ban_count=3
					_ = loadedProxy
					_ = bannedProxy
					return cleanProxy, false, nil
				},
			},
			wantProxyID: "p-clean",
		},
		{
			name: "pool exhausted when all at capacity",
			req:  &hermesv1.ProxyGetBestRequest{TenantId: "t1"},
			store: &mockStore{
				getBestProxyFn: func(_ context.Context, _, _ string) (*ProxyRow, bool, error) {
					return nil, true, nil
				},
			},
			wantExhausted: true,
		},
		{
			name: "no active proxies returns empty response",
			req:  &hermesv1.ProxyGetBestRequest{TenantId: "t1"},
			store: &mockStore{
				getBestProxyFn: func(_ context.Context, _, _ string) (*ProxyRow, bool, error) {
					return nil, false, nil
				},
			},
			wantProxyID: "",
		},
		{
			name: "type filter passed through",
			req:  &hermesv1.ProxyGetBestRequest{TenantId: "t1", Type: hermesv1.ProxyType_PROXY_TYPE_HTTP},
			store: &mockStore{
				getBestProxyFn: func(_ context.Context, _, proxyType string) (*ProxyRow, bool, error) {
					if proxyType != "http" {
						t.Errorf("expected type filter 'http', got %q", proxyType)
					}
					return makeRow("p-http", "t1", "4.4.4.4", 8080, "http", "active", 0, 0), false, nil
				},
			},
			wantProxyID: "p-http",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHandler(tt.store, &mockChecker{})
			resp, err := h.GetBestProxy(context.Background(), tt.req)

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

			if tt.wantExhausted && !resp.PoolExhausted {
				t.Error("expected PoolExhausted=true")
			}
			if !tt.wantExhausted && resp.PoolExhausted {
				t.Error("expected PoolExhausted=false")
			}

			gotID := ""
			if resp.Proxy != nil {
				gotID = resp.Proxy.Id
			}
			if gotID != tt.wantProxyID {
				t.Errorf("proxy ID: got %q, want %q", gotID, tt.wantProxyID)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestFlagProxy
// ---------------------------------------------------------------------------

func TestFlagProxy(t *testing.T) {
	flaggedRow := makeRow("p1", "t1", "1.1.1.1", 1080, "socks5", "flagged", 5, 2)

	tests := []struct {
		name     string
		req      *hermesv1.ProxyFlagRequest
		store    *mockStore
		wantCode codes.Code
		wantID   string
	}{
		{
			name:     "missing id",
			req:      &hermesv1.ProxyFlagRequest{},
			store:    &mockStore{},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "not found",
			req:  &hermesv1.ProxyFlagRequest{Id: "missing", Reason: "test"},
			store: &mockStore{
				flagProxyFn: func(_ context.Context, _ string) (*ProxyRow, error) {
					return nil, ErrNotFound
				},
			},
			wantCode: codes.NotFound,
		},
		{
			name: "success flags proxy",
			req:  &hermesv1.ProxyFlagRequest{Id: "p1", Reason: "high_ban_rate"},
			store: &mockStore{
				flagProxyFn: func(_ context.Context, id string) (*ProxyRow, error) {
					if id != "p1" {
						t.Errorf("unexpected id %q", id)
					}
					return flaggedRow, nil
				},
			},
			wantID: "p1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHandler(tt.store, &mockChecker{})
			resp, err := h.FlagProxy(context.Background(), tt.req)

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
			if resp.Proxy.Id != tt.wantID {
				t.Errorf("proxy ID: got %q, want %q", resp.Proxy.Id, tt.wantID)
			}
			if resp.Proxy.Status != hermesv1.ProxyStatus_PROXY_STATUS_FLAGGED {
				t.Errorf("expected FLAGGED status, got %v", resp.Proxy.Status)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestDeleteProxy
// ---------------------------------------------------------------------------

func TestDeleteProxy(t *testing.T) {
	tests := []struct {
		name            string
		req             *hermesv1.ProxyDeleteRequest
		store           *mockStore
		wantCode        codes.Code
		wantUnassigned  int32
	}{
		{
			name:     "missing id",
			req:      &hermesv1.ProxyDeleteRequest{},
			store:    &mockStore{},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "not found",
			req:  &hermesv1.ProxyDeleteRequest{Id: "missing"},
			store: &mockStore{
				deleteProxyFn: func(_ context.Context, _ string, _ bool) (int32, error) {
					return 0, ErrNotFound
				},
			},
			wantCode: codes.NotFound,
		},
		{
			name: "has assignments without force",
			req:  &hermesv1.ProxyDeleteRequest{Id: "p1", Force: false},
			store: &mockStore{
				deleteProxyFn: func(_ context.Context, _ string, _ bool) (int32, error) {
					return 0, ErrHasAssignments
				},
			},
			wantCode: codes.FailedPrecondition,
		},
		{
			name: "force delete unassigns numbers",
			req:  &hermesv1.ProxyDeleteRequest{Id: "p1", Force: true},
			store: &mockStore{
				deleteProxyFn: func(_ context.Context, _ string, force bool) (int32, error) {
					if !force {
						t.Error("expected force=true")
					}
					return 3, nil
				},
			},
			wantUnassigned: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHandler(tt.store, &mockChecker{})
			resp, err := h.DeleteProxy(context.Background(), tt.req)

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
			if resp.UnassignedCount != tt.wantUnassigned {
				t.Errorf("unassigned count: got %d, want %d", resp.UnassignedCount, tt.wantUnassigned)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestCheckAllProxies
// ---------------------------------------------------------------------------

func TestCheckAllProxies(t *testing.T) {
	tests := []struct {
		name        string
		req         *hermesv1.ProxyCheckAllRequest
		store       *mockStore
		checker     *mockChecker
		wantCode    codes.Code
		wantTotal   int32
		wantHealthy int32
		wantDead    int32
	}{
		{
			name:     "missing tenant_id",
			req:      &hermesv1.ProxyCheckAllRequest{},
			store:    &mockStore{},
			checker:  &mockChecker{},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "mixed health results",
			req:  &hermesv1.ProxyCheckAllRequest{TenantId: "t1"},
			store: &mockStore{
				getAllProxiesForTenantFn: func(_ context.Context, _ string) ([]*ProxyRow, error) {
					return []*ProxyRow{
						makeRow("p1", "t1", "1.1.1.1", 1080, "socks5", "active", 0, 0),
						makeRow("p2", "t1", "2.2.2.2", 1080, "socks5", "active", 0, 0),
						makeRow("p3", "t1", "3.3.3.3", 1080, "socks5", "flagged", 5, 0),
					}, nil
				},
				updateProxyHealthFn: func(_ context.Context, _, _ string) error { return nil },
			},
			checker:     &mockChecker{result: &health.Result{Reachable: true}},
			wantTotal:   3,
			wantHealthy: 2,
			wantDead:    0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHandler(tt.store, tt.checker)
			resp, err := h.CheckAllProxies(context.Background(), tt.req)

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
			if resp.Total != tt.wantTotal {
				t.Errorf("total: got %d, want %d", resp.Total, tt.wantTotal)
			}
			if resp.Healthy != tt.wantHealthy {
				t.Errorf("healthy: got %d, want %d", resp.Healthy, tt.wantHealthy)
			}
		})
	}
}
