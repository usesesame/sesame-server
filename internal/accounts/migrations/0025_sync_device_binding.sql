-- Bind each Sesame Sync device to the desktop connection that enrolled it.
--
-- Sync authenticated a
-- caller as an account and then acted on the vault for that account. Nothing
-- tied the desktop token in the request to a Sync device record, so:
--
--   * A device revoked from Sync kept a valid desktop token and could keep
--     downloading new snapshots, which it could still decrypt with the vault key
--     it already held. Revocation stopped nothing it actually cared about.
--   * Any desktop token for the account could revoke any Sync device, including
--     every survivor, because the request never had to prove which device it
--     was.
--
-- The binding makes "which Sync device is calling" answerable from the token
-- alone, which is what the download and revoke paths now require.
--
-- Sync is still not enabled by this migration. It creates a nullable column and
-- an index on tables that are empty in every deployment, so it is safe to apply
-- and safe to roll back.
ALTER TABLE sesame_sync_devices
  ADD COLUMN IF NOT EXISTS desktop_device_id TEXT;

-- Deliberately not a foreign key to the desktop connection table. A connection
-- row is replaced when a desktop relinks, and losing the Sync binding on relink
-- would silently strand a device. The binding is validated in the transaction
-- that uses it instead.
--
-- Partial and unique: one Sync device per desktop connection per vault, so a
-- single desktop cannot accumulate Sync identities, while the many rows with no
-- binding yet do not collide with each other.
CREATE UNIQUE INDEX IF NOT EXISTS sesame_sync_devices_desktop_binding_idx
  ON sesame_sync_devices (vault_id, desktop_device_id)
  WHERE desktop_device_id IS NOT NULL;

-- Looking a device up by the calling desktop token happens on every read, so it
-- gets its own index rather than scanning the vault's devices.
CREATE INDEX IF NOT EXISTS sesame_sync_devices_desktop_lookup_idx
  ON sesame_sync_devices (desktop_device_id)
  WHERE desktop_device_id IS NOT NULL;
