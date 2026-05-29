-- Reverse of 000002. Type-level reversibility only — the bytes inside
-- access_token / secret / session_key remain ciphertext but the column
-- type is restored to TEXT (hex-encoded). cookies cannot meaningfully
-- round-trip back to JSONB; we explicitly nuke to '{}'::jsonb. Rollback
-- of this migration therefore requires every session to re-bridge.

ALTER TABLE mbs_sessions ALTER COLUMN access_token TYPE TEXT USING encode(access_token, 'hex');
ALTER TABLE mbs_sessions ALTER COLUMN secret      TYPE TEXT USING encode(secret,      'hex');
ALTER TABLE mbs_sessions ALTER COLUMN session_key TYPE TEXT USING encode(session_key, 'hex');

-- cookies: drop the BYTEA default, change type back to JSONB, reinstall
-- the original JSONB default. The empty-bytes → '{}'::jsonb mapping is
-- correct: both represent "no jar yet" in their respective worlds.
ALTER TABLE mbs_sessions ALTER COLUMN cookies DROP DEFAULT;
ALTER TABLE mbs_sessions ALTER COLUMN cookies TYPE JSONB USING '{}'::jsonb;
ALTER TABLE mbs_sessions ALTER COLUMN cookies SET DEFAULT '{}'::jsonb;
