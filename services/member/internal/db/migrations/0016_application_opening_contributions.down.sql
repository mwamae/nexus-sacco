ALTER TABLE membership_applications
  DROP COLUMN IF EXISTS opening_bosa_amount,
  DROP COLUMN IF EXISTS opening_share_amount;
