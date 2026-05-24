-- PR 1 of the BOSA / FOSA segmentation work — step 1 of 2.
--
-- PostgreSQL forbids using a newly-added enum value within the same
-- transaction that created it. The migration runner wraps each .up.sql
-- in its own tx, so the new value has to land in a standalone
-- migration before 0028 can reference it in the segment backfill.
--
-- Idempotent so re-runs don't fail.

ALTER TYPE deposit_product_type ADD VALUE IF NOT EXISTS 'member_deposit';
