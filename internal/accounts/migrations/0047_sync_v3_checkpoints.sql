CREATE TABLE sesame_sync3_checkpoints (
  vault_id TEXT NOT NULL REFERENCES sesame_sync3_vaults (id) ON DELETE CASCADE,
  device_id TEXT NOT NULL,
  version INTEGER NOT NULL,
  device_list_index BIGINT NOT NULL CHECK (device_list_index >= 1),
  device_list_digest TEXT NOT NULL,
  header_revision BIGINT NOT NULL CHECK (header_revision >= 0),
  header_digest TEXT NOT NULL,
  observed_at BIGINT NOT NULL CHECK (observed_at >= 1),
  signature TEXT NOT NULL,
  accepted_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (vault_id, device_id)
);
