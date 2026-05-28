-- Reverse of 000001_init.up.sql. Drops in reverse FK-dependency order.

DROP TRIGGER IF EXISTS trg_mbs_sessions_updated_at ON mbs_sessions;
DROP FUNCTION IF EXISTS mbs_sessions_set_updated_at();

DROP TABLE IF EXISTS mbs_phone_threads;
DROP TABLE IF EXISTS mbs_session_assets;
DROP TABLE IF EXISTS mbs_sessions;
