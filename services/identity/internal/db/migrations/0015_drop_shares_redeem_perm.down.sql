INSERT INTO permissions (code, description, category)
  VALUES ('shares:redeem', 'Redeem shares (member exit / buyback)', 'shares')
  ON CONFLICT (code) DO NOTHING;
