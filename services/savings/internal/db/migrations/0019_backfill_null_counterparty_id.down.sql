-- One-time data backfill — there is no meaningful inverse. Rolling
-- back would mean re-NULLing rows that the application is now
-- depending on. Leave as a no-op.

SELECT 1;
