-- 000005: reverse index for inbound thread-resolution.
-- The send path writes mbs_phone_threads keyed on (uid, page_id, phone).
-- Inbound resolution needs the reverse direction: (uid, thread_id) -> phone,
-- so the inbox can enrich a customer's inbound message with the real phone
-- and unify it with the outbound conversation (both keyed on thread_id =
-- customer_id). Without this index the lookup is a seq scan per inbound.
CREATE INDEX IF NOT EXISTS idx_mbs_phone_threads_uid_thread
    ON mbs_phone_threads (uid, thread_id);
