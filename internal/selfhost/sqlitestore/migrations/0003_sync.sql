CREATE TABLE sync_vaults (
	id TEXT PRIMARY KEY,
	owner_key TEXT NOT NULL UNIQUE,
	current_revision INTEGER NOT NULL,
	head_index INTEGER NOT NULL,
	epoch INTEGER NOT NULL,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
) STRICT;

CREATE TABLE sync_devices (
	vault_id TEXT NOT NULL REFERENCES sync_vaults(id) ON DELETE CASCADE,
	device_id TEXT NOT NULL,
	token_device_id TEXT NOT NULL,
	signing_key TEXT NOT NULL,
	package_key TEXT NOT NULL,
	state TEXT NOT NULL CHECK (state IN ('pending', 'approved', 'revoked')),
	added_index INTEGER NOT NULL,
	activated_epoch INTEGER NOT NULL,
	created_at INTEGER NOT NULL,
	PRIMARY KEY (vault_id, device_id),
	UNIQUE (vault_id, token_device_id)
) STRICT;

CREATE TABLE sync_device_list (
	vault_id TEXT NOT NULL REFERENCES sync_vaults(id) ON DELETE CASCADE,
	idx INTEGER NOT NULL,
	previous_digest TEXT NOT NULL,
	kind TEXT NOT NULL,
	signer_device_id TEXT NOT NULL,
	subject_device_id TEXT NOT NULL,
	subject_signing_key TEXT NOT NULL,
	subject_package_key TEXT NOT NULL,
	sealed_label TEXT NOT NULL,
	epoch_after INTEGER NOT NULL,
	snapshot_floor INTEGER NOT NULL,
	signature TEXT NOT NULL,
	PRIMARY KEY (vault_id, idx)
) STRICT;

CREATE TABLE sync_key_packages (
	vault_id TEXT NOT NULL REFERENCES sync_vaults(id) ON DELETE CASCADE,
	recipient_device_id TEXT NOT NULL,
	sender_device_id TEXT NOT NULL,
	vault_epoch INTEGER NOT NULL,
	entry_digest TEXT NOT NULL,
	ciphertext TEXT NOT NULL,
	signature TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	PRIMARY KEY (vault_id, recipient_device_id)
) STRICT;

CREATE TABLE sync_headers (
	vault_id TEXT NOT NULL REFERENCES sync_vaults(id) ON DELETE CASCADE,
	revision INTEGER NOT NULL,
	version INTEGER NOT NULL,
	device_id TEXT NOT NULL,
	vault_epoch INTEGER NOT NULL,
	device_list_index INTEGER NOT NULL,
	device_list_digest TEXT NOT NULL,
	previous_digest TEXT NOT NULL,
	ciphertext_digest TEXT NOT NULL,
	ciphertext_length INTEGER NOT NULL,
	nonce TEXT NOT NULL,
	signature TEXT NOT NULL,
	digest TEXT NOT NULL,
	receipt TEXT NOT NULL,
	accepted_at INTEGER NOT NULL,
	PRIMARY KEY (vault_id, revision)
) STRICT;

CREATE TABLE sync_ciphertexts (
	vault_id TEXT NOT NULL REFERENCES sync_vaults(id) ON DELETE CASCADE,
	revision INTEGER NOT NULL,
	body BLOB NOT NULL,
	PRIMARY KEY (vault_id, revision)
) STRICT;

CREATE TABLE sync_pairings (
	vault_id TEXT NOT NULL REFERENCES sync_vaults(id) ON DELETE CASCADE,
	device_id TEXT NOT NULL,
	token_device_id TEXT NOT NULL,
	signing_key TEXT NOT NULL,
	package_key TEXT NOT NULL,
	offer BLOB,
	answer BLOB,
	confirm BLOB,
	state TEXT NOT NULL CHECK (state IN ('offered', 'answered', 'confirmed')),
	expires_at INTEGER NOT NULL,
	created_at INTEGER NOT NULL,
	PRIMARY KEY (vault_id, device_id),
	UNIQUE (vault_id, token_device_id)
) STRICT;

CREATE TABLE sync_nonces (
	scope TEXT NOT NULL,
	nonce TEXT NOT NULL,
	expires_at INTEGER NOT NULL,
	PRIMARY KEY (scope, nonce)
) STRICT;

CREATE INDEX sync_nonces_expires_at ON sync_nonces (expires_at);
