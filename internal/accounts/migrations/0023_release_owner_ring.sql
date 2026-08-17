CREATE TABLE IF NOT EXISTS sesame_release_ring_members (
  account_id TEXT PRIMARY KEY REFERENCES sesame_accounts(id) ON DELETE CASCADE,
  channel TEXT NOT NULL CHECK (channel IN ('owner')),
  granted_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
