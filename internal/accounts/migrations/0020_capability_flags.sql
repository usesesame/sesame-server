INSERT INTO sesame_feature_flags (key, value) VALUES
  ('desktop_linking_enabled', 'true'),
  ('downloads_enabled', 'true'),
  ('updater_enabled', 'false')
ON CONFLICT (key) DO NOTHING;
