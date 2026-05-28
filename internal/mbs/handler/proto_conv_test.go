package handler

import (
	"testing"
	"time"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/hermes-waba/hermes/internal/mbs/store"
)

func TestStateRoundTrip(t *testing.T) {
	// Every named DB value must round-trip.
	for _, s := range []string{"active", "suspended", "burned", "bridging"} {
		got := protoStateToDB(dbStateToProto(s))
		if got != s {
			t.Errorf("%q: round-trip got %q", s, got)
		}
	}
	// Unknown / empty maps both ways via UNSPECIFIED.
	if dbStateToProto("garbage") != hermesv1.MbsSessionState_MBS_SESSION_STATE_UNSPECIFIED {
		t.Errorf("unknown db state should map to UNSPECIFIED")
	}
	if protoStateToDB(hermesv1.MbsSessionState_MBS_SESSION_STATE_UNSPECIFIED) != "" {
		t.Errorf("UNSPECIFIED should map to empty string (= no filter)")
	}
}

func TestStateToSubjectFragment(t *testing.T) {
	cases := map[hermesv1.MbsSessionState]string{
		hermesv1.MbsSessionState_MBS_SESSION_STATE_ACTIVE:      "connected",
		hermesv1.MbsSessionState_MBS_SESSION_STATE_SUSPENDED:   "disconnected",
		hermesv1.MbsSessionState_MBS_SESSION_STATE_BURNED:      "burned",
		hermesv1.MbsSessionState_MBS_SESSION_STATE_BRIDGING:    "",
		hermesv1.MbsSessionState_MBS_SESSION_STATE_UNSPECIFIED: "",
	}
	for in, want := range cases {
		got := stateToSubjectFragment(in)
		if got != want {
			t.Errorf("%v: got %q want %q", in, got, want)
		}
	}
}

func TestSessionRowToProto_FullFields(t *testing.T) {
	connackAt := time.Now().Add(-time.Hour)
	connackRC := int16(0)
	created := time.Now().Add(-72 * time.Hour)
	updated := time.Now().Add(-time.Minute)

	row := &store.SessionRow{
		UID:           61590134170831,
		TenantID:      "tenant-A",
		DisplayName:   "Firwanata",
		State:         "active",
		DeviceID:      "device-xyz",
		AppVersion:    "551.0.0.55.106",
		LastConnackRC: &connackRC,
		LastConnackAt: &connackAt,
		CreatedAt:     created,
		UpdatedAt:     updated,
	}
	primary := &store.AssetRow{
		UID:                    61590134170831,
		PageID:                 "page-1",
		PageName:               "Firwanata Official",
		WabaID:                 "waba-1",
		WecMailboxID:           "mbox-1",
		WecPhoneNumber:         "62812",
		BusinessPresenceNodeID: "bpn-1",
		IgAccountID:            "",
		IsPrimary:              true,
	}

	got := sessionRowToProto(row, primary)
	if got == nil {
		t.Fatal("nil result")
	}
	if got.Uid != row.UID {
		t.Errorf("uid")
	}
	if got.TenantId != "tenant-A" {
		t.Errorf("tenant")
	}
	if got.DisplayName != "Firwanata" {
		t.Errorf("display_name")
	}
	if got.State != hermesv1.MbsSessionState_MBS_SESSION_STATE_ACTIVE {
		t.Errorf("state")
	}
	if got.LastConnackRc != int32(connackRC) {
		t.Errorf("connack_rc")
	}
	if got.LastConnackAt == nil || !got.LastConnackAt.AsTime().Equal(connackAt) {
		t.Errorf("connack_at")
	}
	if got.PrimaryAsset == nil || got.PrimaryAsset.PageId != "page-1" {
		t.Errorf("primary asset")
	}
	if !got.PrimaryAsset.HasWaba {
		t.Errorf("has_waba should be true (waba_id set)")
	}
}

func TestSessionRowToProto_NilInput(t *testing.T) {
	if sessionRowToProto(nil, nil) != nil {
		t.Error("nil input should return nil")
	}
}

func TestSessionRowToProto_NoPrimary(t *testing.T) {
	row := &store.SessionRow{UID: 1, State: "active", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	got := sessionRowToProto(row, nil)
	if got.PrimaryAsset != nil {
		t.Errorf("expected nil primary_asset when not provided")
	}
}

func TestAssetRowToProto_HasWaba(t *testing.T) {
	// has_waba = true when EITHER waba_id OR mailbox is set.
	cases := []struct {
		name    string
		row     *store.AssetRow
		hasWaba bool
	}{
		{"both", &store.AssetRow{WabaID: "w", WecMailboxID: "m"}, true},
		{"waba only", &store.AssetRow{WabaID: "w"}, true},
		{"mailbox only", &store.AssetRow{WecMailboxID: "m"}, true},
		{"neither", &store.AssetRow{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := assetRowToProto(c.row)
			if got.HasWaba != c.hasWaba {
				t.Errorf("got %v want %v", got.HasWaba, c.hasWaba)
			}
		})
	}
	// Nil input.
	if assetRowToProto(nil) != nil {
		t.Error("nil input should return nil")
	}
}

func TestFindPrimaryAsset(t *testing.T) {
	// Explicit primary wins.
	rows := []*store.AssetRow{
		{PageID: "a", IsPrimary: false},
		{PageID: "b", IsPrimary: true},
		{PageID: "c", IsPrimary: false},
	}
	if findPrimaryAsset(rows).PageID != "b" {
		t.Errorf("explicit primary should win")
	}

	// No primary flag → first element.
	rows2 := []*store.AssetRow{
		{PageID: "x"}, {PageID: "y"},
	}
	if findPrimaryAsset(rows2).PageID != "x" {
		t.Errorf("no-primary fallback should be first element")
	}

	// Empty → nil.
	if findPrimaryAsset(nil) != nil {
		t.Error("empty should be nil")
	}
}

func TestAssetRowsToProto_SkipsNils(t *testing.T) {
	rows := []*store.AssetRow{
		{PageID: "a"},
		nil,
		{PageID: "b"},
	}
	got := assetRowsToProto(rows)
	if len(got) != 2 {
		t.Errorf("expected 2 (nil filtered), got %d", len(got))
	}
	if got[0].PageId != "a" || got[1].PageId != "b" {
		t.Errorf("order/content: %+v", got)
	}
}
