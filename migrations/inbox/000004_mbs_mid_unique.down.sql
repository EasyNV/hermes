-- Reverse of 000004_mbs_mid_unique.up.sql.
-- Restores the chunk-1 (non-unique) partial index shape.

BEGIN;

CREATE INDEX IF NOT EXISTS idx_messages_mbs_mid
    ON messages (mbs_mid) WHERE mbs_mid != '';

DROP INDEX IF EXISTS uq_messages_mbs_mid;

COMMIT;
