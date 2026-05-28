-- Stage E1 (Chunk 2):
--   - pod_id ownership column (matches wa_numbers.pod_id pattern; replaces
--     advisory-lock proposal — survives pgbouncer in tx mode)
--   - Stage D cookie freshness columns
--   - Stage B/B.1/B.2 asset enrichment columns
--
-- All additions use constant DEFAULTs, so Postgres 11+ does metadata-only
-- ADD COLUMN — fast on tables with existing rows.

-- ─── Pod ownership (CAS-style claim) ──────────────────────────────
ALTER TABLE mbs_sessions ADD COLUMN pod_id TEXT NOT NULL DEFAULT '';
CREATE INDEX idx_mbs_sessions_pod_id ON mbs_sessions (pod_id) WHERE pod_id <> '';

-- ─── Stage D cookie freshness ─────────────────────────────────────
ALTER TABLE mbs_sessions ADD COLUMN last_refreshed_at TIMESTAMPTZ;
ALTER TABLE mbs_sessions ADD COLUMN last_validated_at TIMESTAMPTZ;
CREATE INDEX idx_mbs_sessions_refresh_due
    ON mbs_sessions (last_refreshed_at)
    WHERE state = 'active';

-- ─── Stage B/B.1/B.2 asset enrichment ─────────────────────────────
ALTER TABLE mbs_session_assets ADD COLUMN business_id TEXT;
ALTER TABLE mbs_session_assets ADD COLUMN business_name TEXT;
ALTER TABLE mbs_session_assets ADD COLUMN wec_account_registered BOOLEAN NOT NULL DEFAULT FALSE;

COMMENT ON COLUMN mbs_sessions.pod_id IS
    'Which hermes-mbs pod currently owns this session. Empty = unclaimed. Single-pod compose: always "hermes-mbs". Multi-pod K8s: claimed via CAS UPDATE on first connect.';
COMMENT ON COLUMN mbs_sessions.last_refreshed_at IS
    'Most recent timestamp when web.Client received Set-Cookie headers from business.facebook.com. NULL = never refreshed since envelope creation.';
COMMENT ON COLUMN mbs_sessions.last_validated_at IS
    'Most recent timestamp when any web.Client GET returned HTTP 200 against this session. NULL = never validated.';
COMMENT ON COLUMN mbs_session_assets.business_id IS
    'Parent business id from BizAppBusinessScopingConfigQuery scope_id. NULL until enriched.';
COMMENT ON COLUMN mbs_session_assets.business_name IS
    'Human-readable business name (Stage B.1).';
COMMENT ON COLUMN mbs_session_assets.wec_account_registered IS
    'Whether BizInboxWhatsAppConfigQuery confirms WEC mailbox is active (Stage B.2). FALSE blocks send-to-phone unless --force.';
