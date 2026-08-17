-- 0004_account_purpose: give website accounts a narrow, explicit job.
--
-- The API remains vault-blind. These tables contain account access, browser
-- sessions, release entitlements, short-lived account-action tokens, and
-- privacy-filtered support text. They must never contain vault material.

ALTER TABLE sesame_accounts
  ADD COLUMN IF NOT EXISTS email_verified_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS beta_access BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE sesame_sessions
  ADD COLUMN IF NOT EXISTS id TEXT,
  ADD COLUMN IF NOT EXISTS label TEXT NOT NULL DEFAULT 'Browser',
  ADD COLUMN IF NOT EXISTS authenticated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  ADD COLUMN IF NOT EXISTS last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

UPDATE sesame_sessions
SET id = SUBSTRING(ENCODE(token_hash, 'hex') FROM 1 FOR 32)
WHERE id IS NULL;

ALTER TABLE sesame_sessions ALTER COLUMN id SET NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS sesame_sessions_id_idx ON sesame_sessions(id);
CREATE INDEX IF NOT EXISTS sesame_sessions_account_idx ON sesame_sessions(account_id, created_at DESC);

CREATE TABLE IF NOT EXISTS sesame_beta_eligibility (
  email TEXT PRIMARY KEY,
  status TEXT NOT NULL DEFAULT 'eligible' CHECK (status IN ('eligible', 'registered', 'revoked')),
  note TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  registered_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS sesame_beta_invites (
  code_hash BYTEA PRIMARY KEY,
  email TEXT,
  max_uses INTEGER NOT NULL DEFAULT 1 CHECK (max_uses > 0),
  uses INTEGER NOT NULL DEFAULT 0 CHECK (uses >= 0),
  expires_at TIMESTAMPTZ NOT NULL,
  revoked_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS sesame_beta_invites_expiry_idx ON sesame_beta_invites(expires_at);

CREATE TABLE IF NOT EXISTS sesame_account_tokens (
  token_hash BYTEA PRIMARY KEY,
  account_id TEXT NOT NULL REFERENCES sesame_accounts(id) ON DELETE CASCADE,
  purpose TEXT NOT NULL CHECK (purpose IN ('verify_email', 'recover_password', 'change_email')),
  payload TEXT NOT NULL DEFAULT '',
  expires_at TIMESTAMPTZ NOT NULL,
  used_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS sesame_account_tokens_owner_idx ON sesame_account_tokens(account_id, purpose);
CREATE INDEX IF NOT EXISTS sesame_account_tokens_expiry_idx ON sesame_account_tokens(expires_at);

CREATE TABLE IF NOT EXISTS sesame_licences (
  id TEXT PRIMARY KEY,
  account_id TEXT NOT NULL REFERENCES sesame_accounts(id) ON DELETE CASCADE,
  product TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('active', 'expired', 'revoked')),
  issued_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  expires_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS sesame_licences_account_idx ON sesame_licences(account_id, issued_at DESC);

CREATE TABLE IF NOT EXISTS sesame_releases (
  id TEXT PRIMARY KEY,
  channel TEXT NOT NULL,
  platform TEXT NOT NULL,
  version TEXT NOT NULL,
  download_url TEXT NOT NULL CHECK (download_url LIKE 'https://%'),
  sha256 TEXT NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
  signature TEXT NOT NULL CHECK (LENGTH(signature) >= 64),
  signing_key_id TEXT NOT NULL CHECK (LENGTH(signing_key_id) > 0),
  supported_windows TEXT NOT NULL CHECK (LENGTH(supported_windows) > 0),
  release_notes_url TEXT NOT NULL CHECK (release_notes_url LIKE 'https://%'),
  rollback_notice TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL CHECK (status IN ('draft', 'published', 'withdrawn')),
  published_at TIMESTAMPTZ,
  CHECK (status <> 'published' OR published_at IS NOT NULL),
  UNIQUE (channel, platform, version)
);

CREATE TABLE IF NOT EXISTS sesame_support_requests (
  id TEXT PRIMARY KEY,
  account_id TEXT REFERENCES sesame_accounts(id) ON DELETE SET NULL,
  email TEXT NOT NULL,
  subject TEXT NOT NULL,
  message TEXT NOT NULL,
  app_version TEXT NOT NULL DEFAULT '',
  diagnostic_code TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'received' CHECK (status IN ('received', 'closed')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE sesame_desktop_link_codes
  ADD COLUMN IF NOT EXISTS id TEXT,
  ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  ADD COLUMN IF NOT EXISTS cancelled_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS connected_device_id TEXT;

UPDATE sesame_desktop_link_codes
SET id = SUBSTRING(ENCODE(code_hash, 'hex') FROM 1 FOR 32)
WHERE id IS NULL;

ALTER TABLE sesame_desktop_link_codes ALTER COLUMN id SET NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS sesame_desktop_link_codes_id_idx ON sesame_desktop_link_codes(id);
CREATE INDEX IF NOT EXISTS sesame_desktop_link_codes_account_idx ON sesame_desktop_link_codes(account_id, created_at DESC);
