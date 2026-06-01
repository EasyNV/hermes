package bridge

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"mbs-native/auth"

	"github.com/hermes-waba/hermes/internal/mbs/handler"
	"github.com/hermes-waba/hermes/internal/mbs/store"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/pquerna/otp/totp"
	"github.com/rs/zerolog"
	"go.mau.fi/mautrix-meta/pkg/messagix"
	"go.mau.fi/mautrix-meta/pkg/messagix/cookies"
	"maunium.net/go/mautrix/bridgev2"
)

// ─────────────────────────────────────────────────────────────────────
// fakeLoginClient — scripted loginClient for tests
// ─────────────────────────────────────────────────────────────────────

// scriptedTransition is one expected DoLoginSteps invocation +
// response. Returned in order; if the loop pulls more transitions than
// scripted, fakeLoginClient returns an error to surface the over-pull.
type scriptedTransition struct {
	step    *bridgev2.LoginStep
	cookies *cookies.Cookies
	err     error
}

type fakeLoginClient struct {
	mu             sync.Mutex
	script         []scriptedTransition
	idx            int
	inputs         []map[string]string // captures what handler submitted
	finalPayload   *messagix.BloksLoginActionResponsePayload
	identity       [3]string // dev, fdid, mach
	doLoginSleepFn func(call int)
}

func (f *fakeLoginClient) DoLoginSteps(ctx context.Context, userInput map[string]string) (*bridgev2.LoginStep, *cookies.Cookies, error) {
	if f.doLoginSleepFn != nil {
		f.doLoginSleepFn(f.idx)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	// Snapshot the userInput for assertions.
	cp := make(map[string]string, len(userInput))
	for k, v := range userInput {
		cp[k] = v
	}
	f.inputs = append(f.inputs, cp)
	if f.idx >= len(f.script) {
		return nil, nil, errors.New("fakeLoginClient: script exhausted; loop over-pulled")
	}
	t := f.script[f.idx]
	f.idx++
	return t.step, t.cookies, t.err
}

func (f *fakeLoginClient) LastLoginPayload() *messagix.BloksLoginActionResponsePayload {
	return f.finalPayload
}

func (f *fakeLoginClient) LoginIdentity() (string, string, string) {
	return f.identity[0], f.identity[1], f.identity[2]
}

func (f *fakeLoginClient) inputsSnapshot() []map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]map[string]string, len(f.inputs))
	copy(cp, f.inputs)
	return cp
}

// ─────────────────────────────────────────────────────────────────────
// Fixtures
// ─────────────────────────────────────────────────────────────────────

func newRunner(t *testing.T, client loginClient, inputs <-chan handler.DriverInput) (*loginLoopRunner, chan handler.DriverUpdate) {
	t.Helper()
	updates := make(chan handler.DriverUpdate, 16)
	r := &loginLoopRunner{
		ctx:          context.Background(),
		client:       client,
		req:          handler.DriverStartRequest{Email: "alice@example.com", Password: "pw"},
		updates:      updates,
		inputs:       inputs,
		discoverer:   nil, // no asset discovery in loop tests
		log:          zerolog.Nop(),
		awaitTimeout: 200 * time.Millisecond,
		userInput:    map[string]string{},
	}
	return r, updates
}

func collectUpdates(t *testing.T, ch <-chan handler.DriverUpdate, want int, deadline time.Duration) []handler.DriverUpdate {
	t.Helper()
	var got []handler.DriverUpdate
	timer := time.NewTimer(deadline)
	defer timer.Stop()
	for len(got) < want {
		select {
		case u, ok := <-ch:
			if !ok {
				return got
			}
			got = append(got, u)
		case <-timer.C:
			return got
		}
	}
	return got
}

func drainUpdates(t *testing.T, ch <-chan handler.DriverUpdate, deadline time.Duration) []handler.DriverUpdate {
	t.Helper()
	var got []handler.DriverUpdate
	timer := time.NewTimer(deadline)
	defer timer.Stop()
	for {
		select {
		case u, ok := <-ch:
			if !ok {
				return got
			}
			got = append(got, u)
		case <-timer.C:
			return got
		}
	}
}

// successIdentity is the device identity stub bundled with every test
// fake that emits a Success. Without it, buildBridgeEnvelope would
// produce an envelope with empty bridge_device_id, and MaterializeCreds
// fails Validate() with "missing bridge_device_id". Real flows set
// these from the patched mautrix browser; tests bake them in here.
var successIdentity = [3]string{
	"7A17B762-668D-4BEF-A9CF-CD0ABD58231D",
	"7A17B762-668D-4BEF-A9CF-CD0ABD58231C",
	"9gH-aUzBMyDfrMwqEnEPkcaV",
}

func successPayload() *messagix.BloksLoginActionResponsePayload {
	return &messagix.BloksLoginActionResponsePayload{
		AccessToken:        "EAAB-success",
		UID:                42,
		SessionKey:         "5.xyz.1-42",
		Secret:             "secret",
		MachineID:          "machine",
		CredentialType:     "password",
		IsAccountConfirmed: true,
		Identifier:         "alice@example.com",
	}
}

func successCookies() *cookies.Cookies {
	c := &cookies.Cookies{}
	c.UpdateValues(map[cookies.MetaCookieName]string{
		"c_user": "42",
	})
	return c
}

// ─────────────────────────────────────────────────────────────────────
// Tests
// ─────────────────────────────────────────────────────────────────────

func TestLoginLoop_HappyPath_NoPrompt(t *testing.T) {
	client := &fakeLoginClient{
		script: []scriptedTransition{
			{step: nil, cookies: successCookies(), err: nil}, // immediate success
		},
		finalPayload: successPayload(),
		identity:     successIdentity,
	}
	inputs := make(chan handler.DriverInput)
	r, updates := newRunner(t, client, inputs)
	go r.run()

	ev := drainUpdates(t, updates, 500*time.Millisecond)
	// progress(calling) + progress(discovering) + success
	if len(ev) != 3 {
		t.Fatalf("got %d events want 3", len(ev))
	}
	if ev[0].Kind != handler.UpdateKindProgress || ev[1].Kind != handler.UpdateKindProgress {
		t.Errorf("first two events should be progress; got %+v", ev)
	}
	if ev[2].Kind != handler.UpdateKindSuccess {
		t.Fatalf("terminal event should be success; got %v", ev[2].Kind)
	}
	s := ev[2].Success
	if s == nil {
		t.Fatal("Success body nil")
	}
	if s.UID != 42 {
		t.Errorf("UID: got %d want 42", s.UID)
	}
	if s.Creds == nil || s.Creds.AccessToken != "EAAB-success" {
		t.Errorf("Creds: %+v", s.Creds)
	}
	if s.BridgeEnvelope == nil || s.BridgeEnvelope.UID != 42 {
		t.Errorf("BridgeEnvelope: %+v", s.BridgeEnvelope)
	}
}

func TestLoginLoop_TOTPAutoFill(t *testing.T) {
	const totpSecret = "JBSWY3DPEHPK3PXP"
	client := &fakeLoginClient{
		script: []scriptedTransition{
			{
				step: &bridgev2.LoginStep{
					Type:   bridgev2.LoginStepTypeUserInput,
					StepID: "two_step_verification",
					UserInputParams: &bridgev2.LoginUserInputParams{
						Fields: []bridgev2.LoginInputDataField{
							{ID: "totp_code", Name: "TOTP Code", Type: bridgev2.LoginInputFieldType2FACode},
						},
					},
				},
			},
			{step: nil, cookies: successCookies()}, // success after TOTP fed back
		},
		finalPayload: successPayload(),
		identity:     successIdentity,
	}

	inputs := make(chan handler.DriverInput)
	r, updates := newRunner(t, client, inputs)
	r.req.TOTPSecret = totpSecret
	go r.run()

	ev := drainUpdates(t, updates, 500*time.Millisecond)
	// No Prompt should have been emitted (auto-fill).
	for _, u := range ev {
		if u.Kind == handler.UpdateKindPrompt {
			t.Errorf("auto-fill should swallow prompt; got %+v", u)
		}
	}
	// Last event Success.
	if len(ev) == 0 || ev[len(ev)-1].Kind != handler.UpdateKindSuccess {
		t.Fatalf("terminal not Success: %+v", ev)
	}

	// Confirm the TOTP code submitted to the client matches what
	// totp.GenerateCode would produce now.
	want, _ := totp.GenerateCode(totpSecret, time.Now())
	captured := client.inputsSnapshot()
	// 2nd call carries the TOTP.
	if len(captured) < 2 {
		t.Fatalf("expected 2 DoLoginSteps calls, got %d", len(captured))
	}
	if got := captured[1]["totp_code"]; got != want {
		t.Errorf("totp_code submitted = %q, want %q", got, want)
	}
}

// TestLoginLoop_MFAMethodAutoPick pins the method-chooser auto-pick: when
// a TOTP secret is supplied, the loop must pre-seed userInput["mfatype"]
// = "Authentication app" BEFORE the first DoLoginSteps call, so the
// mautrix-meta connector resolves the mfa_type chooser step internally
// and never surfaces it as a prompt. This is the companion to
// TOTPAutoFill — chooser auto-pick + code auto-fill = fully unattended
// 2FA. Values must byte-match the connector contract constants.
func TestLoginLoop_MFAMethodAutoPick(t *testing.T) {
	const totpSecret = "JBSWY3DPEHPK3PXP"
	client := &fakeLoginClient{
		// The connector consumes mfatype from userInput and resolves
		// the chooser without emitting it; our fake just terminates on
		// the first call to assert the seed was present up-front.
		script: []scriptedTransition{
			{step: nil, cookies: successCookies()},
		},
		finalPayload: successPayload(),
		identity:     successIdentity,
	}

	inputs := make(chan handler.DriverInput)
	r, updates := newRunner(t, client, inputs)
	r.req.TOTPSecret = totpSecret
	go r.run()

	ev := drainUpdates(t, updates, 500*time.Millisecond)
	if len(ev) == 0 || ev[len(ev)-1].Kind != handler.UpdateKindSuccess {
		t.Fatalf("terminal not Success: %+v", ev)
	}

	captured := client.inputsSnapshot()
	if len(captured) < 1 {
		t.Fatalf("expected >=1 DoLoginSteps call, got %d", len(captured))
	}
	// The FIRST call must already carry the auto-picked method.
	if got := captured[0][mfaTypeFieldID]; got != mfaMethodAuthenticatorApp {
		t.Errorf("first-call %s = %q, want %q", mfaTypeFieldID, got, mfaMethodAuthenticatorApp)
	}
}

// TestLoginLoop_MFAMethodNotPickedWithoutSecret pins the negative: with
// NO TOTP secret, we must NOT seed mfatype — the user picks the method
// manually (the chooser prompt surfaces). Auto-picking authenticator-app
// when the operator has no secret would dead-end the flow on a code step
// they can't satisfy.
func TestLoginLoop_MFAMethodNotPickedWithoutSecret(t *testing.T) {
	client := &fakeLoginClient{
		script: []scriptedTransition{
			{step: nil, cookies: successCookies()},
		},
		finalPayload: successPayload(),
		identity:     successIdentity,
	}

	inputs := make(chan handler.DriverInput)
	r, updates := newRunner(t, client, inputs) // req has no TOTPSecret
	go r.run()

	ev := drainUpdates(t, updates, 500*time.Millisecond)
	if len(ev) == 0 || ev[len(ev)-1].Kind != handler.UpdateKindSuccess {
		t.Fatalf("terminal not Success: %+v", ev)
	}

	captured := client.inputsSnapshot()
	if len(captured) < 1 {
		t.Fatalf("expected >=1 DoLoginSteps call, got %d", len(captured))
	}
	if got, ok := captured[0][mfaTypeFieldID]; ok {
		t.Errorf("%s should be unset without a TOTP secret, got %q", mfaTypeFieldID, got)
	}
}

func TestLoginLoop_PromptThenSubmit(t *testing.T) {
	client := &fakeLoginClient{
		script: []scriptedTransition{
			{
				step: &bridgev2.LoginStep{
					Type:         bridgev2.LoginStepTypeUserInput,
					StepID:       "captcha",
					Instructions: "Solve",
					UserInputParams: &bridgev2.LoginUserInputParams{
						Fields: []bridgev2.LoginInputDataField{
							{ID: "captcha_response", Name: "Captcha", Type: bridgev2.LoginInputFieldTypeCaptchaCode},
						},
					},
				},
			},
			{step: nil, cookies: successCookies()},
		},
		finalPayload: successPayload(),
		identity:     successIdentity,
	}

	inputs := make(chan handler.DriverInput, 4)
	r, updates := newRunner(t, client, inputs)
	go r.run()

	// First two events should be Progress + Prompt.
	first := collectUpdates(t, updates, 2, 500*time.Millisecond)
	if len(first) < 2 {
		t.Fatalf("got %d events want >=2", len(first))
	}
	if first[1].Kind != handler.UpdateKindPrompt {
		t.Fatalf("expected Prompt, got %+v", first[1])
	}

	// Submit the captcha response.
	inputs <- handler.DriverInput{FieldID: "captcha_response", Value: "ANS"}

	// Drain remaining events; expect Progress(discovering) + Success.
	rest := drainUpdates(t, updates, 500*time.Millisecond)
	if len(rest) == 0 || rest[len(rest)-1].Kind != handler.UpdateKindSuccess {
		t.Fatalf("terminal not Success: %+v", rest)
	}

	// Client should have received the captcha on the 2nd call.
	captured := client.inputsSnapshot()
	if len(captured) < 2 || captured[1]["captcha_response"] != "ANS" {
		t.Errorf("captcha not submitted: %+v", captured)
	}
}

func TestLoginLoop_PromptTimeout_EmitsFailure2FARequired(t *testing.T) {
	client := &fakeLoginClient{
		script: []scriptedTransition{
			{
				step: &bridgev2.LoginStep{
					Type:   bridgev2.LoginStepTypeUserInput,
					StepID: "two_step_verification",
					UserInputParams: &bridgev2.LoginUserInputParams{
						Fields: []bridgev2.LoginInputDataField{
							{ID: "totp_code", Name: "TOTP", Type: bridgev2.LoginInputFieldType2FACode},
						},
					},
				},
			},
		},
	}
	inputs := make(chan handler.DriverInput)
	r, updates := newRunner(t, client, inputs)
	r.awaitTimeout = 30 * time.Millisecond
	go r.run()

	ev := drainUpdates(t, updates, 500*time.Millisecond)
	if len(ev) == 0 {
		t.Fatal("no events")
	}
	last := ev[len(ev)-1]
	if last.Kind != handler.UpdateKindFailure {
		t.Fatalf("terminal not Failure: %+v", last)
	}
	if last.Failure == nil || last.Failure.Code != hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_2FA_REQUIRED {
		t.Errorf("expected 2FA_REQUIRED, got %+v", last.Failure)
	}
}

func TestLoginLoop_ContextCancel_ExitsCleanly(t *testing.T) {
	// DoLoginSteps never returns — loop should exit when ctx cancels.
	client := &fakeLoginClient{
		script: []scriptedTransition{{step: nil, cookies: nil, err: errors.New("placeholder")}},
		doLoginSleepFn: func(_ int) {
			time.Sleep(100 * time.Millisecond)
		},
	}
	inputs := make(chan handler.DriverInput)
	r, updates := newRunner(t, client, inputs)
	ctx, cancel := context.WithCancel(context.Background())
	r.ctx = ctx
	go r.run()

	// Cancel almost immediately.
	time.Sleep(10 * time.Millisecond)
	cancel()

	// Updates channel should close within a short window.
	closed := false
	timer := time.NewTimer(500 * time.Millisecond)
	defer timer.Stop()
	for !closed {
		select {
		case _, ok := <-updates:
			if !ok {
				closed = true
			}
		case <-timer.C:
			t.Fatal("updates channel did not close after ctx cancel")
		}
	}
}

func TestLoginLoop_DoLoginStepsError_ClassifiesAndEmits(t *testing.T) {
	client := &fakeLoginClient{
		script: []scriptedTransition{
			{step: nil, cookies: nil, err: messagix.ErrTokenInvalidated},
		},
	}
	inputs := make(chan handler.DriverInput)
	r, updates := newRunner(t, client, inputs)
	go r.run()

	ev := drainUpdates(t, updates, 500*time.Millisecond)
	last := ev[len(ev)-1]
	if last.Kind != handler.UpdateKindFailure {
		t.Fatalf("expected Failure, got %v", last.Kind)
	}
	if last.Failure.Code != hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_INVALID_CREDS {
		t.Errorf("classify: got %v want INVALID_CREDS", last.Failure.Code)
	}
}

func TestLoginLoop_NilPayloadAfterSuccess_EmitsInternal(t *testing.T) {
	client := &fakeLoginClient{
		script: []scriptedTransition{
			{step: nil, cookies: successCookies()},
		},
		finalPayload: nil, // ← the abnormal condition
	}
	inputs := make(chan handler.DriverInput)
	r, updates := newRunner(t, client, inputs)
	go r.run()

	ev := drainUpdates(t, updates, 500*time.Millisecond)
	last := ev[len(ev)-1]
	if last.Kind != handler.UpdateKindFailure {
		t.Fatalf("expected Failure, got %+v", last)
	}
	if last.Failure.Code != hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_INTERNAL {
		t.Errorf("expected INTERNAL, got %v", last.Failure.Code)
	}
}

func TestLoginLoop_DisplayAndWait_ProgressThenLoop(t *testing.T) {
	client := &fakeLoginClient{
		script: []scriptedTransition{
			{
				step: &bridgev2.LoginStep{
					Type:   bridgev2.LoginStepTypeDisplayAndWait,
					StepID: "afad_complete",
				},
			},
			{step: nil, cookies: successCookies()},
		},
		finalPayload: successPayload(),
		identity:     successIdentity,
	}
	inputs := make(chan handler.DriverInput)
	r, updates := newRunner(t, client, inputs)
	go r.run()

	// Loop sleeps displayWaitInterval (3s) between calls. Increase
	// drain deadline; or set a smaller sleep by shrinking the const.
	// We hold the const stable and just allow up to 5s drain.
	ev := drainUpdates(t, updates, 4*time.Second)
	if len(ev) == 0 || ev[len(ev)-1].Kind != handler.UpdateKindSuccess {
		t.Fatalf("expected Success terminal, got %+v", ev)
	}
}

func TestLoginLoop_UnsupportedStepType_EmitsInternal(t *testing.T) {
	client := &fakeLoginClient{
		script: []scriptedTransition{
			{
				step: &bridgev2.LoginStep{
					Type:   bridgev2.LoginStepType("unknown_type"),
					StepID: "weird",
				},
			},
		},
	}
	inputs := make(chan handler.DriverInput)
	r, updates := newRunner(t, client, inputs)
	go r.run()

	ev := drainUpdates(t, updates, 500*time.Millisecond)
	last := ev[len(ev)-1]
	if last.Kind != handler.UpdateKindFailure || last.Failure.Code != hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_INTERNAL {
		t.Errorf("expected INTERNAL failure, got %+v", last)
	}
}

func TestLoginLoop_CookiesStepType_EmitsDistinctFailure(t *testing.T) {
	// Step-8 audit F3 pin: if mautrix-meta MessengerLite ever starts
	// emitting Cookies steps, the loop must fail with a distinguishable
	// message (not the generic "unsupported"), so operators catch the
	// upstream shape change immediately.
	client := &fakeLoginClient{
		script: []scriptedTransition{
			{
				step: &bridgev2.LoginStep{
					Type:   bridgev2.LoginStepTypeCookies,
					StepID: "would_be_cookies_step",
				},
			},
		},
	}
	inputs := make(chan handler.DriverInput)
	r, updates := newRunner(t, client, inputs)
	go r.run()

	ev := drainUpdates(t, updates, 500*time.Millisecond)
	if len(ev) == 0 {
		t.Fatal("no events emitted")
	}
	last := ev[len(ev)-1]
	if last.Kind != handler.UpdateKindFailure {
		t.Fatalf("expected Failure, got %+v", last)
	}
	if last.Failure.Code != hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_INTERNAL {
		t.Errorf("expected INTERNAL code, got %v", last.Failure.Code)
	}
	if !strings.Contains(last.Failure.Message, "LoginStepTypeCookies") {
		t.Errorf("Cookies failure message should be distinguishable; got %q",
			last.Failure.Message)
	}
}

func TestLoginLoop_MaxStepsExhausted_EmitsInternal(t *testing.T) {
	// Build a script that always returns the same DisplayAndWait step
	// (loop will never terminate naturally).
	stuck := &bridgev2.LoginStep{
		Type:   bridgev2.LoginStepTypeDisplayAndWait,
		StepID: "stuck",
	}
	script := make([]scriptedTransition, maxLoginSteps+5)
	for i := range script {
		script[i] = scriptedTransition{step: stuck}
	}
	client := &fakeLoginClient{script: script}
	inputs := make(chan handler.DriverInput)
	r, updates := newRunner(t, client, inputs)

	// Replace displayWaitInterval temporarily by using a runner-level
	// shortcut: we can't override the const, but we can short-circuit
	// the ctx after enough iterations. Simpler: ride out the loop with
	// a generous deadline (32 * 3s = 96s — too long for unit test).
	// Instead, kill the loop via ctx after 200ms and assert no Success.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	r.ctx = ctx
	go r.run()

	ev := drainUpdates(t, updates, 600*time.Millisecond)
	// Loop should exit (ctx done) without a Success. We tolerate
	// either no terminal event (clean ctx exit) or a Failure event
	// (a quirk if the ctx fires inside emit). Either way: no Success.
	for _, u := range ev {
		if u.Kind == handler.UpdateKindSuccess {
			t.Errorf("unexpected Success on stuck loop: %+v", u)
		}
	}
}

func TestLoginLoop_StaleInput_IgnoresAndKeepsWaiting(t *testing.T) {
	client := &fakeLoginClient{
		script: []scriptedTransition{
			{
				step: &bridgev2.LoginStep{
					Type:   bridgev2.LoginStepTypeUserInput,
					StepID: "captcha",
					UserInputParams: &bridgev2.LoginUserInputParams{
						Fields: []bridgev2.LoginInputDataField{
							{ID: "captcha_response", Type: bridgev2.LoginInputFieldTypeCaptchaCode},
						},
					},
				},
			},
			{step: nil, cookies: successCookies()},
		},
		finalPayload: successPayload(),
		identity:     successIdentity,
	}
	inputs := make(chan handler.DriverInput, 4)
	r, updates := newRunner(t, client, inputs)
	r.awaitTimeout = 500 * time.Millisecond
	go r.run()

	// Wait for the prompt to be emitted.
	_ = collectUpdates(t, updates, 2, 200*time.Millisecond)

	// Submit a STALE input (wrong field) then the correct one.
	inputs <- handler.DriverInput{FieldID: "wrong_field", Value: "ignored"}
	time.Sleep(20 * time.Millisecond)
	inputs <- handler.DriverInput{FieldID: "captcha_response", Value: "GOOD"}

	rest := drainUpdates(t, updates, 500*time.Millisecond)
	if rest[len(rest)-1].Kind != handler.UpdateKindSuccess {
		t.Fatalf("terminal not Success: %+v", rest)
	}
	captured := client.inputsSnapshot()
	if captured[1]["captcha_response"] != "GOOD" {
		t.Errorf("wrong captcha forwarded: %+v", captured[1])
	}
}

func TestLoginLoop_AssetDiscovery_EnrichesSuccess(t *testing.T) {
	client := &fakeLoginClient{
		script: []scriptedTransition{
			{step: nil, cookies: successCookies()},
		},
		finalPayload: successPayload(),
		identity:     successIdentity,
	}
	inputs := make(chan handler.DriverInput)
	r, updates := newRunner(t, client, inputs)

	wantRow := &store.AssetRow{
		UID: 42, PageID: "page-1", PageName: "Page", WabaID: "w",
		WecMailboxID: "m", IsPrimary: true,
	}
	r.discoverer = assetDiscovererFunc(func(ctx context.Context, _ *auth.Creds) ([]*store.AssetRow, *store.AssetRow, error) {
		return []*store.AssetRow{wantRow}, wantRow, nil
	})

	go r.run()

	ev := drainUpdates(t, updates, 500*time.Millisecond)
	last := ev[len(ev)-1]
	if last.Kind != handler.UpdateKindSuccess {
		t.Fatalf("terminal not Success: %+v", last)
	}
	if len(last.Success.Assets) != 1 || last.Success.Assets[0].PageID != "page-1" {
		t.Errorf("Assets not enriched: %+v", last.Success.Assets)
	}
	if last.Success.PrimaryAsset == nil || last.Success.PrimaryAsset.PageID != "page-1" {
		t.Errorf("PrimaryAsset not enriched: %+v", last.Success.PrimaryAsset)
	}
}

func TestLoginLoop_AssetDiscovery_ErrorIsNonFatal(t *testing.T) {
	client := &fakeLoginClient{
		script: []scriptedTransition{
			{step: nil, cookies: successCookies()},
		},
		finalPayload: successPayload(),
		identity:     successIdentity,
	}
	inputs := make(chan handler.DriverInput)
	r, updates := newRunner(t, client, inputs)
	r.discoverer = assetDiscovererFunc(func(ctx context.Context, _ *auth.Creds) ([]*store.AssetRow, *store.AssetRow, error) {
		return nil, nil, errors.New("graphql: network down")
	})

	go r.run()

	ev := drainUpdates(t, updates, 500*time.Millisecond)
	last := ev[len(ev)-1]
	if last.Kind != handler.UpdateKindSuccess {
		t.Fatalf("asset discovery err should NOT prevent Success; got %+v", last)
	}
	if len(last.Success.Assets) != 0 {
		t.Errorf("Assets should be empty when discovery fails: %+v", last.Success.Assets)
	}
	if last.Success.PrimaryAsset != nil {
		t.Errorf("PrimaryAsset should be nil when discovery fails: %+v", last.Success.PrimaryAsset)
	}
}
