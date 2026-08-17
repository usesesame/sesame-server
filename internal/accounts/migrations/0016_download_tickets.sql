-- 0016_download_tickets: short-lived, account-bound download capabilities.
--
-- Only hashes of tickets and idempotency keys are retained. The destination
-- is captured with the ticket so a later release edit cannot change what an
-- already-authorised download retrieves.

CREATE TABLE IF NOT EXISTS sesame_download_tickets (
  token_hash BYTEA PRIMARY KEY,
  account_id TEXT NOT NULL REFERENCES sesame_accounts(id) ON DELETE CASCADE,
  release_id TEXT NOT NULL REFERENCES sesame_releases(id) ON DELETE RESTRICT,
  platform TEXT NOT NULL,
  artifact_url TEXT NOT NULL CHECK (artifact_url LIKE 'https://%'),
  artifact_sha256 TEXT NOT NULL CHECK (artifact_sha256 ~ '^[0-9a-f]{64}$'),
  idempotency_key_hash BYTEA NOT NULL,
  request_hash BYTEA NOT NULL,
  expires_at TIMESTAMPTZ NOT NULL,
  redeemed_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (account_id, idempotency_key_hash)
);

CREATE INDEX IF NOT EXISTS sesame_download_tickets_expiry_idx ON sesame_download_tickets(expires_at);
CREATE INDEX IF NOT EXISTS sesame_download_tickets_account_idx ON sesame_download_tickets(account_id, created_at DESC);
