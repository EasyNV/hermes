-- Stage F (chunk 1): add display_name column to mbs_sessions.
--
-- internal/mbs/store/pg.go SELECTs and INSERTs `display_name` in every
-- query path (CreateSession, ListSessionsByPod, ListSessionsNeedingRefresh,
-- etc.) but the column was never added by Stage E1 migrations 1-3. The
-- service boots successfully but the reconnect loop and refresh ticker
-- spam errors with SQLSTATE 42703 ("column does not exist") on every
-- tick.
--
-- Adding here as a discrete chunk-1 follow-up. Existing rows (none in
-- dev, possibly some in staging) get an empty string default so the
-- NOT NULL constraint is satisfiable without a backfill job.

ALTER TABLE mbs_sessions
    ADD COLUMN display_name TEXT NOT NULL DEFAULT '';

COMMENT ON COLUMN mbs_sessions.display_name IS
    'Human-readable session label shown in the agent inbox. Cleartext.';
