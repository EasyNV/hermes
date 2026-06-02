package store

import "time"

// SessionRow is the in-Go representation of one row in mbs_sessions.
//
// Encrypted fields hold AES-256-GCM ciphertext as produced by
// pkg/crypto.EncryptAESGCM with AAD = BuildAAD(<column>, uid). The
// store layer does NOT touch the DEK or perform any encryption; that
// happens at the session-manager boundary (chunk 3+).
type SessionRow struct {
	UID         int64
	TenantID    string
	DisplayName string
	LoginEmail  string // email/identifier the operator bridged with; display-only
	State       string // active | suspended | burned | bridging

	// ─── pod_id ownership (CAS-claimed; '' = unclaimed) ─────────────
	PodID string

	// ─── Encrypted at rest (BYTEA) ──────────────────────────────────
	EncryptedAccessToken []byte
	EncryptedSecret      []byte
	EncryptedSessionKey  []byte
	EncryptedCookies     []byte
	EncryptedTOTPSecret  []byte // nullable

	// ─── Identity (rotates rarely) ──────────────────────────────────
	MachineID        string
	DeviceID         string
	FamilyDeviceID   string
	AppVersion       string
	BuildNumber      string
	DeviceModel      string
	AndroidVer       string
	Manufacturer     string
	Locale           string
	Density          string
	ABI              string
	VersionID        string
	ScreenWidth      int
	ScreenHeight     int
	MQTTCapabilities int

	// ─── Bridge metadata ────────────────────────────────────────────
	BridgeEnvelope []byte // raw JSONB; non-secret

	// ─── Health (Stage D) ───────────────────────────────────────────
	LastRefreshedAt *time.Time
	LastValidatedAt *time.Time
	LastConnackRC   *int16
	LastConnackAt   *time.Time

	// ─── Burn audit ─────────────────────────────────────────────────
	BurnedAt     *time.Time
	BurnedReason string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// AssetRow mirrors one row of mbs_session_assets — a single business
// asset (page + WABA + WEC mailbox) discovered for a session.
type AssetRow struct {
	UID                    int64
	PageID                 string
	PageName               string
	BusinessPresenceNodeID string
	BusinessID             string // Stage B.1
	BusinessName           string // Stage B.1
	WabaID                 string
	WecMailboxID           string
	WecPhoneNumber         string
	IgAccountID            string
	IsPrimary              bool
	WECAccountRegistered   bool // Stage B.2
	DiscoveredAt           time.Time
}

// PhoneThreadRow mirrors one row of mbs_phone_threads — the
// phone→thread_id resolver cache populated by
// BizInboxWhatsAppCustomerMutation results (Path C).
type PhoneThreadRow struct {
	UID          int64
	PageID       string
	Phone        string // E.164 minus leading + (e.g. "6282142497885")
	ThreadID     string // customer_id from mutation
	WecMailboxID string
	LastSendAt   *time.Time
	CreatedAt    time.Time
}
