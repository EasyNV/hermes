-- Stage E3 chunk 3 follow-up: upgrade idx_messages_mbs_mid from
-- non-unique to UNIQUE so the chunk-3 inbound consumer's
-- ON CONFLICT (mbs_mid) WHERE mbs_mid != '' DO NOTHING can target
-- a real arbiter and de-dup Meta retransmits race-safely.
--
-- Background: chunk-1 (000003) created a regular partial index. The
-- plan called for a unique one; the actual SQL omitted UNIQUE. Caught
-- at chunk-3 plan-stage hostile audit. Since 000003 hasn't shipped to
-- prod (E3.1-G1 still open), the simplest fix is an additive migration
-- that creates the UNIQUE variant and drops the duplicate non-unique
-- one. Safe to re-run.

BEGIN;

-- Create the unique partial index first. If duplicates already exist in
-- a prod DB (none should — chunk-3 consumer hasn't run yet) this fails
-- loud, which is what we want.
CREATE UNIQUE INDEX IF NOT EXISTS uq_messages_mbs_mid
    ON messages (mbs_mid) WHERE mbs_mid != '';

-- Drop the redundant non-unique index. UNIQUE serves both lookup and
-- dedup roles.
DROP INDEX IF EXISTS idx_messages_mbs_mid;

COMMIT;
