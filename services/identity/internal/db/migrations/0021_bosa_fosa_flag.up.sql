-- Per-tenant feature flag for the BOSA / FOSA segmentation rollout.
-- When false (the default), the deposit-product UI keeps the legacy
-- single-list view and the SASRA return / scoring pipelines behave
-- as today. When true, segment chips, filter pills, the BOSA-only
-- form fields, and the segment-aware loan-multiplier path light up.
--
-- Mirrors the unified_inbox_enabled rollout in 0020.

ALTER TABLE tenants
  ADD COLUMN IF NOT EXISTS bosa_fosa_enabled boolean NOT NULL DEFAULT false;
