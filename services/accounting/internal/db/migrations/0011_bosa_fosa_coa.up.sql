-- PR 3 of the BOSA / FOSA segmentation work.
--
-- Two CoA changes:
--
--   1. Reclassify 2100 "Fixed Deposits" from `member_deposits` to
--      `member_savings`. Term deposits are still withdrawable on
--      maturity, so prudentially they belong in the FOSA bucket the
--      SASRA return treats as liquidity-relevant. The 'member_deposits'
--      subtype is reserved for the new BOSA bond going forward.
--
--   2. Insert two new BOSA accounts per tenant:
--      • 2050 — Member Deposits (BOSA)
--      • 2051 — BOSA Interest Payable
--      Codes 2052–2059 are reserved for tenants that want sub-classed
--      BOSA products; left empty here.
--
-- Idempotent: UPDATE is a no-op once the subtype already matches, and
-- the INSERT uses `WHERE NOT EXISTS` per (tenant, code).

UPDATE chart_of_accounts
   SET type = 'member_savings'
 WHERE code = '2100'
   AND type = 'member_deposits';

INSERT INTO chart_of_accounts (
  tenant_id, code, name, class, type, normal_balance, is_system_locked, description
)
SELECT t.id, c.code, c.name, c.class::account_class, c.typ, c.nb::account_normal_balance, true, c.descr
  FROM tenants t
  CROSS JOIN (VALUES
    ('2050', 'Member Deposits (BOSA)',
       'liability', 'member_deposits', 'credit',
       'Non-withdrawable member deposit bond — secures loans, redeemable on exit'),
    ('2051', 'BOSA Interest Payable',
       'liability', 'current_liability', 'credit',
       'Accrued BOSA dividend / interest awaiting AGM declaration')
  ) AS c(code, name, class, typ, nb, descr)
 WHERE NOT EXISTS (
   SELECT 1 FROM chart_of_accounts existing
    WHERE existing.tenant_id = t.id
      AND existing.code = c.code
 );
