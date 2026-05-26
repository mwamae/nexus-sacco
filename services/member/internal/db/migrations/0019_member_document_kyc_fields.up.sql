-- document_kind enum expansion for the KYC workstation.
--
-- FORWARD-ONLY: postgres can't safely drop enum values without
-- rebuilding the type + every column that references it. If you
-- need to remove a value, write a new migration that creates a new
-- type, migrates the column, and drops the old type. The .down.sql
-- companion to this migration intentionally doesn't try.
--
-- Adds the canonical KYC artefact types the SACCO ops team has been
-- collecting outside the system today (KRA PINs, proof of address,
-- payslips, business permits, etc) plus a catch-all 'other' that
-- breaks the singleton-per-kind constraint added in migration 0020.
--
-- Ordering note: this migration MUST commit before 0020 because the
-- partial unique index in 0020 references the 'other' enum value.

ALTER TYPE document_kind ADD VALUE IF NOT EXISTS 'kra_pin_certificate';
ALTER TYPE document_kind ADD VALUE IF NOT EXISTS 'proof_of_address';
ALTER TYPE document_kind ADD VALUE IF NOT EXISTS 'bank_statement';
ALTER TYPE document_kind ADD VALUE IF NOT EXISTS 'payslip';
ALTER TYPE document_kind ADD VALUE IF NOT EXISTS 'employment_letter';
ALTER TYPE document_kind ADD VALUE IF NOT EXISTS 'business_permit';
ALTER TYPE document_kind ADD VALUE IF NOT EXISTS 'signed_application_form';
ALTER TYPE document_kind ADD VALUE IF NOT EXISTS 'next_of_kin_id';
ALTER TYPE document_kind ADD VALUE IF NOT EXISTS 'other';
