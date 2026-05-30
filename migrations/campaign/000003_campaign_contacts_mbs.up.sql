-- chunk 9: campaign_contacts.mbs_session_uid for MBS-channel campaigns.
-- Additive column (D2=9-α) — WA campaigns keep using wa_number_id.
-- Channel discriminator on campaigns.channel decides which column is read.

ALTER TABLE campaign_contacts
  ADD COLUMN mbs_session_uid BIGINT NULL;

CREATE INDEX IF NOT EXISTS idx_campaign_contacts_mbs
  ON campaign_contacts(campaign_id, mbs_session_uid)
  WHERE mbs_session_uid IS NOT NULL;
