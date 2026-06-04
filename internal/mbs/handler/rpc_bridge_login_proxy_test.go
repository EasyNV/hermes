package handler

import (
	"context"
	"errors"
	"testing"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeLoginProxyClient implements session.ProxyClient for the login-leg
// helper tests. GetBestProxy + AssignProxy + GetProxy are wired via funcs.
type fakeLoginProxyClient struct {
	getProxy     func(*hermesv1.ProxyGetRequest) (*hermesv1.ProxyGetResponse, error)
	getBestProxy func(*hermesv1.ProxyGetBestRequest) (*hermesv1.ProxyGetBestResponse, error)
	assignProxy  func(*hermesv1.ProxyAssignRequest) (*hermesv1.ProxyAssignResponse, error)
	assignCalls  []*hermesv1.ProxyAssignRequest
}

func (f *fakeLoginProxyClient) GetProxy(_ context.Context, in *hermesv1.ProxyGetRequest, _ ...grpc.CallOption) (*hermesv1.ProxyGetResponse, error) {
	if f.getProxy != nil {
		return f.getProxy(in)
	}
	return nil, errors.New("GetProxy not wired in this test")
}
func (f *fakeLoginProxyClient) GetBestProxy(_ context.Context, in *hermesv1.ProxyGetBestRequest, _ ...grpc.CallOption) (*hermesv1.ProxyGetBestResponse, error) {
	return f.getBestProxy(in)
}
func (f *fakeLoginProxyClient) AssignProxy(_ context.Context, in *hermesv1.ProxyAssignRequest, _ ...grpc.CallOption) (*hermesv1.ProxyAssignResponse, error) {
	f.assignCalls = append(f.assignCalls, in)
	if f.assignProxy != nil {
		return f.assignProxy(in)
	}
	return &hermesv1.ProxyAssignResponse{}, nil
}

func TestResolveLoginProxy(t *testing.T) {
	bestProxy := &hermesv1.Proxy{
		Id: "px-7", Host: "9.9.9.9", Port: 1080,
		Username: "u", Password: "p", Type: hermesv1.ProxyType_PROXY_TYPE_SOCKS5,
	}

	t.Run("nil client, soft → direct", func(t *testing.T) {
		h := &Handler{proxyClient: nil, proxyAutoAssign: true, proxyRequired: false}
		url, id, err := h.resolveLoginProxy(context.Background(), "tenant", "")
		if err != nil || url != "" || id != "" {
			t.Fatalf("got (%q,%q,%v), want empty+nil", url, id, err)
		}
	})

	t.Run("nil client, required → FailedPrecondition", func(t *testing.T) {
		h := &Handler{proxyClient: nil, proxyAutoAssign: true, proxyRequired: true}
		_, _, err := h.resolveLoginProxy(context.Background(), "tenant", "")
		if status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("got %v, want FailedPrecondition", err)
		}
	})

	t.Run("auto-assign off → direct", func(t *testing.T) {
		pc := &fakeLoginProxyClient{
			getBestProxy: func(*hermesv1.ProxyGetBestRequest) (*hermesv1.ProxyGetBestResponse, error) {
				t.Fatal("GetBestProxy should not be called when auto-assign is off")
				return nil, nil
			},
		}
		h := &Handler{proxyClient: pc, proxyAutoAssign: false}
		url, id, err := h.resolveLoginProxy(context.Background(), "tenant", "")
		if err != nil || url != "" || id != "" {
			t.Fatalf("got (%q,%q,%v), want empty+nil", url, id, err)
		}
	})

	t.Run("pool hit → url + id", func(t *testing.T) {
		pc := &fakeLoginProxyClient{
			getBestProxy: func(in *hermesv1.ProxyGetBestRequest) (*hermesv1.ProxyGetBestResponse, error) {
				if in.TenantId != "tenant" {
					t.Errorf("tenant = %q, want tenant", in.TenantId)
				}
				return &hermesv1.ProxyGetBestResponse{Proxy: bestProxy}, nil
			},
		}
		h := &Handler{proxyClient: pc, proxyAutoAssign: true}
		url, id, err := h.resolveLoginProxy(context.Background(), "tenant", "")
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if want := "socks5://u:p@9.9.9.9:1080"; url != want {
			t.Errorf("url = %q, want %q", url, want)
		}
		if id != "px-7" {
			t.Errorf("id = %q, want px-7", id)
		}
	})

	t.Run("pool empty, soft → direct", func(t *testing.T) {
		pc := &fakeLoginProxyClient{
			getBestProxy: func(*hermesv1.ProxyGetBestRequest) (*hermesv1.ProxyGetBestResponse, error) {
				return &hermesv1.ProxyGetBestResponse{Proxy: nil}, nil
			},
		}
		h := &Handler{proxyClient: pc, proxyAutoAssign: true, proxyRequired: false}
		url, id, err := h.resolveLoginProxy(context.Background(), "tenant", "")
		if err != nil || url != "" || id != "" {
			t.Fatalf("got (%q,%q,%v), want empty+nil", url, id, err)
		}
	})

	t.Run("pool empty, required → FailedPrecondition", func(t *testing.T) {
		pc := &fakeLoginProxyClient{
			getBestProxy: func(*hermesv1.ProxyGetBestRequest) (*hermesv1.ProxyGetBestResponse, error) {
				return &hermesv1.ProxyGetBestResponse{Proxy: nil}, nil
			},
		}
		h := &Handler{proxyClient: pc, proxyAutoAssign: true, proxyRequired: true}
		_, _, err := h.resolveLoginProxy(context.Background(), "tenant", "")
		if status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("got %v, want FailedPrecondition", err)
		}
	})

	t.Run("GetBestProxy error, soft → direct", func(t *testing.T) {
		pc := &fakeLoginProxyClient{
			getBestProxy: func(*hermesv1.ProxyGetBestRequest) (*hermesv1.ProxyGetBestResponse, error) {
				return nil, errors.New("proxy svc down")
			},
		}
		h := &Handler{proxyClient: pc, proxyAutoAssign: true, proxyRequired: false}
		url, _, err := h.resolveLoginProxy(context.Background(), "tenant", "")
		if err != nil || url != "" {
			t.Fatalf("got (%q,%v), want empty+nil", url, err)
		}
	})

	t.Run("explicit proxy honored, skips auto-pick", func(t *testing.T) {
		pc := &fakeLoginProxyClient{
			getProxy: func(in *hermesv1.ProxyGetRequest) (*hermesv1.ProxyGetResponse, error) {
				if in.Id != "px-explicit" {
					t.Errorf("GetProxy id = %q, want px-explicit", in.Id)
				}
				return &hermesv1.ProxyGetResponse{Proxy: &hermesv1.Proxy{
					Id: "px-explicit", Host: "5.5.5.5", Port: 1080,
					TenantId: "tenant", Type: hermesv1.ProxyType_PROXY_TYPE_SOCKS5,
				}}, nil
			},
			getBestProxy: func(*hermesv1.ProxyGetBestRequest) (*hermesv1.ProxyGetBestResponse, error) {
				t.Fatal("GetBestProxy should not be called when an explicit proxy resolves")
				return nil, nil
			},
		}
		h := &Handler{proxyClient: pc, proxyAutoAssign: true}
		url, id, err := h.resolveLoginProxy(context.Background(), "tenant", "px-explicit")
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if want := "socks5://5.5.5.5:1080"; url != want {
			t.Errorf("url = %q, want %q", url, want)
		}
		if id != "px-explicit" {
			t.Errorf("id = %q, want px-explicit", id)
		}
	})

	t.Run("explicit proxy cross-tenant rejected, falls back to auto", func(t *testing.T) {
		pc := &fakeLoginProxyClient{
			getProxy: func(*hermesv1.ProxyGetRequest) (*hermesv1.ProxyGetResponse, error) {
				return &hermesv1.ProxyGetResponse{Proxy: &hermesv1.Proxy{
					Id: "px-other", Host: "6.6.6.6", Port: 1080, TenantId: "other-tenant",
				}}, nil
			},
			getBestProxy: func(*hermesv1.ProxyGetBestRequest) (*hermesv1.ProxyGetBestResponse, error) {
				return &hermesv1.ProxyGetBestResponse{Proxy: bestProxy}, nil
			},
		}
		h := &Handler{proxyClient: pc, proxyAutoAssign: true}
		_, id, err := h.resolveLoginProxy(context.Background(), "tenant", "px-other")
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		// Cross-tenant explicit rejected → auto-pick the pool's best.
		if id != "px-7" {
			t.Errorf("id = %q, want px-7 (auto fallback)", id)
		}
	})

	t.Run("explicit proxy unresolvable, falls back to direct (auto off)", func(t *testing.T) {
		pc := &fakeLoginProxyClient{
			getProxy: func(*hermesv1.ProxyGetRequest) (*hermesv1.ProxyGetResponse, error) {
				return nil, errors.New("not found")
			},
		}
		h := &Handler{proxyClient: pc, proxyAutoAssign: false}
		url, id, err := h.resolveLoginProxy(context.Background(), "tenant", "px-missing")
		if err != nil || url != "" || id != "" {
			t.Fatalf("got (%q,%q,%v), want empty+nil", url, id, err)
		}
	})
}

func TestPersistLoginProxy(t *testing.T) {
	t.Run("pins via AssignProxy(MBS, uid)", func(t *testing.T) {
		pc := &fakeLoginProxyClient{}
		h := &Handler{proxyClient: pc}
		h.persistLoginProxy(context.Background(), 61590752691262, "px-7")
		if len(pc.assignCalls) != 1 {
			t.Fatalf("AssignProxy calls = %d, want 1", len(pc.assignCalls))
		}
		got := pc.assignCalls[0]
		if got.ProxyId != "px-7" {
			t.Errorf("ProxyId = %q, want px-7", got.ProxyId)
		}
		if got.TargetType != hermesv1.ProxyTargetType_PROXY_TARGET_TYPE_MBS {
			t.Errorf("TargetType = %v, want MBS", got.TargetType)
		}
		if got.TargetId != "61590752691262" {
			t.Errorf("TargetId = %q, want uid string", got.TargetId)
		}
	})

	t.Run("empty proxyID → no-op", func(t *testing.T) {
		pc := &fakeLoginProxyClient{}
		h := &Handler{proxyClient: pc}
		h.persistLoginProxy(context.Background(), 1, "")
		if len(pc.assignCalls) != 0 {
			t.Fatalf("AssignProxy calls = %d, want 0", len(pc.assignCalls))
		}
	})

	t.Run("nil client → no panic, no-op", func(t *testing.T) {
		h := &Handler{proxyClient: nil}
		h.persistLoginProxy(context.Background(), 1, "px-7") // must not panic
	})

	t.Run("AssignProxy error is non-fatal", func(t *testing.T) {
		pc := &fakeLoginProxyClient{
			assignProxy: func(*hermesv1.ProxyAssignRequest) (*hermesv1.ProxyAssignResponse, error) {
				return nil, errors.New("assign failed")
			},
		}
		h := &Handler{proxyClient: pc}
		// Should swallow the error (logs only).
		h.persistLoginProxy(context.Background(), 1, "px-7")
	})
}
