-- Revert to the original 5-state CHECK. Any rows in the 'migrated'
-- state at downgrade time would violate; the script below short-
-- circuits to declined so the constraint reload succeeds.

UPDATE pending_approvals SET status = 'declined' WHERE status = 'migrated';

ALTER TABLE pending_approvals
  DROP CONSTRAINT IF EXISTS pending_approvals_status_check;

ALTER TABLE pending_approvals
  ADD CONSTRAINT pending_approvals_status_check
  CHECK (status IN ('pending','approved','declined','cancelled','execution_error'));
