-- Stage E3 chunk 1: add channel discriminator + MBS-specific keys to
-- conversations; add mbs_mid to messages. Additive — existing WA rows
-- default to channel='wa' and pass the new CHECK constraint.

BEGIN;

ALTER TABLE conversations
  ADD COLUMN channel TEXT NOT NULL DEFAULT 'wa'
    CHECK (channel IN ('wa', 'mbs'));

ALTER TABLE conversations
  ALTER COLUMN wa_number_id DROP NOT NULL;

ALTER TABLE conversations
  ADD COLUMN mbs_session_uid TEXT,
  ADD COLUMN mbs_thread_id   TEXT,
  ADD COLUMN mbs_page_id     TEXT;

-- WA rows must have wa_number_id; MBS rows must have both
-- mbs_session_uid and mbs_thread_id. Mutually exclusive keying.
ALTER TABLE conversations
  ADD CONSTRAINT chk_channel_keys CHECK (
    (channel = 'wa'  AND wa_number_id    IS NOT NULL AND mbs_thread_id IS NULL)
    OR
    (channel = 'mbs' AND mbs_thread_id   IS NOT NULL AND mbs_session_uid IS NOT NULL)
  );

-- Drop the WA-only UNIQUE constraint. Postgres auto-names it; we drop
-- by the most common auto-name and tolerate absence (re-run safety).
ALTER TABLE conversations
  DROP CONSTRAINT IF EXISTS conversations_workspace_id_contact_id_wa_number_id_key;

-- Re-establish uniqueness per channel via partial unique indexes.
CREATE UNIQUE INDEX IF NOT EXISTS uq_conversations_wa
  ON conversations (workspace_id, contact_id, wa_number_id)
  WHERE channel = 'wa';

CREATE UNIQUE INDEX IF NOT EXISTS uq_conversations_mbs
  ON conversations (workspace_id, mbs_session_uid, mbs_thread_id)
  WHERE channel = 'mbs';

-- Common MBS thread lookup pattern.
CREATE INDEX IF NOT EXISTS idx_conversations_mbs_thread
  ON conversations (mbs_thread_id) WHERE channel = 'mbs';

-- Messages: add Meta MID alongside existing wa_message_id. Default
-- empty string preserves existing rows' validity.
ALTER TABLE messages
  ADD COLUMN mbs_mid TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_messages_mbs_mid
  ON messages (mbs_mid) WHERE mbs_mid != '';

COMMIT;
