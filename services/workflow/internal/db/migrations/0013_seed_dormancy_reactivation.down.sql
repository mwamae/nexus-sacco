BEGIN;
-- Soft-rollback: deactivate the seeded definitions rather than delete,
-- since live wf_instances may reference them.
UPDATE wf_definitions SET active = false
 WHERE process_kind IN ('deposit_account_reactivation', 'standing_order_resume');
COMMIT;
