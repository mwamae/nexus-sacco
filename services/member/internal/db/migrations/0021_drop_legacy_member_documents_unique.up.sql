-- Drop the legacy UNIQUE INDEX on (counterparty_id, kind) — migration
-- 0020 tried to remove it via DROP CONSTRAINT IF EXISTS, but on this
-- environment the uniqueness was backed by a bare UNIQUE INDEX
-- (created by an earlier migration as CREATE UNIQUE INDEX, not
-- ADD CONSTRAINT). DROP CONSTRAINT therefore no-op'd silently and
-- the index survived alongside the new partial unique, defeating the
-- 'other' kind's intended ability to repeat.

DROP INDEX IF EXISTS member_documents_counterparty_id_kind_key;
