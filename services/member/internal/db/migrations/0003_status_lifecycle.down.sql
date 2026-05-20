-- Destructive: collapses dormant/blacklisted/exited/deceased rows back
-- to the closest legacy state. Only run in dev.

CREATE TYPE member_status_legacy AS ENUM ('pending', 'active', 'suspended', 'locked', 'closed', 'rejected');

ALTER TABLE members
  ALTER COLUMN status DROP DEFAULT,
  ALTER COLUMN status TYPE member_status_legacy USING (
    CASE status::text
      WHEN 'dormant' THEN 'suspended'::member_status_legacy
      WHEN 'blacklisted' THEN 'closed'::member_status_legacy
      WHEN 'exited' THEN 'closed'::member_status_legacy
      WHEN 'deceased' THEN 'closed'::member_status_legacy
      ELSE status::text::member_status_legacy
    END
  ),
  ALTER COLUMN status SET DEFAULT 'pending'::member_status_legacy;

DROP TYPE member_status;
ALTER TYPE member_status_legacy RENAME TO member_status;

DROP TABLE IF EXISTS member_status_proposals;
DROP TABLE IF EXISTS member_status_changes;
DROP TYPE  IF EXISTS member_status_reason;

ALTER TABLE members
  DROP COLUMN IF EXISTS exit_completed_at,
  DROP COLUMN IF EXISTS exit_initiated_at,
  DROP COLUMN IF EXISTS deceased_notified_at,
  DROP COLUMN IF EXISTS deceased_at,
  DROP COLUMN IF EXISTS blacklisted_at,
  DROP COLUMN IF EXISTS blacklist_authorized_by,
  DROP COLUMN IF EXISTS blacklist_reason,
  DROP COLUMN IF EXISTS dormancy_threshold_at,
  DROP COLUMN IF EXISTS dormancy_warning_sent_at,
  DROP COLUMN IF EXISTS last_activity_at,
  DROP COLUMN IF EXISTS status_changed_at;

ALTER TABLE tenant_operations DROP COLUMN IF EXISTS dormancy_threshold_days;
