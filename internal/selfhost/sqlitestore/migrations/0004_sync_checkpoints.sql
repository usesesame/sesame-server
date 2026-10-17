CREATE TABLE sync_checkpoints (
	vault_id TEXT NOT NULL REFERENCES sync_vaults(id) ON DELETE CASCADE,
	device_id TEXT NOT NULL,
	version INTEGER NOT NULL,
	device_list_index INTEGER NOT NULL CHECK (device_list_index >= 1),
	device_list_digest TEXT NOT NULL,
	header_revision INTEGER NOT NULL CHECK (header_revision >= 0),
	header_digest TEXT NOT NULL,
	observed_at INTEGER NOT NULL CHECK (observed_at >= 1),
	signature TEXT NOT NULL,
	accepted_at INTEGER NOT NULL,
	PRIMARY KEY (vault_id, device_id)
) STRICT;
