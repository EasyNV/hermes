-- Stage E1 (Chunk 2): convert plaintext secret columns to BYTEA so the
-- application layer can store AES-256-GCM ciphertext at rest. Cookies
-- column changes from JSONB to BYTEA as well — the encrypted form is
-- opaque bytes, not JSON.
--
-- This migration is safe to run with empty tables (first deploy) or
-- after the legacy import job (which writes already-encrypted bytes
-- directly). It is NOT safe to run with plaintext rows still present;
-- the operator runs MBS_ENCRYPT_REWRITE_ON_STARTUP=true on the first
-- pod start to seal any leftover cleartext.

ALTER TABLE mbs_sessions ALTER COLUMN access_token TYPE BYTEA USING access_token::bytea;
ALTER TABLE mbs_sessions ALTER COLUMN secret      TYPE BYTEA USING secret::bytea;
ALTER TABLE mbs_sessions ALTER COLUMN session_key TYPE BYTEA USING session_key::bytea;

-- cookies was JSONB plaintext; the encrypted form is opaque bytes.
-- ::text::bytea preserves the JSON serialization losslessly so a
-- subsequent encrypt-rewrite job can decode → encrypt → store.
ALTER TABLE mbs_sessions ALTER COLUMN cookies TYPE BYTEA USING cookies::text::bytea;

COMMENT ON COLUMN mbs_sessions.access_token IS
    'AES-256-GCM encrypted OAuth user token. AAD: mbs.access_token.uid=<uid>. Cleartext never on disk.';
COMMENT ON COLUMN mbs_sessions.secret IS
    'AES-256-GCM encrypted CAA secret. AAD: mbs.secret.uid=<uid>.';
COMMENT ON COLUMN mbs_sessions.session_key IS
    'AES-256-GCM encrypted CAA session_key. AAD: mbs.session_key.uid=<uid>.';
COMMENT ON COLUMN mbs_sessions.cookies IS
    'AES-256-GCM encrypted cookie jar (JSON cleartext, opaque ciphertext). AAD: mbs.cookies.uid=<uid>. Set-Cookie merges happen in memory then re-seal.';
