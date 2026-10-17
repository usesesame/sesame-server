CREATE TABLE sesame_sync3_vaults (
  id TEXT PRIMARY KEY,
  owner_key TEXT NOT NULL UNIQUE,
  current_revision BIGINT NOT NULL CHECK (current_revision >= 0),
  head_index BIGINT NOT NULL CHECK (head_index >= 0),
  epoch BIGINT NOT NULL CHECK (epoch >= 0),
  created_at TIMESTAMPTZ NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE sesame_sync3_devices (
  vault_id TEXT NOT NULL REFERENCES sesame_sync3_vaults (id) ON DELETE CASCADE,
  device_id TEXT NOT NULL,
  token_device_id TEXT NOT NULL,
  signing_key TEXT NOT NULL,
  package_key TEXT NOT NULL,
  state TEXT NOT NULL CHECK (state IN ('pending', 'approved', 'revoked')),
  added_index BIGINT NOT NULL CHECK (added_index >= 0),
  activated_epoch BIGINT NOT NULL CHECK (activated_epoch >= 0),
  created_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (vault_id, device_id)
);

CREATE UNIQUE INDEX sesame_sync3_devices_token_key
  ON sesame_sync3_devices (vault_id, token_device_id);

CREATE TABLE sesame_sync3_device_list (
  vault_id TEXT NOT NULL REFERENCES sesame_sync3_vaults (id) ON DELETE CASCADE,
  idx BIGINT NOT NULL CHECK (idx >= 1),
  previous_digest TEXT NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('create', 'add', 'remove')),
  signer_device_id TEXT NOT NULL,
  subject_device_id TEXT NOT NULL,
  subject_signing_key TEXT NOT NULL,
  subject_package_key TEXT NOT NULL,
  sealed_label TEXT NOT NULL CHECK (char_length(sealed_label) <= 400),
  epoch_after BIGINT NOT NULL CHECK (epoch_after >= 0),
  snapshot_floor BIGINT NOT NULL CHECK (snapshot_floor >= 0),
  signature TEXT NOT NULL,
  PRIMARY KEY (vault_id, idx)
);

CREATE TABLE sesame_sync3_key_packages (
  vault_id TEXT NOT NULL REFERENCES sesame_sync3_vaults (id) ON DELETE CASCADE,
  recipient_device_id TEXT NOT NULL,
  sender_device_id TEXT NOT NULL,
  vault_epoch BIGINT NOT NULL CHECK (vault_epoch >= 0),
  entry_digest TEXT NOT NULL,
  ciphertext TEXT NOT NULL,
  signature TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (vault_id, recipient_device_id)
);

CREATE TABLE sesame_sync3_headers (
  vault_id TEXT NOT NULL REFERENCES sesame_sync3_vaults (id) ON DELETE CASCADE,
  revision BIGINT NOT NULL CHECK (revision >= 0),
  version INTEGER NOT NULL,
  device_id TEXT NOT NULL,
  vault_epoch BIGINT NOT NULL CHECK (vault_epoch >= 0),
  device_list_index BIGINT NOT NULL CHECK (device_list_index >= 0),
  device_list_digest TEXT NOT NULL,
  previous_digest TEXT NOT NULL,
  ciphertext_digest TEXT NOT NULL,
  ciphertext_length BIGINT NOT NULL CHECK (ciphertext_length >= 0),
  nonce TEXT NOT NULL,
  signature TEXT NOT NULL,
  digest TEXT NOT NULL,
  receipt TEXT NOT NULL,
  accepted_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (vault_id, revision)
);

CREATE TABLE sesame_sync3_ciphertexts (
  vault_id TEXT NOT NULL REFERENCES sesame_sync3_vaults (id) ON DELETE CASCADE,
  revision BIGINT NOT NULL CHECK (revision >= 0),
  body BYTEA NOT NULL CHECK (octet_length(body) <= 10485760),
  PRIMARY KEY (vault_id, revision)
);

CREATE TABLE sesame_sync3_pairings (
  vault_id TEXT NOT NULL REFERENCES sesame_sync3_vaults (id) ON DELETE CASCADE,
  device_id TEXT NOT NULL,
  token_device_id TEXT NOT NULL,
  signing_key TEXT NOT NULL,
  package_key TEXT NOT NULL,
  offer BYTEA NOT NULL CHECK (octet_length(offer) <= 512),
  answer BYTEA CHECK (octet_length(answer) <= 512),
  confirm BYTEA CHECK (octet_length(confirm) <= 512),
  state TEXT NOT NULL CHECK (state IN ('offered', 'answered', 'confirmed')),
  expires_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (vault_id, device_id)
);

CREATE UNIQUE INDEX sesame_sync3_pairings_token_key
  ON sesame_sync3_pairings (vault_id, token_device_id);

CREATE INDEX sesame_sync3_pairings_expiry_idx
  ON sesame_sync3_pairings (expires_at);

CREATE TABLE sesame_sync3_nonces (
  scope TEXT NOT NULL,
  nonce TEXT NOT NULL,
  expires_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (scope, nonce)
);

CREATE INDEX sesame_sync3_nonces_expiry_idx
  ON sesame_sync3_nonces (expires_at);
