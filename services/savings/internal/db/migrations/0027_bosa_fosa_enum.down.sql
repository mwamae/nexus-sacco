-- Postgres does not support removing values from an enum without
-- recreating the type. Reverting this migration is a destructive
-- operation that the 0028 down handles holistically (drop the
-- segment column first, then DROP TYPE … CASCADE), so this file is
-- intentionally a no-op. Use `0028.down` to fully roll back.
SELECT 1;
