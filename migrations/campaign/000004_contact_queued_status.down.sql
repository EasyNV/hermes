-- close-the-loop down: revert campaign_contacts.status to the pre-chunk set.
--
-- Any rows currently in the new 'queued' state must be moved to a value the
-- old constraint allows BEFORE re-adding it, or the ADD CONSTRAINT fails.
-- 'pending' is the safe choice: a queued contact hasn't been confirmed sent,
-- so on rollback it becomes re-dispatchable rather than falsely 'sent'.

DROP INDEX IF EXISTS idx_campaign_contacts_inflight;

UPDATE campaign_contacts SET status = 'pending' WHERE status = 'queued';

ALTER TABLE campaign_contacts
  DROP CONSTRAINT campaign_contacts_status_check;

ALTER TABLE campaign_contacts
  ADD CONSTRAINT campaign_contacts_status_check
  CHECK (status IN ('pending', 'sent', 'delivered', 'failed', 'skipped'));
