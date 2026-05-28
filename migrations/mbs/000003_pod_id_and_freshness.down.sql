-- Reverse of 000003.

ALTER TABLE mbs_session_assets DROP COLUMN IF EXISTS wec_account_registered;
ALTER TABLE mbs_session_assets DROP COLUMN IF EXISTS business_name;
ALTER TABLE mbs_session_assets DROP COLUMN IF EXISTS business_id;

DROP INDEX IF EXISTS idx_mbs_sessions_refresh_due;
ALTER TABLE mbs_sessions DROP COLUMN IF EXISTS last_validated_at;
ALTER TABLE mbs_sessions DROP COLUMN IF EXISTS last_refreshed_at;

DROP INDEX IF EXISTS idx_mbs_sessions_pod_id;
ALTER TABLE mbs_sessions DROP COLUMN IF EXISTS pod_id;
