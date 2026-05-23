-- Restore the original (over-broad) NULLS NOT DISTINCT constraint.
DROP INDEX IF EXISTS receipts_channel_ref_unique;

ALTER TABLE receipts
  ADD CONSTRAINT receipts_channel_ref_unique
    UNIQUE NULLS NOT DISTINCT (tenant_id, channel, channel_ref)
    DEFERRABLE INITIALLY IMMEDIATE;
