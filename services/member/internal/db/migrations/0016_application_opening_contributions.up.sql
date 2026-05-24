-- PR 5b — opening BOSA + opening share-purchase fields on the
-- membership application. The capture form (NewApplication.tsx) lets
-- the front-desk officer enter both alongside the existing
-- registration-fee block; the materialise handler reads them when
-- the application is approved and fans out to the savings service to
-- create + fund a share account and a BOSA deposit account.
--
-- Both columns are optional. Zero / NULL means "no opening
-- contribution captured on the application" — the materialise
-- handler skips the corresponding savings call entirely. A tenant
-- can leave this off forever without breakage.

ALTER TABLE membership_applications
  ADD COLUMN IF NOT EXISTS opening_share_amount numeric(18,2) NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS opening_bosa_amount  numeric(18,2) NOT NULL DEFAULT 0;
