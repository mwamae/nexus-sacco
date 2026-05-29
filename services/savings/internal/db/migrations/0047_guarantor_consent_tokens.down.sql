DROP FUNCTION IF EXISTS find_guarantor_token_tenant(bytea);
DROP TABLE IF EXISTS guarantor_consent_tokens;
ALTER TABLE tenant_operations
  DROP COLUMN IF EXISTS guarantor_public_base_url,
  DROP COLUMN IF EXISTS guarantor_max_otp_attempts,
  DROP COLUMN IF EXISTS guarantor_reminder_hours_second,
  DROP COLUMN IF EXISTS guarantor_reminder_hours_first,
  DROP COLUMN IF EXISTS guarantor_token_expiry_days,
  DROP COLUMN IF EXISTS guarantor_sms_template,
  DROP COLUMN IF EXISTS guarantor_sms_enabled;
