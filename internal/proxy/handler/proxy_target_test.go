package handler

import (
	"context"
	"testing"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestResolveAssignTarget covers the WA/MBS routing precedence: explicit
// target_type wins; UNSPECIFIED falls back to the legacy wa_number_id (WA).
func TestResolveAssignTarget(t *testing.T) {
	cases := []struct {
		name        string
		tt          hermesv1.ProxyTargetType
		targetID    string
		legacyWaID  string
		wantKind    ProxyTargetKind
		wantID      string
		wantErrCode codes.Code // codes.OK = no error
	}{
		{"legacy WA via wa_number_id", hermesv1.ProxyTargetType_PROXY_TARGET_TYPE_UNSPECIFIED, "", "wa-123", TargetWA, "wa-123", codes.OK},
		{"explicit WA via target_id", hermesv1.ProxyTargetType_PROXY_TARGET_TYPE_WA, "wa-456", "", TargetWA, "wa-456", codes.OK},
		{"explicit WA falls back to legacy id", hermesv1.ProxyTargetType_PROXY_TARGET_TYPE_WA, "", "wa-789", TargetWA, "wa-789", codes.OK},
		{"MBS via target_id", hermesv1.ProxyTargetType_PROXY_TARGET_TYPE_MBS, "61590752691262", "", TargetMBS, "61590752691262", codes.OK},
		{"MBS missing target_id", hermesv1.ProxyTargetType_PROXY_TARGET_TYPE_MBS, "", "", 0, "", codes.InvalidArgument},
		{"UNSPECIFIED missing everything", hermesv1.ProxyTargetType_PROXY_TARGET_TYPE_UNSPECIFIED, "", "", 0, "", codes.InvalidArgument},
		{"WA missing everything", hermesv1.ProxyTargetType_PROXY_TARGET_TYPE_WA, "", "", 0, "", codes.InvalidArgument},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kind, id, err := resolveAssignTarget(c.tt, c.targetID, c.legacyWaID)
			if c.wantErrCode != codes.OK {
				if status.Code(err) != c.wantErrCode {
					t.Fatalf("err code = %v, want %v", status.Code(err), c.wantErrCode)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if kind != c.wantKind || id != c.wantID {
				t.Errorf("got (%v,%q), want (%v,%q)", kind, id, c.wantKind, c.wantID)
			}
		})
	}
}

// TestAssignProxy_MBSTarget verifies the handler routes an MBS target through
// AssignProxyTarget with kind=TargetMBS and the uid as target_id.
func TestAssignProxy_MBSTarget(t *testing.T) {
	var gotKind ProxyTargetKind
	var gotTarget, gotProxy string
	store := &mockStore{
		assignProxyTargetFn: func(_ context.Context, kind ProxyTargetKind, targetID, proxyID string) (*ProxyRow, error) {
			gotKind, gotTarget, gotProxy = kind, targetID, proxyID
			return &ProxyRow{ID: proxyID, AssignedCount: 1}, nil
		},
	}
	h := newTestHandler(store, &mockChecker{})
	resp, err := h.AssignProxy(context.Background(), &hermesv1.ProxyAssignRequest{
		ProxyId:    "px-1",
		TargetType: hermesv1.ProxyTargetType_PROXY_TARGET_TYPE_MBS,
		TargetId:   "61590752691262",
	})
	if err != nil {
		t.Fatalf("AssignProxy: %v", err)
	}
	if gotKind != TargetMBS || gotTarget != "61590752691262" || gotProxy != "px-1" {
		t.Errorf("routed (%v,%q,%q), want (TargetMBS,61590752691262,px-1)", gotKind, gotTarget, gotProxy)
	}
	if resp.AssignedCount != 1 {
		t.Errorf("assigned_count = %d, want 1", resp.AssignedCount)
	}
}

// TestAssignProxy_LegacyWAUnchanged verifies a legacy wa_number_id-only request
// still routes to TargetWA (wire-compat with existing callers).
func TestAssignProxy_LegacyWAUnchanged(t *testing.T) {
	var gotKind ProxyTargetKind
	var gotTarget string
	store := &mockStore{
		assignProxyTargetFn: func(_ context.Context, kind ProxyTargetKind, targetID, proxyID string) (*ProxyRow, error) {
			gotKind, gotTarget = kind, targetID
			return &ProxyRow{ID: proxyID, AssignedCount: 1}, nil
		},
	}
	h := newTestHandler(store, &mockChecker{})
	_, err := h.AssignProxy(context.Background(), &hermesv1.ProxyAssignRequest{
		WaNumberId: "wa-legacy",
		ProxyId:    "px-1",
	})
	if err != nil {
		t.Fatalf("AssignProxy: %v", err)
	}
	if gotKind != TargetWA || gotTarget != "wa-legacy" {
		t.Errorf("routed (%v,%q), want (TargetWA,wa-legacy)", gotKind, gotTarget)
	}
}
