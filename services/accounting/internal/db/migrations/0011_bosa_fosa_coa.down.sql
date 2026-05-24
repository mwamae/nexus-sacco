-- Roll back the BOSA / FOSA CoA additions. Refuses to drop accounts
-- that already carry journal_lines balances — that would orphan the
-- ledger. Tenants with balances must manually transfer them first.

DO $$
DECLARE
  bad_count int;
BEGIN
  SELECT count(*) INTO bad_count
    FROM chart_of_accounts c
    JOIN journal_lines jl ON jl.account_id = c.id
   WHERE c.code IN ('2050', '2051');
  IF bad_count > 0 THEN
    RAISE EXCEPTION 'cannot drop 2050/2051: % journal_lines reference them', bad_count;
  END IF;
END $$;

DELETE FROM chart_of_accounts WHERE code IN ('2050', '2051');

UPDATE chart_of_accounts
   SET type = 'member_deposits'
 WHERE code = '2100'
   AND type = 'member_savings';
