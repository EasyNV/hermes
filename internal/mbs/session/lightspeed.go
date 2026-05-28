package session

import (
	"errors"
	"fmt"
	"strconv"

	"mbs-native/client"

	"github.com/hermes-waba/hermes/internal/mbs/store"
)

// buildLightspeedAssets maps the store's primary AssetRow (strings) to
// the client's LightspeedAssets struct (int64). Lightspeed CONNECT
// requires WABA_ID + PAGE + WHATSAPP_MAILBOX_ID at field 4.30; all
// three are sourced from the primary asset row in the DB.
//
// Validation:
//   - Nil asset → "no primary asset" with creds-enrich hint
//   - Any string field empty → "missing X" error
//   - Any string field non-numeric → parse error
//
// SessionTag is left nil so the builder computes it live from creds.
func buildLightspeedAssets(asset *store.AssetRow) (*client.LightspeedAssets, error) {
	if asset == nil {
		return nil, errors.New("session: no primary asset on session (run creds-enrich or re-bridge)")
	}
	if asset.WabaID == "" {
		return nil, errors.New("session: primary asset has empty waba_id")
	}
	if asset.PageID == "" {
		return nil, errors.New("session: primary asset has empty page_id")
	}
	if asset.WecMailboxID == "" {
		return nil, errors.New("session: primary asset has empty wec_mailbox_id")
	}

	wabaID, err := strconv.ParseInt(asset.WabaID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("session: invalid waba_id %q: %w", asset.WabaID, err)
	}
	pageID, err := strconv.ParseInt(asset.PageID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("session: invalid page_id %q: %w", asset.PageID, err)
	}
	mailboxID, err := strconv.ParseInt(asset.WecMailboxID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("session: invalid wec_mailbox_id %q: %w", asset.WecMailboxID, err)
	}

	return &client.LightspeedAssets{
		WABAID:        wabaID,
		PageID:        pageID,
		WAMailboxID:   mailboxID,
		SessionTag:    nil, // computed live from creds.ComputeSessionTag
		MqttCapsLS:    34444559,
		NetworkKindLS: 90,
	}, nil
}

// pickPrimary returns the IsPrimary asset from a list, or the first if
// none is marked primary (fallback for legacy import rows). Returns
// nil if the slice is empty.
func pickPrimary(assets []*store.AssetRow) *store.AssetRow {
	if len(assets) == 0 {
		return nil
	}
	for _, a := range assets {
		if a.IsPrimary {
			return a
		}
	}
	// No row marked primary — fall back to first.
	return assets[0]
}
