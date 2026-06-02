package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"mbs-native/auth"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/hermes-waba/hermes/internal/mbs/store"
	"github.com/hermes-waba/hermes/pkg/crypto"
	"github.com/pquerna/otp/totp"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// BridgeLogin is a bidirectional streaming RPC that orchestrates a CAA
// (Confidential Authentication API) login attempt via a bridge.Driver
// (in chunk 4: scripted fakeDriver in tests; in chunk 5: mautrix-meta
// in-process driver).
//
// Wire-level state machine:
//
//   client → handler:  BridgeLoginStart   (first message, REQUIRED)
//   handler → client:  BridgeLoginProgress (stage updates)
//   handler → client:  BridgeLoginPrompt   (2FA / captcha — optional)
//   client → handler:  BridgeLoginInput    (in response to prompt)
//   handler → client:  BridgeLoginSuccess  | BridgeLoginFailure (terminal)
//   client → handler:  BridgeLoginCancel   (optional; aborts ctx)
//
// Persistence boundary (correctness-critical):
//
//   Driver returns DriverSuccess with plaintext Creds + cookies +
//   optional TOTP secret. Handler encrypts EACH field with column-bound
//   AAD before any DB write. If CreateSession fails, NOTHING has been
//   written — no half-persisted secrets, no plaintext in logs.
//
// Semaphore: MaxConcurrentBridgeLogins (default 4). Acquire with a
// bounded timeout; exceed → ResourceExhausted. Prevents flood-OOM.
//
// TOTP auto-fill: if Start.TotpSecret is set AND a Prompt arrives with
// StepID="two_step_verification" containing a field id "totp_code",
// derive the current code from the base32 secret + Submit without
// surfacing the prompt to the client.
func (h *Handler) BridgeLogin(stream hermesv1.HermesMbs_BridgeLoginServer) error {
	ctx := stream.Context()
	tenantID, err := requireTenant(ctx)
	if err != nil {
		return err
	}

	// Acquire bridge semaphore with bounded wait. Default 100ms cap.
	if err := h.acquireBridgeSlot(ctx); err != nil {
		return err
	}
	defer h.releaseBridgeSlot()

	// First message MUST be BridgeLoginStart.
	first, err := stream.Recv()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return status.Error(codes.InvalidArgument, "stream closed before BridgeLoginStart")
		}
		return mapClientErr(err)
	}
	start := first.GetStart()
	if start == nil {
		return status.Error(codes.InvalidArgument, "first message must be BridgeLoginStart")
	}
	if start.Email == "" || start.Password == "" {
		return status.Error(codes.InvalidArgument, "email and password are required")
	}
	// Tenant cross-check: start.tenant_id must match the metadata
	// tenant. Empty body tenant_id is allowed (use ctx tenant).
	if start.TenantId != "" && start.TenantId != tenantID {
		return status.Error(codes.InvalidArgument, "tenant_id in BridgeLoginStart does not match caller tenant")
	}
	effectiveTenant := tenantID

	// Build per-invocation driver via factory. Logger carries tenant
	// + email for the duration of one attempt.
	driverLog := h.log.With().
		Str("rpc", "BridgeLogin").
		Str("tenant_id", effectiveTenant).
		Str("email", redactEmail(start.Email)).
		Logger()
	driver := h.driverFactory(DriverOptions{
		Logger:          driverLog,
		Timeout:         180 * time.Second,
		Await2FATimeout: 120 * time.Second,
	})
	if driver == nil {
		return status.Error(codes.Internal, "driver factory returned nil")
	}
	defer driver.Close()

	// Driver-owned cancel: client Cancel propagates here.
	driverCtx, driverCancel := context.WithCancel(ctx)
	defer driverCancel()

	updates, err := driver.Run(driverCtx, DriverStartRequest{
		Email:             start.Email,
		Password:          start.Password,
		TOTPSecret:        start.TotpSecret,
		ForceNewDeviceID:  start.ForceNewDeviceId,
		PersistTOTPSecret: start.PersistTotpSecret,
		TenantID:          effectiveTenant,
	})
	if err != nil {
		return mapClientErr(err)
	}

	// Reader goroutine drains client → handler. Exits on stream EOF,
	// ctx done, or stop signal.
	//
	// NOTE: we DON'T wait on the reader to drain in defer — Recv()
	// may block until the stream's gRPC ctx is closed (which only
	// happens after the handler returns and gRPC tears down the
	// stream). The reader observes that cancellation via Recv()
	// returning an error. Closing `stop` is a best-effort hint for
	// the case where Recv has already returned and the loop is
	// between iterations.
	stopReader := make(chan struct{})
	go h.bridgeReaderLoop(stream, driver, driverCancel, stopReader)
	defer close(stopReader)

	// Main loop: relay driver updates to client. We listen on
	// driverCtx (a child of stream.Context) so a client-sent Cancel
	// message — which calls driverCancel — also unblocks this loop.
	for {
		select {
		case <-driverCtx.Done():
			return driverCtx.Err()
		case upd, ok := <-updates:
			if !ok {
				// Driver channel closed without success/failure — abnormal exit.
				_ = stream.Send(failureUpdate(hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_INTERNAL,
					"driver closed update channel unexpectedly", false))
				return mapBridgeErr(hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_INTERNAL,
					"driver closed update channel unexpectedly")
			}
			done, err := h.handleDriverUpdate(stream, driver, start, effectiveTenant, upd)
			if done {
				return err
			}
		}
	}
}

// handleDriverUpdate processes one driver Update. Returns (done, err):
// done=true means this update was terminal (Success/Failure or fatal
// internal error); err is the gRPC error to return to caller.
func (h *Handler) handleDriverUpdate(
	stream hermesv1.HermesMbs_BridgeLoginServer,
	driver Driver,
	start *hermesv1.BridgeLoginStart,
	tenantID string,
	upd DriverUpdate,
) (done bool, err error) {
	switch upd.Kind {
	case UpdateKindProgress:
		if upd.Progress == nil {
			return false, nil
		}
		_ = stream.Send(&hermesv1.BridgeLoginUpdate{
			Event: &hermesv1.BridgeLoginUpdate_Progress{
				Progress: &hermesv1.BridgeLoginProgress{
					Stage:  upd.Progress.Stage.String(),
					Detail: upd.Progress.Detail,
				},
			},
		})
		return false, nil

	case UpdateKindPrompt:
		if upd.Prompt == nil {
			return false, nil
		}
		// TOTP auto-fill check.
		if h.tryAutoFillTOTP(driver, start, upd.Prompt) {
			// Auto-filled — don't surface to client. Driver will emit
			// further updates (next prompt or terminal).
			return false, nil
		}
		_ = stream.Send(&hermesv1.BridgeLoginUpdate{
			Event: &hermesv1.BridgeLoginUpdate_Prompt{
				Prompt: promptToProto(upd.Prompt),
			},
		})
		return false, nil

	case UpdateKindSuccess:
		if upd.Success == nil {
			return true, mapBridgeErr(hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_INTERNAL,
				"driver emitted UpdateKindSuccess with nil Success body")
		}
		if upd.Success.Creds == nil {
			return true, mapBridgeErr(hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_INTERNAL,
				"driver Success.Creds is nil")
		}
		if err := h.persistBridgeSuccess(stream.Context(), tenantID, start, upd.Success); err != nil {
			// Distinguish security-relevant tenant collision from
			// generic persist failure. Tenant collision → fail
			// closed with PermissionDenied + a distinct outcome
			// metric ("failure_tenant_collision") so we can alert
			// on attempted cross-tenant overwrites.
			if errors.Is(err, store.ErrTenantMismatch) {
				_ = stream.Send(failureUpdate(hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_INTERNAL,
					"session uid is bound to a different tenant", false))
				h.recordBridgeOutcome("failure_tenant_collision")
				h.log.Warn().
					Int64("uid", upd.Success.Creds.UserID).
					Str("attempting_tenant", tenantID).
					Msg("BridgeLogin: BLOCKED cross-tenant overwrite attempt")
				return true, status.Error(codes.PermissionDenied,
					"session uid is bound to a different tenant")
			}
			// Persist failed — emit failure to client + close stream.
			_ = stream.Send(failureUpdate(hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_INTERNAL,
				"persist: "+err.Error(), false))
			h.recordBridgeOutcome("failure_persist")
			return true, mapBridgeErr(hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_INTERNAL,
				"persist failed: "+err.Error())
		}
		// Lifecycle event — session went UNSPECIFIED → ACTIVE via "created".
		h.publisher.PublishSessionLifecycle(
			upd.Success.Creds.UserID, tenantID,
			hermesv1.MbsSessionState_MBS_SESSION_STATE_UNSPECIFIED,
			hermesv1.MbsSessionState_MBS_SESSION_STATE_ACTIVE,
			"created", 0, h.podID,
		)
		// Send success to client.
		_ = stream.Send(&hermesv1.BridgeLoginUpdate{
			Event: &hermesv1.BridgeLoginUpdate_Success{
				Success: successToProto(upd.Success),
			},
		})
		h.recordBridgeOutcome("success")
		return true, nil

	case UpdateKindFailure:
		if upd.Failure == nil {
			return true, mapBridgeErr(hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_INTERNAL,
				"driver emitted UpdateKindFailure with nil Failure body")
		}
		_ = stream.Send(&hermesv1.BridgeLoginUpdate{
			Event: &hermesv1.BridgeLoginUpdate_Failure{
				Failure: &hermesv1.BridgeLoginFailure{
					Code:      upd.Failure.Code.String(),
					Message:   upd.Failure.Message,
					Retryable: upd.Failure.Retryable,
				},
			},
		})
		h.recordBridgeOutcome("failure_" + sanitizeOutcomeLabel(upd.Failure.Code.String()))
		return true, mapBridgeErr(upd.Failure.Code, upd.Failure.Message)

	default:
		// Unknown kind — log + skip.
		h.log.Warn().Int("kind", int(upd.Kind)).Msg("BridgeLogin: unknown driver update kind")
		return false, nil
	}
}

// bridgeReaderLoop drains client→handler messages. Submits Inputs to
// the driver; Cancel triggers driverCancel; stream EOF or
// stop signal exits cleanly.
//
// Concurrency: this goroutine blocks in stream.Recv() between messages.
// stream.Recv() in production unblocks when the gRPC framework tears
// down the stream (handler returned). In tests, fakeBridgeStream.Recv()
// unblocks when its ctx is cancelled — which the test fixture must
// arrange. The `stop` channel is checked between Recv calls to short-
// circuit when possible.
func (h *Handler) bridgeReaderLoop(
	stream hermesv1.HermesMbs_BridgeLoginServer,
	driver Driver,
	driverCancel context.CancelFunc,
	stop <-chan struct{},
) {
	for {
		select {
		case <-stop:
			return
		default:
		}
		msg, err := stream.Recv()
		if err != nil {
			// EOF, ctx.Done, or transport err — main loop owns the
			// terminal status.
			return
		}
		select {
		case <-stop:
			return
		default:
		}
		switch payload := msg.Payload.(type) {
		case *hermesv1.BridgeLoginRequest_Input:
			if payload.Input == nil {
				continue
			}
			if err := driver.Submit(DriverInput{
				FieldID: payload.Input.FieldId,
				Value:   payload.Input.Value,
			}); err != nil {
				h.log.Warn().Err(err).Str("field", payload.Input.FieldId).
					Msg("BridgeLogin: driver.Submit failed")
			}
		case *hermesv1.BridgeLoginRequest_Cancel:
			driverCancel()
			return
		case *hermesv1.BridgeLoginRequest_Start:
			// Spurious second Start — log + ignore.
			h.log.Warn().Msg("BridgeLogin: ignoring duplicate Start after first")
		}
	}
}

// tryAutoFillTOTP returns true if the prompt was auto-handled. Looks
// for StepID="two_step_verification" with a field id "totp_code".
// Requires start.TotpSecret to be set (base32). If TOTP derivation
// fails, fall through (surface the prompt to the client so they can
// enter it manually).
func (h *Handler) tryAutoFillTOTP(driver Driver, start *hermesv1.BridgeLoginStart, prompt *DriverPrompt) bool {
	if start.TotpSecret == "" {
		return false
	}
	if prompt.StepID != "two_step_verification" {
		return false
	}
	var hasTOTPField bool
	for _, f := range prompt.Fields {
		if f.ID == "totp_code" {
			hasTOTPField = true
			break
		}
	}
	if !hasTOTPField {
		return false
	}
	code, err := totp.GenerateCode(start.TotpSecret, time.Now())
	if err != nil {
		h.log.Warn().Err(err).Msg("BridgeLogin: TOTP derivation failed; falling through to prompt")
		return false
	}
	if err := driver.Submit(DriverInput{FieldID: "totp_code", Value: code}); err != nil {
		h.log.Warn().Err(err).Msg("BridgeLogin: auto-Submit TOTP failed; falling through to prompt")
		return false
	}
	return true
}

// ─────────────────────────────────────────────────────────────────────
// Persistence
// ─────────────────────────────────────────────────────────────────────

// persistBridgeSuccess encrypts secrets + writes the session row +
// assets in one logical operation. Each encrypt uses column-bound AAD
// so a column-swap attack against the DB ciphertext fails by
// construction.
//
// Order:
//
//   1. Encrypt access_token, secret, session_key  (3 column-bound AAD ops)
//   2. Marshal BridgeEnvelope (cookies live in here; encrypt as a unit)
//   3. Encrypt cookies (the marshaled envelope JSON) under "cookies" AAD
//   4. Encrypt totp_secret if PersistTotpSecret was set
//   5. Compose SessionRow + CreateSession (or update path for existing)
//   6. UpsertAssets (only after session row exists, FK satisfied)
//   7. SetPrimaryAsset
//
// On ANY error, NOTHING is partially written: encryption is pure (no
// side-effects), and the first DB call is CreateSession. UpsertAssets
// failing after CreateSession succeeded is the only "half-state"
// possible — the session row exists with empty assets — but that's a
// recoverable state (asset rediscovery is idempotent).
func (h *Handler) persistBridgeSuccess(
	ctx context.Context,
	tenantID string,
	start *hermesv1.BridgeLoginStart,
	success *DriverSuccess,
) error {
	uid := success.Creds.UserID
	if uid == 0 {
		return errors.New("driver Creds.UserID is zero")
	}

	// 1. Encrypt the 3 secret token fields.
	encAT, err := crypto.EncryptAESGCM(h.dek, []byte(success.Creds.AccessToken),
		store.BuildAAD(store.AADAccessToken, uid))
	if err != nil {
		return fmt.Errorf("encrypt access_token: %w", err)
	}
	encSec, err := crypto.EncryptAESGCM(h.dek, []byte(success.Creds.Secret),
		store.BuildAAD(store.AADSecret, uid))
	if err != nil {
		return fmt.Errorf("encrypt secret: %w", err)
	}
	encSK, err := crypto.EncryptAESGCM(h.dek, []byte(success.Creds.SessionKey),
		store.BuildAAD(store.AADSessionKey, uid))
	if err != nil {
		return fmt.Errorf("encrypt session_key: %w", err)
	}

	// 2-3. Encrypt cookies (marshaled BridgeEnvelope JSON if provided).
	var encCookies []byte
	var envelopeJSON []byte
	if success.BridgeEnvelope != nil {
		envelopeJSON, err = json.Marshal(success.BridgeEnvelope)
		if err != nil {
			return fmt.Errorf("marshal bridge envelope: %w", err)
		}
		encCookies, err = crypto.EncryptAESGCM(h.dek, envelopeJSON,
			store.BuildAAD(store.AADCookies, uid))
		if err != nil {
			return fmt.Errorf("encrypt cookies: %w", err)
		}
	}

	// 4. Encrypt totp_secret if persistence requested.
	var encTOTP []byte
	if start.PersistTotpSecret && start.TotpSecret != "" {
		encTOTP, err = crypto.EncryptAESGCM(h.dek, []byte(start.TotpSecret),
			store.BuildAAD(store.AADTOTPSecret, uid))
		if err != nil {
			return fmt.Errorf("encrypt totp_secret: %w", err)
		}
	}

	// 5. Compose SessionRow. DisplayName from driver; identity fields
	// copied from Creds (these mirror what client.New will read back).
	now := time.Now()
	row := &store.SessionRow{
		UID:                  uid,
		TenantID:             tenantID,
		DisplayName:          success.DisplayName,
		LoginEmail:           start.Email, // operator's login identifier; display-only
		State:                "active",
		PodID:                h.podID,
		EncryptedAccessToken: encAT,
		EncryptedSecret:      encSec,
		EncryptedSessionKey:  encSK,
		EncryptedCookies:     encCookies,
		EncryptedTOTPSecret:  encTOTP,
		BridgeEnvelope:       envelopeJSON, // plaintext metadata (non-secret)
		MachineID:            success.Creds.MachineID,
		DeviceID:             success.Creds.DeviceID,
		FamilyDeviceID:       success.Creds.FamilyDeviceID,
		AppVersion:           success.Creds.AppVersion,
		BuildNumber:          success.Creds.BuildNumber,
		DeviceModel:          success.Creds.DeviceModel,
		AndroidVer:           success.Creds.AndroidVer,
		Manufacturer:         success.Creds.Manufacturer,
		Locale:               success.Creds.Locale,
		Density:              success.Creds.Density,
		ScreenWidth:          success.Creds.Width,
		ScreenHeight:         success.Creds.Height,
		ABI:                  success.Creds.Abi,
		VersionID:            success.Creds.VersionID,
		MQTTCapabilities:     int(success.Creds.MqttCapabilities),
		LastValidatedAt:      &now,
		LastRefreshedAt:      &now,
		CreatedAt:            now,
		UpdatedAt:            now,
	}

	// Upsert pattern: try CreateSession first; on "already exists"
	// fall through to UpdateSessionTokens + UpdateSessionCookies.
	// This handles the re-bridge case (Sam re-runs BridgeLogin on the
	// same uid to refresh creds after a burn).
	if err := h.store.CreateSession(ctx, row); err != nil {
		// CreateSession failed. Three cases:
		//   1. Row already exists, same tenant  → legitimate re-bridge
		//   2. Row already exists, DIFFERENT tenant → 🚨 SECURITY: fail closed
		//   3. Row does NOT exist                → real create error, propagate
		//
		// We MUST distinguish case 2 from case 1, otherwise a bridge
		// attempt by tenant-A for a Facebook account that's already
		// bound to tenant-B silently overwrites tenant-B's encrypted
		// tokens (the tenant_id on the row stays tenant-B, so
		// tenant-B's RPCs still pass the cross-check — but the
		// secrets they decrypt are now attacker-supplied). See
		// .hermes/plans/2026-05-27_stage-e1-chunk4-step10-hostile-audit.md F1.
		existing, getErr := h.store.GetSession(ctx, uid)
		if getErr != nil {
			if errors.Is(getErr, store.ErrNotFound) {
				// Case 3: no existing row, original create error wins.
				return fmt.Errorf("create session: %w", err)
			}
			return fmt.Errorf("create session and read-back both failed: create=%v read=%w", err, getErr)
		}
		if existing.TenantID != tenantID {
			// Case 2: cross-tenant collision. Persist NOTHING. Wrap
			// store.ErrTenantMismatch so the outer dispatch maps
			// this to PermissionDenied (see handleDriverUpdate).
			return fmt.Errorf("uid %d is owned by a different tenant: %w",
				uid, store.ErrTenantMismatch)
		}
		// Case 1: legitimate re-bridge. Update tokens + cookies.
		// We don't refresh every plaintext identity field — those
		// are mostly stable.
		if uErr := h.store.UpdateSessionTokens(ctx, uid, encAT, encSec, encSK); uErr != nil {
			return fmt.Errorf("update tokens on re-bridge: %w", uErr)
		}
		if len(encCookies) > 0 {
			if cErr := h.store.UpdateSessionCookies(ctx, uid, encCookies, now, now); cErr != nil {
				return fmt.Errorf("update cookies on re-bridge: %w", cErr)
			}
		}
		// Reset state to active in case the prior session was burned.
		_ = h.store.UpdateSessionState(ctx, uid, "active", nil)
	}

	// 6. UpsertAssets.
	if len(success.Assets) > 0 {
		if err := h.store.UpsertAssets(ctx, uid, success.Assets); err != nil {
			return fmt.Errorf("upsert assets: %w", err)
		}
	}
	// 7. SetPrimaryAsset.
	if success.PrimaryAsset != nil && success.PrimaryAsset.PageID != "" {
		if err := h.store.SetPrimaryAsset(ctx, uid, success.PrimaryAsset.PageID); err != nil {
			// Non-fatal — IsPrimary flag in UpsertAssets is the
			// fallback path. Log and continue.
			h.log.Warn().Err(err).Int64("uid", uid).Str("page", success.PrimaryAsset.PageID).
				Msg("BridgeLogin: SetPrimaryAsset failed (non-fatal)")
		}
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────

// acquireBridgeSlot tries to acquire a semaphore token within
// h.bridgeAcquireTimeout. Returns ResourceExhausted on timeout.
func (h *Handler) acquireBridgeSlot(ctx context.Context) error {
	select {
	case h.bridgeSem <- struct{}{}:
		return nil
	default:
	}
	// Bounded wait.
	timer := time.NewTimer(h.bridgeAcquireTimeout)
	defer timer.Stop()
	select {
	case h.bridgeSem <- struct{}{}:
		return nil
	case <-timer.C:
		if h.metrics != nil && h.metrics.DriverSemaphoreFull != nil {
			h.metrics.DriverSemaphoreFull.Inc()
		}
		return status.Error(codes.ResourceExhausted, "bridge login concurrency limit reached")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *Handler) releaseBridgeSlot() {
	select {
	case <-h.bridgeSem:
	default:
		// Should never happen — acquire paired with release.
		h.log.Error().Msg("BridgeLogin: release on empty semaphore")
	}
}

// failureUpdate builds a BridgeLoginUpdate{failure: ...}.
func failureUpdate(code hermesv1.BridgeLoginErrorCode, msg string, retryable bool) *hermesv1.BridgeLoginUpdate {
	return &hermesv1.BridgeLoginUpdate{
		Event: &hermesv1.BridgeLoginUpdate_Failure{
			Failure: &hermesv1.BridgeLoginFailure{
				Code:      code.String(),
				Message:   msg,
				Retryable: retryable,
			},
		},
	}
}

// promptToProto converts a DriverPrompt to BridgeLoginPrompt.
func promptToProto(p *DriverPrompt) *hermesv1.BridgeLoginPrompt {
	fields := make([]*hermesv1.BridgeLoginField, 0, len(p.Fields))
	for _, f := range p.Fields {
		fields = append(fields, &hermesv1.BridgeLoginField{
			Id: f.ID, Name: f.Name, Type: f.Type,
		})
	}
	return &hermesv1.BridgeLoginPrompt{
		StepId:       p.StepID,
		Instructions: p.Instructions,
		Fields:       fields,
	}
}

// successToProto converts a DriverSuccess to BridgeLoginSuccess.
func successToProto(s *DriverSuccess) *hermesv1.BridgeLoginSuccess {
	out := &hermesv1.BridgeLoginSuccess{
		Uid:         s.Creds.UserID,
		DisplayName: s.DisplayName,
		PageCount:   int32(len(s.Assets)),
		Assets:      assetRowsToProto(s.Assets),
	}
	if s.PrimaryAsset != nil {
		out.PrimaryPageId = s.PrimaryAsset.PageID
		out.PrimaryPageName = s.PrimaryAsset.PageName
		out.PrimaryWabaId = s.PrimaryAsset.WabaID
		out.PrimaryWecMailboxId = s.PrimaryAsset.WecMailboxID
		out.PrimaryWecPhoneNumber = s.PrimaryAsset.WecPhoneNumber
	}
	return out
}

// redactEmail returns the email with the local-part partially masked
// for log enrichment. "alice@example.com" → "al***@example.com".
// Empty input → "".
func redactEmail(email string) string {
	at := strings.IndexByte(email, '@')
	if at <= 0 {
		return ""
	}
	local := email[:at]
	domain := email[at:]
	if len(local) <= 2 {
		return strings.Repeat("*", len(local)) + domain
	}
	return local[:2] + "***" + domain
}

// sanitizeOutcomeLabel converts a BridgeLoginErrorCode.String() like
// "BRIDGE_ERR_INVALID_CREDS" into a Prometheus-friendly lowercase
// fragment.
func sanitizeOutcomeLabel(s string) string {
	s = strings.TrimPrefix(s, "BRIDGE_ERR_")
	return strings.ToLower(s)
}

// recordBridgeOutcome increments BridgeLogins{outcome=...}. nil-safe.
func (h *Handler) recordBridgeOutcome(outcome string) {
	if h.metrics == nil || h.metrics.BridgeLogins == nil {
		return
	}
	h.metrics.BridgeLogins.WithLabelValues(outcome).Inc()
}

// Force-import auth so the package is in the dep graph for tests that
// only build the handler.
var _ = sync.Mutex{}
var _ = auth.Creds{}
