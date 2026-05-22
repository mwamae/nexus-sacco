-- Revert: re-activate 5010. (The original description from migration
-- 0001 is restored.)
UPDATE chart_of_accounts
   SET is_active = true,
       description = 'Dividends declared to shareholders'
 WHERE code = '5010';
