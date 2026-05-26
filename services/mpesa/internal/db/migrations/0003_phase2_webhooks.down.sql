-- Phase-2 rollback. The workflow seed leaves rows in another service's
-- tables, so down-migrating tears those out as well. Skips removal when
-- a tenant has live wf_instances pointing at the definition (foreign
-- key would block) — operator must hand-resolve those first.

DELETE FROM wf_levels      WHERE definition_id IN
  (SELECT id FROM wf_definitions WHERE process_kind = 'mpesa_unallocated_reconciliation');
DELETE FROM wf_definitions WHERE process_kind = 'mpesa_unallocated_reconciliation';

DROP INDEX IF EXISTS mpesa_inbound_events_status_idx;
DROP INDEX IF EXISTS mpesa_inbound_events_resolved_member_idx;
DROP INDEX IF EXISTS mpesa_inbound_events_paybill_time_idx;

ALTER TABLE mpesa_inbound_events
  DROP COLUMN IF EXISTS workflow_instance_id,
  DROP COLUMN IF EXISTS resolved_via,
  DROP COLUMN IF EXISTS resolved_member_id,
  DROP COLUMN IF EXISTS status;

ALTER TABLE mpesa_paybills DROP CONSTRAINT IF EXISTS mpesa_paybills_webhook_token_key;
ALTER TABLE mpesa_paybills
  DROP COLUMN IF EXISTS webhook_token,
  DROP COLUMN IF EXISTS allow_msisdn_fallback,
  DROP COLUMN IF EXISTS strict_validation;

DROP TYPE IF EXISTS mpesa_resolved_via;
DROP TYPE IF EXISTS mpesa_inbound_status;
