-- One-time backfill for rows that slipped through migration 0018's
-- backfill window because the member-onboarding approve flow used to
-- insert default share + deposit accounts BEFORE stamping
-- members.counterparty_id. The trigger from 0018 looked up
-- members.counterparty_id, got NULL, and silently inserted NULL —
-- which becomes invisible-row data corruption the moment any read
-- switches to filtering by counterparty_id.
--
-- The handler-side fix (split MaterialiseIndividualMember +
-- OpenDefaultIndividualAccounts with CP creation between them) lands
-- alongside this migration. This file just sweeps up rows already
-- written by the old order.
--
-- Idempotent: re-running does nothing because the WHERE clause
-- excludes rows whose counterparty_id is already set.

UPDATE share_accounts sa
   SET counterparty_id = m.counterparty_id
  FROM members m
 WHERE sa.member_id = m.id
   AND sa.counterparty_id IS NULL
   AND m.counterparty_id IS NOT NULL;

UPDATE deposit_accounts da
   SET counterparty_id = m.counterparty_id
  FROM members m
 WHERE da.member_id = m.id
   AND da.counterparty_id IS NULL
   AND m.counterparty_id IS NOT NULL;

UPDATE deposit_transactions dt
   SET counterparty_id = m.counterparty_id
  FROM members m
 WHERE dt.member_id = m.id
   AND dt.counterparty_id IS NULL
   AND m.counterparty_id IS NOT NULL;

UPDATE share_transactions st
   SET counterparty_id = m.counterparty_id
  FROM members m
 WHERE st.member_id = m.id
   AND st.counterparty_id IS NULL
   AND m.counterparty_id IS NOT NULL;

UPDATE loans l
   SET counterparty_id = m.counterparty_id
  FROM members m
 WHERE l.member_id = m.id
   AND l.counterparty_id IS NULL
   AND m.counterparty_id IS NOT NULL;

UPDATE loan_applications la
   SET counterparty_id = m.counterparty_id
  FROM members m
 WHERE la.member_id = m.id
   AND la.counterparty_id IS NULL
   AND m.counterparty_id IS NOT NULL;

UPDATE loan_transactions lt
   SET counterparty_id = m.counterparty_id
  FROM members m
 WHERE lt.member_id = m.id
   AND lt.counterparty_id IS NULL
   AND m.counterparty_id IS NOT NULL;

UPDATE tax_payable_ledger tpl
   SET counterparty_id = m.counterparty_id
  FROM members m
 WHERE tpl.member_id = m.id
   AND tpl.counterparty_id IS NULL
   AND m.counterparty_id IS NOT NULL;

UPDATE loan_guarantees lg
   SET guarantor_counterparty_id = m.counterparty_id
  FROM members m
 WHERE lg.guarantor_member_id = m.id
   AND lg.guarantor_counterparty_id IS NULL
   AND m.counterparty_id IS NOT NULL;
