-- close-the-loop chunk: add 'queued' to the campaign_contacts lifecycle.
--
-- New lifecycle: pending -> queued (task published to NATS) -> sent|failed
-- (terminal write-back from the MbsOutboundEvent result consumer).
--
-- Before this, dispatch wrote status='sent' the instant the task was queued,
-- with no feedback from the consumer — so a banned sender / dead recipient
-- still reported "sent". 'queued' is the in-flight state the result consumer
-- transitions out of, idempotently, guarded on status='queued'.

ALTER TABLE campaign_contacts
  DROP CONSTRAINT campaign_contacts_status_check;

ALTER TABLE campaign_contacts
  ADD CONSTRAINT campaign_contacts_status_check
  CHECK (status IN ('pending', 'queued', 'sent', 'delivered', 'failed', 'skipped'));

-- Partial index for the completion check + the stuck-queued reaper. Both query
-- "any pending-or-queued rows for this campaign?" hot. Tables are small here so
-- a plain (non-CONCURRENTLY) index is sub-ms and safe inside the migration txn.
CREATE INDEX IF NOT EXISTS idx_campaign_contacts_inflight
  ON campaign_contacts(campaign_id, status)
  WHERE status IN ('pending', 'queued');
