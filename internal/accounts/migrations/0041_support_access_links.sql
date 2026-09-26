CREATE TABLE IF NOT EXISTS sesame_support_access_links (
  token_hash      BYTEA PRIMARY KEY,
  ticket_id       TEXT NOT NULL REFERENCES sesame_support_requests(id) ON DELETE CASCADE,
  requester_email TEXT NOT NULL,
  expires_at      TIMESTAMPTZ NOT NULL,
  revoked_at      TIMESTAMPTZ,
  last_used_at    TIMESTAMPTZ,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS sesame_support_access_links_ticket_idx
  ON sesame_support_access_links(ticket_id) WHERE revoked_at IS NULL;
CREATE INDEX IF NOT EXISTS sesame_support_access_links_expiry_idx
  ON sesame_support_access_links(expires_at);
