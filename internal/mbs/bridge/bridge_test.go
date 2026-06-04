package bridge

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hermes-waba/hermes/internal/mbs/handler"
	"github.com/rs/zerolog"
	mautrixmessagix "go.mau.fi/mautrix-meta/pkg/messagix"
	mautrixcookies "go.mau.fi/mautrix-meta/pkg/messagix/cookies"
	mautrixv2 "maunium.net/go/mautrix/bridgev2"
)

// fakeFactoryClient wraps fakeLoginClient so the bridge ClientFactory
// can hand it out without re-running productionClientFactory (which
// would actually try to wire mautrix-meta HTTP). Tests injecting this
// stay 100% offline.
func fakeFactoryClient(client *fakeLoginClient) func(zerolog.Logger, bool, string) (loginClient, error) {
	return func(_ zerolog.Logger, _ bool, _ string) (loginClient, error) {
		return client, nil
	}
}

func TestNewDriverFactory_NilDepsProducesNonNilDriver(t *testing.T) {
	factory := NewDriverFactory(Deps{
		// All zero — defaults apply.
		ClientFactory: fakeFactoryClient(&fakeLoginClient{
			script:       []scriptedTransition{{step: nil, cookies: successCookies()}},
			finalPayload: successPayload(),
			identity:     successIdentity,
		}),
	})
	d := factory(handler.DriverOptions{})
	if d == nil {
		t.Fatal("factory returned nil")
	}
	if _, ok := d.(*MautrixDriver); !ok {
		t.Errorf("expected *MautrixDriver, got %T", d)
	}
}

func TestMautrixDriver_HappyPathThroughFactory(t *testing.T) {
	factory := NewDriverFactory(Deps{
		ClientFactory: fakeFactoryClient(&fakeLoginClient{
			script:       []scriptedTransition{{step: nil, cookies: successCookies()}},
			finalPayload: successPayload(),
			identity:     successIdentity,
		}),
	})
	d := factory(handler.DriverOptions{})
	defer d.Close()

	updates, err := d.Run(context.Background(), handler.DriverStartRequest{
		Email: "alice@example.com", Password: "pw",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Drain to success.
	var terminal handler.DriverUpdate
	timer := time.NewTimer(500 * time.Millisecond)
	defer timer.Stop()
	for {
		select {
		case u, ok := <-updates:
			if !ok {
				goto done
			}
			terminal = u
		case <-timer.C:
			t.Fatal("did not terminate")
		}
	}
done:
	if terminal.Kind != handler.UpdateKindSuccess {
		t.Fatalf("terminal not Success: %+v", terminal)
	}
}

func TestMautrixDriver_CloseIsIdempotent(t *testing.T) {
	factory := NewDriverFactory(Deps{
		ClientFactory: fakeFactoryClient(&fakeLoginClient{}),
	})
	d := factory(handler.DriverOptions{})
	for i := 0; i < 3; i++ {
		if err := d.Close(); err != nil {
			t.Errorf("Close #%d: %v", i, err)
		}
	}
}

func TestMautrixDriver_RunOnClosedDriver(t *testing.T) {
	factory := NewDriverFactory(Deps{
		ClientFactory: fakeFactoryClient(&fakeLoginClient{}),
	})
	d := factory(handler.DriverOptions{})
	_ = d.Close()

	updates, err := d.Run(context.Background(), handler.DriverStartRequest{
		Email: "x", Password: "y",
	})
	if err == nil {
		t.Errorf("Run on closed driver should error")
	}
	// Channel should be closed already.
	select {
	case _, ok := <-updates:
		if ok {
			t.Errorf("closed driver should return closed channel")
		}
	default:
		// Allow either a closed channel or a deferred close.
	}
}

func TestMautrixDriver_SubmitBeforeRun_Buffers(t *testing.T) {
	factory := NewDriverFactory(Deps{
		ClientFactory: fakeFactoryClient(&fakeLoginClient{}),
	})
	d := factory(handler.DriverOptions{})
	defer d.Close()

	// Submit before Run — should buffer (cap=4) without error.
	for i := 0; i < 4; i++ {
		if err := d.Submit(handler.DriverInput{FieldID: "x", Value: "y"}); err != nil {
			t.Errorf("Submit #%d: %v", i, err)
		}
	}
	// 5th overflows the buffer.
	if err := d.Submit(handler.DriverInput{FieldID: "x", Value: "y"}); err == nil {
		t.Errorf("expected buffer-full error on 5th submit")
	}
}

func TestMautrixDriver_SubmitAfterCloseReturnsError(t *testing.T) {
	factory := NewDriverFactory(Deps{
		ClientFactory: fakeFactoryClient(&fakeLoginClient{}),
	})
	d := factory(handler.DriverOptions{})
	_ = d.Close()

	if err := d.Submit(handler.DriverInput{FieldID: "x", Value: "y"}); err == nil {
		t.Errorf("Submit on closed driver should error")
	}
}

func TestMautrixDriver_ClientFactoryError_PropagatesViaRun(t *testing.T) {
	factory := NewDriverFactory(Deps{
		ClientFactory: func(_ zerolog.Logger, _ bool, _ string) (loginClient, error) {
			return nil, errors.New("bad client factory")
		},
	})
	d := factory(handler.DriverOptions{})
	defer d.Close()

	updates, err := d.Run(context.Background(), handler.DriverStartRequest{
		Email: "x", Password: "y",
	})
	if err == nil {
		t.Errorf("Run should error when client factory fails")
	}
	// Channel is closed when factory fails.
	select {
	case _, ok := <-updates:
		if ok {
			t.Errorf("channel should be closed on factory failure")
		}
	default:
	}
}

func TestMautrixDriver_RunOnce_SecondCallReturnsCachedError(t *testing.T) {
	// Inject a factory that always errors, run once → error cached;
	// run again → should return the same cached error (sync.Once semantics).
	factory := NewDriverFactory(Deps{
		ClientFactory: func(_ zerolog.Logger, _ bool, _ string) (loginClient, error) {
			return nil, errors.New("boom")
		},
	})
	d := factory(handler.DriverOptions{})
	defer d.Close()

	_, err1 := d.Run(context.Background(), handler.DriverStartRequest{Email: "a", Password: "b"})
	_, err2 := d.Run(context.Background(), handler.DriverStartRequest{Email: "a", Password: "b"})
	if err1 == nil || err2 == nil {
		t.Errorf("expected error both times")
	}
	if err1.Error() != err2.Error() {
		t.Errorf("expected cached error: err1=%v err2=%v", err1, err2)
	}
}

func TestMautrixDriver_CloseCancelsRunningLoop(t *testing.T) {
	// Use a DoLoginSteps that blocks until ctx fires, so Close() can
	// actually unblock it. Sleeping unconditionally would defeat the
	// purpose of the test (we'd be measuring the sleep, not the cancel).
	blocker := &blockingLoginClient{}
	factory := NewDriverFactory(Deps{
		ClientFactory: func(_ zerolog.Logger, _ bool, _ string) (loginClient, error) {
			return blocker, nil
		},
	})
	d := factory(handler.DriverOptions{})

	updates, err := d.Run(context.Background(), handler.DriverStartRequest{
		Email: "x", Password: "y",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Allow loop to enter DoLoginSteps then cancel via Close.
	time.Sleep(30 * time.Millisecond)
	_ = d.Close()

	// Channel should drain + close within a short window.
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case _, ok := <-updates:
			if !ok {
				return
			}
		case <-timer.C:
			t.Fatal("channel did not close after Close")
		}
	}
}

// blockingLoginClient blocks DoLoginSteps until ctx fires. Used to
// validate ctx-cancel paths without time-based races. Implements
// loginClient (defined in login_loop.go).
type blockingLoginClient struct{}

func (blockingLoginClient) DoLoginSteps(ctx context.Context, _ map[string]string) (*mautrixv2.LoginStep, *mautrixcookies.Cookies, error) {
	<-ctx.Done()
	return nil, nil, ctx.Err()
}
func (blockingLoginClient) LastLoginPayload() *mautrixmessagix.BloksLoginActionResponsePayload {
	return nil
}
func (blockingLoginClient) LoginIdentity() (string, string, string) { return "", "", "" }

func TestDefaultDiscoverer_IsNonNil(t *testing.T) {
	if d := DefaultDiscoverer(); d == nil {
		t.Errorf("DefaultDiscoverer returned nil")
	}
}

func TestResolveDeps_AppliesDefaults(t *testing.T) {
	got := resolveDeps(Deps{})
	if got.AssetDiscoverer == nil {
		t.Errorf("AssetDiscoverer default not applied")
	}
	if got.Timeout != 180*time.Second {
		t.Errorf("Timeout default: got %v want 180s", got.Timeout)
	}
	if got.Await2FATimeout != 120*time.Second {
		t.Errorf("Await2FATimeout default: got %v want 120s", got.Await2FATimeout)
	}
	if got.ClientFactory == nil {
		t.Errorf("ClientFactory default not applied")
	}
}

func TestResolveDeps_PreservesProvidedValues(t *testing.T) {
	custom := &fakeLoginClient{}
	got := resolveDeps(Deps{
		Timeout:         99 * time.Second,
		Await2FATimeout: 88 * time.Second,
		ClientFactory:   fakeFactoryClient(custom),
	})
	if got.Timeout != 99*time.Second {
		t.Errorf("custom Timeout not preserved: %v", got.Timeout)
	}
	if got.Await2FATimeout != 88*time.Second {
		t.Errorf("custom Await2FATimeout not preserved: %v", got.Await2FATimeout)
	}
}
