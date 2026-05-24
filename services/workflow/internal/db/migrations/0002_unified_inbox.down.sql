-- Reverse of 0002_unified_inbox.up.sql. Enum value drops aren't
-- supported by Postgres without a type recreate; we leave 'majority',
-- 'comment', 'claim', and 'release' in place. The columns + indexes
-- come off cleanly. subject_id stays uuid — going back to text would
-- be a destructive cast we shouldn't do automatically.

DROP INDEX IF EXISTS wf_instances_claimed_idx;
DROP INDEX IF EXISTS wf_instances_sla_breach_idx;

ALTER TABLE wf_instances
  DROP COLUMN IF EXISTS summary,
  DROP COLUMN IF EXISTS source_url,
  DROP COLUMN IF EXISTS claimed_by,
  DROP COLUMN IF EXISTS claimed_at,
  DROP COLUMN IF EXISTS claim_expires,
  DROP COLUMN IF EXISTS sla_breach_at;
