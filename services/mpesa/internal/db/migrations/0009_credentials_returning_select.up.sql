-- Grant column-level SELECT on the non-secret columns of
-- mpesa_paybill_credentials so INSERT ... RETURNING works for nexus_app.
--
-- Background: migration 0002 revoked all SELECT on this table to
-- prevent a bulk read of the ciphertext column. That's still the
-- threat model — but the revoke was too broad: INSERT … RETURNING
-- needs SELECT on every column it returns (Postgres rule), so the
-- write path stopped working too (handler returned 500 "permission
-- denied for table" the moment a rotate fired).
--
-- This grant restores SELECT on EVERY column EXCEPT `ciphertext`.
-- The threat — "bulk read of credentials" — is unchanged because
-- ciphertext is the only sensitive column; key_id, kind, timestamps
-- are metadata and already surfaced via the dashboard. A
-- `SELECT ciphertext FROM mpesa_paybill_credentials` from nexus_app
-- still errors with permission-denied (test pins this).
--
-- Going forward the documented read path is still
-- mpesa_credentials_read (SECURITY DEFINER) — this grant exists
-- only to let RETURNING land on writes.

GRANT SELECT (id, tenant_id, paybill_id, kind, key_id, created_by, created_at, updated_at)
  ON mpesa_paybill_credentials
  TO nexus_app;
