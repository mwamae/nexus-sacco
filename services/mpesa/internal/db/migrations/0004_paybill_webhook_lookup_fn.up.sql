-- Webhook entry-point lookup.
--
-- Safaricom-facing webhooks hit /v1/mpesa/c2b/{paybill_id}/... without
-- a tenant subdomain in the URL. The handler therefore can't set
-- app.tenant_id before the lookup, which means RLS on
-- mpesa_paybills (USING tenant_id = current_tenant_id()) would
-- silently return zero rows for the nexus_app role.
--
-- This SECURITY DEFINER function performs the (id, webhook_token)
-- lookup with RLS bypassed (it runs as nexus, which holds BYPASSRLS
-- by virtue of being the table owner). Once the function returns
-- the tenant_id, the handler can WithTenantTx that tenant for the
-- rest of the request — RLS resumes from there.
--
-- The function returns at most one row. Token comparison is a plain
-- string equality (handled by the index `mpesa_paybills_webhook_token_key`);
-- both inputs are required, so an attacker cannot enumerate paybills
-- by id alone.

CREATE OR REPLACE FUNCTION mpesa_paybill_resolve_by_token(
  p_id    uuid,
  p_token text
)
RETURNS TABLE (
  id                    uuid,
  tenant_id             uuid,
  label                 text,
  shortcode             text,
  purpose               mpesa_paybill_purpose,
  scope                 text[],
  environment           mpesa_environment,
  status                mpesa_paybill_status,
  distribution_policy_id uuid,
  strict_validation     boolean,
  allow_msisdn_fallback boolean,
  webhook_token         text,
  created_by            uuid,
  created_at            timestamptz,
  updated_at            timestamptz
)
LANGUAGE sql
SECURITY DEFINER
SET search_path = public
AS $$
  SELECT id, tenant_id, label, shortcode, purpose, scope, environment, status,
         distribution_policy_id, strict_validation, allow_msisdn_fallback, webhook_token,
         created_by, created_at, updated_at
    FROM mpesa_paybills
   WHERE id = p_id
     AND webhook_token = p_token
     AND p_token <> ''
   LIMIT 1;
$$;

REVOKE ALL ON FUNCTION mpesa_paybill_resolve_by_token(uuid, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION mpesa_paybill_resolve_by_token(uuid, text) TO nexus_app;

COMMENT ON FUNCTION mpesa_paybill_resolve_by_token(uuid, text) IS
  'Webhook entry-point lookup. Bypasses RLS so the handler can discover the tenant_id from a paybill_id + token pair carried in the URL. Caller must WithTenantTx with the returned tenant_id for any further DB writes.';
