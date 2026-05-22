-- Track the org_members row that an approved institutional
-- application materialises into. Mirrors materialized_member_id
-- (which migration 0005 added for the individual path).
--
-- Why a separate column: the application can materialise into ONE
-- of the two legacy tables — they're independent UUID spaces with
-- distinct FK targets — and we want the FK to be real, not
-- polymorphic. The application_no is searchable across both columns,
-- so reporting joins stay straightforward.
--
-- Historical note: applications approved before this migration ran
-- always wrote to members regardless of kind (the latent bug being
-- fixed in this PR). Their materialized_member_id is set; for
-- institutional ones, that row is the bogus members shim with
-- gender=undisclosed + id_doc_number=registration_no. This migration
-- does NOT retroactively repair those rows — it only opens the path
-- for new approvals to land in the correct table.

ALTER TABLE membership_applications
  ADD COLUMN IF NOT EXISTS materialized_org_id uuid REFERENCES org_members(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS applications_materialized_org_idx
  ON membership_applications (materialized_org_id) WHERE materialized_org_id IS NOT NULL;

COMMENT ON COLUMN membership_applications.materialized_org_id IS
  'For approved institutional applications: the org_members row created by the activation pipeline. NULL for individual applications (use materialized_member_id instead). Exactly one of the two is set on any approved row going forward.';
