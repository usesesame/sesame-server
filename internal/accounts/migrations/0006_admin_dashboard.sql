-- 0006_admin_dashboard: a separate, vault-blind staff control plane.

CREATE TABLE IF NOT EXISTS sesame_admin_accounts (
  id TEXT PRIMARY KEY,
  email TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  role TEXT NOT NULL CHECK (role IN ('super', 'support', 'ops', 'billing', 'readonly')),
  totp_secret BYTEA,
  totp_verified BOOLEAN NOT NULL DEFAULT FALSE,
  suspended BOOLEAN NOT NULL DEFAULT FALSE,
  created_by TEXT REFERENCES sesame_admin_accounts(id) ON DELETE SET NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  last_login_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS sesame_admin_accounts_role_idx ON sesame_admin_accounts(role);

CREATE TABLE IF NOT EXISTS sesame_admin_sessions (
  token_hash BYTEA PRIMARY KEY,
  admin_id TEXT NOT NULL REFERENCES sesame_admin_accounts(id) ON DELETE CASCADE,
  ip_hash TEXT NOT NULL DEFAULT '',
  user_agent TEXT NOT NULL DEFAULT '',
  expires_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS sesame_admin_sessions_expiry_idx ON sesame_admin_sessions(expires_at);

CREATE TABLE IF NOT EXISTS sesame_admin_setup_tokens (
  token_hash BYTEA PRIMARY KEY,
  admin_id TEXT NOT NULL REFERENCES sesame_admin_accounts(id) ON DELETE CASCADE,
  expires_at TIMESTAMPTZ NOT NULL,
  used_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS sesame_admin_audit_log (
  id BIGSERIAL PRIMARY KEY,
  admin_id TEXT REFERENCES sesame_admin_accounts(id) ON DELETE SET NULL,
  admin_email TEXT NOT NULL,
  action TEXT NOT NULL,
  target_type TEXT NOT NULL,
  target_id TEXT,
  detail JSONB NOT NULL DEFAULT '{}',
  ip_hash TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS sesame_admin_audit_log_admin_idx ON sesame_admin_audit_log(admin_id, created_at DESC);
CREATE INDEX IF NOT EXISTS sesame_admin_audit_log_action_idx ON sesame_admin_audit_log(action, created_at DESC);
CREATE INDEX IF NOT EXISTS sesame_admin_audit_log_target_idx ON sesame_admin_audit_log(target_type, target_id);

CREATE TABLE IF NOT EXISTS sesame_feature_flags (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  updated_by TEXT REFERENCES sesame_admin_accounts(id) ON DELETE SET NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO sesame_feature_flags (key, value) VALUES
  ('registration_mode', 'invite'),
  ('cloud_sync_available', 'false'),
  ('public_download', 'false')
ON CONFLICT (key) DO NOTHING;

CREATE TABLE IF NOT EXISTS sesame_product_plans (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  price TEXT NOT NULL,
  billing TEXT NOT NULL CHECK (billing IN ('none', 'one_time', 'monthly', 'yearly')),
  description TEXT NOT NULL,
  available BOOLEAN NOT NULL DEFAULT FALSE,
  includes JSONB NOT NULL DEFAULT '[]',
  updated_by TEXT REFERENCES sesame_admin_accounts(id) ON DELETE SET NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO sesame_product_plans (id, name, price, billing, description, available, includes) VALUES
  ('free', 'Free', '0', 'none', 'Core local security stays free.', TRUE, '["Local encrypted vault","Supported imports","Passwords and TOTP","Recovery details","Encrypted backup and export"]'),
  ('founding-pro', 'Founding Pro', '15.00', 'one_time', 'Planned early licence for desktop convenience features.', FALSE, '["Advanced cleanup","Multiple local vaults","Improved backup tools"]'),
  ('sync', 'Sesame Sync', '2.00', 'monthly', 'Planned optional encrypted sync between approved devices.', FALSE, '["End-to-end encrypted records","Device approval","Conflict review","Local access without a subscription"]')
ON CONFLICT (id) DO NOTHING;

ALTER TABLE sesame_accounts
  ADD COLUMN IF NOT EXISTS suspended_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS suspended_reason TEXT,
  ADD COLUMN IF NOT EXISTS beta_granted_by TEXT REFERENCES sesame_admin_accounts(id) ON DELETE SET NULL,
  ADD COLUMN IF NOT EXISTS beta_granted_at TIMESTAMPTZ;
