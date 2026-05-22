-- Remove the shares:redeem permission — share capital is equity and
-- cannot be redeemed in this SACCO. role_permissions rows referencing
-- it cascade off via ON DELETE CASCADE.
DELETE FROM permissions WHERE code = 'shares:redeem';
