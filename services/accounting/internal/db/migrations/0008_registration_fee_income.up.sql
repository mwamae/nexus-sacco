-- ═══════════════════════════════════════════════════════════════════
-- Registration Fee Income (Phase 12/A).
--
-- Membership registration fees are income to the SACCO, recorded
-- under the income class. The accounting auto-poster will credit
-- this code when the onboarding pipeline activates a new member
-- whose application included a paid registration fee. Refunds on a
-- declined application reverse the same code.
--
-- Backfills the code into every existing tenant's chart of accounts;
-- new tenants pick it up by re-running the per-tenant seeding logic.
-- ═══════════════════════════════════════════════════════════════════

INSERT INTO chart_of_accounts
  (tenant_id, code, name, class, type, normal_balance, is_system_locked, description)
SELECT t.id, '4080', 'Registration Fee Income',
       'income'::account_class, 'fee_income', 'credit'::account_normal_balance,
       true,
       'Membership registration fees collected from new applicants. Reverses on refund of a declined application.'
  FROM tenants t
 ON CONFLICT (tenant_id, code) DO NOTHING;
