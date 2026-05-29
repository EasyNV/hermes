package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/hermes-waba/hermes/internal/mbs/handler"
	"github.com/hermes-waba/hermes/internal/mbs/store"
	"github.com/hermes-waba/hermes/pkg/crypto"
	"github.com/rs/zerolog"

	"mbs-native/auth"
)

// Options bundles every input Run needs. Constructed by the cmd/mbs-import
// main or cmd/mbs/main bootstrap branch and validated in Run.
//
// Required: SessionsDir, TenantID, Store, DEK (non-zero), Logger.
// Optional: Publisher (nil → skip lifecycle emit), DryRun, Force, Now.
//
// Publisher: when nil, no NATS events are emitted. Downstream services
// will instead discover the new rows when they next list sessions. For
// production migrations we recommend wiring a real publisher so the
// inbox UI lights up immediately. For one-off operator runs (single
// session reimport on the same pod) NopPublisher is fine.
//
// Force overrides the idempotent skip behavior. When true, an existing
// session row owned by the SAME tenant is REPLACED via UpdateSession*.
// Cross-tenant collisions are NEVER overwritten — even with --force —
// because that would silently overwrite another tenant's secrets with
// importer-supplied bytes. The runtime guard mirrors the bridge handler.
//
// Now is the time source for LastValidatedAt / LastRefreshedAt /
// CreatedAt; tests inject a fixed clock. Zero value uses time.Now.
type Options struct {
	SessionsDir string
	TenantID    string
	Store       store.Store
	DEK         crypto.DataEncryptionKey
	Publisher   handler.EventPublisher
	Logger      zerolog.Logger
	DryRun      bool
	Force       bool
	Now         func() time.Time
}

// Stats is the per-run accounting bundle returned by Run. Used by the
// CLI to set exit codes and by the cron path to log the outcome.
type Stats struct {
	Total      int  // every <uid>.json discovered
	Imported   int  // CreateSession succeeded
	Skipped    int  // ExistsSession=true and !Force
	Forced     int  // existed AND Force=true AND replace succeeded
	Failed     int  // any error path (parse, encrypt, persist, tenant collision)
	DryRun     bool // mirror of Options.DryRun
	StartedAt  time.Time
	FinishedAt time.Time
}

// Run executes the import. Single tenant per call. Returns the Stats
// (always non-nil) and an error iff the run could not start at all
// (bad options, unreadable directory). Per-session failures are
// captured in Stats.Failed and logged; they do NOT abort the run.
//
// Idempotent under normal operation: re-running over the same
// directory leaves the rows untouched (Skipped++). Re-running with
// Force=true overwrites the secret columns of same-tenant rows but
// refuses cross-tenant.
//
// The importer NEVER writes back to the JSON files. Once imported,
// the archive is read-only; the database is the new source of truth.
func Run(ctx context.Context, opts Options) (*Stats, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	log := opts.Logger

	files, err := walkSessions(opts.SessionsDir)
	if err != nil {
		return nil, fmt.Errorf("walk sessions dir: %w", err)
	}

	stats := &Stats{
		Total:     len(files),
		DryRun:    opts.DryRun,
		StartedAt: now(),
	}
	log.Info().
		Str("sessions_dir", opts.SessionsDir).
		Str("tenant_id", opts.TenantID).
		Int("discovered", len(files)).
		Bool("dry_run", opts.DryRun).
		Bool("force", opts.Force).
		Msg("import starting")

	for _, sf := range files {
		select {
		case <-ctx.Done():
			log.Warn().
				Err(ctx.Err()).
				Int("processed", stats.Imported+stats.Skipped+stats.Forced+stats.Failed).
				Int("remaining", stats.Total-(stats.Imported+stats.Skipped+stats.Forced+stats.Failed)).
				Msg("import canceled by context")
			stats.FinishedAt = now()
			return stats, ctx.Err()
		default:
		}

		result := importOne(ctx, opts, sf, now())
		switch result.outcome {
		case outcomeImported:
			stats.Imported++
			if opts.Publisher != nil && !opts.DryRun {
				opts.Publisher.PublishSessionLifecycle(
					sf.UID, opts.TenantID,
					hermesv1.MbsSessionState_MBS_SESSION_STATE_UNSPECIFIED,
					hermesv1.MbsSessionState_MBS_SESSION_STATE_ACTIVE,
					"imported", 0, "",
				)
			}
		case outcomeForced:
			stats.Forced++
			if opts.Publisher != nil && !opts.DryRun {
				// Re-import event surfaces in audit logs as "forced".
				opts.Publisher.PublishSessionLifecycle(
					sf.UID, opts.TenantID,
					hermesv1.MbsSessionState_MBS_SESSION_STATE_ACTIVE,
					hermesv1.MbsSessionState_MBS_SESSION_STATE_ACTIVE,
					"imported_force", 0, "",
				)
			}
		case outcomeSkipped:
			stats.Skipped++
		case outcomeFailed:
			stats.Failed++
		}
	}

	stats.FinishedAt = now()
	log.Info().
		Int("total", stats.Total).
		Int("imported", stats.Imported).
		Int("forced", stats.Forced).
		Int("skipped", stats.Skipped).
		Int("failed", stats.Failed).
		Bool("dry_run", stats.DryRun).
		Dur("elapsed", stats.FinishedAt.Sub(stats.StartedAt)).
		Msg("import complete")
	return stats, nil
}

// ─── internals ────────────────────────────────────────────────────────

type outcome int

const (
	outcomeImported outcome = iota
	outcomeForced
	outcomeSkipped
	outcomeFailed
)

type importResult struct {
	outcome outcome
	err     error // populated on outcomeFailed for log enrichment
}

// importOne handles a single SessionFile. Errors are wrapped, logged
// here, and surfaced as outcomeFailed; the caller increments the
// failure counter and continues the run. This keeps Run readable as a
// straight loop.
func importOne(ctx context.Context, opts Options, sf SessionFile, now time.Time) importResult {
	log := opts.Logger.With().Int64("uid", sf.UID).Logger()

	// 1. Load creds.
	creds, err := auth.LoadFromFile(sf.SessionPath)
	if err != nil {
		log.Error().Err(err).Str("path", sf.SessionPath).Msg("load creds")
		return importResult{outcome: outcomeFailed, err: err}
	}
	if creds.UserID != sf.UID {
		// walker pairs by filename; this catches a corrupted JSON
		// where the embedded uid doesn't match the filename. We
		// trust the filename (that's what every other tool keys
		// by) and refuse the import.
		log.Error().
			Int64("filename_uid", sf.UID).
			Int64("creds_user_id", creds.UserID).
			Msg("uid mismatch: filename vs creds.UserID")
		return importResult{outcome: outcomeFailed, err: fmt.Errorf(
			"uid mismatch: filename=%d creds.UserID=%d", sf.UID, creds.UserID)}
	}

	// 2. Load envelope sidecar if present. Sidecar absence is normal
	// for pre-Stage-D sessions; only a malformed file is an error.
	var envelope *auth.BridgeEnvelope
	if sf.EnvelopePath != "" {
		envelope, err = loadEnvelope(sf.EnvelopePath)
		if err != nil {
			log.Error().Err(err).Str("path", sf.EnvelopePath).Msg("load envelope")
			return importResult{outcome: outcomeFailed, err: err}
		}
	}

	// 3. Existence check (drives Skip vs Force-replace vs Create).
	exists, err := opts.Store.ExistsSession(ctx, sf.UID)
	if err != nil {
		log.Error().Err(err).Msg("ExistsSession failed")
		return importResult{outcome: outcomeFailed, err: err}
	}
	if exists && !opts.Force {
		log.Info().Msg("skip: session already exists (use --force to overwrite)")
		return importResult{outcome: outcomeSkipped}
	}

	// 4. CROSS-TENANT GUARD (applies even with --force).
	// On overwrite paths we must read the existing row and verify
	// the tenant matches. We do this BEFORE encrypting so we don't
	// waste CPU on a row we won't persist.
	if exists {
		existing, err := opts.Store.GetSession(ctx, sf.UID)
		if err != nil {
			log.Error().Err(err).Msg("GetSession on existing row")
			return importResult{outcome: outcomeFailed, err: err}
		}
		if existing.TenantID != opts.TenantID {
			log.Error().
				Str("import_tenant", opts.TenantID).
				Str("existing_tenant", existing.TenantID).
				Msg("REFUSED: existing session belongs to a different tenant (--force does NOT bypass this guard)")
			return importResult{outcome: outcomeFailed, err: fmt.Errorf(
				"uid %d belongs to tenant %q, refusing to overwrite with %q: %w",
				sf.UID, existing.TenantID, opts.TenantID, store.ErrTenantMismatch)}
		}
	}

	// 5. Encrypt secrets.
	cols, err := encryptForUID(opts.DEK, sf.UID, creds, envelope)
	if err != nil {
		log.Error().Err(err).Msg("encryptForUID failed")
		return importResult{outcome: outcomeFailed, err: err}
	}

	// 6. Dry-run short-circuit AFTER encrypt — exercises the full
	// happy-path code except the writes, so DryRun catches DEK / AAD
	// / parsing bugs without touching the database.
	if opts.DryRun {
		if exists {
			log.Info().Msg("dry-run: would force-replace")
			return importResult{outcome: outcomeForced}
		}
		log.Info().Msg("dry-run: would import")
		return importResult{outcome: outcomeImported}
	}

	// 7. Persist.
	row := buildRow(opts.TenantID, sf.UID, creds, cols, now)

	if !exists {
		// Fresh import via CreateSession.
		if err := opts.Store.CreateSession(ctx, row); err != nil {
			log.Error().Err(err).Msg("CreateSession failed")
			return importResult{outcome: outcomeFailed, err: err}
		}
	} else {
		// Force-replace path: same tenant, existing row. Update the
		// secret columns and the cookies blob. Identity fields stay
		// stable — if the operator wants to override device-id or
		// app version they should burn + re-bridge, not import.
		if err := opts.Store.UpdateSessionTokens(ctx, sf.UID,
			cols.AccessToken, cols.Secret, cols.SessionKey); err != nil {
			log.Error().Err(err).Msg("UpdateSessionTokens failed")
			return importResult{outcome: outcomeFailed, err: err}
		}
		if len(cols.Cookies) > 0 {
			if err := opts.Store.UpdateSessionCookies(ctx, sf.UID,
				cols.Cookies, now, now); err != nil {
				log.Error().Err(err).Msg("UpdateSessionCookies failed")
				return importResult{outcome: outcomeFailed, err: err}
			}
		}
		// Reset to active in case the previous row was burned.
		_ = opts.Store.UpdateSessionState(ctx, sf.UID, "active", nil)
	}

	// 8. Assets — legacy JSON archives never carried discovered
	// page/WABA/WEC asset rows (those live in mbs_session_assets,
	// not in creds.json). The creds.json's denormalized PageID /
	// WABAID / WECMailboxID fields ARE populated when the operator
	// ran bootstrap, so we synthesize a single primary asset row
	// from them. If they're empty, no assets are upserted — the
	// session lands in the database as "no primary asset"; first
	// use will discover via the live GraphQL queries.
	if pageID := strings.TrimSpace(creds.PageID); pageID != "" {
		asset := &store.AssetRow{
			UID:                  sf.UID,
			PageID:               pageID,
			PageName:             creds.PageName,
			BusinessID:           creds.BusinessID,
			WabaID:               creds.WABAID,
			WecMailboxID:         creds.WECMailboxID,
			WecPhoneNumber:       creds.WECPhoneNumber,
			IsPrimary:            true,
			WECAccountRegistered: creds.WECAccountRegistered,
			DiscoveredAt:         now,
		}
		if err := opts.Store.UpsertAssets(ctx, sf.UID, []*store.AssetRow{asset}); err != nil {
			// Non-fatal: row is persisted, asset upsert can be
			// retried by hand. Log loudly and continue.
			log.Warn().Err(err).Str("page_id", pageID).
				Msg("UpsertAssets failed (non-fatal — session row persisted)")
		} else {
			if err := opts.Store.SetPrimaryAsset(ctx, sf.UID, pageID); err != nil {
				log.Warn().Err(err).Str("page_id", pageID).
					Msg("SetPrimaryAsset failed (non-fatal — IsPrimary flag in row should suffice)")
			}
		}
	}

	if exists {
		return importResult{outcome: outcomeForced}
	}
	return importResult{outcome: outcomeImported}
}

// buildRow assembles a SessionRow from the parsed creds + encrypted
// columns. Mirrors the bridge handler's SessionRow construction so
// imported rows are indistinguishable from bridged rows at the storage
// layer. PodID is intentionally empty — the importer doesn't own any
// session; first GetOrConnect from a live pod will CAS-claim it.
func buildRow(tenantID string, uid int64, creds *auth.Creds, cols encryptedColumns, now time.Time) *store.SessionRow {
	return &store.SessionRow{
		UID:                  uid,
		TenantID:             tenantID,
		DisplayName:          "", // legacy JSON has no display name; gateway can backfill
		State:                "active",
		PodID:                "",
		EncryptedAccessToken: cols.AccessToken,
		EncryptedSecret:      cols.Secret,
		EncryptedSessionKey:  cols.SessionKey,
		EncryptedCookies:     cols.Cookies,
		EncryptedTOTPSecret:  cols.TOTPSecret,
		BridgeEnvelope:       cols.BridgeEnvelope,
		MachineID:            creds.MachineID,
		DeviceID:             creds.DeviceID,
		FamilyDeviceID:       creds.FamilyDeviceID,
		AppVersion:           creds.AppVersion,
		BuildNumber:          creds.BuildNumber,
		DeviceModel:          creds.DeviceModel,
		AndroidVer:           creds.AndroidVer,
		Manufacturer:         creds.Manufacturer,
		Locale:               creds.Locale,
		Density:              creds.Density,
		ScreenWidth:          creds.Width,
		ScreenHeight:         creds.Height,
		ABI:                  creds.Abi,
		VersionID:            creds.VersionID,
		MQTTCapabilities:     int(creds.MqttCapabilities),
		LastValidatedAt:      &now,
		LastRefreshedAt:      &now,
		CreatedAt:            now,
		UpdatedAt:            now,
	}
}

// loadEnvelope reads + parses a <uid>.bridge.json sidecar.
func loadEnvelope(path string) (*auth.BridgeEnvelope, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read envelope %q: %w", path, err)
	}
	var env auth.BridgeEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("unmarshal envelope %q: %w", path, err)
	}
	return &env, nil
}

func (o *Options) validate() error {
	if o == nil {
		return errors.New("importer: nil options")
	}
	if o.SessionsDir == "" {
		return errors.New("importer: SessionsDir is required")
	}
	if o.TenantID == "" {
		return errors.New("importer: TenantID is required")
	}
	if o.Store == nil {
		return errors.New("importer: Store is required")
	}
	if o.DEK.IsZero() {
		return errors.New("importer: DEK is required (zero-value DEK rejected)")
	}
	// Logger is a zerolog.Logger struct, zero value is usable
	// (Disabled sink); no nil check needed.
	return nil
}
