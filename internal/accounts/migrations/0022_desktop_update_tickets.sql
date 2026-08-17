-- Desktop updater tickets are opaque, hashed, device-bound, and deliberately
-- reusable until expiry so a range request can resume an interrupted download.
CREATE TABLE IF NOT EXISTS sesame_desktop_update_tickets (
  token_hash       BYTEA PRIMARY KEY,
  account_id       TEXT NOT NULL REFERENCES sesame_accounts(id) ON DELETE CASCADE,
  device_id        TEXT NOT NULL,
  release_id       TEXT NOT NULL REFERENCES sesame_releases(id) ON DELETE RESTRICT,
  artifact_url     TEXT NOT NULL CHECK (artifact_url LIKE 'https://%'),
  artifact_sha256  TEXT NOT NULL CHECK (artifact_sha256 ~ '^[0-9a-f]{64}$'),
  updater_signature TEXT NOT NULL CHECK (LENGTH(updater_signature) >= 64),
  expires_at       TIMESTAMPTZ NOT NULL,
  last_redeemed_at TIMESTAMPTZ,
  redemption_count INTEGER NOT NULL DEFAULT 0 CHECK (redemption_count >= 0),
  created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS sesame_desktop_update_tickets_expiry_idx
  ON sesame_desktop_update_tickets (expires_at);
