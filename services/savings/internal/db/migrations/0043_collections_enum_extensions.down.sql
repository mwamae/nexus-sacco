-- Postgres does not support removing values from an ENUM type once
-- they've been added. The down migration is therefore a no-op; the
-- new values remain available but become inert if the handler is
-- reverted to the pre-fixup behaviour.
--
-- To genuinely roll back, the ENUM type would need to be dropped +
-- recreated, which requires migrating every dependent column. Not
-- worth it for a strictly-additive change.

SELECT 1;
