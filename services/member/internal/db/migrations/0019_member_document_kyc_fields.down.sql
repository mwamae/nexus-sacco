-- Forward-only migration. Postgres can't safely drop enum values
-- without rebuilding the type + every column that references it.
-- Removing a value requires:
--   1. CREATE TYPE document_kind_new AS ENUM (...subset...);
--   2. ALTER TABLE member_documents ALTER COLUMN kind TYPE document_kind_new USING kind::text::document_kind_new;
--   3. DROP TYPE document_kind; ALTER TYPE document_kind_new RENAME TO document_kind;
-- The rebuild is intentionally not automated here — it requires a
-- ground-truth list of rows to retain and is too destructive for
-- an automatic down.
SELECT 'NO-OP — enum values are not safely droppable; see 0019 header';
