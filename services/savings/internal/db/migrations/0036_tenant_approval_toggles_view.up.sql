-- tenant_approval_toggles — derived-from-wf_definitions view that
-- exposes the legacy per-kind boolean columns.
--
-- Why a view: the unified-approvals migration moved the truth of
-- "is this kind gated?" from tenant_operations.approval_<kind>
-- booleans onto the workflow service's wf_definitions.active flag.
-- The booleans on tenant_operations stay as deprecated columns
-- (writers no longer flip them; readers should switch to this view
-- or query wf_definitions directly).
--
-- This view exposes the legacy column shape — one row per tenant,
-- one boolean per process_kind — so anything that joined on
-- tenant_operations for the toggles keeps working during the
-- deprecation window. Maps every domain.ApprovalKind to its
-- corresponding wf process_kind per
-- services/savings/internal/handler/approval_kind_map.go::ProcessKindForApprovalKind:
--
--   approval_deposit            ← cash_deposit
--   approval_withdrawal         ← cash_withdrawal
--   approval_deposit_transfer   ← cash_account_transfer
--   approval_share_purchase     ← share_purchase
--   approval_share_transfer     ← share_transfer
--   approval_share_bonus        ← share_bonus_issue
--   approval_share_lien         ← share_lien
--   approval_loan_disbursement  ← loan_disbursement
--   approval_loan_repayment     ← loan_repayment
--   approval_loan_settle        ← loan_settle
--   approval_loan_reverse       ← loan_reverse
--   approval_loan_writeoff      ← loan_write_off
--   approval_loan_reschedule    ← loan_reschedule
--   approval_loan_moratorium    ← loan_moratorium
--   approval_loan_settlement_discount ← loan_settlement_discount
--   approval_fee_posting        ← fee_posting
--   approval_welfare_posting    ← welfare_posting
--   approval_application_fee    ← application_fee
--   approval_member_bosa_exit   ← member_bosa_exit
--
-- approval_allow_self has no wf analogue — the wf engine handles
-- the segregation-of-duties check inside its Action handler, so
-- this column is set from tenant_operations.approval_allow_self
-- directly (it stays an authoritative tenant setting).
--
-- Note on RLS: views inherit policies from their base tables;
-- wf_definitions already has tenant-scoped RLS, so this view is
-- safe to read in any tenant tx.

CREATE OR REPLACE VIEW tenant_approval_toggles AS
WITH active_kinds AS (
  SELECT tenant_id, array_agg(process_kind) AS kinds
    FROM wf_definitions
   WHERE active = true
   GROUP BY tenant_id
)
SELECT
  t.id AS tenant_id,
  COALESCE('cash_deposit'             = ANY(ak.kinds), false) AS approval_deposit,
  COALESCE('cash_withdrawal'          = ANY(ak.kinds), false) AS approval_withdrawal,
  COALESCE('cash_account_transfer'    = ANY(ak.kinds), false) AS approval_deposit_transfer,
  COALESCE('share_purchase'           = ANY(ak.kinds), false) AS approval_share_purchase,
  COALESCE('share_transfer'           = ANY(ak.kinds), false) AS approval_share_transfer,
  COALESCE('share_bonus_issue'        = ANY(ak.kinds), false) AS approval_share_bonus,
  COALESCE('share_lien'               = ANY(ak.kinds), false) AS approval_share_lien,
  COALESCE('loan_disbursement'        = ANY(ak.kinds), false) AS approval_loan_disbursement,
  COALESCE('loan_repayment'           = ANY(ak.kinds), false) AS approval_loan_repayment,
  COALESCE('loan_settle'              = ANY(ak.kinds), false) AS approval_loan_settle,
  COALESCE('loan_reverse'             = ANY(ak.kinds), false) AS approval_loan_reverse,
  COALESCE('loan_write_off'           = ANY(ak.kinds), false) AS approval_loan_writeoff,
  COALESCE('loan_reschedule'          = ANY(ak.kinds), false) AS approval_loan_reschedule,
  COALESCE('loan_moratorium'          = ANY(ak.kinds), false) AS approval_loan_moratorium,
  COALESCE('loan_settlement_discount' = ANY(ak.kinds), false) AS approval_loan_settlement_discount,
  COALESCE('fee_posting'              = ANY(ak.kinds), false) AS approval_fee_posting,
  COALESCE('welfare_posting'          = ANY(ak.kinds), false) AS approval_welfare_posting,
  COALESCE('application_fee'          = ANY(ak.kinds), false) AS approval_application_fee,
  COALESCE('member_bosa_exit'         = ANY(ak.kinds), false) AS approval_member_bosa_exit,
  COALESCE(tops.approval_allow_self, false) AS approval_allow_self
FROM tenants t
LEFT JOIN active_kinds ak ON ak.tenant_id = t.id
LEFT JOIN tenant_operations tops ON tops.tenant_id = t.id;

COMMENT ON VIEW tenant_approval_toggles IS
  'Derived view exposing the legacy tenant_operations.approval_<kind> shape, computed from wf_definitions.active. Read-only — UPDATE goes through the workflow definitions API.';

-- Deprecate the underlying boolean columns. They still exist (legacy
-- writers will keep functioning during the deprecation window) but
-- new readers should target tenant_approval_toggles instead.
COMMENT ON COLUMN tenant_operations.approval_deposit IS
  'DEPRECATED — read from tenant_approval_toggles.approval_deposit (derived from wf_definitions). Column kept for one release for in-flight migration safety.';
COMMENT ON COLUMN tenant_operations.approval_withdrawal IS
  'DEPRECATED — see tenant_approval_toggles.';
COMMENT ON COLUMN tenant_operations.approval_deposit_transfer IS
  'DEPRECATED — see tenant_approval_toggles.';
