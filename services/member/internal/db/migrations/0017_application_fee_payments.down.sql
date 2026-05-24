-- Reverts 0017. The denormalised columns on membership_applications
-- were not touched by 0017 — they keep whatever the handler last
-- recomputed into them, which is a reasonable post-rollback state.

DROP TABLE IF EXISTS application_fee_payments;
