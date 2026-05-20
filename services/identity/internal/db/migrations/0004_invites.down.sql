-- Reverse 0004_invites.up.sql.
-- Existing pending users with NULL password_hash would block the NOT NULL
-- restoration; callers must clean those up first.
DROP TABLE IF EXISTS user_invites;
ALTER TABLE users ALTER COLUMN password_hash SET NOT NULL;
