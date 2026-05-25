-- ═══════════════════════════════════════════════════════════════════
-- Fee income accounts + welfare fund payable.
--
-- The fee_catalog seeded by savings migration 0023 mapped four fee
-- codes (membership_registration → 4110, statement_fee → 4120,
-- account_closure → 4130, ad_hoc → 4190) to GL accounts that the
-- CoA never defined. Cashiers collecting any of these fees hit
-- ErrUnknownAccount on posting and the receipt rolled back.
--
-- A fifth code (welfare_contribution → 2510) was even worse:
-- 2510 is "Institutional Loans", a long-term external-borrowing
-- liability, so posting welfare there silently inflated the
-- SASRA borrowings ratio. Welfare contributions are member funds
-- the SACCO holds in trust for the welfare scheme — a current
-- liability, not income, not borrowings.
--
-- This migration backfills the missing rows. Savings's companion
-- migration 0031 re-points the fee_catalog at the right targets:
--   • membership_registration → 4080 (consolidates with the
--     existing Registration Fee Income, used by the application
--     fee path).
--   • welfare_contribution    → 2300 (the new Welfare Fund Payable
--     introduced below).
--   • the other three codes keep 4110/4120/4130/4190, which now
--     exist in every tenant's CoA.
-- ═══════════════════════════════════════════════════════════════════

INSERT INTO chart_of_accounts (
  tenant_id, code, name, class, type, normal_balance, is_system_locked, description
)
SELECT t.id, c.code, c.name,
       c.cls::account_class, c.typ, c.nb::account_normal_balance,
       true, c.descr
  FROM tenants t CROSS JOIN (VALUES
    ('4110', 'Membership Registration Fee Income',
       'income',    'fee_income',        'credit',
       'New-member registration fee. Catalog code: membership_registration. Note: PR-fee-late and the application_fee_payments path use 4080 (Registration Fee Income); the new fee_catalog reconcile rewires this code to 4080 as well so the two streams roll up.'),
    ('4120', 'Statement Fee Income',
       'income',    'fee_income',        'credit',
       'Printed-statement requests. Catalog code: statement_fee.'),
    ('4130', 'Account Closure Fee Income',
       'income',    'fee_income',        'credit',
       'Closing a deposit / share account. Catalog code: account_closure.'),
    ('4190', 'Other Ad-Hoc Fee Income',
       'income',    'fee_income',        'credit',
       'Catch-all for cashier-entered ad-hoc fees that do not fit the standard catalog. Catalog code: ad_hoc.'),
    ('2300', 'Welfare Fund Payable',
       'liability', 'current_liability', 'credit',
       'Member welfare-scheme contributions held in trust until disbursed back to members. Not SACCO income. Catalog code: welfare_contribution.')
  ) AS c(code, name, cls, typ, nb, descr)
 ON CONFLICT (tenant_id, code) DO NOTHING;
