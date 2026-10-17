CREATE TABLE sync_devices_rejoined (
	vault_id TEXT NOT NULL REFERENCES sync_vaults(id) ON DELETE CASCADE,
	device_id TEXT NOT NULL,
	token_device_id TEXT NOT NULL,
	signing_key TEXT NOT NULL,
	package_key TEXT NOT NULL,
	state TEXT NOT NULL CHECK (state IN ('pending', 'approved', 'revoked')),
	added_index INTEGER NOT NULL,
	activated_epoch INTEGER NOT NULL,
	created_at INTEGER NOT NULL,
	PRIMARY KEY (vault_id, device_id)
) STRICT;

INSERT INTO sync_devices_rejoined SELECT * FROM sync_devices;
DROP TABLE sync_devices;
ALTER TABLE sync_devices_rejoined RENAME TO sync_devices;
CREATE UNIQUE INDEX sync_devices_token_key
  ON sync_devices (vault_id, token_device_id) WHERE state <> 'revoked';
