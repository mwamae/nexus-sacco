-- Reverse of 0003_seed_process_kinds.up.sql. Deletes the 19 seeded
-- definitions (and their wf_levels, via CASCADE). Leaves alone any
-- definitions a tenant created or edited manually for unrelated kinds.
-- Does NOT touch the three pre-existing kinds (loan_disbursement,
-- member_onboarding, member_status_change).
--
-- Will fail if any wf_instances reference these definitions — which
-- is intentional, since dropping a definition with live instances
-- would orphan their levels snapshot. Operator should cancel/complete
-- those instances first.

DELETE FROM wf_definitions
 WHERE process_kind IN (
   'cash_deposit',
   'cash_withdrawal',
   'cash_account_transfer',
   'share_purchase',
   'share_transfer',
   'share_bonus_issue',
   'loan_application_decision',
   'loan_reschedule',
   'loan_moratorium',
   'loan_write_off',
   'loan_settlement_discount',
   'member_blacklist',
   'member_close',
   'member_reactivate',
   'bulk_dormancy_run',
   'interest_run',
   'dividend_run',
   'year_end_close',
   'manual_journal_entry',
   'journal_reversal'
 );
