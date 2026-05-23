-- Fee catalog — the per-tenant list of ad-hoc fees a cashier can post
-- through the Collection Desk's "Fee payment" line type. Replaces the
-- free-text fee_code field that v1 accepted, so the picker shows a
-- vetted list and the GL credit goes to the right account.
--
-- Scope: standalone, non-product fees (statement, account closure,
-- membership registration, welfare contribution, replacement card, etc.).
-- Loan-product fees (loan_product_fees table) and deposit-product
-- fees (columns on deposit_products) stay where they are — those flow
-- through the loan-disbursement / deposit-maintenance paths, not the
-- desk's general-purpose fee line.
--
-- amount_default + amount_editable shape the UI: if editable=false,
-- the picker locks the amount field. ad-hoc fees default to
-- amount=0 + editable=true so the cashier types the figure.

BEGIN;

CREATE TABLE fee_catalog (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id        uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  code             text NOT NULL,                   -- stable id: 'membership_registration', etc.
  label            text NOT NULL,                   -- display: 'Membership registration fee'
  description      text,                            -- optional long-form
  amount_default   numeric(20, 2) NOT NULL DEFAULT 0 CHECK (amount_default >= 0),
  amount_editable  boolean NOT NULL DEFAULT true,
  gl_credit_code   text NOT NULL,                   -- GL revenue account code (e.g. 4100)
  is_active        boolean NOT NULL DEFAULT true,
  sort_order       int  NOT NULL DEFAULT 100,
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, code)
);

ALTER TABLE fee_catalog ENABLE ROW LEVEL SECURITY;
CREATE POLICY fee_catalog_tenant ON fee_catalog
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());

CREATE INDEX fee_catalog_active_idx ON fee_catalog(tenant_id, is_active, sort_order);

-- Seed a small starter catalog per tenant. Codes chosen to match
-- the four fee types I called out as missing in the earlier audit
-- (statement, closure, membership, welfare) + a free-form ad_hoc
-- entry so cashiers can still type a one-off amount when nothing
-- fits. GL codes default to the 41xx revenue range.
INSERT INTO fee_catalog (tenant_id, code, label, description, amount_default, amount_editable, gl_credit_code, sort_order)
SELECT t.id, x.code, x.label, x.description, x.amount_default, x.amount_editable, x.gl_credit_code, x.sort_order
  FROM tenants t
  CROSS JOIN (VALUES
    ('membership_registration', 'Membership registration fee', 'One-off fee charged at member onboarding.',                  1000.00, false, '4110', 10),
    ('statement_fee',           'Statement fee',                'Printed-statement request.',                                 100.00, false, '4120', 20),
    ('account_closure',         'Account closure fee',          'Closing a deposit / share account.',                         500.00, false, '4130', 30),
    ('welfare_contribution',    'Welfare contribution',         'Member-welfare scheme contribution.',                        0.00,   true,  '2510', 40),
    ('ad_hoc',                  'Other / ad-hoc fee',           'Free-form fee. Cashier types the amount and narration.',     0.00,   true,  '4190', 99)
  ) AS x(code, label, description, amount_default, amount_editable, gl_credit_code, sort_order)
ON CONFLICT (tenant_id, code) DO NOTHING;

COMMIT;
