-- SECURITY DEFINER write path for mpesa_paybill_credentials.
--
-- Background: ON CONFLICT DO UPDATE needs TABLE-level SELECT
-- (Postgres rule, independent of which columns the EXCLUDED clause
-- references — it's a planner-level permission check). Migration
-- 0002 revoked that to prevent bulk reads of ciphertext, which left
-- the write path broken once a credential already existed.
--
-- Migration 0009 added column-level SELECT for the RETURNING list,
-- which is enough for a plain INSERT … RETURNING but NOT for an
-- upsert. Granting table-level SELECT back would defeat 0002.
--
-- This function is the inverse of mpesa_credentials_read: it runs as
-- the table owner (SECURITY DEFINER), does the upsert internally,
-- and returns just the metadata fields the handler audits on. The
-- caller still cannot bulk-read ciphertext directly.
--
-- RLS enforcement: the function reads current_tenant_id() from the
-- caller's session GUC and asserts that the inserted/updated row's
-- tenant_id matches. The outer WithTenantTx sets that GUC before
-- calling — same as every other tenant-scoped write.

-- DROP first because RETURN type changes aren't allowed via
-- CREATE OR REPLACE.
DROP FUNCTION IF EXISTS mpesa_credentials_write(uuid, uuid, mpesa_credential_kind, text, bytea, uuid);

CREATE FUNCTION mpesa_credentials_write(
  p_tenant_id   uuid,
  p_paybill_id  uuid,
  p_kind        mpesa_credential_kind,
  p_key_id      text,
  p_ciphertext  bytea,
  p_created_by  uuid
)
-- OUT columns prefixed with out_ to avoid ambiguity with table column
-- names inside the RETURNING clause (Postgres collapses output names
-- into the inner query's namespace, so an unprefixed `paybill_id` in
-- the SELECT resolves to two candidates).
RETURNS TABLE (
  out_id         uuid,
  out_paybill_id uuid,
  out_kind       mpesa_credential_kind,
  out_key_id     text,
  out_updated_at timestamptz
)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
  v_session_tenant uuid := current_tenant_id();
BEGIN
  -- Defense in depth: even though the function runs as the table
  -- owner (RLS-bypassing), require the caller's session GUC to
  -- match the row's tenant_id. A buggy or compromised handler that
  -- forgets WithTenantTx will surface as an exception here, not a
  -- silent cross-tenant write.
  IF v_session_tenant IS NULL OR v_session_tenant <> p_tenant_id THEN
    RAISE EXCEPTION 'mpesa_credentials_write: session tenant % does not match row tenant %',
      v_session_tenant, p_tenant_id
      USING ERRCODE = '42501';
  END IF;

  RETURN QUERY
    INSERT INTO mpesa_paybill_credentials AS c
      (tenant_id, paybill_id, kind, key_id, ciphertext, created_by)
    VALUES
      (p_tenant_id, p_paybill_id, p_kind, p_key_id, p_ciphertext, p_created_by)
    ON CONFLICT (paybill_id, kind) DO UPDATE
      SET key_id     = EXCLUDED.key_id,
          ciphertext = EXCLUDED.ciphertext,
          updated_at = now(),
          created_by = COALESCE(EXCLUDED.created_by, c.created_by)
    RETURNING c.id, c.paybill_id, c.kind, c.key_id, c.updated_at;
END;
$$;

GRANT EXECUTE ON FUNCTION
  mpesa_credentials_write(uuid, uuid, mpesa_credential_kind, text, bytea, uuid)
  TO nexus_app;
