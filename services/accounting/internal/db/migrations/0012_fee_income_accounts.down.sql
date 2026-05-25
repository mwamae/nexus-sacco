-- Roll back the five CoA additions. Refuses to drop accounts that
-- already carry journal_lines balances; tenants with posted activity
-- on these codes must move the balances manually first.

DO $$
DECLARE
  bad_count int;
BEGIN
  SELECT count(*) INTO bad_count
    FROM chart_of_accounts c
    JOIN journal_lines jl ON jl.account_id = c.id
   WHERE c.code IN ('4110','4120','4130','4190','2300');
  IF bad_count > 0 THEN
    RAISE EXCEPTION 'cannot drop fee CoA accounts: % journal_lines reference them', bad_count;
  END IF;
END $$;

DELETE FROM chart_of_accounts WHERE code IN ('4110','4120','4130','4190','2300');
