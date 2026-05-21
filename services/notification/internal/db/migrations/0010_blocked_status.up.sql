-- Adds the 'blocked' status value to notification_status.
-- Lives in its own migration because Postgres forbids using a
-- newly-added enum value within the transaction that added it.
-- Workers and handlers set this when a delivery is rejected for
-- insufficient credits (see notification_deliveries.blocked_reason
-- added in 0009).

ALTER TYPE notification_status ADD VALUE IF NOT EXISTS 'blocked';
