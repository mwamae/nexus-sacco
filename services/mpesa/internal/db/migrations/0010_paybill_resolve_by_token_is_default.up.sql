-- Recreate mpesa_paybill_resolve_by_token to include the is_default
-- column. The Go side's scanPaybill helper started reading is_default
-- when the list endpoint was added; the SECURITY DEFINER function used
-- by the webhook auth path (ByIDAndToken) still returned the original
-- 15 columns, so SELECT * FROM the function blew up scanPaybill with
-- "expected 16 fields, got 15" — every webhook 401'd.
--
-- Postgres won't let us change a function's RETURNS TABLE signature in
-- place; we DROP + CREATE. The function lives behind nexus_app via the
-- existing GRANT EXECUTE — we re-grant after recreate.

DROP FUNCTION IF EXISTS mpesa_paybill_resolve_by_token(uuid, text);

CREATE OR REPLACE FUNCTION mpesa_paybill_resolve_by_token(p_id uuid, p_token text)
RETURNS TABLE (
  id uuid, tenant_id uuid, label text, shortcode text,
  purpose mpesa_paybill_purpose, scope text[], environment mpesa_environment,
  status mpesa_paybill_status, distribution_policy_id uuid,
  strict_validation boolean, allow_msisdn_fallback boolean,
  webhook_token text, is_default boolean,
  created_by uuid, created_at timestamptz, updated_at timestamptz
)
LANGUAGE sql
SECURITY DEFINER
SET search_path = public
AS $$
  SELECT id, tenant_id, label, shortcode, purpose, scope, environment, status,
         distribution_policy_id, strict_validation, allow_msisdn_fallback,
         webhook_token, is_default,
         created_by, created_at, updated_at
    FROM mpesa_paybills
   WHERE id = p_id
     AND webhook_token = p_token
     AND p_token <> ''
   LIMIT 1;
$$;

GRANT EXECUTE ON FUNCTION mpesa_paybill_resolve_by_token(uuid, text) TO nexus_app;
