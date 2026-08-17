-- Vault-key rotation on revocation, two-phase device activation, and an
-- authenticated revision chain.
--
-- Revocation advanced an integer epoch and nothing else. The vault key never
-- changed, so a device that kept the key it was given could still read anything
-- encrypted under it. An epoch is a number; it does not remove knowledge of a
-- key. Rotation is the only thing that does.
--
-- Sync remains disabled. Every column added here is nullable or defaulted on
-- tables that are empty in every deployment.

-- Two-phase activation.
--
-- After a rekey a surviving device is carried to the new epoch, but it has not
-- yet proved it can open the key package wrapped under the new key. Until it
-- does, it must not be treated as a live member: counting it as active is how a
-- rekey can silently strand a device that cannot actually read the vault.
--
-- A device is fully active when activated_epoch equals the vault epoch. The
-- device that performed the rekey activates in the same transaction, because it
-- generated the key.
ALTER TABLE sesame_sync_devices
  ADD COLUMN IF NOT EXISTS activated_epoch BIGINT NOT NULL DEFAULT 0
  CHECK (activated_epoch >= 0);

-- Existing approved devices are active at their current epoch. Without this an
-- already-approved device would read as never activated.
UPDATE sesame_sync_devices
SET activated_epoch = device_epoch
WHERE state = 'approved' AND activated_epoch = 0;

-- The revision chain.
--
-- A device that lost its local state could not tell a rollback from a fresh
-- start, because nothing bound a revision to the one before it. Each accepted
-- envelope now carries the digest of its predecessor, so the history is a
-- chain rather than a set of independently signed rows. The digest is over the
-- signed envelope, computed by the client and covered by its signature.
ALTER TABLE sesame_sync_envelopes
  ADD COLUMN IF NOT EXISTS previous_digest TEXT NOT NULL DEFAULT '';

ALTER TABLE sesame_sync_envelopes
  ADD COLUMN IF NOT EXISTS digest TEXT NOT NULL DEFAULT '';

-- Server-signed acceptance receipts.
--
-- The service never attested what it accepted, so a client had only its own
-- local file as evidence of the highest revision it had seen. The receipt is
-- signed with the capability signing key the desktop already verifies, and it
-- covers the vault, the revision, the envelope digest, and the time.
ALTER TABLE sesame_sync_envelopes
  ADD COLUMN IF NOT EXISTS receipt TEXT NOT NULL DEFAULT '';

-- Rekey bookkeeping, so an interrupted rotation is visible rather than silent.
ALTER TABLE sesame_sync_vaults
  ADD COLUMN IF NOT EXISTS rekeyed_at TIMESTAMPTZ;

-- 'device_denied' is a pending device being refused, which is not a revocation:
-- it never held the vault key, so nothing has to be rotated for it. Treating
-- the two the same forced a pointless epoch advance and a pointless rekey.
--
-- 'vault_rekeyed' and 'device_activated' record the new transitions.
ALTER TABLE sesame_sync_audit
  DROP CONSTRAINT IF EXISTS sesame_sync_audit_action_check;

ALTER TABLE sesame_sync_audit
  ADD CONSTRAINT sesame_sync_audit_action_check
  CHECK (action IN (
    'vault_created', 'device_enrolled', 'device_approved',
    'device_revoked', 'vault_epoch_advanced', 'vault_deleted',
    'device_denied', 'vault_rekeyed', 'device_activated', 'vault_reset'
  ));
