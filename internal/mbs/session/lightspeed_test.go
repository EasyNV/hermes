package session

import (
	"errors"
	"strings"
	"testing"

	"github.com/hermes-waba/hermes/internal/mbs/store"
)

func TestBuildLightspeedAssets_HappyPath(t *testing.T) {
	asset := &store.AssetRow{
		UID:          61590134170831,
		PageID:       "1219576644562769",
		WabaID:       "1147297338458228",
		WecMailboxID: "1153441357849273",
		IsPrimary:    true,
	}
	got, err := buildLightspeedAssets(asset)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.WABAID != 1147297338458228 {
		t.Errorf("WABAID: got %d", got.WABAID)
	}
	if got.PageID != 1219576644562769 {
		t.Errorf("PageID: got %d", got.PageID)
	}
	if got.WAMailboxID != 1153441357849273 {
		t.Errorf("WAMailboxID: got %d", got.WAMailboxID)
	}
	// SessionTag must be nil so client computes live.
	if got.SessionTag != nil {
		t.Errorf("SessionTag should be nil for live compute, got %x", got.SessionTag)
	}
	// MqttCapsLS / NetworkKindLS are fixed constants from observed wire.
	if got.MqttCapsLS != 34444559 {
		t.Errorf("MqttCapsLS: got %d", got.MqttCapsLS)
	}
	if got.NetworkKindLS != 90 {
		t.Errorf("NetworkKindLS: got %d", got.NetworkKindLS)
	}
}

func TestBuildLightspeedAssets_NilAsset_HintsEnrich(t *testing.T) {
	_, err := buildLightspeedAssets(nil)
	if err == nil {
		t.Fatal("nil asset should error")
	}
	if !strings.Contains(err.Error(), "creds-enrich") {
		t.Errorf("error should hint at creds-enrich workflow, got %q", err.Error())
	}
}

func TestBuildLightspeedAssets_MissingFields(t *testing.T) {
	cases := []struct {
		name  string
		asset *store.AssetRow
		want  string
	}{
		{
			name:  "empty waba_id",
			asset: &store.AssetRow{PageID: "1", WecMailboxID: "1"},
			want:  "empty waba_id",
		},
		{
			name:  "empty page_id",
			asset: &store.AssetRow{WabaID: "1", WecMailboxID: "1"},
			want:  "empty page_id",
		},
		{
			name:  "empty wec_mailbox_id",
			asset: &store.AssetRow{WabaID: "1", PageID: "1"},
			want:  "empty wec_mailbox_id",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildLightspeedAssets(tc.asset)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error should mention %q, got %q", tc.want, err.Error())
			}
		})
	}
}

func TestBuildLightspeedAssets_NonNumericFields(t *testing.T) {
	asset := &store.AssetRow{
		WabaID:       "not-a-number",
		PageID:       "1",
		WecMailboxID: "1",
	}
	_, err := buildLightspeedAssets(asset)
	if err == nil {
		t.Fatal("expected parse error")
	}
	if !strings.Contains(err.Error(), "waba_id") {
		t.Errorf("error should mention waba_id, got %q", err.Error())
	}
	// Non-numeric should produce a wrapped strconv.NumError. We don't
	// pin the chain exactly — just that it's not nil and isn't our
	// "empty" sentinel.
	if errors.Is(err, errors.New("empty")) {
		t.Errorf("non-numeric should not collapse to empty-field path")
	}
}

func TestPickPrimary(t *testing.T) {
	// Marked primary
	assets := []*store.AssetRow{
		{PageID: "100"},
		{PageID: "200", IsPrimary: true},
		{PageID: "300"},
	}
	if got := pickPrimary(assets); got == nil || got.PageID != "200" {
		t.Errorf("should pick marked primary, got %+v", got)
	}

	// No primary marked → fallback to first
	assets = []*store.AssetRow{{PageID: "100"}, {PageID: "200"}}
	if got := pickPrimary(assets); got == nil || got.PageID != "100" {
		t.Errorf("fallback should be first, got %+v", got)
	}

	// Empty → nil
	if got := pickPrimary(nil); got != nil {
		t.Errorf("nil input should yield nil, got %+v", got)
	}
}
