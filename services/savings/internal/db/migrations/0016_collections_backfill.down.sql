-- Down migration: there's no clean reversal for a backfill (we'd be
-- destroying cases that may now have contact history / PTPs attached).
-- Down is intentionally a no-op; if a rollback is genuinely required,
-- handle it via an explicit SQL session that knows which loans to skip.
SELECT 1;
