-- Stage F follow-up (2026-06-02): add login_email column to mbs_sessions.
--
-- The email the operator logs in with (BridgeLoginStart.email) was used
-- transiently during the login state machine and then discarded — there was
-- no column to persist it. Operators had no way to tell which Meta account a
-- session row (keyed only by numeric uid) corresponds to, both in the MBS
-- Pages list and in the campaign sender picker.
--
-- Persisted at bridge-login success (cmd/mbs persistence path) going forward.
-- Existing rows get '' and can be backfilled manually; the NOT NULL DEFAULT ''
-- keeps the constraint satisfiable without a backfill job.

ALTER TABLE mbs_sessions
    ADD COLUMN login_email TEXT NOT NULL DEFAULT '';

COMMENT ON COLUMN mbs_sessions.login_email IS
    'Email/identifier the operator bridged this account with. Cleartext, '
    'display-only — used to identify the account in the UI. Populated at '
    'bridge-login success; empty for pre-2026-06-02 rows until backfilled.';
