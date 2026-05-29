-- Phase 5 follow-up — drop the stale BEFORE INSERT trigger on
-- loan_guarantees that bridged the old guarantor_member_id column
-- to the new guarantor_counterparty_id.
--
-- The column guarantor_member_id was dropped in the Phase D
-- counterparty refactor (loan_guarantees now has only
-- guarantor_counterparty_id). The trigger function was missed and
-- continued to reference NEW.guarantor_member_id, which Postgres
-- evaluates at runtime and rejects with:
--   ERROR: record "new" has no field "guarantor_member_id" (SQLSTATE 42703)
--
-- Every INSERT into loan_guarantees has been blocked since — including
-- the loan-application creation path that adds guarantor rows.
--
-- The bridge is no longer needed: LoanGuaranteeStore.CreateTx writes
-- guarantor_counterparty_id directly, and the handler validates it as
-- a real counterparty id before calling.

DROP TRIGGER IF EXISTS trg_loan_guarantees_populate_guarantor_cp
  ON loan_guarantees;

DROP FUNCTION IF EXISTS populate_guarantor_counterparty_id();
