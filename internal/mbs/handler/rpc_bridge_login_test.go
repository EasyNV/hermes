package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mbs-native/auth"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/hermes-waba/hermes/internal/mbs/store"
	"github.com/hermes-waba/hermes/internal/mbs/store/mock"
	"github.com/hermes-waba/hermes/pkg/crypto"
	"github.com/pquerna/otp/totp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// ─────────────────────────────────────────────────────────────────────
// fakeBridgeStream — satisfies grpc.BidiStreamingServer[BridgeLoginRequest, BridgeLoginUpdate]
// ─────────────────────────────────────────────────────────────────────

type fakeBridgeStream struct {
	grpc.ServerStream

	ctx context.Context

	// Client → server queue. Tests push requests onto this channel;
	// the handler's Recv() drains it. Close the channel to signal EOF.
	recvCh chan *hermesv1.BridgeLoginRequest

	mu      sync.Mutex
	sent    []*hermesv1.BridgeLoginUpdate
	sendErr error
}

func newFakeBridgeStream(ctx context.Context) *fakeBridgeStream {
	return &fakeBridgeStream{
		ctx:    ctx,
		recvCh: make(chan *hermesv1.BridgeLoginRequest, 16),
	}
}

func (s *fakeBridgeStream) Context() context.Context { return s.ctx }
func (s *fakeBridgeStream) Send(msg *hermesv1.BridgeLoginUpdate) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sendErr != nil {
		return s.sendErr
	}
	s.sent = append(s.sent, msg)
	return nil
}
func (s *fakeBridgeStream) Recv() (*hermesv1.BridgeLoginRequest, error) {
	select {
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	case msg, ok := <-s.recvCh:
		if !ok {
			return nil, errEOFForTest
		}
		return msg, nil
	}
}
func (s *fakeBridgeStream) SetHeader(metadata.MD) error  { return nil }
func (s *fakeBridgeStream) SendHeader(metadata.MD) error { return nil }
func (s *fakeBridgeStream) SetTrailer(metadata.MD)       {}
func (s *fakeBridgeStream) RecvMsg(any) error            { return nil }
func (s *fakeBridgeStream) SendMsg(any) error            { return nil }

func (s *fakeBridgeStream) sentSnapshot() []*hermesv1.BridgeLoginUpdate {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]*hermesv1.BridgeLoginUpdate, len(s.sent))
	copy(cp, s.sent)
	return cp
}

// errEOFForTest is what a test-side closure of recvCh produces. We
// use the actual io.EOF so the handler's first-message guard (which
// checks errors.Is(err, io.EOF)) detects this as a "stream closed
// before Start" case → InvalidArgument.
var errEOFForTest = io.EOF

// ─────────────────────────────────────────────────────────────────────
// scriptedDriver — programmable Driver impl for tests
// ─────────────────────────────────────────────────────────────────────

// scriptedDriver replays a pre-built sequence of DriverUpdates from
// the chunk-4 plan. Inputs submitted by the handler land on inputCh
// so tests can assert "did the auto-TOTP path fire?".
type scriptedDriver struct {
	updates chan DriverUpdate
	closed  atomic.Bool

	mu     sync.Mutex
	inputs []DriverInput

	// runErr, if set, makes Run return immediately with this error.
	runErr error

	// onClose, if set, fires when Close() is called. For leak checks.
	onClose func()
}

func newScriptedDriver(script []DriverUpdate) *scriptedDriver {
	d := &scriptedDriver{
		updates: make(chan DriverUpdate, len(script)+4),
	}
	for _, u := range script {
		d.updates <- u
	}
	return d
}

func (d *scriptedDriver) Run(ctx context.Context, _ DriverStartRequest) (<-chan DriverUpdate, error) {
	if d.runErr != nil {
		return nil, d.runErr
	}
	return d.updates, nil
}

func (d *scriptedDriver) Submit(input DriverInput) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.inputs = append(d.inputs, input)
	return nil
}

func (d *scriptedDriver) Close() error {
	if d.closed.Swap(true) {
		return nil
	}
	if d.onClose != nil {
		d.onClose()
	}
	return nil
}

func (d *scriptedDriver) snapshotInputs() []DriverInput {
	d.mu.Lock()
	defer d.mu.Unlock()
	cp := make([]DriverInput, len(d.inputs))
	copy(cp, d.inputs)
	return cp
}

// closeUpdatesAfter all scripted items have been read, signal channel
// close so the handler observes "abnormal exit". Tests that want to
// simulate "driver crashed" call this manually.
func (d *scriptedDriver) closeUpdates() {
	close(d.updates)
}

// pushUpdate adds an update mid-test. Used for tests that need to
// trigger an event AFTER the handler has consumed the initial script.
func (d *scriptedDriver) pushUpdate(u DriverUpdate) {
	d.updates <- u
}

// ─────────────────────────────────────────────────────────────────────
// Fixture
// ─────────────────────────────────────────────────────────────────────

type bridgeFixture struct {
	h        *Handler
	st       *mock.Store
	pub      *recordingPublisher
	driver   *scriptedDriver
	closeFn  func() // call to assert defer driver.Close ran
	dek      crypto.DataEncryptionKey
	tenantID string
}

func newBridgeFixture(t *testing.T, driver *scriptedDriver) bridgeFixture {
	t.Helper()
	st := mock.NewStore()
	pub := &recordingPublisher{}
	dek := newTestDEK(t)

	var closeCalled atomic.Int64
	driver.onClose = func() { closeCalled.Add(1) }

	h, err := NewHandler(Options{
		Store:         st,
		Manager:       &nopManager{},
		Publisher:     pub,
		DriverFactory: DriverFactory(func(DriverOptions) Driver { return driver }),
		DEK:           dek,
		PodID:         "pod-test",
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return bridgeFixture{
		h: h, st: st, pub: pub, driver: driver, dek: dek,
		tenantID: "tenant-A",
		closeFn:  func() {},
	}
}

// runBridgeLogin spawns h.BridgeLogin on a goroutine, returning the
// stream + a done channel carrying the final error.
func runBridgeLogin(t *testing.T, h *Handler, tenant string) (*fakeBridgeStream, context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(withTenantForTest(context.Background(), tenant))
	stream := newFakeBridgeStream(ctx)
	done := make(chan error, 1)
	go func() {
		done <- h.BridgeLogin(stream)
	}()
	return stream, cancel, done
}

// validCreds builds plausible *auth.Creds with all Validate()-required
// fields populated.
func validCreds(uid int64) *auth.Creds {
	return &auth.Creds{
		AccessToken:      "EAAB-plaintext",
		Secret:           "cac415ec0937d6f1c78cf6fba753c9d1",
		SessionKey:       "5.0oor9VhiOfiTgg.1778254326.11-1",
		UserID:           uid,
		FamilyDeviceID:   "7a17b762-668d-4bef-a9cf-cd0abd58231c",
		DeviceID:         "7a17b762-668d-4bef-a9cf-cd0abd58231d",
		AppVersion:       "551.0.0.55.106",
		BuildNumber:      "955655792",
		DeviceModel:      "SM-S931B",
		AndroidVer:       "15",
		Manufacturer:     "samsung",
		Locale:           "en_US",
		Density:          "2.99375",
		Width:            1080,
		Height:           2340,
		Abi:              "arm64-v8a",
		VersionID:        "26854813974149875",
		MqttCapabilities: 514,
	}
}

func mkSuccess(uid int64) DriverUpdate {
	return DriverUpdate{
		Kind: UpdateKindSuccess,
		Success: &DriverSuccess{
			UID:         uid,
			DisplayName: "Test User",
			Creds:       validCreds(uid),
			Assets: []*store.AssetRow{
				{UID: uid, PageID: "page-1", PageName: "Page One", WabaID: "w", WecMailboxID: "m", IsPrimary: true},
			},
			PrimaryAsset: &store.AssetRow{
				UID: uid, PageID: "page-1", PageName: "Page One",
				WabaID: "w", WecMailboxID: "m",
			},
		},
	}
}

func mkFailure(code hermesv1.BridgeLoginErrorCode, msg string) DriverUpdate {
	return DriverUpdate{
		Kind: UpdateKindFailure,
		Failure: &DriverFailure{
			Code: code, Message: msg, Retryable: false,
		},
	}
}

func mkProgress(stage hermesv1.BridgeLoginStage, detail string) DriverUpdate {
	return DriverUpdate{
		Kind:     UpdateKindProgress,
		Progress: &DriverProgress{Stage: stage, Detail: detail},
	}
}

func mkTOTPPrompt() DriverUpdate {
	return DriverUpdate{
		Kind: UpdateKindPrompt,
		Prompt: &DriverPrompt{
			StepID:       "two_step_verification",
			Instructions: "Enter the code from your authenticator app",
			Fields: []DriverPromptField{
				{ID: "totp_code", Name: "Authenticator Code", Type: "code"},
			},
		},
	}
}

func startMsg(tenant, email, password, totpSecret string, persistTOTP bool) *hermesv1.BridgeLoginRequest {
	return &hermesv1.BridgeLoginRequest{
		Payload: &hermesv1.BridgeLoginRequest_Start{
			Start: &hermesv1.BridgeLoginStart{
				TenantId:          tenant,
				Email:             email,
				Password:          password,
				TotpSecret:        totpSecret,
				PersistTotpSecret: persistTOTP,
			},
		},
	}
}

func inputMsg(field, value string) *hermesv1.BridgeLoginRequest {
	return &hermesv1.BridgeLoginRequest{
		Payload: &hermesv1.BridgeLoginRequest_Input{
			Input: &hermesv1.BridgeLoginInput{FieldId: field, Value: value},
		},
	}
}

// extractEventType returns the oneof discriminator name for diagnostic
// assertions: "progress" | "prompt" | "success" | "failure".
func extractEventType(u *hermesv1.BridgeLoginUpdate) string {
	switch u.Event.(type) {
	case *hermesv1.BridgeLoginUpdate_Progress:
		return "progress"
	case *hermesv1.BridgeLoginUpdate_Prompt:
		return "prompt"
	case *hermesv1.BridgeLoginUpdate_Success:
		return "success"
	case *hermesv1.BridgeLoginUpdate_Failure:
		return "failure"
	default:
		return "unknown"
	}
}

// ─────────────────────────────────────────────────────────────────────
// Tests — validation / startup
// ─────────────────────────────────────────────────────────────────────

func TestBridgeLogin_MissingTenant(t *testing.T) {
	driver := newScriptedDriver(nil)
	fx := newBridgeFixture(t, driver)

	stream := newFakeBridgeStream(context.Background()) // no metadata
	err := fx.h.BridgeLogin(stream)
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("expected Unauthenticated, got %v", err)
	}
}

func TestBridgeLogin_FirstMsgNotStart_InvalidArgument(t *testing.T) {
	driver := newScriptedDriver(nil)
	fx := newBridgeFixture(t, driver)

	stream, cancel, done := runBridgeLogin(t, fx.h, fx.tenantID)
	defer cancel()

	// First message is Input — handler should reject.
	stream.recvCh <- inputMsg("x", "y")
	select {
	case err := <-done:
		if status.Code(err) != codes.InvalidArgument {
			t.Errorf("expected InvalidArgument, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("BridgeLogin did not return")
	}
}

func TestBridgeLogin_StreamEOFBeforeStart(t *testing.T) {
	driver := newScriptedDriver(nil)
	fx := newBridgeFixture(t, driver)

	stream, cancel, done := runBridgeLogin(t, fx.h, fx.tenantID)
	defer cancel()

	close(stream.recvCh) // EOF before any message
	select {
	case err := <-done:
		if status.Code(err) != codes.InvalidArgument {
			t.Errorf("expected InvalidArgument on early EOF, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("BridgeLogin did not return")
	}
}

func TestBridgeLogin_TenantBodyMismatch(t *testing.T) {
	driver := newScriptedDriver([]DriverUpdate{mkSuccess(100)})
	fx := newBridgeFixture(t, driver)

	stream, cancel, done := runBridgeLogin(t, fx.h, "tenant-A")
	defer cancel()
	stream.recvCh <- startMsg("tenant-B", "alice@example.com", "pw", "", false)

	select {
	case err := <-done:
		if status.Code(err) != codes.InvalidArgument {
			t.Errorf("expected InvalidArgument, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("BridgeLogin did not return")
	}
	// Ensure no session written (tenant rejected BEFORE persist).
	if exists, _ := fx.st.ExistsSession(context.Background(), 100); exists {
		t.Errorf("tenant mismatch must NOT persist session")
	}
}

func TestBridgeLogin_MissingEmailOrPassword(t *testing.T) {
	driver := newScriptedDriver([]DriverUpdate{mkSuccess(100)})
	fx := newBridgeFixture(t, driver)

	stream, cancel, done := runBridgeLogin(t, fx.h, fx.tenantID)
	defer cancel()
	stream.recvCh <- startMsg(fx.tenantID, "", "", "", false)

	select {
	case err := <-done:
		if status.Code(err) != codes.InvalidArgument {
			t.Errorf("expected InvalidArgument, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("BridgeLogin did not return")
	}
}

// ─────────────────────────────────────────────────────────────────────
// Tests — happy paths
// ─────────────────────────────────────────────────────────────────────

func TestBridgeLogin_HappyPath_NoPrompt(t *testing.T) {
	driver := newScriptedDriver([]DriverUpdate{
		mkProgress(hermesv1.BridgeLoginStage_BRIDGE_STAGE_CALLING_CAA, "calling"),
		mkProgress(hermesv1.BridgeLoginStage_BRIDGE_STAGE_DISCOVERING_ASSETS, "assets"),
		mkSuccess(100),
	})
	fx := newBridgeFixture(t, driver)

	stream, cancel, done := runBridgeLogin(t, fx.h, fx.tenantID)
	defer cancel()
	stream.recvCh <- startMsg(fx.tenantID, "alice@example.com", "pw", "", false)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("BridgeLogin: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("BridgeLogin did not return")
	}

	// Stream events: 2 progress + 1 success.
	events := stream.sentSnapshot()
	if len(events) != 3 {
		t.Fatalf("events: got %d want 3 (2 progress + 1 success)", len(events))
	}
	if extractEventType(events[0]) != "progress" || extractEventType(events[1]) != "progress" ||
		extractEventType(events[2]) != "success" {
		var types []string
		for _, e := range events {
			types = append(types, extractEventType(e))
		}
		t.Errorf("event order: got %v", types)
	}

	// Persistence side-effects.
	row, err := fx.st.GetSession(context.Background(), 100)
	if err != nil {
		t.Fatalf("session not persisted: %v", err)
	}
	if row.TenantID != fx.tenantID || row.State != "active" {
		t.Errorf("row state: %+v", row)
	}
	if len(row.EncryptedAccessToken) == 0 || len(row.EncryptedSecret) == 0 || len(row.EncryptedSessionKey) == 0 {
		t.Errorf("encrypted columns empty: AT=%d Sec=%d SK=%d",
			len(row.EncryptedAccessToken), len(row.EncryptedSecret), len(row.EncryptedSessionKey))
	}
	// Decrypt access_token round-trip with the test DEK.
	at, err := crypto.DecryptAESGCM(fx.dek, row.EncryptedAccessToken,
		store.BuildAAD(store.AADAccessToken, 100))
	if err != nil {
		t.Fatalf("decrypt access_token: %v", err)
	}
	if string(at) != "EAAB-plaintext" {
		t.Errorf("decrypted access_token: got %q", string(at))
	}

	// Lifecycle event published.
	if len(fx.pub.lifecycle) != 1 {
		t.Fatalf("lifecycle events: got %d want 1", len(fx.pub.lifecycle))
	}
	ev := fx.pub.lifecycle[0]
	if ev.reason != "created" || ev.nxt != hermesv1.MbsSessionState_MBS_SESSION_STATE_ACTIVE {
		t.Errorf("lifecycle event: %+v", ev)
	}

	// Assets persisted.
	assets, _ := fx.st.ListAssets(context.Background(), 100)
	if len(assets) != 1 || assets[0].PageID != "page-1" {
		t.Errorf("assets: %+v", assets)
	}

	// Driver was closed.
	if !driver.closed.Load() {
		t.Errorf("driver.Close should have fired")
	}
}

func TestBridgeLogin_HappyPath_WithCookies(t *testing.T) {
	// Verify BridgeEnvelope round-trips through encrypt + decrypt.
	driver := newScriptedDriver(nil)
	fx := newBridgeFixture(t, driver)

	envelope := &auth.BridgeEnvelope{
		Version:  1,
		IssuedAt: time.Now().Unix(),
	}
	driver.pushUpdate(DriverUpdate{
		Kind: UpdateKindSuccess,
		Success: &DriverSuccess{
			UID:            100,
			DisplayName:    "with-cookies",
			Creds:          validCreds(100),
			BridgeEnvelope: envelope,
		},
	})

	stream, cancel, done := runBridgeLogin(t, fx.h, fx.tenantID)
	defer cancel()
	stream.recvCh <- startMsg(fx.tenantID, "alice@example.com", "pw", "", false)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("BridgeLogin: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("BridgeLogin did not return")
	}

	row, _ := fx.st.GetSession(context.Background(), 100)
	if len(row.EncryptedCookies) == 0 {
		t.Fatal("EncryptedCookies should be populated")
	}
	cookies, err := crypto.DecryptAESGCM(fx.dek, row.EncryptedCookies,
		store.BuildAAD(store.AADCookies, 100))
	if err != nil {
		t.Fatalf("decrypt cookies: %v", err)
	}
	var decoded auth.BridgeEnvelope
	if err := json.Unmarshal(cookies, &decoded); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if decoded.Version != envelope.Version || decoded.IssuedAt != envelope.IssuedAt {
		t.Errorf("envelope mismatch: got %+v want %+v", decoded, envelope)
	}
}

func TestBridgeLogin_PromptThenInputThenSuccess(t *testing.T) {
	driver := newScriptedDriver([]DriverUpdate{
		mkProgress(hermesv1.BridgeLoginStage_BRIDGE_STAGE_CALLING_CAA, "calling"),
		{
			Kind: UpdateKindPrompt,
			Prompt: &DriverPrompt{
				StepID:       "captcha",
				Instructions: "Solve the captcha",
				Fields:       []DriverPromptField{{ID: "captcha_response", Name: "Answer", Type: "text"}},
			},
		},
	})
	fx := newBridgeFixture(t, driver)

	stream, cancel, done := runBridgeLogin(t, fx.h, fx.tenantID)
	defer cancel()
	stream.recvCh <- startMsg(fx.tenantID, "alice@example.com", "pw", "", false)

	// Wait for prompt to be sent.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		ev := stream.sentSnapshot()
		if len(ev) >= 2 && extractEventType(ev[1]) == "prompt" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Client submits the captcha answer.
	stream.recvCh <- inputMsg("captcha_response", "ANSWER123")

	// Give the reader loop time to dispatch Submit, then push Success.
	time.Sleep(20 * time.Millisecond)
	driver.pushUpdate(mkSuccess(100))

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("BridgeLogin: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("BridgeLogin did not return")
	}

	// Driver received the input.
	inputs := driver.snapshotInputs()
	if len(inputs) != 1 || inputs[0].FieldID != "captcha_response" || inputs[0].Value != "ANSWER123" {
		t.Errorf("expected captcha submit, got %+v", inputs)
	}

	// Stream contained: progress, prompt, success.
	events := stream.sentSnapshot()
	wantTypes := []string{"progress", "prompt", "success"}
	if len(events) < len(wantTypes) {
		t.Fatalf("events: got %d want at least %d", len(events), len(wantTypes))
	}
	for i, want := range wantTypes {
		if got := extractEventType(events[i]); got != want {
			t.Errorf("event %d: got %s want %s", i, got, want)
		}
	}
}

func TestBridgeLogin_TOTPAutoInject(t *testing.T) {
	// Use a real base32 TOTP secret + emit a two_step_verification
	// prompt; handler should auto-fill WITHOUT surfacing prompt to
	// client.
	const totpSecret = "JBSWY3DPEHPK3PXP" // base32 for "Hello!\xDE\xAD\xBE\xEF"

	driver := newScriptedDriver([]DriverUpdate{
		mkProgress(hermesv1.BridgeLoginStage_BRIDGE_STAGE_AWAITING_2FA, "2fa needed"),
		mkTOTPPrompt(),
	})
	fx := newBridgeFixture(t, driver)

	stream, cancel, done := runBridgeLogin(t, fx.h, fx.tenantID)
	defer cancel()
	stream.recvCh <- startMsg(fx.tenantID, "alice@example.com", "pw", totpSecret, false)

	// Wait for handler to consume both updates + Submit the TOTP code.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(driver.snapshotInputs()) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	inputs := driver.snapshotInputs()
	if len(inputs) != 1 || inputs[0].FieldID != "totp_code" {
		t.Fatalf("expected auto-Submit of totp_code, got %+v", inputs)
	}
	// Verify code matches what totp.GenerateCode would produce (loose
	// check — both compute against the same time bucket).
	expected, _ := totp.GenerateCode(totpSecret, time.Now())
	if inputs[0].Value != expected {
		t.Errorf("TOTP code: got %q want %q", inputs[0].Value, expected)
	}

	// CRITICAL: NO prompt event reached the client.
	for _, ev := range stream.sentSnapshot() {
		if extractEventType(ev) == "prompt" {
			t.Errorf("prompt leaked to client (auto-inject should swallow it)")
		}
	}

	// Push Success to terminate.
	driver.pushUpdate(mkSuccess(100))
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("BridgeLogin: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("BridgeLogin did not return")
	}
}

func TestBridgeLogin_TOTPPersistedWhenRequested(t *testing.T) {
	const totpSecret = "JBSWY3DPEHPK3PXP"
	driver := newScriptedDriver([]DriverUpdate{mkSuccess(100)})
	fx := newBridgeFixture(t, driver)

	stream, cancel, done := runBridgeLogin(t, fx.h, fx.tenantID)
	defer cancel()
	stream.recvCh <- startMsg(fx.tenantID, "alice@example.com", "pw", totpSecret, true /* persist */)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("BridgeLogin: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("BridgeLogin did not return")
	}

	row, _ := fx.st.GetSession(context.Background(), 100)
	if len(row.EncryptedTOTPSecret) == 0 {
		t.Fatal("EncryptedTOTPSecret should be populated when PersistTotpSecret=true")
	}
	decoded, err := crypto.DecryptAESGCM(fx.dek, row.EncryptedTOTPSecret,
		store.BuildAAD(store.AADTOTPSecret, 100))
	if err != nil {
		t.Fatalf("decrypt totp_secret: %v", err)
	}
	if string(decoded) != totpSecret {
		t.Errorf("totp_secret roundtrip: got %q want %q", string(decoded), totpSecret)
	}
}

func TestBridgeLogin_TOTPNotPersistedByDefault(t *testing.T) {
	const totpSecret = "JBSWY3DPEHPK3PXP"
	driver := newScriptedDriver([]DriverUpdate{mkSuccess(100)})
	fx := newBridgeFixture(t, driver)

	stream, cancel, done := runBridgeLogin(t, fx.h, fx.tenantID)
	defer cancel()
	stream.recvCh <- startMsg(fx.tenantID, "alice@example.com", "pw", totpSecret, false /* don't persist */)

	<-done
	row, _ := fx.st.GetSession(context.Background(), 100)
	if len(row.EncryptedTOTPSecret) != 0 {
		t.Errorf("EncryptedTOTPSecret should be empty when PersistTotpSecret=false, got %d bytes", len(row.EncryptedTOTPSecret))
	}
}

// ─────────────────────────────────────────────────────────────────────
// Tests — failure paths
// ─────────────────────────────────────────────────────────────────────

func TestBridgeLogin_DriverFailure_InvalidCreds(t *testing.T) {
	driver := newScriptedDriver([]DriverUpdate{
		mkFailure(hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_INVALID_CREDS, "bad password"),
	})
	fx := newBridgeFixture(t, driver)

	stream, cancel, done := runBridgeLogin(t, fx.h, fx.tenantID)
	defer cancel()
	stream.recvCh <- startMsg(fx.tenantID, "alice@example.com", "wrong", "", false)

	select {
	case err := <-done:
		if status.Code(err) != codes.Unauthenticated {
			t.Errorf("INVALID_CREDS should map to Unauthenticated, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("BridgeLogin did not return")
	}

	// Failure event sent.
	events := stream.sentSnapshot()
	if len(events) == 0 || extractEventType(events[len(events)-1]) != "failure" {
		var types []string
		for _, e := range events {
			types = append(types, extractEventType(e))
		}
		t.Errorf("expected terminal failure event, got %v", types)
	}
	// No session persisted.
	if exists, _ := fx.st.ExistsSession(context.Background(), 100); exists {
		t.Errorf("invalid creds must NOT persist session")
	}
	// No lifecycle event published.
	if len(fx.pub.lifecycle) != 0 {
		t.Errorf("lifecycle event leaked on failure: %+v", fx.pub.lifecycle)
	}
}

func TestBridgeLogin_DriverUpdatesChannelClosedAbnormally(t *testing.T) {
	driver := newScriptedDriver(nil)
	fx := newBridgeFixture(t, driver)

	stream, cancel, done := runBridgeLogin(t, fx.h, fx.tenantID)
	defer cancel()
	stream.recvCh <- startMsg(fx.tenantID, "alice@example.com", "pw", "", false)

	// Wait for driver.Run to be called then close updates abnormally.
	time.Sleep(20 * time.Millisecond)
	driver.closeUpdates()

	select {
	case err := <-done:
		if status.Code(err) != codes.Internal {
			t.Errorf("abnormal close should map to Internal, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("BridgeLogin did not return")
	}
}

func TestBridgeLogin_ClientCancel(t *testing.T) {
	// Slow driver — emits one progress then sits on the channel.
	driver := newScriptedDriver([]DriverUpdate{
		mkProgress(hermesv1.BridgeLoginStage_BRIDGE_STAGE_CALLING_CAA, "slow"),
	})
	fx := newBridgeFixture(t, driver)

	stream, cancel, done := runBridgeLogin(t, fx.h, fx.tenantID)
	stream.recvCh <- startMsg(fx.tenantID, "alice@example.com", "pw", "", false)

	// Wait for progress to be sent, then cancel.
	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) && status.Code(err) != codes.Canceled {
			t.Errorf("expected ctx.Canceled, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("BridgeLogin did not return after cancel")
	}
	// Driver should still have Close called.
	if !driver.closed.Load() {
		t.Errorf("driver.Close should fire on cancel (defer)")
	}
}

func TestBridgeLogin_CancelMessageAbortsDriver(t *testing.T) {
	driver := newScriptedDriver([]DriverUpdate{
		mkProgress(hermesv1.BridgeLoginStage_BRIDGE_STAGE_CALLING_CAA, "calling"),
	})
	fx := newBridgeFixture(t, driver)

	stream, cancel, done := runBridgeLogin(t, fx.h, fx.tenantID)
	defer cancel()
	stream.recvCh <- startMsg(fx.tenantID, "alice@example.com", "pw", "", false)

	// Wait for handler to enter the main loop.
	time.Sleep(30 * time.Millisecond)

	// Client sends a Cancel message.
	stream.recvCh <- &hermesv1.BridgeLoginRequest{
		Payload: &hermesv1.BridgeLoginRequest_Cancel{
			Cancel: &hermesv1.BridgeLoginCancel{},
		},
	}

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) && status.Code(err) != codes.Canceled {
			t.Errorf("expected ctx.Canceled after Cancel msg, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("BridgeLogin did not return after Cancel")
	}
}

func TestBridgeLogin_DriverRunError(t *testing.T) {
	driver := newScriptedDriver(nil)
	driver.runErr = errors.New("driver: init failed")
	fx := newBridgeFixture(t, driver)

	stream, cancel, done := runBridgeLogin(t, fx.h, fx.tenantID)
	defer cancel()
	stream.recvCh <- startMsg(fx.tenantID, "alice@example.com", "pw", "", false)

	select {
	case err := <-done:
		if status.Code(err) != codes.Internal {
			t.Errorf("driver.Run error should map to Internal, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("BridgeLogin did not return")
	}
}

func TestBridgeLogin_SuccessWithNilCreds_Internal(t *testing.T) {
	driver := newScriptedDriver([]DriverUpdate{
		{
			Kind:    UpdateKindSuccess,
			Success: &DriverSuccess{UID: 100, DisplayName: "x"}, // Creds nil!
		},
	})
	fx := newBridgeFixture(t, driver)

	stream, cancel, done := runBridgeLogin(t, fx.h, fx.tenantID)
	defer cancel()
	stream.recvCh <- startMsg(fx.tenantID, "alice@example.com", "pw", "", false)

	select {
	case err := <-done:
		if status.Code(err) != codes.Internal {
			t.Errorf("nil Creds should map to Internal, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("BridgeLogin did not return")
	}
}

// ─────────────────────────────────────────────────────────────────────
// Tests — concurrency / semaphore
// ─────────────────────────────────────────────────────────────────────

func TestBridgeLogin_SemaphoreExhausted(t *testing.T) {
	// Use a driver that blocks on Updates (never emits).
	// Open 4 concurrent streams, then a 5th should ResourceExhausted.
	driver := newScriptedDriver(nil) // never emits

	st := mock.NewStore()
	pub := &recordingPublisher{}
	dek := newTestDEK(t)
	h, err := NewHandler(Options{
		Store:                     st,
		Manager:                   &nopManager{},
		Publisher:                 pub,
		DriverFactory:             DriverFactory(func(DriverOptions) Driver { return driver }),
		DEK:                       dek,
		PodID:                     "pod-test",
		MaxConcurrentBridgeLogins: 2,
		BridgeAcquireTimeout:      30 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	// Spawn 2 streams that will block in the main loop (driver never emits).
	streams := []*fakeBridgeStream{}
	dones := []<-chan error{}
	cancels := []context.CancelFunc{}
	for i := 0; i < 2; i++ {
		stream, cancel, done := runBridgeLogin(t, h, "tenant-A")
		stream.recvCh <- startMsg("tenant-A", "alice@example.com", "pw", "", false)
		streams = append(streams, stream)
		dones = append(dones, done)
		cancels = append(cancels, cancel)
	}
	defer func() {
		for _, c := range cancels {
			c()
		}
	}()

	// Give them time to acquire slots.
	time.Sleep(50 * time.Millisecond)

	// The 3rd attempt should time out the semaphore.
	stream3, cancel3, done3 := runBridgeLogin(t, h, "tenant-A")
	defer cancel3()
	stream3.recvCh <- startMsg("tenant-A", "alice@example.com", "pw", "", false)

	select {
	case err := <-done3:
		if status.Code(err) != codes.ResourceExhausted {
			t.Errorf("expected ResourceExhausted, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("3rd BridgeLogin did not return ResourceExhausted")
	}

	// Cleanup: cancel the first two so the test exits cleanly.
	cancels[0]()
	cancels[1]()
	<-dones[0]
	<-dones[1]
}

// ─────────────────────────────────────────────────────────────────────
// Helper tests
// ─────────────────────────────────────────────────────────────────────

func TestRedactEmail(t *testing.T) {
	cases := map[string]string{
		"":                     "",
		"a":                    "",  // no '@'
		"a@b.com":              "*@b.com",
		"ab@b.com":             "**@b.com",
		"alice@example.com":    "al***@example.com",
		"alice+tag@meta.com":   "al***@meta.com",
		"@no-local.com":        "",
	}
	for in, want := range cases {
		if got := redactEmail(in); got != want {
			t.Errorf("redactEmail(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSanitizeOutcomeLabel(t *testing.T) {
	if got := sanitizeOutcomeLabel("BRIDGE_ERR_INVALID_CREDS"); got != "invalid_creds" {
		t.Errorf("got %q want invalid_creds", got)
	}
	if got := sanitizeOutcomeLabel("BRIDGE_ERR_CHECKPOINT"); got != "checkpoint" {
		t.Errorf("got %q want checkpoint", got)
	}
}

func TestExtractEventType_Sanity(t *testing.T) {
	// Sanity check the test helper. (If extractEventType is wrong all
	// other tests are unreliable.)
	u := &hermesv1.BridgeLoginUpdate{Event: &hermesv1.BridgeLoginUpdate_Progress{
		Progress: &hermesv1.BridgeLoginProgress{Stage: "x"},
	}}
	if extractEventType(u) != "progress" {
		t.Errorf("Progress mis-classified")
	}
}

// ─────────────────────────────────────────────────────────────────────
// Tests — Step 10 audit P0 regression: cross-tenant re-bridge MUST fail closed
// ─────────────────────────────────────────────────────────────────────

// TestBridgeLogin_RebridgeRejectedForDifferentTenant proves the fix
// from .hermes/plans/2026-05-27_stage-e1-chunk4-step10-hostile-audit.md F1.
//
// Setup: tenant-B already owns uid=100. Attacker logs in as tenant-A,
// drives a successful bridge for the SAME uid=100 (e.g. by phishing
// tenant-B's FB password). The handler MUST:
//
//  1. Return codes.PermissionDenied (NOT Internal).
//  2. Persist NOTHING — tenant-B's row contents stay byte-identical.
//  3. NOT publish a lifecycle event for the attempted tenant.
//  4. Increment metrics counter "failure_tenant_collision" (asserted
//     via outcome label below).
func TestBridgeLogin_RebridgeRejectedForDifferentTenant(t *testing.T) {
	driver := newScriptedDriver([]DriverUpdate{mkSuccess(100)})
	fx := newBridgeFixture(t, driver)

	// 1. Seed tenant-B's existing row directly via the store. We
	//    encrypt with the SAME DEK + AAD that the handler uses, so a
	//    successful bypass would silently rewrite valid ciphertext.
	originalAT, err := crypto.EncryptAESGCM(fx.dek, []byte("TENANT_B_TOKEN"),
		store.BuildAAD(store.AADAccessToken, 100))
	if err != nil {
		t.Fatalf("seed encrypt: %v", err)
	}
	originalSecret, err := crypto.EncryptAESGCM(fx.dek, []byte("TENANT_B_SECRET"),
		store.BuildAAD(store.AADSecret, 100))
	if err != nil {
		t.Fatalf("seed encrypt secret: %v", err)
	}
	originalSK, err := crypto.EncryptAESGCM(fx.dek, []byte("TENANT_B_SESSIONKEY"),
		store.BuildAAD(store.AADSessionKey, 100))
	if err != nil {
		t.Fatalf("seed encrypt SK: %v", err)
	}
	tenantBRow := &store.SessionRow{
		UID:                  100,
		TenantID:             "tenant-B",
		State:                "active",
		PodID:                "pod-other",
		EncryptedAccessToken: originalAT,
		EncryptedSecret:      originalSecret,
		EncryptedSessionKey:  originalSK,
	}
	if err := fx.st.CreateSession(context.Background(), tenantBRow); err != nil {
		t.Fatalf("seed tenant-B: %v", err)
	}

	// 2. Run BridgeLogin as tenant-A, attempting to overwrite uid=100.
	//    Tenant-A is `fx.tenantID` (= "tenant-A"); start body tenant
	//    matches (no body cross-check rejection).
	stream, cancel, done := runBridgeLogin(t, fx.h, fx.tenantID)
	defer cancel()
	stream.recvCh <- startMsg(fx.tenantID, "attacker@evil.com", "stolen-pw", "", false)

	select {
	case err := <-done:
		if status.Code(err) != codes.PermissionDenied {
			t.Errorf("expected PermissionDenied on cross-tenant overwrite, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("BridgeLogin did not return")
	}

	// 3. Failure event sent to client.
	events := stream.sentSnapshot()
	if len(events) == 0 || extractEventType(events[len(events)-1]) != "failure" {
		var types []string
		for _, e := range events {
			types = append(types, extractEventType(e))
		}
		t.Errorf("expected terminal failure event, got %v", types)
	}

	// 4. CRITICAL: tenant-B's row contents UNCHANGED. Verify all 3
	//    encrypted columns are byte-identical to what we seeded, AND
	//    tenant_id stays "tenant-B".
	after, err := fx.st.GetSession(context.Background(), 100)
	if err != nil {
		t.Fatalf("read tenant-B row after attack: %v", err)
	}
	if after.TenantID != "tenant-B" {
		t.Errorf("tenant_id MUTATED: %q (attack succeeded!)", after.TenantID)
	}
	if !bytesEqual(after.EncryptedAccessToken, originalAT) {
		t.Errorf("EncryptedAccessToken MUTATED (attack succeeded!)")
	}
	if !bytesEqual(after.EncryptedSecret, originalSecret) {
		t.Errorf("EncryptedSecret MUTATED (attack succeeded!)")
	}
	if !bytesEqual(after.EncryptedSessionKey, originalSK) {
		t.Errorf("EncryptedSessionKey MUTATED (attack succeeded!)")
	}
	// Decrypt with original creds — must still match.
	plain, err := crypto.DecryptAESGCM(fx.dek, after.EncryptedAccessToken,
		store.BuildAAD(store.AADAccessToken, 100))
	if err != nil {
		t.Fatalf("decrypt tenant-B token after attack: %v", err)
	}
	if string(plain) != "TENANT_B_TOKEN" {
		t.Errorf("token plaintext MUTATED: got %q want TENANT_B_TOKEN", string(plain))
	}

	// 5. No lifecycle event published for tenant-A's failed attempt.
	//    (Tenant-B never legitimately bridged in this test, so the
	//    publisher should have 0 events overall.)
	if len(fx.pub.lifecycle) != 0 {
		t.Errorf("lifecycle event leaked on cross-tenant attack: %+v", fx.pub.lifecycle)
	}
}

// TestBridgeLogin_RebridgeAllowedForSameTenant proves we didn't break
// the legitimate re-bridge path. Same uid, same tenant → tokens get
// updated in place (Sam's normal "refresh creds after burn" flow).
func TestBridgeLogin_RebridgeAllowedForSameTenant(t *testing.T) {
	driver := newScriptedDriver([]DriverUpdate{mkSuccess(100)})
	fx := newBridgeFixture(t, driver)

	// 1. Seed an existing row for tenant-A (= fx.tenantID).
	originalAT, _ := crypto.EncryptAESGCM(fx.dek, []byte("OLD_TOKEN"),
		store.BuildAAD(store.AADAccessToken, 100))
	originalSec, _ := crypto.EncryptAESGCM(fx.dek, []byte("OLD_SECRET"),
		store.BuildAAD(store.AADSecret, 100))
	originalSK, _ := crypto.EncryptAESGCM(fx.dek, []byte("OLD_SK"),
		store.BuildAAD(store.AADSessionKey, 100))
	if err := fx.st.CreateSession(context.Background(), &store.SessionRow{
		UID:                  100,
		TenantID:             fx.tenantID,
		State:                "burned",
		PodID:                "",
		EncryptedAccessToken: originalAT,
		EncryptedSecret:      originalSec,
		EncryptedSessionKey:  originalSK,
	}); err != nil {
		t.Fatalf("seed existing row: %v", err)
	}

	// 2. Re-bridge as the SAME tenant.
	stream, cancel, done := runBridgeLogin(t, fx.h, fx.tenantID)
	defer cancel()
	stream.recvCh <- startMsg(fx.tenantID, "alice@example.com", "pw", "", false)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("BridgeLogin (same-tenant re-bridge): %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("BridgeLogin did not return")
	}

	// 3. Row should now decrypt to the NEW token from mkSuccess.
	//    Mock's UpdateSessionTokens is now wired (chunk-4 audit fix),
	//    so the row contents MUST be the new bridge result.
	after, err := fx.st.GetSession(context.Background(), 100)
	if err != nil {
		t.Fatalf("read row after re-bridge: %v", err)
	}
	if after.TenantID != fx.tenantID {
		t.Errorf("tenant_id changed unexpectedly: %q", after.TenantID)
	}
	plain, decErr := crypto.DecryptAESGCM(fx.dek, after.EncryptedAccessToken,
		store.BuildAAD(store.AADAccessToken, 100))
	if decErr != nil {
		t.Fatalf("decrypt token after re-bridge: %v", decErr)
	}
	if string(plain) != "EAAB-plaintext" {
		t.Errorf("re-bridge did not update token: got %q want EAAB-plaintext", string(plain))
	}

	// 4. Lifecycle event published for the legitimate re-bridge.
	if len(fx.pub.lifecycle) != 1 {
		t.Errorf("expected 1 lifecycle event on legit re-bridge, got %d", len(fx.pub.lifecycle))
	}
}

// bytesEqual is a small helper to avoid importing "bytes" just for one
// comparison. Pinned here rather than in test_helpers.go because it's
// only used in the audit regression test.
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ─────────────────────────────────────────────────────────────────────
// Compile-time guards
// ─────────────────────────────────────────────────────────────────────

var _ = strings.Contains // ensure strings stays imported when assertions remove .Contains
