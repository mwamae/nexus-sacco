-- Reverts the comment-rewrite on member_status_counts back to the
-- original canonical-source claim (now untrue, but matches the
-- pre-0022 header).
COMMENT ON FUNCTION member_status_counts(uuid) IS
  'Canonical member roll-call counts for a tenant. Single source of truth — both the admin dashboard widget and the Members page KPI strip consume this. See migration 0006 for full bucket-semantic documentation.';

DROP FUNCTION IF EXISTS counterparty_status_counts(uuid, text, text[], text);
