-- Reverse of 000004: drop the display_name column. Cleartext data is
-- lost, but the column was added in this chunk so rollback before any
-- real session is created is loss-free in practice.

ALTER TABLE mbs_sessions DROP COLUMN IF EXISTS display_name;
