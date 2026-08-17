-- Sesame Sync control plane storage.
--
-- Sync is NOT enabled by this migration. The /v1/sync routes exist now, but they
-- answer 403 unless a Sync store is configured and the cloud_sync_available
-- capability flag is true. Only backend/cmd/api-sync-preview configures a store,
-- and it refuses to start outside development; cmd/api, which is what ships,
-- never does. Applying this migration creates empty tables and changes nothing a
-- user can reach.
--
-- The service stores opaque ciphertext and routing metadata only. No column
-- here may ever hold vault plaintext, a master password, a recovery kit, a
-- vault key, a TOTP value, or a backup code. Ciphertext columns are BYTEA and
-- are never parsed, indexed by content, or logged.

-- One synced vault per account for now. The vault id is client-generated and
-- opaque: the server must not be able to correlate it with vault contents.
-- vault_epoch increases whenever a device is revoked, which invalidates every
-- envelope signed under an older epoch.
CREATE TABLE IF NOT EXISTS sesame_sync_vaults (
  id               TEXT PRIMARY KEY,
  account_id       TEXT NOT NULL REFERENCES sesame_accounts(id) ON DELETE CASCADE,
  vault_epoch      BIGINT NOT NULL DEFAULT 1 CHECK (vault_epoch >= 1),
  current_revision BIGINT NOT NULL DEFAULT 0 CHECK (current_revision >= 0),
  created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (account_id)
);

-- A device holds two distinct keys: Ed25519 for signing envelopes and proofs,
-- X25519 for receiving key packages. They are separate on purpose. Ed25519
-- cannot perform key agreement, and reusing one key for both roles is the
-- classic failure in this design.
CREATE TABLE IF NOT EXISTS sesame_sync_devices (
  id                    TEXT PRIMARY KEY,
  vault_id              TEXT NOT NULL REFERENCES sesame_sync_vaults(id) ON DELETE CASCADE,
  signing_public_key    BYTEA NOT NULL CHECK (LENGTH(signing_public_key) = 32),
  encryption_public_key BYTEA NOT NULL CHECK (LENGTH(encryption_public_key) = 32),
  state                 TEXT NOT NULL CHECK (state IN ('pending', 'approved', 'revoked')),
  device_epoch          BIGINT NOT NULL DEFAULT 0 CHECK (device_epoch >= 0),
  label                 TEXT NOT NULL DEFAULT '' CHECK (LENGTH(label) <= 64),
  created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  approved_at           TIMESTAMPTZ,
  revoked_at            TIMESTAMPTZ,
  -- A key pair belongs to exactly one device record per vault, so a revoked
  -- device cannot re-enroll the same key and inherit its history.
  UNIQUE (vault_id, signing_public_key)
);

CREATE INDEX IF NOT EXISTS sesame_sync_devices_vault_state_idx
  ON sesame_sync_devices (vault_id, state);

-- Enrollment challenges are vault-bound, single-use, and short-lived.
-- consumed_at exists so consumption is a row update inside the same transaction
-- as the approval, which is what makes the ceremony atomic.
CREATE TABLE IF NOT EXISTS sesame_sync_challenges (
  value       BYTEA PRIMARY KEY CHECK (LENGTH(value) = 32),
  vault_id    TEXT NOT NULL REFERENCES sesame_sync_vaults(id) ON DELETE CASCADE,
  expires_at  TIMESTAMPTZ NOT NULL,
  consumed_at TIMESTAMPTZ,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS sesame_sync_challenges_expiry_idx
  ON sesame_sync_challenges (expires_at);

-- The vault key wrapped to one recipient device's X25519 key by an already
-- approved sender. Opaque to the service.
CREATE TABLE IF NOT EXISTS sesame_sync_key_packages (
  vault_id            TEXT NOT NULL REFERENCES sesame_sync_vaults(id) ON DELETE CASCADE,
  recipient_device_id TEXT NOT NULL REFERENCES sesame_sync_devices(id) ON DELETE CASCADE,
  sender_device_id    TEXT NOT NULL REFERENCES sesame_sync_devices(id) ON DELETE CASCADE,
  ciphertext          BYTEA NOT NULL CHECK (LENGTH(ciphertext) BETWEEN 16 AND 65536),
  signature           BYTEA NOT NULL CHECK (LENGTH(signature) = 64),
  created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (vault_id, recipient_device_id),
  -- A device must not hand itself the vault key.
  CHECK (recipient_device_id <> sender_device_id)
);

-- Size limits mirror backend/internal/syncproto exactly: MaxCiphertextBytes
-- (10 MiB) here and MaxEncryptedKeyPackageBytes (64 KiB) above. If they drift,
-- a payload the protocol accepts fails at the database as a server error
-- instead of a clean rejection. TestSyncStorageLimitsMatchTheProtocol pins it.
--
-- The compare-and-swap target. UNIQUE (vault_id, revision) is what makes two
-- racing devices resolve to exactly one winner even if the surrounding
-- transaction logic is ever wrong.
CREATE TABLE IF NOT EXISTS sesame_sync_envelopes (
  vault_id          TEXT NOT NULL REFERENCES sesame_sync_vaults(id) ON DELETE CASCADE,
  revision          BIGINT NOT NULL CHECK (revision >= 1),
  previous_revision BIGINT NOT NULL CHECK (previous_revision >= 0),
  device_id         TEXT NOT NULL REFERENCES sesame_sync_devices(id) ON DELETE RESTRICT,
  vault_epoch       BIGINT NOT NULL CHECK (vault_epoch >= 1),
  device_epoch      BIGINT NOT NULL CHECK (device_epoch >= 0),
  operation         TEXT NOT NULL CHECK (operation IN ('snapshot', 'tombstone')),
  tombstone_id      TEXT NOT NULL DEFAULT '',
  nonce             BYTEA NOT NULL CHECK (LENGTH(nonce) = 24),
  ciphertext        BYTEA NOT NULL CHECK (LENGTH(ciphertext) BETWEEN 16 AND 10485760),
  signature         BYTEA NOT NULL CHECK (LENGTH(signature) = 64),
  created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (vault_id, revision),
  -- Revisions are contiguous. A gap would let a device skip a conflict.
  CHECK (revision = previous_revision + 1),
  CHECK ((operation = 'tombstone') = (tombstone_id <> ''))
);

CREATE INDEX IF NOT EXISTS sesame_sync_envelopes_vault_recent_idx
  ON sesame_sync_envelopes (vault_id, revision DESC);

-- Append-only, matching sesame_admin_audit_log. Records control-plane events
-- only: never vault content, never ciphertext, never a size that could
-- fingerprint a vault.
CREATE TABLE IF NOT EXISTS sesame_sync_audit (
  id         BIGSERIAL PRIMARY KEY,
  vault_id   TEXT NOT NULL,
  device_id  TEXT NOT NULL DEFAULT '',
  action     TEXT NOT NULL CHECK (action IN (
               'vault_created', 'device_enrolled', 'device_approved',
               'device_revoked', 'vault_epoch_advanced', 'vault_deleted'
             )),
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS sesame_sync_audit_vault_idx
  ON sesame_sync_audit (vault_id, created_at DESC);

CREATE OR REPLACE FUNCTION sesame_reject_sync_audit_mutation()
RETURNS TRIGGER AS $$
BEGIN
  RAISE EXCEPTION 'sesame_sync_audit is append-only';
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS sesame_sync_audit_append_only ON sesame_sync_audit;
CREATE TRIGGER sesame_sync_audit_append_only
BEFORE UPDATE OR DELETE ON sesame_sync_audit
FOR EACH ROW EXECUTE FUNCTION sesame_reject_sync_audit_mutation();
