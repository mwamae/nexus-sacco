DELETE FROM role_permissions WHERE permission_code IN (
  'shares:view', 'shares:buy', 'shares:transfer', 'shares:redeem',
  'shares:adjust', 'shares:bonus_issue', 'shares:lien',
  'dividends:view', 'dividends:run', 'dividends:approve'
);
DELETE FROM permissions WHERE code IN (
  'shares:view', 'shares:buy', 'shares:transfer', 'shares:redeem',
  'shares:adjust', 'shares:bonus_issue', 'shares:lien',
  'dividends:view', 'dividends:run', 'dividends:approve'
);
