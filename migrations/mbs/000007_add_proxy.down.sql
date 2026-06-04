-- Rollback MBS proxy support (2026-06-03).

DROP INDEX IF EXISTS idx_mbs_sessions_proxy;
ALTER TABLE mbs_sessions DROP COLUMN IF EXISTS proxy_assigned_at;
ALTER TABLE mbs_sessions DROP COLUMN IF EXISTS proxy_id;
