-- 0002_webauthn: passkey (WebAuthn) credentials for website account sign-in.
-- These authenticate the website account only. They never touch the local
-- vault, its key, or its contents.

CREATE TABLE IF NOT EXISTS sesame_webauthn_credentials (
	credential_id BYTEA PRIMARY KEY,
	account_id TEXT NOT NULL REFERENCES sesame_accounts(id) ON DELETE CASCADE,
	credential JSONB NOT NULL,
	name TEXT NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS sesame_webauthn_credentials_account_idx
	ON sesame_webauthn_credentials(account_id);

-- Short-lived registration/login ceremony state, keyed by an id held in a
-- HttpOnly cookie for the duration of one ceremony.
CREATE TABLE IF NOT EXISTS sesame_webauthn_sessions (
	id BYTEA PRIMARY KEY,
	account_id TEXT REFERENCES sesame_accounts(id) ON DELETE CASCADE,
	data BYTEA NOT NULL,
	expires_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS sesame_webauthn_sessions_expiry_idx
	ON sesame_webauthn_sessions(expires_at);
