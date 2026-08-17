-- Make account deletion actually erase Sesame Sync audit rows.
--
-- Sesame_sync_audit.vault_id was a bare TEXT column with no foreign key, so
-- deleting an account cascaded through vaults, devices, envelopes, and key
-- packages and left the audit rows behind, pointing at a vault that no longer
-- exists. Every Sync row for the account is supposed to be
-- removed, and that was not true.
--
-- The table is append-only, enforced by a trigger that rejects UPDATE and
-- DELETE. A foreign key cascade is a DELETE, so the trigger has to allow the
-- cascade through while still refusing an ordinary one. Both are handled
-- below.

-- Orphans first: any row whose vault is already gone cannot be covered by a
-- foreign key that is about to be added. There are none in a deployment that has
-- never enabled Sync; this is here so the migration is correct wherever it runs.
ALTER TABLE sesame_sync_audit DISABLE TRIGGER USER;

DELETE FROM sesame_sync_audit
WHERE vault_id NOT IN (SELECT id FROM sesame_sync_vaults);

ALTER TABLE sesame_sync_audit ENABLE TRIGGER USER;

ALTER TABLE sesame_sync_audit
  DROP CONSTRAINT IF EXISTS sesame_sync_audit_vault_id_fkey;

ALTER TABLE sesame_sync_audit
  ADD CONSTRAINT sesame_sync_audit_vault_id_fkey
  FOREIGN KEY (vault_id) REFERENCES sesame_sync_vaults(id) ON DELETE CASCADE;

-- The append-only trigger must not block the cascade above.
--
-- session_replication_role is not usable here because the delete arrives on an
-- ordinary connection. Instead the trigger asks whether the vault still exists:
-- during a cascade it is already gone, and an ordinary DELETE against a live
-- vault is still refused. That keeps the append-only property for every case it
-- was written to cover, which is someone editing history, and stops it from
-- defeating erasure.
CREATE OR REPLACE FUNCTION sesame_reject_sync_audit_mutation()
RETURNS TRIGGER AS $$
BEGIN
  IF TG_OP = 'DELETE' AND NOT EXISTS (
    SELECT 1 FROM sesame_sync_vaults WHERE id = OLD.vault_id
  ) THEN
    RETURN OLD;
  END IF;
  RAISE EXCEPTION 'sesame_sync_audit is append-only';
END;
$$ LANGUAGE plpgsql;
