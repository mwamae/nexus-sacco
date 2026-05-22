-- Default deposit product for the activation pipeline to open as the
-- new member's savings account. Nullable — if unset, the activation
-- skips the auto-open step and logs a hint for the operator.
ALTER TABLE tenant_membership
  ADD COLUMN IF NOT EXISTS default_deposit_product_id uuid;
