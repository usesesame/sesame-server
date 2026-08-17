-- 0001_initial: website account, sessions, and desktop-link tables.
-- The API is vault-blind: nothing here stores vault contents, keys, or secrets.

CREATE TABLE IF NOT EXISTS sesame_accounts (
	id TEXT PRIMARY KEY,
	email TEXT NOT NULL UNIQUE,
	password_hash TEXT NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS sesame_sessions (
	token_hash BYTEA PRIMARY KEY,
	account_id TEXT NOT NULL REFERENCES sesame_accounts(id) ON DELETE CASCADE,
	expires_at TIMESTAMPTZ NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS sesame_sessions_expiry_idx ON sesame_sessions(expires_at);

CREATE TABLE IF NOT EXISTS sesame_desktop_link_codes (
	code_hash BYTEA PRIMARY KEY,
	account_id TEXT NOT NULL REFERENCES sesame_accounts(id) ON DELETE CASCADE,
	expires_at TIMESTAMPTZ NOT NULL,
	used_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS sesame_desktop_connections (
	token_hash BYTEA PRIMARY KEY,
	account_id TEXT NOT NULL REFERENCES sesame_accounts(id) ON DELETE CASCADE,
	device_id TEXT NOT NULL UNIQUE,
	device_name TEXT NOT NULL,
	expires_at TIMESTAMPTZ NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS sesame_desktop_link_codes_expiry_idx ON sesame_desktop_link_codes(expires_at);
CREATE INDEX IF NOT EXISTS sesame_desktop_connections_expiry_idx ON sesame_desktop_connections(expires_at);
