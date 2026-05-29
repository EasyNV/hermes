package bridge_test

// integration_test.go drives the chunk-4 ↔ chunk-5 contract end-to-end:
// a real *handler.Handler wired with bridge.NewDriverFactory + a scripted
// loginClient. Validates that:
//
//   1. handler.DriverFactory plugs into Handler.Options.DriverFactory
//      and produces working drivers per BridgeLogin RPC.
//   2. The Driver interface — Run / Submit / Close — works
//      correctly when invoked by the real handler reader loop.
//   3. Login Success flows end-to-end through the persist boundary:
//      the session row is written, encrypted columns roundtrip, the
//      lifecycle event publishes, and the handler returns OK.
//   4. Login Failure cleanly produces a gRPC error code, NO session
//      is persisted, NO lifecycle event fires.
//   5. The tenant cross-check from chunk 4 (audit P0 fix) still
//      blocks cross-tenant rebridge when the loginClient succeeds
//      for a uid owned by a different tenant.
//
// Lives in `bridge_test` (external test package) to mirror how a
// production cmd/mbs/main.go would wire the two packages: as
// downstream consumer of both, not via package-internal access.

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mbs-native/auth"
	"mbs-native/client"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/hermes-waba/hermes/internal/mbs/bridge"
	"github.com/hermes-waba/hermes/internal/mbs/handler"
	"github.com/hermes-waba/hermes/internal/mbs/session"
	"github.com/hermes-waba/hermes/internal/mbs/store"
	"github.com/hermes-waba/hermes/internal/mbs/store/mock"
	"github.com/hermes-waba/hermes/pkg/crypto"
	"github.com/rs/zerolog"
	"go.mau.fi/mautrix-meta/pkg/messagix"
	"go.mau.fi/mautrix-meta/pkg/messagix/cookies"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"maunium.net/go/mautrix/bridgev2"
)

// ─────────────────────────────────────────────────────────────────────
// integrationFakeLoginClient — minimal scripted loginClient for
// driving the bridge's loginLoop from outside the bridge package.
// Must implement bridge.loginClient implicitly via duck-typing the
// three methods the bridge requires.
// ─────────────────────────────────────────────────────────────────────

type integrationScript struct {
	step    *bridgev2.LoginStep
	cookies *cookies.Cookies
	err     error
}

type integrationFakeClient struct {
	mu       sync.Mutex
	script   []integrationScript
	idx      int
	inputs   []map[string]string
	payload  *messagix.BloksLoginActionResponsePayload
	identity [3]string
}

func (f *integrationFakeClient) DoLoginSteps(ctx context.Context, userInput map[string]string) (*bridgev2.LoginStep, *cookies.Cookies, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make(map[string]string, len(userInput))
	for k, v := range userInput {
		cp[k] = v
	}
	f.inputs = append(f.inputs, cp)
	if f.idx >= len(f.script) {
		// Hold the caller until ctx cancels — avoids spurious panics
		// when the loop polls one transition past success.
		<-ctx.Done()
		return nil, nil, ctx.Err()
	}
	t := f.script[f.idx]
	f.idx++
	return t.step, t.cookies, t.err
}

func (f *integrationFakeClient) LastLoginPayload() *messagix.BloksLoginActionResponsePayload {
	return f.payload
}

func (f *integrationFakeClient) LoginIdentity() (string, string, string) {
	return f.identity[0], f.identity[1], f.identity[2]
}

// goldIdentity matches the bridge package's internal successIdentity;
// duplicated here because that symbol is unexported.
var goldIdentity = [3]string{
	"7A17B762-668D-4BEF-A9CF-CD0ABD58231D",
	"7A17B762-668D-4BEF-A9CF-CD0ABD58231C",
	"9gH-aUzBMyDfrMwqEnEPkcaV",
}

func goldPayload(uid int64) *messagix.BloksLoginActionResponsePayload {
	return &messagix.BloksLoginActionResponsePayload{
		AccessToken:        "EAAB-integration-token",
		UID:                uid,
		SessionKey:         "5.integration.1-test",
		Secret:             "integration-secret",
		MachineID:          "machine-id",
		CredentialType:     "password",
		IsAccountConfirmed: true,
		Identifier:         "alice@example.com",
	}
}

func goldCookies() *cookies.Cookies {
	c := &cookies.Cookies{}
	c.UpdateValues(map[cookies.MetaCookieName]string{
		"c_user": "100",
		"datr":   "datrvalue",
		"xs":     "xsvalue",
	})
	return c
}

// ─────────────────────────────────────────────────────────────────────
// integrationManager — minimal session.Manager. The handler requires
// one to construct a Handler; BridgeLogin doesn't call into it on the
// happy path, so this is a trivial stub.
// ─────────────────────────────────────────────────────────────────────

type integrationManager struct{}

func (integrationManager) GetOrConnect(context.Context, int64) (*session.Connected, error) {
	return nil, nil
}
func (integrationManager) Disconnect(int64) error { return nil }
func (integrationManager) Subscribe(int64) (<-chan *session.InboundDelta, func()) {
	ch := make(chan *session.InboundDelta)
	close(ch)
	return ch, func() {}
}
func (integrationManager) Send(context.Context, int64, int64, string) (*client.SendResult, error) {
	return nil, nil
}
func (integrationManager) Drain(context.Context) error    { return nil }
func (integrationManager) Shutdown(context.Context) error { return nil }

// ─────────────────────────────────────────────────────────────────────
// integrationPublisher — records lifecycle events for assertions.
// ─────────────────────────────────────────────────────────────────────

type integrationLifecycleEvent struct {
	uid      int64
	tenantID string
	prev     hermesv1.MbsSessionState
	next     hermesv1.MbsSessionState
	reason   string
}

type integrationPublisher struct {
	mu       sync.Mutex
	lifecycle []integrationLifecycleEvent
	outbound  int
	inbound   int
}

func (p *integrationPublisher) PublishInboundMessage(int64, string, string, string, string, string, string, string, time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.inbound++
}
func (p *integrationPublisher) PublishOutbound(int64, string, string, string, string, int64, bool, string, time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.outbound++
}
func (p *integrationPublisher) PublishSessionLifecycle(uid int64, tenantID string, prev, next hermesv1.MbsSessionState, reason string, _ int32, _ string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lifecycle = append(p.lifecycle, integrationLifecycleEvent{
		uid: uid, tenantID: tenantID, prev: prev, next: next, reason: reason,
	})
}

func (p *integrationPublisher) lifecycleSnapshot() []integrationLifecycleEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	cp := make([]integrationLifecycleEvent, len(p.lifecycle))
	copy(cp, p.lifecycle)
	return cp
}

// ─────────────────────────────────────────────────────────────────────
// integrationStream — satisfies grpc.BidiStreamingServer for
// HermesMbs_BridgeLoginServer. Lets the test push BridgeLoginRequest
// values + collect BridgeLoginUpdate values without setting up a real
// gRPC server.
// ─────────────────────────────────────────────────────────────────────

type integrationStream struct {
	grpc.ServerStream
	ctx     context.Context
	recv    chan *hermesv1.BridgeLoginRequest
	mu      sync.Mutex
	sent    []*hermesv1.BridgeLoginUpdate
	sendErr error
}

func newIntegrationStream(ctx context.Context) *integrationStream {
	return &integrationStream{
		ctx:  ctx,
		recv: make(chan *hermesv1.BridgeLoginRequest, 8),
	}
}

func (s *integrationStream) Context() context.Context { return s.ctx }

func (s *integrationStream) Send(msg *hermesv1.BridgeLoginUpdate) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sendErr != nil {
		return s.sendErr
	}
	s.sent = append(s.sent, msg)
	return nil
}

func (s *integrationStream) Recv() (*hermesv1.BridgeLoginRequest, error) {
	select {
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	case msg, ok := <-s.recv:
		if !ok {
			return nil, io.EOF
		}
		return msg, nil
	}
}

func (s *integrationStream) SetHeader(metadata.MD) error  { return nil }
func (s *integrationStream) SendHeader(metadata.MD) error { return nil }
func (s *integrationStream) SetTrailer(metadata.MD)       {}
func (s *integrationStream) RecvMsg(any) error            { return nil }
func (s *integrationStream) SendMsg(any) error            { return nil }

func (s *integrationStream) sentSnapshot() []*hermesv1.BridgeLoginUpdate {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]*hermesv1.BridgeLoginUpdate, len(s.sent))
	copy(cp, s.sent)
	return cp
}

func (s *integrationStream) sentTypes() []string {
	out := []string{}
	for _, u := range s.sentSnapshot() {
		switch u.Event.(type) {
		case *hermesv1.BridgeLoginUpdate_Progress:
			out = append(out, "progress")
		case *hermesv1.BridgeLoginUpdate_Prompt:
			out = append(out, "prompt")
		case *hermesv1.BridgeLoginUpdate_Success:
			out = append(out, "success")
		case *hermesv1.BridgeLoginUpdate_Failure:
			out = append(out, "failure")
		}
	}
	return out
}

// ─────────────────────────────────────────────────────────────────────
// Fixture
// ─────────────────────────────────────────────────────────────────────

type integrationFixture struct {
	h        *handler.Handler
	st       *mock.Store
	pub      *integrationPublisher
	dek      crypto.DataEncryptionKey
	tenantID string
}

func newIntegrationFixture(t *testing.T, client *integrationFakeClient) integrationFixture {
	t.Helper()
	st := mock.NewStore()
	pub := &integrationPublisher{}
	dek := mustTestDEK(t)

	// Wire the factory: bridge.NewDriverFactory hands MautrixDrivers
	// to the handler, but we override ClientFactory to inject the
	// scripted fake so we never touch real mautrix HTTP.
	factory := bridge.NewDriverFactory(bridge.Deps{
		Logger: zerolog.Nop(),
		ClientFactory: func(_ zerolog.Logger, _ bool) (bridge.LoginClient, error) {
			return client, nil
		},
	})

	h, err := handler.NewHandler(handler.Options{
		Store:         st,
		Manager:       integrationManager{},
		Publisher:     pub,
		DriverFactory: factory,
		DEK:           dek,
		PodID:         "pod-integration",
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return integrationFixture{
		h: h, st: st, pub: pub, dek: dek, tenantID: "tenant-A",
	}
}

// mustTestDEK generates a fresh per-test DEK from crypto/rand.
// Mirrors the handler-package newTestDEK helper. Lives here because
// that helper is unexported.
func mustTestDEK(t *testing.T) crypto.DataEncryptionKey {
	t.Helper()
	var dek crypto.DataEncryptionKey
	if _, err := rand.Read(dek[:]); err != nil {
		t.Fatalf("rand DEK: %v", err)
	}
	return dek
}

// loginClientShim is the integration test's import-side alias to the
// exported bridge.LoginClient interface. Kept as a separate name to
// signal "test-only injection point" to readers.
type loginClientShim = bridge.LoginClient

// startMsg / inputMsg helpers
func startMsg(tenant, email, password, totp string) *hermesv1.BridgeLoginRequest {
	return &hermesv1.BridgeLoginRequest{
		Payload: &hermesv1.BridgeLoginRequest_Start{
			Start: &hermesv1.BridgeLoginStart{
				TenantId: tenant, Email: email, Password: password, TotpSecret: totp,
			},
		},
	}
}

func runBridgeLogin(t *testing.T, h *handler.Handler, tenant string) (*integrationStream, context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(handler.WithTenantForTest(context.Background(), tenant))
	stream := newIntegrationStream(ctx)
	done := make(chan error, 1)
	go func() {
		done <- h.BridgeLogin(stream)
	}()
	return stream, cancel, done
}

// ─────────────────────────────────────────────────────────────────────
// Tests
// ─────────────────────────────────────────────────────────────────────

func TestIntegration_BridgeFactory_HappyPath_End2End(t *testing.T) {
	client := &integrationFakeClient{
		script: []integrationScript{
			{step: nil, cookies: goldCookies()},
		},
		payload:  goldPayload(100),
		identity: goldIdentity,
	}
	fx := newIntegrationFixture(t, client)

	stream, cancel, done := runBridgeLogin(t, fx.h, fx.tenantID)
	defer cancel()
	stream.recv <- startMsg(fx.tenantID, "alice@example.com", "pw", "")

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("BridgeLogin returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("BridgeLogin did not return within 3s")
	}

	// Stream events end with success.
	types := stream.sentTypes()
	if len(types) == 0 || types[len(types)-1] != "success" {
		t.Errorf("terminal event not success: %v", types)
	}

	// Persistence: row exists, encrypted columns populated.
	row, err := fx.st.GetSession(context.Background(), 100)
	if err != nil {
		t.Fatalf("GetSession after success: %v", err)
	}
	if row.TenantID != fx.tenantID {
		t.Errorf("tenant_id: %q", row.TenantID)
	}
	if row.State != "active" {
		t.Errorf("state: %q", row.State)
	}
	if len(row.EncryptedAccessToken) == 0 ||
		len(row.EncryptedSecret) == 0 ||
		len(row.EncryptedSessionKey) == 0 {
		t.Errorf("encrypted columns empty: AT=%d Sec=%d SK=%d",
			len(row.EncryptedAccessToken),
			len(row.EncryptedSecret),
			len(row.EncryptedSessionKey))
	}

	// Round-trip decrypt access_token + verify it matches the
	// gold payload (proves the AAD + DEK plumbing all the way down
	// from the bridge driver's loginLoop output).
	plain, err := crypto.DecryptAESGCM(fx.dek, row.EncryptedAccessToken,
		store.BuildAAD(store.AADAccessToken, 100))
	if err != nil {
		t.Fatalf("decrypt access_token: %v", err)
	}
	if string(plain) != "EAAB-integration-token" {
		t.Errorf("decrypted token: got %q", string(plain))
	}

	// BridgeEnvelope JSON roundtrip (plaintext metadata column).
	var env auth.BridgeEnvelope
	if err := json.Unmarshal(row.BridgeEnvelope, &env); err != nil {
		t.Fatalf("BridgeEnvelope unmarshal: %v", err)
	}
	if env.UID != 100 || env.AccessToken != "EAAB-integration-token" {
		t.Errorf("envelope mismatch: %+v", env)
	}
	if env.BridgeDeviceID != "7A17B762-668D-4BEF-A9CF-CD0ABD58231D" {
		t.Errorf("envelope BridgeDeviceID: %q", env.BridgeDeviceID)
	}

	// Lifecycle event published exactly once with reason "created".
	ev := fx.pub.lifecycleSnapshot()
	if len(ev) != 1 {
		t.Fatalf("lifecycle: got %d events want 1", len(ev))
	}
	if ev[0].reason != "created" ||
		ev[0].next != hermesv1.MbsSessionState_MBS_SESSION_STATE_ACTIVE ||
		ev[0].tenantID != fx.tenantID ||
		ev[0].uid != 100 {
		t.Errorf("lifecycle event: %+v", ev[0])
	}
}

func TestIntegration_BridgeFactory_PromptThenInput_End2End(t *testing.T) {
	client := &integrationFakeClient{
		script: []integrationScript{
			{
				step: &bridgev2.LoginStep{
					Type:         bridgev2.LoginStepTypeUserInput,
					StepID:       "captcha",
					Instructions: "Solve",
					UserInputParams: &bridgev2.LoginUserInputParams{
						Fields: []bridgev2.LoginInputDataField{
							{ID: "captcha_response", Type: bridgev2.LoginInputFieldTypeCaptchaCode, Name: "Captcha"},
						},
					},
				},
			},
			{step: nil, cookies: goldCookies()},
		},
		payload:  goldPayload(200),
		identity: goldIdentity,
	}
	fx := newIntegrationFixture(t, client)

	stream, cancel, done := runBridgeLogin(t, fx.h, fx.tenantID)
	defer cancel()
	stream.recv <- startMsg(fx.tenantID, "alice@example.com", "pw", "")

	// Wait for the prompt to be sent to the stream.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		types := stream.sentTypes()
		if len(types) >= 2 && types[1] == "prompt" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Submit the captcha response.
	stream.recv <- &hermesv1.BridgeLoginRequest{
		Payload: &hermesv1.BridgeLoginRequest_Input{
			Input: &hermesv1.BridgeLoginInput{
				FieldId: "captcha_response",
				Value:   "ANSWER",
			},
		},
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("BridgeLogin: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("BridgeLogin did not return")
	}

	// Verify the loginLoop forwarded the captcha to the fake client.
	client.mu.Lock()
	if len(client.inputs) < 2 || client.inputs[1]["captcha_response"] != "ANSWER" {
		t.Errorf("captcha not forwarded: %+v", client.inputs)
	}
	client.mu.Unlock()

	// Stream ended with Success.
	types := stream.sentTypes()
	if types[len(types)-1] != "success" {
		t.Errorf("terminal not success: %v", types)
	}
}

func TestIntegration_BridgeFactory_LoginFailure_NoPersist(t *testing.T) {
	client := &integrationFakeClient{
		script: []integrationScript{
			{err: messagix.ErrTokenInvalidated},
		},
	}
	fx := newIntegrationFixture(t, client)

	stream, cancel, done := runBridgeLogin(t, fx.h, fx.tenantID)
	defer cancel()
	stream.recv <- startMsg(fx.tenantID, "alice@example.com", "wrong", "")

	select {
	case err := <-done:
		// classifyMautrixErr → INVALID_CREDS, handler maps to Unauthenticated.
		if status.Code(err) != codes.Unauthenticated {
			t.Errorf("expected Unauthenticated, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("BridgeLogin did not return")
	}

	// No session persisted.
	if exists, _ := fx.st.ExistsSession(context.Background(), 100); exists {
		t.Errorf("session should NOT be persisted on login failure")
	}
	// No lifecycle event.
	if got := fx.pub.lifecycleSnapshot(); len(got) != 0 {
		t.Errorf("lifecycle should NOT publish on login failure: %+v", got)
	}
	// Terminal stream event is failure.
	types := stream.sentTypes()
	if len(types) == 0 || types[len(types)-1] != "failure" {
		t.Errorf("terminal not failure: %v", types)
	}
}

func TestIntegration_BridgeFactory_CrossTenantRebridge_StillBlocked(t *testing.T) {
	// Chunk-4 audit regression: tenant-A drives a successful bridge
	// for a uid already owned by tenant-B. The handler's persist
	// boundary MUST reject this with PermissionDenied + leave the
	// existing row untouched. This test re-validates that the chunk-5
	// driver doesn't accidentally bypass the check.
	client := &integrationFakeClient{
		script: []integrationScript{
			{step: nil, cookies: goldCookies()},
		},
		payload:  goldPayload(300),
		identity: goldIdentity,
	}
	fx := newIntegrationFixture(t, client)

	// Seed tenant-B's existing row with valid ciphertext under the
	// SAME dek so a successful bypass would silently overwrite real
	// secrets.
	tenantBToken, _ := crypto.EncryptAESGCM(fx.dek, []byte("TENANT_B_TOKEN"),
		store.BuildAAD(store.AADAccessToken, 300))
	tenantBSecret, _ := crypto.EncryptAESGCM(fx.dek, []byte("TENANT_B_SECRET"),
		store.BuildAAD(store.AADSecret, 300))
	tenantBSK, _ := crypto.EncryptAESGCM(fx.dek, []byte("TENANT_B_SK"),
		store.BuildAAD(store.AADSessionKey, 300))
	if err := fx.st.CreateSession(context.Background(), &store.SessionRow{
		UID: 300, TenantID: "tenant-B", State: "active", PodID: "pod-other",
		EncryptedAccessToken: tenantBToken,
		EncryptedSecret:      tenantBSecret,
		EncryptedSessionKey:  tenantBSK,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Attempt rebridge as tenant-A.
	stream, cancel, done := runBridgeLogin(t, fx.h, "tenant-A")
	defer cancel()
	stream.recv <- startMsg("tenant-A", "attacker@evil.com", "stolen-pw", "")

	select {
	case err := <-done:
		if status.Code(err) != codes.PermissionDenied {
			t.Errorf("expected PermissionDenied, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("BridgeLogin did not return")
	}

	// tenant-B's row contents UNCHANGED.
	row, _ := fx.st.GetSession(context.Background(), 300)
	if row.TenantID != "tenant-B" {
		t.Errorf("tenant_id MUTATED: %q", row.TenantID)
	}
	plain, err := crypto.DecryptAESGCM(fx.dek, row.EncryptedAccessToken,
		store.BuildAAD(store.AADAccessToken, 300))
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if string(plain) != "TENANT_B_TOKEN" {
		t.Errorf("token MUTATED: got %q want TENANT_B_TOKEN", string(plain))
	}
	// No lifecycle event for the attempt.
	if got := fx.pub.lifecycleSnapshot(); len(got) != 0 {
		t.Errorf("lifecycle should NOT publish on cross-tenant attack: %+v", got)
	}
}

func TestIntegration_BridgeFactory_ContextCancel_CleanExit(t *testing.T) {
	// Client never emits — loop sits waiting on DoLoginSteps.
	// Cancel the stream ctx; BridgeLogin should return Canceled
	// + no goroutines should leak (assertable via the channel-close).
	client := &integrationFakeClient{
		script: []integrationScript{}, // empty → DoLoginSteps holds until ctx
	}
	fx := newIntegrationFixture(t, client)

	stream, cancel, done := runBridgeLogin(t, fx.h, fx.tenantID)
	stream.recv <- startMsg(fx.tenantID, "alice@example.com", "pw", "")

	// Let the loop reach DoLoginSteps then cancel.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		// Either the gRPC-wrapped Canceled OR a bare context.Canceled
		// is acceptable — both signal "stream torn down on client
		// cancel". The handler doesn't always wrap ctx errors into
		// gRPC statuses when the cancel races the main loop's emit.
		if err == nil {
			t.Errorf("expected cancellation error, got nil")
		} else if code := status.Code(err); code != codes.Canceled && err.Error() != context.Canceled.Error() {
			t.Errorf("expected Canceled, got %v (code=%v)", err, code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("BridgeLogin did not return after ctx cancel")
	}

	// No session persisted.
	if exists, _ := fx.st.ExistsSession(context.Background(), 100); exists {
		t.Errorf("session leaked on cancel")
	}
}

// Compile-time guard: the integration fixture's atomic + io imports
// are referenced indirectly via test helpers; assert they're real.
var _ = atomic.Int32{}
var _ = io.EOF
