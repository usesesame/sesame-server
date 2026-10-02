ALTER TABLE sesame_admin_sessions
  ADD COLUMN IF NOT EXISTS authenticated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

UPDATE sesame_admin_sessions SET authenticated_at = 'epoch'::timestamptz;
