-- mbs/0001_init.sql
-- Phase 1 schema for hermes-mbs (Meta Business Suite session management).
-- See docs/research/mbs-bridge-integration.md (architecture) and
-- mbs-bridge-integration-phase2.md (current state) for the rationale.

-- ─────────────────────────────────────────────────────────────────
-- mbs_sessions — one row per (tenant, uid)
-- Mirrors the file-based ~/.mbs-native/sessions/<uid>.json shape used
-- by the standalone mbs-native CLI.
-- ─────────────────────────────────────────────────────────────────

CREATE TABLE mbs_sessions (
    uid                BIGINT PRIMARY KEY,
    tenant_id          UUID NOT NULL REFERENCES tenants(id),

    -- ─── Auth (rotates on bridge re-login / token refresh) ────────
    access_token       TEXT NOT NULL,
    session_key        TEXT NOT NULL,
    secret             TEXT NOT NULL,
    machine_id         TEXT NOT NULL,

    -- ─── Device identity (persistent across token refreshes) ─────
    -- These are the cache keys the MQTToT broker uses; rotating them
    -- forces a fresh CONNECT #1 warmup.
    device_id          UUID NOT NULL,
    family_device_id   UUID NOT NULL,

    -- ─── User-Agent profile ──────────────────────────────────────
    -- Rarely changes; bump when BizApp app_version bumps.
    app_version        TEXT NOT NULL DEFAULT '551.0.0.55.106',
    build_number       TEXT NOT NULL DEFAULT '955655792',
    device_model       TEXT NOT NULL DEFAULT 'SM-S931B',
    android_ver        TEXT NOT NULL DEFAULT '15',
    manufacturer       TEXT NOT NULL DEFAULT 'samsung',
    locale             TEXT NOT NULL DEFAULT 'en_US',
    density            TEXT NOT NULL DEFAULT '2.99375',
    screen_width       INT  NOT NULL DEFAULT 1080,
    screen_height      INT  NOT NULL DEFAULT 2340,
    abi                TEXT NOT NULL DEFAULT 'arm64-v8a',
    version_id         TEXT NOT NULL DEFAULT '26854813974149875',
    mqtt_capabilities  INT  NOT NULL DEFAULT 514,

    -- ─── Bridge metadata ─────────────────────────────────────────
    bridge_source      TEXT NOT NULL DEFAULT 'mautrix-meta-ios',
    bridge_envelope    JSONB NOT NULL,    -- raw BridgeEnvelope blob

    -- ─── Session cookies (preserved for refresh paths) ───────────
    cookies            JSONB NOT NULL DEFAULT '{}'::jsonb,

    -- ─── Status ──────────────────────────────────────────────────
    state              TEXT NOT NULL DEFAULT 'active',
                       -- active | suspended | burned | bridging
    last_connack_rc    SMALLINT,
    last_connack_at    TIMESTAMPTZ,
    burned_at          TIMESTAMPTZ,
    burned_reason      TEXT,

    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT mbs_sessions_state_chk
        CHECK (state IN ('active', 'suspended', 'burned', 'bridging')),
    UNIQUE (tenant_id, uid)
);

CREATE INDEX idx_mbs_sessions_tenant_state ON mbs_sessions (tenant_id, state);
CREATE INDEX idx_mbs_sessions_updated      ON mbs_sessions (updated_at);

COMMENT ON COLUMN mbs_sessions.access_token IS
    'OAuth user token from mautrix-meta CAA login. Treat as secret.';
COMMENT ON COLUMN mbs_sessions.bridge_envelope IS
    'Original BridgeEnvelope v1 blob — useful for refresh paths and audit.';
COMMENT ON COLUMN mbs_sessions.device_id IS
    'CONNECT field 4.8 — the MQTToT broker''s session cache key. Rotating burns the cached (uid,device_id)->(token,UA) entry.';


-- ─────────────────────────────────────────────────────────────────
-- mbs_session_assets — discovered business assets per session
-- (one session may admin multiple WABA-connected pages)
-- ─────────────────────────────────────────────────────────────────

CREATE TABLE mbs_session_assets (
    uid                       BIGINT NOT NULL REFERENCES mbs_sessions(uid) ON DELETE CASCADE,
    page_id                   TEXT NOT NULL,
    page_name                 TEXT,
    business_presence_node_id TEXT,
    waba_id                   TEXT,
    wec_mailbox_id            TEXT,
    wec_phone_number          TEXT,
    ig_account_id             TEXT,
    is_primary                BOOLEAN NOT NULL DEFAULT FALSE,
    discovered_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    PRIMARY KEY (uid, page_id)
);

CREATE INDEX idx_mbs_session_assets_waba    ON mbs_session_assets (waba_id) WHERE waba_id IS NOT NULL;
CREATE INDEX idx_mbs_session_assets_primary ON mbs_session_assets (uid)     WHERE is_primary = TRUE;
-- One primary per session
CREATE UNIQUE INDEX uniq_mbs_session_assets_one_primary
    ON mbs_session_assets (uid) WHERE is_primary = TRUE;


-- ─────────────────────────────────────────────────────────────────
-- mbs_phone_threads — phone → thread_id resolver cache
-- (Path C: BizInboxWhatsAppCustomerMutation result cache)
-- ─────────────────────────────────────────────────────────────────

CREATE TABLE mbs_phone_threads (
    uid          BIGINT NOT NULL REFERENCES mbs_sessions(uid) ON DELETE CASCADE,
    page_id      TEXT NOT NULL,                    -- which page resolved this
    phone        TEXT NOT NULL,                    -- E.164 minus leading + (e.g. "6282142497885")
    thread_id    TEXT NOT NULL,                    -- customer_id returned by mutation
    wec_mailbox_id TEXT NOT NULL,
    last_send_at TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    PRIMARY KEY (uid, page_id, phone)
);

CREATE INDEX idx_mbs_phone_threads_uid_recent ON mbs_phone_threads (uid, last_send_at DESC NULLS LAST);

COMMENT ON TABLE mbs_phone_threads IS
    'Cache of BizInboxWhatsAppCustomerMutation results. Same (uid, page_id, phone) returns the same thread_id deterministically — no TTL needed; evict on explicit bust.';


-- ─────────────────────────────────────────────────────────────────
-- Optional: encrypted TOTP secret column (for unattended re-bridge)
-- Application encrypts before insert; DB sees only ciphertext.
-- ─────────────────────────────────────────────────────────────────

ALTER TABLE mbs_sessions ADD COLUMN totp_secret_enc BYTEA;

COMMENT ON COLUMN mbs_sessions.totp_secret_enc IS
    'Application-encrypted TOTP base32 secret. NULL when 2FA is push-only or the operator did not opt in to unattended re-bridge.';


-- ─────────────────────────────────────────────────────────────────
-- updated_at trigger
-- ─────────────────────────────────────────────────────────────────

CREATE OR REPLACE FUNCTION mbs_sessions_set_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_mbs_sessions_updated_at
    BEFORE UPDATE ON mbs_sessions
    FOR EACH ROW
    EXECUTE FUNCTION mbs_sessions_set_updated_at();
