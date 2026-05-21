-- Postgres has no first-class "remove enum value" command. Down migration
-- is a no-op; tearing 0009 down already drops the column that references
-- the blocked rows.
SELECT 1;
