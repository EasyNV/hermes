-- Revert 000006: drop login_email column.
ALTER TABLE mbs_sessions
    DROP COLUMN IF EXISTS login_email;
