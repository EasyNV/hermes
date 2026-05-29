package bridge

import (
	"context"
	"errors"
	"time"

	"mbs-native/auth"
	"mbs-native/graphql"

	"github.com/hermes-waba/hermes/internal/mbs/store"
)

// AssetDiscoverer turns post-login creds into the WABA-connected pages
// the handler persists into `mbs_session_assets`. Defined as a small
// interface so:
//
//  1. Tests inject a fake (we don't want unit tests hitting Meta's
//     /graphql endpoint, and we definitely don't want them depending
//     on network reachability).
//  2. The wire-level path is swappable. Today it's mbs-native's
//     scoping + mailbox query chain. Future variants (e.g., a different
//     query set when Meta deprecates a doc_id) plug in here without
//     touching the loginLoop.
//
// Contract:
//   - Returns (assets, primary, nil) on success. Primary may be nil if
//     the account admins no WABA-connected pages — that's a legitimate
//     state, NOT an error (e.g., personal FB account with no Pages
//     attached, or all Pages disconnected from WABA).
//   - Returns (nil, nil, err) on hard failure (network, malformed
//     response, etc.). Caller (loginLoop) treats this as non-fatal:
//     emits Success with empty Assets, logs a Warn. The session is
//     bridged; the asset list can be discovered later via a
//     refresh-tick or manual --rediscover.
type AssetDiscoverer interface {
	DiscoverFromCreds(ctx context.Context, creds *auth.Creds) (assets []*store.AssetRow, primary *store.AssetRow, err error)
}

// assetDiscovererFunc adapts a plain function into the interface.
// Helper for tests that want a one-liner stub without declaring a
// dedicated type.
type assetDiscovererFunc func(ctx context.Context, creds *auth.Creds) ([]*store.AssetRow, *store.AssetRow, error)

func (f assetDiscovererFunc) DiscoverFromCreds(ctx context.Context, creds *auth.Creds) ([]*store.AssetRow, *store.AssetRow, error) {
	return f(ctx, creds)
}

// defaultAssetDiscoveryTimeout caps how long we'll wait on Meta's
// /graphql calls before emitting an empty asset list and letting the
// session proceed. Matches the legacy mbs-native asset-discovery
// default; surfaced as a const so tests can override.
const defaultAssetDiscoveryTimeout = 30 * time.Second

// graphqlAssetDiscoverer is the production AssetDiscoverer. Wraps the
// two-phase pipeline from re/mbs/mbs-native/cmd/mbs-native/asset_discovery.go,
// minus the cookie-fallback variant (Stage B added cookies; the bridge
// driver runs BEFORE we have a stable cookie jar persisted, so we use
// the access_token path only). Cookie-fallback enrichment can run
// separately via the chunk-7 refresh ticker.
//
// Pipeline:
//
//  1. graphql.New(creds) — builds a /graphql client bound to creds.
//  2. FetchBusinessScopingConfig — list all admin'd pages.
//  3. PickWABAAsset — choose the first WABA-connected page (Sam can
//     override at send-time via page_id_override).
//  4. FetchPageMailboxInfo for the WABA-connected pages — pulls
//     wec_mailbox_id + wec phone number for primary, best-effort for
//     non-primary (we want them populated so SendMessage's
//     page_id_override path works without re-querying).
//
// On any error during step 2-4, returns the error to the caller. The
// loginLoop will treat the failure as non-fatal — Success still emits,
// just with nil PrimaryAsset and empty Assets. Refresh-tick can
// rediscover later.
type graphqlAssetDiscoverer struct {
	// timeout is the per-discovery bound. Default 30s.
	timeout time.Duration
}

// newGraphQLAssetDiscoverer returns the production AssetDiscoverer.
// Pass timeout=0 for the default.
func newGraphQLAssetDiscoverer(timeout time.Duration) AssetDiscoverer {
	if timeout <= 0 {
		timeout = defaultAssetDiscoveryTimeout
	}
	return &graphqlAssetDiscoverer{timeout: timeout}
}

func (d *graphqlAssetDiscoverer) DiscoverFromCreds(ctx context.Context, creds *auth.Creds) ([]*store.AssetRow, *store.AssetRow, error) {
	if creds == nil {
		return nil, nil, errors.New("DiscoverFromCreds: nil creds")
	}
	if creds.AccessToken == "" || creds.UserID == 0 {
		return nil, nil, errors.New("DiscoverFromCreds: creds incomplete (missing access_token or user_id)")
	}

	dctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()

	gc, err := graphql.New(creds)
	if err != nil {
		return nil, nil, err
	}

	// Phase 1 — scoping. Lists all pages the user admins, with each
	// page's WABA/IG status flags.
	gqlAssets, err := gc.FetchBusinessScopingConfig(dctx)
	if err != nil {
		return nil, nil, err
	}
	if len(gqlAssets) == 0 {
		// User admins no pages — legitimate state, return empty.
		return nil, nil, nil
	}

	// Phase 2 — pick the WABA-connected primary. ErrNoWABAAsset is
	// NOT an error from our perspective (account is bridged, just
	// no WhatsApp surface to wire up).
	primaryGQL, candidates, pickErr := graphql.PickWABAAsset(gqlAssets)
	if pickErr != nil && !errors.Is(pickErr, graphql.ErrNoWABAAsset) {
		return nil, nil, pickErr
	}

	// Phase 3 — enrich WABA candidates with mailbox info. Non-WABA
	// pages skipped (we don't need their mailbox for send-by-phone).
	// Best-effort per-page: a single mailbox lookup failing doesn't
	// abort the rest.
	rows := make([]*store.AssetRow, 0, len(gqlAssets))
	now := time.Now()
	for _, a := range gqlAssets {
		row := assetFromGQL(creds.UserID, &a, now)
		if a.HasWABA() {
			if mb, mberr := gc.FetchPageMailboxInfo(dctx, a.PageID, true); mberr == nil && mb != nil {
				if wec := mb.WECMailbox(); wec != nil {
					row.WecMailboxID = wec.ID
				}
				row.WecPhoneNumber = a.WABAPhoneNumber
			}
		}
		// Tag primary so the handler's SetPrimaryAsset is a no-op
		// when this asset row already carries IsPrimary=true.
		if primaryGQL != nil && a.PageID == primaryGQL.PageID {
			row.IsPrimary = true
		}
		rows = append(rows, row)
	}

	// Build the primary row from the same enriched slice (so its
	// WecMailboxID is populated).
	var primaryRow *store.AssetRow
	if primaryGQL != nil {
		for _, r := range rows {
			if r.PageID == primaryGQL.PageID {
				primaryRow = r
				break
			}
		}
	}

	// candidates is informational — we don't surface it to handler
	// today, but having >1 means the user admins multiple
	// WABA-connected pages and the picker made a default choice.
	// Log via the discoverer's caller if interesting.
	_ = candidates

	return rows, primaryRow, nil
}

// assetFromGQL maps a graphql.Asset to our store.AssetRow shape.
// Mailbox + phone fields are filled by the caller (Phase 3) — this
// function only handles the scoping-result fields.
func assetFromGQL(uid int64, a *graphql.Asset, now time.Time) *store.AssetRow {
	return &store.AssetRow{
		UID:                    uid,
		PageID:                 a.PageID,
		PageName:               a.PageName,
		BusinessPresenceNodeID: a.BusinessPresenceNodeID,
		BusinessID:             a.BusinessID,
		BusinessName:           a.BusinessName,
		WabaID:                 a.WABAID,
		IgAccountID:            a.IGAccountID,
		WECAccountRegistered:   a.HasWABA(),
		DiscoveredAt:           now,
	}
}
