-- chunk 9 down: drop the additive mbs_session_uid column + its partial index.
-- WA campaigns are unaffected (wa_number_id column survives).

DROP INDEX IF EXISTS idx_campaign_contacts_mbs;

ALTER TABLE campaign_contacts
  DROP COLUMN IF EXISTS mbs_session_uid;
