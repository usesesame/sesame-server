-- 0040_support_operations: retention, automatic closure, and saved replies.
-- A waiting request that goes quiet is closed by the system, not an admin, so
-- the requester can be told it closed automatically. Saved replies are
-- staff-authored canned text that still passes the reply secret-shape guard.

ALTER TABLE sesame_support_requests
  ADD COLUMN IF NOT EXISTS closed_by_system BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE IF NOT EXISTS sesame_support_saved_replies (
  id                  TEXT PRIMARY KEY,
  title               TEXT NOT NULL CHECK (LENGTH(title) > 0 AND LENGTH(title) <= 120),
  body                TEXT NOT NULL CHECK (LENGTH(body) > 0 AND LENGTH(body) <= 8000),
  created_by_admin_id TEXT REFERENCES sesame_admin_accounts(id) ON DELETE SET NULL,
  created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS sesame_support_saved_replies_updated_idx
  ON sesame_support_saved_replies(updated_at DESC);
