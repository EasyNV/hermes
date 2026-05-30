-- Down migration: reverse 000002_multi_channel.up.sql in opposite order.
-- WARNING: any MBS-channel campaigns + their senders will fail the
-- restored constraints. Operator must DELETE FROM campaigns WHERE
-- channel='mbs' first if the down path is taken on a dirty DB.

DROP INDEX IF EXISTS idx_campaign_senders_active;

ALTER TABLE campaign_senders
    RENAME TO campaign_numbers;

ALTER TABLE campaign_numbers
    ADD COLUMN wa_number_id UUID;

-- Backfill: surviving rows are WA (or operator pruned MBS rows above).
UPDATE campaign_numbers
   SET wa_number_id = sender_id::UUID
 WHERE sender_kind = 'wa';

DELETE FROM campaign_numbers WHERE sender_kind <> 'wa';

ALTER TABLE campaign_numbers
    ALTER COLUMN wa_number_id SET NOT NULL;

ALTER TABLE campaign_numbers
    DROP CONSTRAINT campaign_numbers_pkey;

ALTER TABLE campaign_numbers
    ADD PRIMARY KEY (campaign_id, wa_number_id);

ALTER TABLE campaign_numbers
    DROP COLUMN sender_id,
    DROP COLUMN sender_kind;

ALTER TABLE campaigns
    DROP COLUMN channel;
