package session

import (
	"context"
	"errors"
	"testing"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/rs/zerolog"
	"google.golang.org/grpc"
)

// fakeProxyClient implements ProxyClient for resolver tests.
type fakeProxyClient struct {
	getProxy     func(*hermesv1.ProxyGetRequest) (*hermesv1.ProxyGetResponse, error)
	getBestProxy func(*hermesv1.ProxyGetBestRequest) (*hermesv1.ProxyGetBestResponse, error)
	assignProxy  func(*hermesv1.ProxyAssignRequest) (*hermesv1.ProxyAssignResponse, error)
	assignCalls  []*hermesv1.ProxyAssignRequest
}

func (f *fakeProxyClient) GetProxy(_ context.Context, in *hermesv1.ProxyGetRequest, _ ...grpc.CallOption) (*hermesv1.ProxyGetResponse, error) {
	return f.getProxy(in)
}
func (f *fakeProxyClient) GetBestProxy(_ context.Context, in *hermesv1.ProxyGetBestRequest, _ ...grpc.CallOption) (*hermesv1.ProxyGetBestResponse, error) {
	return f.getBestProxy(in)
}
func (f *fakeProxyClient) AssignProxy(_ context.Context, in *hermesv1.ProxyAssignRequest, _ ...grpc.CallOption) (*hermesv1.ProxyAssignResponse, error) {
	f.assignCalls = append(f.assignCalls, in)
	return f.assignProxy(in)
}

func TestProxyURLFromProto(t *testing.T) {
	cases := []struct {
		name string
		p    *hermesv1.Proxy
		want string
	}{
		{"nil", nil, ""},
		{"empty host", &hermesv1.Proxy{Host: ""}, ""},
		{
			"socks5 with creds",
			&hermesv1.Proxy{Host: "1.2.3.4", Port: 1080, Username: "u", Password: "p", Type: hermesv1.ProxyType_PROXY_TYPE_SOCKS5},
			"socks5://u:p@1.2.3.4:1080",
		},
		{
			"http with creds",
			&hermesv1.Proxy{Host: "h", Port: 8080, Username: "u", Password: "p", Type: hermesv1.ProxyType_PROXY_TYPE_HTTP},
			"http://u:p@h:8080",
		},
		{
			"socks5 no creds",
			&hermesv1.Proxy{Host: "h", Port: 1080, Type: hermesv1.ProxyType_PROXY_TYPE_SOCKS5},
			"socks5://h:1080",
		},
		{
			"unspecified type defaults socks5",
			&hermesv1.Proxy{Host: "h", Port: 1080},
			"socks5://h:1080",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := proxyURLFromProto(c.p); got != c.want {
				t.Errorf("proxyURLFromProto = %q, want %q", got, c.want)
			}
		})
	}
}

func TestNewProxyResolver_NilClient(t *testing.T) {
	r := NewProxyResolver(nil, ProxyResolverConfig{}, zerolog.Nop())
	if got := r(context.Background(), 1, "tenant", "px-1"); got != "" {
		t.Errorf("nil client → %q, want empty (direct)", got)
	}
}

func TestNewProxyResolver_StickyPin(t *testing.T) {
	pc := &fakeProxyClient{
		getProxy: func(in *hermesv1.ProxyGetRequest) (*hermesv1.ProxyGetResponse, error) {
			if in.Id != "px-1" {
				t.Errorf("GetProxy id = %q, want px-1", in.Id)
			}
			return &hermesv1.ProxyGetResponse{Proxy: &hermesv1.Proxy{
				Id: "px-1", Host: "9.9.9.9", Port: 1080, Type: hermesv1.ProxyType_PROXY_TYPE_SOCKS5,
			}}, nil
		},
	}
	r := NewProxyResolver(pc, ProxyResolverConfig{AutoAssign: true}, zerolog.Nop())
	got := r(context.Background(), 100, "tenant", "px-1")
	if got != "socks5://9.9.9.9:1080" {
		t.Errorf("sticky resolve = %q, want socks5://9.9.9.9:1080", got)
	}
	// A resolvable pin must NOT trigger auto-assign.
	if len(pc.assignCalls) != 0 {
		t.Errorf("resolvable pin triggered %d assign calls, want 0", len(pc.assignCalls))
	}
}

func TestNewProxyResolver_NoPinNoAutoAssign(t *testing.T) {
	pc := &fakeProxyClient{}
	r := NewProxyResolver(pc, ProxyResolverConfig{AutoAssign: false}, zerolog.Nop())
	if got := r(context.Background(), 100, "tenant", ""); got != "" {
		t.Errorf("no pin + no auto-assign → %q, want empty (direct)", got)
	}
}

func TestNewProxyResolver_AutoAssign(t *testing.T) {
	pc := &fakeProxyClient{
		getBestProxy: func(in *hermesv1.ProxyGetBestRequest) (*hermesv1.ProxyGetBestResponse, error) {
			if in.TenantId != "tenant-A" {
				t.Errorf("GetBestProxy tenant = %q, want tenant-A", in.TenantId)
			}
			return &hermesv1.ProxyGetBestResponse{Proxy: &hermesv1.Proxy{
				Id: "px-best", Host: "5.5.5.5", Port: 1080, Type: hermesv1.ProxyType_PROXY_TYPE_SOCKS5,
			}}, nil
		},
		assignProxy: func(*hermesv1.ProxyAssignRequest) (*hermesv1.ProxyAssignResponse, error) {
			return &hermesv1.ProxyAssignResponse{}, nil
		},
	}
	r := NewProxyResolver(pc, ProxyResolverConfig{AutoAssign: true}, zerolog.Nop())
	got := r(context.Background(), 61590752691262, "tenant-A", "")
	if got != "socks5://5.5.5.5:1080" {
		t.Errorf("auto-assign resolve = %q, want socks5://5.5.5.5:1080", got)
	}
	// Must have pinned the chosen proxy with target=MBS and the uid as string.
	if len(pc.assignCalls) != 1 {
		t.Fatalf("auto-assign made %d assign calls, want 1", len(pc.assignCalls))
	}
	a := pc.assignCalls[0]
	if a.ProxyId != "px-best" {
		t.Errorf("assign ProxyId = %q, want px-best", a.ProxyId)
	}
	if a.TargetType != hermesv1.ProxyTargetType_PROXY_TARGET_TYPE_MBS {
		t.Errorf("assign TargetType = %v, want MBS", a.TargetType)
	}
	if a.TargetId != "61590752691262" {
		t.Errorf("assign TargetId = %q, want 61590752691262", a.TargetId)
	}
}

func TestNewProxyResolver_AutoAssignNoProxyAvailable(t *testing.T) {
	pc := &fakeProxyClient{
		getBestProxy: func(*hermesv1.ProxyGetBestRequest) (*hermesv1.ProxyGetBestResponse, error) {
			return &hermesv1.ProxyGetBestResponse{Proxy: nil}, nil
		},
	}
	r := NewProxyResolver(pc, ProxyResolverConfig{AutoAssign: true}, zerolog.Nop())
	if got := r(context.Background(), 100, "tenant", ""); got != "" {
		t.Errorf("pool exhausted → %q, want empty (direct)", got)
	}
}

// A pinned proxy that can't be resolved (deleted / service hiccup) falls
// through to auto-assign when enabled, so a dangling pin self-heals.
func TestNewProxyResolver_DanglingPinFallsToAutoAssign(t *testing.T) {
	pc := &fakeProxyClient{
		getProxy: func(*hermesv1.ProxyGetRequest) (*hermesv1.ProxyGetResponse, error) {
			return nil, errors.New("not found")
		},
		getBestProxy: func(*hermesv1.ProxyGetBestRequest) (*hermesv1.ProxyGetBestResponse, error) {
			return &hermesv1.ProxyGetBestResponse{Proxy: &hermesv1.Proxy{
				Id: "px-new", Host: "7.7.7.7", Port: 1080,
			}}, nil
		},
		assignProxy: func(*hermesv1.ProxyAssignRequest) (*hermesv1.ProxyAssignResponse, error) {
			return &hermesv1.ProxyAssignResponse{}, nil
		},
	}
	r := NewProxyResolver(pc, ProxyResolverConfig{AutoAssign: true}, zerolog.Nop())
	got := r(context.Background(), 100, "tenant", "px-dead")
	if got != "socks5://7.7.7.7:1080" {
		t.Errorf("dangling pin self-heal = %q, want socks5://7.7.7.7:1080", got)
	}
}
