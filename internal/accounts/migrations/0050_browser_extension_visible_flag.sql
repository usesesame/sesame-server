INSERT INTO sesame_feature_flags (key, value) VALUES
  ('browser_extension_visible', 'false')
ON CONFLICT (key) DO NOTHING;
