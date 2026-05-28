package handler

import (
	"context"
	"time"

	"mbs-native/auth"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/hermes-waba/hermes/internal/mbs/store"
	"github.com/rs/zerolog"
)

// ──────────────────────────────────────────────────────────────────
// bridge.Driver interface (chunk 4 deliverable; impl in chunk 5)
// ──────────────────────────────────────────────────────────────────
//
// Driver runs one CAA-login attempt and emits a stream of Updates.
// Implementations:
//
//   - fakeDriver (chunk 4 tests) — scripted Update sequence
//   - bridge.MautrixDriver (chunk 5) — real CAA login via mautrix-meta
//
// Concurrency: one Driver per BridgeLogin RPC invocation. No reuse.
// The handler closes the driver via Close() in defer.
//
// Cancellation: ctx cancel on Run MUST cause the driver to close its
// update channel and stop within Options.Timeout. Drivers that hang
// past timeout will be force-released by the bridge semaphore but
// may leak goroutines — bridge impls MUST honor ctx promptly.
type Driver interface {
	// Run starts a login attempt. Returns the update channel, which is
	// closed when the attempt terminates (Success/Failure/Cancelled).
	Run(ctx context.Context, req DriverStartRequest) (<-chan DriverUpdate, error)

	// Submit feeds a user-provided input (e.g., 2FA code) into an
	// active run. Returns an error if there's no active prompt
	// waiting for this field.
	Submit(input DriverInput) error

	// Close releases driver-owned resources. Safe to call multiple
	// times (idempotent). Handler invokes via defer.
	Close() error
}

// DriverFactory builds a fresh Driver per BridgeLogin RPC invocation.
// The handler invokes this once per stream. The factory itself is
// injected at handler construction time so tests can swap in a fake
// without modifying handler code.
type DriverFactory func(opts DriverOptions) Driver

// DriverOptions are per-invocation knobs. Logger carries tenant/email
// tags for the duration of one login attempt.
type DriverOptions struct {
	Logger          zerolog.Logger
	Timeout         time.Duration // default 180s
	Await2FATimeout time.Duration // default 120s
}

// DriverStartRequest is the first thing the handler sends to a driver
// after Run. Bundles credential inputs + persistence preferences.
//
// IMPORTANT: TenantID is informational (for log enrichment). The
// handler does its own persist after Success — drivers MUST NOT
// write to the store directly.
type DriverStartRequest struct {
	Email             string
	Password          string
	TOTPSecret        string // base32; "" if none
	ForceNewDeviceID  bool
	PersistTOTPSecret bool   // handler decides whether to encrypt + store
	TenantID          string // logging only
}

// DriverInput is one user-supplied field for an active Prompt.
type DriverInput struct {
	FieldID string // e.g. "totp_code", "captcha_response"
	Value   string
}

// DriverUpdateKind tags each DriverUpdate.
type DriverUpdateKind int

const (
	UpdateKindProgress DriverUpdateKind = iota + 1
	UpdateKindPrompt
	UpdateKindSuccess
	UpdateKindFailure
)

// DriverUpdate is the union event type the driver emits on the channel
// returned by Run. Exactly one of Progress/Prompt/Success/Failure is
// non-nil; Kind says which.
type DriverUpdate struct {
	Kind     DriverUpdateKind
	Progress *DriverProgress
	Prompt   *DriverPrompt
	Success  *DriverSuccess
	Failure  *DriverFailure
}

// DriverProgress is a no-action heartbeat — useful for the UI ("calling
// CAA", "discovering assets"). Maps 1:1 to hermesv1.BridgeLoginProgress.
type DriverProgress struct {
	Stage  hermesv1.BridgeLoginStage
	Detail string
}

// DriverPrompt is a user-input request. The handler relays it to the
// stream as BridgeLoginPrompt; client responds with a Submit input.
//
// AUTO-FILL: if StepID == "two_step_verification" and the StartRequest
// had a TOTPSecret, the handler computes the code and Submits without
// surfacing the prompt to the gateway/UI.
type DriverPrompt struct {
	StepID       string
	Instructions string
	Fields       []DriverPromptField
}

type DriverPromptField struct {
	ID, Name, Type string // Type: "text" | "code" | "password"
}

// DriverSuccess is the terminal happy-path event. The handler encrypts
// the secrets in Creds, persists, then sends BridgeLoginSuccess to
// the stream.
//
// Drivers MUST populate Assets + (when discoverable) PrimaryAsset.
// The mautrix-meta driver runs PickWABAAsset before emitting Success.
type DriverSuccess struct {
	UID            int64
	DisplayName    string
	Creds          *auth.Creds          // plaintext — handler encrypts before persist
	BridgeEnvelope *auth.BridgeEnvelope // cookies + meta; opaque JSON
	Assets         []*store.AssetRow
	PrimaryAsset   *store.AssetRow // result of PickWABAAsset; nil if no WABA-connected page
}

// DriverFailure is the terminal sad-path event. Code goes into the
// stream as-is; gRPC status code is derived via mapBridgeErr.
type DriverFailure struct {
	Code      hermesv1.BridgeLoginErrorCode
	Message   string
	Retryable bool
}
