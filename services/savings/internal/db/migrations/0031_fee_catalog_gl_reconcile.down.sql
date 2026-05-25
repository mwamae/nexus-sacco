-- Reverts the re-routes. Same idempotency guard — only flip back
-- when the current value still matches what 0031 wrote.

UPDATE fee_catalog
   SET gl_credit_code = '2510', updated_at = now()
 WHERE code = 'welfare_contribution'
   AND gl_credit_code = '2300';

UPDATE fee_catalog
   SET gl_credit_code = '4110', updated_at = now()
 WHERE code = 'membership_registration'
   AND gl_credit_code = '4080';
