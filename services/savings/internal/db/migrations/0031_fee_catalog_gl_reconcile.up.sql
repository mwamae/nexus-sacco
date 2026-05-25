-- Reconcile the fee_catalog → CoA mapping.
--
-- Spec called this 0024_fee_catalog_gl_reconcile but 0024 is already
-- taken (receipts_channel_ref_unique_nullable). Using the next free
-- slot, 0031. Same intent: re-point the two broken fee codes at the
-- right GL accounts now that accounting migration 0012 has seeded
-- 4110/4120/4130/4190/2300 across every tenant.
--
-- Two re-routes:
--   • welfare_contribution: 2510 (Institutional Loans, a long-term
--     external-borrowing liability) → 2300 (Welfare Fund Payable,
--     current liability — member trust funds). Fixes the SASRA
--     borrowings inflation.
--   • membership_registration: 4110 → 4080. Consolidates with the
--     existing Registration Fee Income account used by the
--     application_fee_payments path; having two codes for the same
--     revenue stream is what allowed the gap to ship in the first
--     place.
--
-- The other three codes (4120/4130/4190) keep their existing
-- mapping — they now resolve cleanly against the new accounting
-- 0012 seeds.
--
-- Idempotent: UPDATE … WHERE current GL still matches the old broken
-- target. A tenant that already re-pointed the row by hand is a
-- no-op.

UPDATE fee_catalog
   SET gl_credit_code = '2300', updated_at = now()
 WHERE code = 'welfare_contribution'
   AND gl_credit_code = '2510';

UPDATE fee_catalog
   SET gl_credit_code = '4080', updated_at = now()
 WHERE code = 'membership_registration'
   AND gl_credit_code = '4110';
