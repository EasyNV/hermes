-- Stage F follow-up chunk 8 — multi-channel campaigns
--
-- Two coupled changes:
--
--  1. campaigns gains `channel TEXT` to discriminate WA vs MBS dispatch.
--     Existing rows backfill to 'wa'. CHECK ensures only valid values.
--
--  2. campaign_numbers becomes campaign_senders with a discriminated
--     (sender_kind, sender_id) primary-key suffix. The existing
--     `wa_number_id UUID` rows become `sender_kind='wa'` +
--     `sender_id=wa_number_id::text`. Cardinality: this table holds
--     campaigns × senders rows — low volume, safe to migrate in place.
--     (campaign_contacts stays additive — see chunk 9 migration —
--     because that table can have millions of rows.)
--
-- Down migration reverses all four operations.

-- ── campaigns.channel ──────────────────────────────────────────────
ALTER TABLE campaigns
    ADD COLUMN channel TEXT NOT NULL DEFAULT 'wa'
    CHECK (channel IN ('wa', 'mbs'));

COMMENT ON COLUMN campaigns.channel IS
    'Dispatch channel: ''wa'' routes via hermes-wa (whatsmeow); ''mbs'' routes via hermes-mbs (Meta Business Suite). Per-row immutable after CreateCampaign — campaign-engine branches on this.';

-- ── campaign_numbers → campaign_senders (discriminated column) ─────
ALTER TABLE campaign_numbers
    ADD COLUMN sender_kind TEXT NOT NULL DEFAULT 'wa'
    CHECK (sender_kind IN ('wa', 'mbs'));

ALTER TABLE campaign_numbers
    ADD COLUMN sender_id TEXT;

-- Backfill: existing rows are all WA, sender_id = wa_number_id as text.
UPDATE campaign_numbers
   SET sender_id = wa_number_id::TEXT
 WHERE sender_id IS NULL;

ALTER TABLE campaign_numbers
    ALTER COLUMN sender_id SET NOT NULL;

-- Replace the PK (campaign_id, wa_number_id) with one keyed off the
-- discriminated columns. wa_number_id stays around until the next
-- statement drops it — we need both for the swap window so concurrent
-- engine reads don't see a half-keyed row. (No engine reads here
-- because this is a fresh schema migration on an empty/dev DB; the
-- guard is for future production migrations.)
ALTER TABLE campaign_numbers
    DROP CONSTRAINT campaign_numbers_pkey;

ALTER TABLE campaign_numbers
    DROP COLUMN wa_number_id;

ALTER TABLE campaign_numbers
    ADD PRIMARY KEY (campaign_id, sender_kind, sender_id);

ALTER TABLE campaign_numbers
    RENAME TO campaign_senders;

-- Rotation queries hit (campaign_id, sender_kind, status). One partial
-- index covers the hot path (active only).
CREATE INDEX idx_campaign_senders_active
    ON campaign_senders (campaign_id, sender_kind)
    WHERE status = 'active';

COMMENT ON TABLE campaign_senders IS
    'Senders (numbers or MBS sessions) assigned to a campaign. sender_kind=''wa''+sender_id=wa_numbers.id::text for WhatsApp; sender_kind=''mbs''+sender_id=mbs_sessions.uid::text for Meta Business Suite. Renamed from campaign_numbers in 000002.';
COMMENT ON COLUMN campaign_senders.sender_kind IS
    'Channel discriminator. MUST match campaigns.channel for every row.';
COMMENT ON COLUMN campaign_senders.sender_id IS
    'Sender ID as TEXT (decouples from wa_numbers.id UUID vs mbs_sessions.uid BIGINT). Engine + handler cast on read.';
