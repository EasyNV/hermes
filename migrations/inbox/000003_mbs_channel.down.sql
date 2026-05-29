-- Reverse Stage E3 chunk 1. Restores the prior conversations + messages
-- schema. Safe on a DB that has only WA rows; ANY existing MBS rows
-- MUST be deleted first or the SET NOT NULL step will fail.

BEGIN;

DROP INDEX IF EXISTS idx_messages_mbs_mid;
ALTER TABLE messages DROP COLUMN IF EXISTS mbs_mid;

DROP INDEX IF EXISTS idx_conversations_mbs_thread;
DROP INDEX IF EXISTS uq_conversations_mbs;
DROP INDEX IF EXISTS uq_conversations_wa;

ALTER TABLE conversations
  DROP CONSTRAINT IF EXISTS chk_channel_keys;

ALTER TABLE conversations DROP COLUMN IF EXISTS mbs_page_id;
ALTER TABLE conversations DROP COLUMN IF EXISTS mbs_thread_id;
ALTER TABLE conversations DROP COLUMN IF EXISTS mbs_session_uid;

ALTER TABLE conversations
  ALTER COLUMN wa_number_id SET NOT NULL;

ALTER TABLE conversations DROP COLUMN IF EXISTS channel;

ALTER TABLE conversations
  ADD CONSTRAINT conversations_workspace_id_contact_id_wa_number_id_key
  UNIQUE (workspace_id, contact_id, wa_number_id);

COMMIT;
