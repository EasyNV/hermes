package bridge

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/hermes-waba/hermes/internal/mbs/handler"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/pquerna/otp/totp"
	"github.com/rs/zerolog"
	"go.mau.fi/mautrix-meta/pkg/messagix"
	"go.mau.fi/mautrix-meta/pkg/messagix/cookies"
	"maunium.net/go/mautrix/bridgev2"
)

// LoginClient is the exported alias of loginClient. Integration tests
// outside the bridge package need to inject a fake; the alias keeps
// the interface contract public without re-stating the shape.
//
// Production code should NEVER implement this directly — use
// productionClientFactory (or bridge.NewDriverFactory with default
// ClientFactory) so the real *messagix.Client is wired correctly.
type LoginClient = loginClient

// loginClient is the narrow surface loginLoop needs from a mautrix-meta
// MessengerLite client. Production wraps the real *messagix.Client; tests
// inject a scripted fake.
//
// Three methods only:
//
//	DoLoginSteps — drives one transition. Returns either a step (needs
//	  user input or display) or nil+cookies (terminal success). Errors
//	  bubble up via classifyMautrixErr.
//	LastLoginPayload — accessor for the post-success payload. Only valid
//	  to call after DoLoginSteps returns (nil, cookies, nil).
//	LoginIdentity — accessor for the post-login device identity. Same
//	  valid-after-success rule.
type loginClient interface {
	DoLoginSteps(ctx context.Context, userInput map[string]string) (*bridgev2.LoginStep, *cookies.Cookies, error)
	LastLoginPayload() *messagix.BloksLoginActionResponsePayload
	LoginIdentity() (deviceID, familyDeviceID, machineID string)
}

// messagixLoginClient adapts a *messagix.Client to loginClient. Lives
// in this file (not envelope.go) because it's only used by the loop +
// integration tests; envelope.go has its own adapter for the identity
// branch.
type messagixLoginClient struct {
	c *messagix.Client
}

func (m *messagixLoginClient) DoLoginSteps(ctx context.Context, userInput map[string]string) (*bridgev2.LoginStep, *cookies.Cookies, error) {
	return m.c.MessengerLite.DoLoginSteps(ctx, userInput)
}

func (m *messagixLoginClient) LastLoginPayload() *messagix.BloksLoginActionResponsePayload {
	return m.c.MessengerLite.LastLoginPayload
}

func (m *messagixLoginClient) LoginIdentity() (string, string, string) {
	return m.c.MessengerLite.GetLoginIdentity()
}

// maxLoginSteps caps the state-machine loop. mautrix-meta's real flows
// peak at ~12 steps (email/pw → checkpoint → 2FA → AFAD → finalize); 32
// is generous headroom that still bounds runaway loops if the server
// ever streams an unterminated sequence. Matches the POC default.
const maxLoginSteps = 32

// inputChannelBuffer is the size of MautrixDriver.inputs. Holds at
// most one outstanding 2FA + captcha pair — typical real-world prompts
// are sequential, so this is comfortable headroom.
const inputChannelBuffer = 4

// displayWaitInterval is how long DisplayAndWait steps sleep before
// re-polling. AFAD (approve-from-another-device) flows want a few
// seconds between polls; longer wastes user time, shorter pounds the
// server. 3s matches the POC default.
const displayWaitInterval = 3 * time.Second

// loginLoopRunner is the per-attempt state machine. One instance per
// MautrixDriver.Run call; not reusable.
//
// Inputs:
//
//	ctx       — driver-owned context. Cancel → loop exits within
//	            displayWaitInterval (worst case during a sleep).
//	client    — loginClient adapter (real or fake).
//	req       — handler-supplied start request (email/pw + optional
//	            TOTPSecret + ForceNewDeviceID + tenant tag).
//	updates   — outbound channel; loop emits DriverUpdate events.
//	            Closed by the loop when the attempt terminates.
//	inputs    — inbound channel; driver.Submit writes here. Loop reads
//	            when a Prompt is outstanding.
//	discoverer — AssetDiscoverer for post-success enrichment.
//	log       — per-attempt logger (carries tenant + redacted email).
//	awaitTimeout — per-prompt timeout. Default 120s.
type loginLoopRunner struct {
	ctx          context.Context
	client       loginClient
	req          handler.DriverStartRequest
	updates      chan<- handler.DriverUpdate
	inputs       <-chan handler.DriverInput
	discoverer   AssetDiscoverer
	log          zerolog.Logger
	awaitTimeout time.Duration

	// userInput accumulates field → value pairs across step iterations.
	// Some flows (TOTP) re-request the same field on each pass (mautrix
	// pattern); the special-case in collectField overwrites totp_code
	// every time.
	userInput map[string]string

	// totpNormalized is the cached normalized base32 from req.TOTPSecret.
	// Set lazily on first totp_code prompt. Empty string means "no
	// secret supplied".
	totpNormalized string
}

// run drives the login state machine to terminal. Returns nothing; the
// terminal status (Success/Failure) is on the updates channel.
//
// Closure semantics: this function ALWAYS closes `updates` on exit
// (success, failure, panic, ctx cancel). Caller (MautrixDriver.Run)
// relies on close as the "loop done" signal.
func (r *loginLoopRunner) run() {
	defer close(r.updates)
	defer func() {
		if rec := recover(); rec != nil {
			r.log.Error().Interface("panic", rec).Msg("loginLoop: panic recovered")
			r.emitFailure(hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_INTERNAL,
				fmt.Sprintf("internal panic: %v", rec), false)
		}
	}()

	r.userInput = map[string]string{
		"username": r.req.Email,
		"password": r.req.Password,
	}

	r.emitProgress(hermesv1.BridgeLoginStage_BRIDGE_STAGE_CALLING_CAA, "starting login")

	for step := 0; step < maxLoginSteps; step++ {
		if err := r.ctx.Err(); err != nil {
			// Context already dead — don't emit a failure (the
			// outer handler maps ctx errors to gRPC Canceled).
			r.log.Debug().Err(err).Msg("loginLoop: ctx done")
			return
		}

		loginStep, finalCookies, err := r.client.DoLoginSteps(r.ctx, r.userInput)
		if err != nil {
			classified := classifyMautrixErr(err)
			r.log.Warn().Err(err).
				Str("code", classified.Code.String()).
				Msg("loginLoop: DoLoginSteps failed")
			r.emitFailure(classified.Code, classified.Message, classified.Retryable)
			return
		}

		if loginStep == nil {
			// Terminal success.
			r.handleSuccess(finalCookies)
			return
		}

		switch loginStep.Type {
		case bridgev2.LoginStepTypeUserInput:
			if !r.handleUserInputStep(loginStep) {
				return
			}
		case bridgev2.LoginStepTypeDisplayAndWait:
			r.emitProgress(hermesv1.BridgeLoginStage_BRIDGE_STAGE_AWAITING_2FA, loginStep.StepID)
			select {
			case <-r.ctx.Done():
				return
			case <-time.After(displayWaitInterval):
			}
		case bridgev2.LoginStepTypeComplete:
			// Some mautrix flows emit Complete as a non-nil terminal
			// step (instead of step==nil). Treat as success.
			r.handleSuccess(finalCookies)
			return
		case bridgev2.LoginStepTypeCookies:
			// mautrix-meta MessengerLite NEVER emits Cookies in the
			// CAA login flow today (Cookies is the QR-paired browser-
			// session-import pattern used by IG/Facebook web). If we
			// ever see one here, mautrix-meta upstream changed shape
			// and we need to update the bridge handler accordingly.
			// Distinct error (not generic INTERNAL) so operators
			// catch it in metrics immediately.
			//
			// See .hermes/plans/2026-05-29_stage-e1-chunk5-step8-hostile-audit.md F3.
			r.emitFailure(
				hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_INTERNAL,
				fmt.Sprintf("unexpected LoginStepTypeCookies (step_id=%s) — "+
					"mautrix-meta MessengerLite upstream changed shape; "+
					"bridge package needs to handle Cookies steps",
					loginStep.StepID),
				false,
			)
			return
		default:
			r.emitFailure(
				hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_INTERNAL,
				fmt.Sprintf("unsupported login step type %q (step_id=%s)",
					loginStep.Type, loginStep.StepID),
				false,
			)
			return
		}
	}

	r.emitFailure(
		hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_INTERNAL,
		fmt.Sprintf("exhausted %d login steps without success", maxLoginSteps),
		false,
	)
}

// handleUserInputStep processes one LoginStepTypeUserInput step.
// Returns true if the loop should continue, false if it should
// terminate (because handleField emitted a failure or ctx cancelled).
//
// For each field:
//   - Already in userInput AND not totp_code → skip (mautrix re-asks
//     totp_code every pass because codes are time-sensitive).
//   - field.ID == "totp_code" AND req.TOTPSecret set → auto-fill via
//     pquerna/otp.
//   - otherwise → emit Prompt + wait for user Submit on inputs channel.
func (r *loginLoopRunner) handleUserInputStep(step *bridgev2.LoginStep) bool {
	if step.UserInputParams == nil {
		r.emitFailure(
			hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_INTERNAL,
			fmt.Sprintf("UserInput step %q with nil params", step.StepID),
			false,
		)
		return false
	}

	for _, field := range step.UserInputParams.Fields {
		// Skip fields we already have. Special-case totp_code:
		// mautrix re-asks every step on TOTP flows and we want
		// a fresh code each pass.
		if _, has := r.userInput[field.ID]; has && field.ID != "totp_code" {
			continue
		}
		if !r.collectField(step, field) {
			return false
		}
	}
	return true
}

// collectField populates r.userInput[field.ID] for one field. Returns
// true if collected, false if loop should terminate (emit a failure).
func (r *loginLoopRunner) collectField(step *bridgev2.LoginStep, field bridgev2.LoginInputDataField) bool {
	// Auto-fill TOTP.
	if field.ID == "totp_code" && r.req.TOTPSecret != "" {
		if code, err := r.deriveTOTPCode(); err == nil {
			r.userInput[field.ID] = code
			r.log.Debug().Msg("loginLoop: TOTP auto-fill applied")
			return true
		} else {
			r.log.Warn().Err(err).Msg("loginLoop: TOTP derivation failed; falling through to prompt")
		}
	}

	// Surface a Prompt to the handler. Block (bounded) on Submit
	// matching this field. Handler may auto-fill on its side; either
	// works.
	r.emitPrompt(step, []bridgev2.LoginInputDataField{field})

	timer := time.NewTimer(r.awaitTimeout)
	defer timer.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return false
		case <-timer.C:
			r.emitFailure(
				hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_2FA_REQUIRED,
				fmt.Sprintf("timed out waiting for %s after %s", field.ID, r.awaitTimeout),
				true,
			)
			return false
		case input, ok := <-r.inputs:
			if !ok {
				r.emitFailure(
					hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_INTERNAL,
					"input channel closed before user response",
					false,
				)
				return false
			}
			if input.FieldID != field.ID {
				// Stale or wrong-field input. Log + keep waiting.
				r.log.Debug().
					Str("got", input.FieldID).
					Str("want", field.ID).
					Msg("loginLoop: ignoring non-matching field input")
				continue
			}
			r.userInput[field.ID] = input.Value
			return true
		}
	}
}

// deriveTOTPCode normalizes r.req.TOTPSecret once + computes the
// current 6-digit code. Cached normalization across loop iterations
// in r.totpNormalized.
func (r *loginLoopRunner) deriveTOTPCode() (string, error) {
	if r.totpNormalized == "" {
		norm, err := normalizeTOTPSecret(r.req.TOTPSecret)
		if err != nil {
			return "", err
		}
		r.totpNormalized = norm
	}
	return totp.GenerateCode(r.totpNormalized, time.Now())
}

// handleSuccess wraps post-login work: build envelope, materialize
// creds, discover assets, emit Success.
func (r *loginLoopRunner) handleSuccess(finalCookies *cookies.Cookies) {
	r.emitProgress(hermesv1.BridgeLoginStage_BRIDGE_STAGE_DISCOVERING_ASSETS, "")

	payload := r.client.LastLoginPayload()
	if payload == nil {
		r.emitFailure(
			hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_INTERNAL,
			"login terminated successfully but LastLoginPayload is nil",
			false,
		)
		return
	}

	// Wrap loginClient as loginIdentityProvider for envelope builder.
	identity := loginIdentityAdapter{client: r.client}
	envelope := buildBridgeEnvelope(payload, finalCookies, identity)

	creds, err := envelopeToCreds(envelope)
	if err != nil {
		r.emitFailure(
			hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_INTERNAL,
			fmt.Sprintf("materialize creds: %v", err),
			false,
		)
		return
	}

	// Asset discovery is non-fatal. On error, emit Success with empty
	// assets; refresh-tick or operator can rediscover later.
	var success = &handler.DriverSuccess{
		UID:            payload.UID,
		DisplayName:    extractDisplayName(payload),
		Creds:          creds,
		BridgeEnvelope: envelope,
	}
	if r.discoverer != nil {
		rows, primary, derr := r.discoverer.DiscoverFromCreds(r.ctx, creds)
		if derr != nil {
			// Distinguish "external Meta call failed" from "we ran
			// out of attempt-time mid-discovery." Both produce empty
			// Assets but the operator response is different — the
			// former needs network investigation, the latter just
			// needs the timeout bump.
			ev := r.log.Warn().Err(derr)
			if cerr := r.ctx.Err(); cerr != nil {
				ev = ev.AnErr("ctx_err", cerr)
			}
			ev.Msg("loginLoop: asset discovery failed; emitting Success with empty assets")
		} else {
			success.Assets = rows
			success.PrimaryAsset = primary
		}
	}

	r.emitSuccess(success)
}

// loginIdentityAdapter wraps a loginClient as a loginIdentityProvider
// so envelope.go's buildBridgeEnvelope can pull device-identity values.
type loginIdentityAdapter struct{ client loginClient }

func (l loginIdentityAdapter) LoginIdentity() (string, string, string) {
	return l.client.LoginIdentity()
}

// ─────────────────────────────────────────────────────────────────────
// Emit helpers
// ─────────────────────────────────────────────────────────────────────

func (r *loginLoopRunner) emitProgress(stage hermesv1.BridgeLoginStage, detail string) {
	select {
	case <-r.ctx.Done():
	case r.updates <- handler.DriverUpdate{
		Kind:     handler.UpdateKindProgress,
		Progress: &handler.DriverProgress{Stage: stage, Detail: detail},
	}:
	}
}

func (r *loginLoopRunner) emitPrompt(step *bridgev2.LoginStep, fields []bridgev2.LoginInputDataField) {
	mapped := make([]handler.DriverPromptField, 0, len(fields))
	for _, f := range fields {
		mapped = append(mapped, handler.DriverPromptField{
			ID:   f.ID,
			Name: f.Name,
			Type: string(f.Type),
		})
	}
	select {
	case <-r.ctx.Done():
	case r.updates <- handler.DriverUpdate{
		Kind: handler.UpdateKindPrompt,
		Prompt: &handler.DriverPrompt{
			StepID:       step.StepID,
			Instructions: step.Instructions,
			Fields:       mapped,
		},
	}:
	}
}

func (r *loginLoopRunner) emitSuccess(s *handler.DriverSuccess) {
	select {
	case <-r.ctx.Done():
	case r.updates <- handler.DriverUpdate{
		Kind:    handler.UpdateKindSuccess,
		Success: s,
	}:
	}
}

func (r *loginLoopRunner) emitFailure(code hermesv1.BridgeLoginErrorCode, msg string, retryable bool) {
	select {
	case <-r.ctx.Done():
	case r.updates <- handler.DriverUpdate{
		Kind: handler.UpdateKindFailure,
		Failure: &handler.DriverFailure{
			Code:      code,
			Message:   msg,
			Retryable: retryable,
		},
	}:
	}
}

// ─────────────────────────────────────────────────────────────────────
// Type aliases / housekeeping
// ─────────────────────────────────────────────────────────────────────

// inputsChannel returns a channel that pulls from `src` and feeds the
// loop's `inputs` field. Decouples MautrixDriver.Submit (writes to the
// driver's outbound channel) from the loop's reader. Currently a thin
// pass-through; reserved for filtering/batching if needed later.
func inputsChannel(src <-chan handler.DriverInput) <-chan handler.DriverInput {
	return src
}

// Compile-time guards.
var (
	_ loginClient = (*messagixLoginClient)(nil)
	_             = sync.Mutex{}     // keep sync imported for the test fixture
	_             = errors.New("")   // keep errors imported
)
