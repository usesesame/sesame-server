CREATE TABLE instance (
	singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
	id TEXT NOT NULL,
	name TEXT NOT NULL,
	public_url TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	last_backup_at INTEGER
) STRICT;

CREATE TABLE owners (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	name_key TEXT NOT NULL,
	password_hash TEXT,
	totp_secret BLOB,
	totp_last_counter INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL,
	last_login_at INTEGER,
	removed_at INTEGER,
	CHECK ((password_hash IS NULL) = (totp_secret IS NULL))
) STRICT;

CREATE UNIQUE INDEX owners_active_name ON owners (name_key) WHERE removed_at IS NULL;

CREATE TABLE setup_tokens (
	id TEXT PRIMARY KEY,
	token_hash BLOB NOT NULL UNIQUE,
	kind TEXT NOT NULL CHECK (kind IN ('first', 'invite', 'reset')),
	owner_id TEXT REFERENCES owners (id) ON DELETE CASCADE,
	totp_secret BLOB,
	created_by TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL,
	used_at INTEGER,
	CHECK ((kind = 'first') = (owner_id IS NULL))
) STRICT;

CREATE INDEX setup_tokens_owner ON setup_tokens (owner_id);

CREATE TABLE sessions (
	id TEXT PRIMARY KEY,
	token_hash BLOB NOT NULL UNIQUE,
	owner_id TEXT NOT NULL REFERENCES owners (id) ON DELETE CASCADE,
	user_agent TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL,
	recent_auth_at INTEGER NOT NULL
) STRICT;

CREATE INDEX sessions_owner ON sessions (owner_id);
CREATE INDEX sessions_expiry ON sessions (expires_at);

CREATE TABLE members (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	name_key TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	removed_at INTEGER
) STRICT;

CREATE UNIQUE INDEX members_active_name ON members (name_key) WHERE removed_at IS NULL;

CREATE TABLE devices (
	id TEXT PRIMARY KEY,
	token_hash BLOB NOT NULL UNIQUE,
	name TEXT NOT NULL,
	owner_id TEXT REFERENCES owners (id),
	member_id TEXT REFERENCES members (id),
	app_version TEXT NOT NULL DEFAULT '',
	platform TEXT NOT NULL DEFAULT '',
	architecture TEXT NOT NULL DEFAULT '',
	update_channel TEXT NOT NULL DEFAULT '',
	protocol_version INTEGER NOT NULL DEFAULT 1,
	browser_helper_capable INTEGER NOT NULL DEFAULT 0,
	browser_helper_observed_at INTEGER,
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL,
	last_seen_at INTEGER NOT NULL,
	revoked_at INTEGER,
	CHECK ((owner_id IS NULL) <> (member_id IS NULL))
) STRICT;

CREATE INDEX devices_owner ON devices (owner_id);
CREATE INDEX devices_member ON devices (member_id);

CREATE TABLE pairings (
	id TEXT PRIMARY KEY,
	code_hash BLOB NOT NULL UNIQUE,
	owner_id TEXT REFERENCES owners (id),
	member_id TEXT REFERENCES members (id),
	device_name_hint TEXT NOT NULL DEFAULT '',
	created_by TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL,
	used_at INTEGER,
	cancelled_at INTEGER,
	device_id TEXT REFERENCES devices (id),
	CHECK ((owner_id IS NULL) <> (member_id IS NULL))
) STRICT;

CREATE INDEX pairings_owner ON pairings (owner_id);
CREATE INDEX pairings_member ON pairings (member_id);

CREATE TABLE flags (
	key TEXT PRIMARY KEY,
	enabled INTEGER NOT NULL CHECK (enabled IN (0, 1)),
	updated_at INTEGER NOT NULL
) STRICT;

CREATE TABLE audit_log (
	seq INTEGER PRIMARY KEY,
	actor TEXT NOT NULL,
	action TEXT NOT NULL,
	target TEXT NOT NULL,
	detail TEXT NOT NULL,
	at INTEGER NOT NULL,
	prev_hash BLOB NOT NULL,
	hash BLOB NOT NULL
) STRICT;

CREATE TRIGGER audit_log_no_update BEFORE UPDATE ON audit_log
BEGIN
	SELECT RAISE(ABORT, 'audit log is append-only');
END;

CREATE TRIGGER audit_log_no_delete BEFORE DELETE ON audit_log
BEGIN
	SELECT RAISE(ABORT, 'audit log is append-only');
END;

CREATE TABLE rate_limits (
	key TEXT PRIMARY KEY,
	window_started_at INTEGER NOT NULL,
	window_seconds INTEGER NOT NULL,
	attempts INTEGER NOT NULL
) STRICT;
