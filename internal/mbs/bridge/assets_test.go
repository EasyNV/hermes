package bridge

import (
	"context"
	"errors"
	"testing"

	"mbs-native/auth"

	"github.com/hermes-waba/hermes/internal/mbs/store"
)

// assetDiscovererFunc smoke — confirm the adapter satisfies the
// interface contract (compile-time + behavioral).
func TestAssetDiscovererFunc_AdapterContract(t *testing.T) {
	want := []*store.AssetRow{{PageID: "p1"}}
	wantPrimary := &store.AssetRow{PageID: "p1", IsPrimary: true}
	var called bool
	fn := assetDiscovererFunc(func(ctx context.Context, creds *auth.Creds) ([]*store.AssetRow, *store.AssetRow, error) {
		called = true
		return want, wantPrimary, nil
	})

	var d AssetDiscoverer = fn // compile-time check
	got, primary, err := d.DiscoverFromCreds(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Errorf("adapter did not invoke underlying func")
	}
	if len(got) != 1 || got[0].PageID != "p1" {
		t.Errorf("rows: %+v", got)
	}
	if primary == nil || primary.PageID != "p1" {
		t.Errorf("primary: %+v", primary)
	}
}

// TestNewGraphQLAssetDiscoverer_DefaultsTimeout pins that 0 → 30s
// default. The bridge has a contract with the loginLoop that
// asset-discovery is hard-bounded; zero timeout would unblock indefinitely.
func TestNewGraphQLAssetDiscoverer_DefaultsTimeout(t *testing.T) {
	raw := newGraphQLAssetDiscoverer(0)
	d, ok := raw.(*graphqlAssetDiscoverer)
	if !ok {
		t.Fatalf("expected *graphqlAssetDiscoverer, got %T", raw)
	}
	if d.timeout != defaultAssetDiscoveryTimeout {
		t.Errorf("zero timeout did not default: got %v want %v", d.timeout, defaultAssetDiscoveryTimeout)
	}
}

func TestGraphQLAssetDiscoverer_RejectsNilCreds(t *testing.T) {
	d := newGraphQLAssetDiscoverer(0)
	_, _, err := d.DiscoverFromCreds(context.Background(), nil)
	if err == nil {
		t.Errorf("expected error on nil creds")
	}
}

func TestGraphQLAssetDiscoverer_RejectsIncompleteCreds(t *testing.T) {
	d := newGraphQLAssetDiscoverer(0)
	cases := []*auth.Creds{
		{AccessToken: "", UserID: 100},  // no access token
		{AccessToken: "tok", UserID: 0}, // no user id
	}
	for i, c := range cases {
		_, _, err := d.DiscoverFromCreds(context.Background(), c)
		if err == nil {
			t.Errorf("case %d: expected error on incomplete creds %+v", i, c)
		}
	}
}

// TestAssetDiscoverer_HookContract — verifies the contract the loginLoop
// relies on: when AssetDiscoverer returns (nil, nil, err), the caller
// can treat this as non-fatal. We don't test the loginLoop here (that's
// Step 5); we just confirm the interface admits a "graceful empty" return.
func TestAssetDiscoverer_GracefulEmptyContract(t *testing.T) {
	// Three valid return shapes, all surfaced by the production impl:
	//
	//  1. Success with one or more rows + a non-nil primary.
	//  2. Success with rows but nil primary (no WABA-connected page).
	//  3. Hard error (network, malformed response).
	//
	// loginLoop must accept all three without panicking.
	cases := []struct {
		name    string
		fn      func(ctx context.Context, creds *auth.Creds) ([]*store.AssetRow, *store.AssetRow, error)
		wantErr bool
	}{
		{
			name: "happy_path",
			fn: func(ctx context.Context, creds *auth.Creds) ([]*store.AssetRow, *store.AssetRow, error) {
				rows := []*store.AssetRow{{PageID: "p1", IsPrimary: true}}
				return rows, rows[0], nil
			},
		},
		{
			name: "no_waba",
			fn: func(ctx context.Context, creds *auth.Creds) ([]*store.AssetRow, *store.AssetRow, error) {
				return []*store.AssetRow{{PageID: "p1"}}, nil, nil
			},
		},
		{
			name: "hard_error",
			fn: func(ctx context.Context, creds *auth.Creds) ([]*store.AssetRow, *store.AssetRow, error) {
				return nil, nil, errors.New("graphql: network down")
			},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var d AssetDiscoverer = assetDiscovererFunc(tc.fn)
			_, _, err := d.DiscoverFromCreds(context.Background(), &auth.Creds{
				AccessToken: "tok",
				UserID:      100,
			})
			if tc.wantErr && err == nil {
				t.Errorf("expected error")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}
