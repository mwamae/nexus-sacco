-- Fix receipts unique-ref constraint: it was created with NULLS NOT
-- DISTINCT, so every cash receipt (channel_ref IS NULL) collides with
-- every prior cash receipt for the same tenant. The intent was to
-- dedup non-cash channels by their external reference (M-Pesa code,
-- cheque no, etc.), not to limit a tenant to one cash receipt ever.
--
-- Replace it with a partial unique index restricted to rows that
-- actually have a channel_ref. Cash rows fall outside the index and
-- can coexist without restriction.

ALTER TABLE receipts DROP CONSTRAINT IF EXISTS receipts_channel_ref_unique;

CREATE UNIQUE INDEX IF NOT EXISTS receipts_channel_ref_unique
  ON receipts (tenant_id, channel, channel_ref)
  WHERE channel_ref IS NOT NULL;
