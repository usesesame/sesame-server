package sqlitestore

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"usesesame.app/backend/internal/selfhost"
	"usesesame.app/backend/internal/syncproto"
	"usesesame.app/backend/internal/syncsvc"
)

var holderSyncTables = []string{"sync_devices", "sync_device_list", "sync_key_packages", "sync_headers", "sync_ciphertexts", "sync_pairings", "sync_checkpoints"}

var holderTestActor = selfhost.Actor{Kind: selfhost.ActorSystem}

func seedHolderVault(t *testing.T, store *Store, vaultID, ownerKey string) {
	t.Helper()
	now := time.Unix(1_800_000_000, 0).UTC()
	err := store.SyncStorage().Update(context.Background(), func(tx syncsvc.Tx) error {
		if err := tx.InsertVault(syncsvc.Vault{ID: vaultID, OwnerKey: ownerKey, CurrentRevision: 1, HeadIndex: 1, Epoch: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
			return err
		}
		if err := tx.AppendDeviceListEntry(syncproto.DeviceListEntry{
			VaultID: vaultID, Index: 1, PreviousDigest: "digest-zero", Kind: syncproto.EntryCreate,
			SignerDeviceID: "device-desk", SubjectDeviceID: "device-desk", SubjectSigningKey: "signing-desk",
			SubjectPackageKey: "package-desk", SealedLabel: "label-desk", EpochAfter: 1, Signature: "signature-list",
		}); err != nil {
			return err
		}
		if err := tx.PutDevice(syncsvc.Device{
			VaultID: vaultID, DeviceID: "device-desk", TokenDeviceID: "token-desk-" + vaultID, SigningKey: "signing-desk",
			PackageKey: "package-desk", State: syncsvc.DeviceApproved, AddedIndex: 1, ActivatedEpoch: 1, CreatedAt: now,
		}); err != nil {
			return err
		}
		if err := tx.PutKeyPackage(syncsvc.KeyPackage{KeyPackageV3: syncproto.KeyPackageV3{
			VaultID: vaultID, SenderDeviceID: "device-desk", RecipientDeviceID: "device-phone", VaultEpoch: 1, EntryDigest: "digest-entry",
			Ciphertext: "ciphertext-package", Signature: "signature-package",
		}, CreatedAt: now}); err != nil {
			return err
		}
		if err := tx.InsertHeader(syncsvc.StoredHeader{Header: syncproto.Header{
			Version: 1, VaultID: vaultID, DeviceID: "device-desk", Revision: 1, VaultEpoch: 1, DeviceListIndex: 1,
			DeviceListDigest: "digest-list", PreviousDigest: "digest-zero", CiphertextDigest: "digest-body",
			CiphertextLength: 16, Nonce: "nonce-header", Signature: "signature-header",
		}, Digest: "digest-header", Receipt: "receipt-header", AcceptedAt: now}); err != nil {
			return err
		}
		if err := tx.PutCiphertext(vaultID, 1, []byte("ciphertext-bytes")); err != nil {
			return err
		}
		if err := tx.PutPairing(syncsvc.Pairing{
			VaultID: vaultID, DeviceID: "device-tablet", TokenDeviceID: "token-tablet-" + vaultID, SigningKey: "signing-tablet",
			PackageKey: "package-tablet", Offer: []byte("offer-tablet"), State: syncsvc.PairingOffered,
			ExpiresAt: now.Add(syncsvc.PairingLifetime), CreatedAt: now,
		}); err != nil {
			return err
		}
		return tx.PutCheckpoint(syncsvc.StoredCheckpoint{Checkpoint: syncproto.Checkpoint{
			Version: 3, VaultID: vaultID, DeviceID: "device-desk", DeviceListIndex: 1, DeviceListDigest: "digest-list",
			HeaderRevision: 1, HeaderDigest: "digest-header", ObservedAt: 1_800_000_000, Signature: "signature-checkpoint",
		}, AcceptedAt: now})
	})
	if err != nil {
		t.Fatal(err)
	}
}

func holderVaultRows(t *testing.T, db *sql.DB, vaultID, ownerKey string) map[string]int {
	t.Helper()
	counts := map[string]int{"sync_vaults": mustCount(t, db, `SELECT COUNT(*) FROM sync_vaults WHERE id = ? OR owner_key = ?`, vaultID, ownerKey)}
	for _, table := range holderSyncTables {
		counts[table] = mustCount(t, db, `SELECT COUNT(*) FROM `+table+` WHERE vault_id = ?`, vaultID)
	}
	return counts
}

func requireNoHolderVault(t *testing.T, db *sql.DB, vaultID, ownerKey string) {
	t.Helper()
	for table, count := range holderVaultRows(t, db, vaultID, ownerKey) {
		if count != 0 {
			t.Fatalf("%d rows remain in %s for %s", count, table, ownerKey)
		}
	}
}

func requireWholeHolderVault(t *testing.T, db *sql.DB, vaultID, ownerKey string) {
	t.Helper()
	for table, count := range holderVaultRows(t, db, vaultID, ownerKey) {
		if count != 1 {
			t.Fatalf("%s holds %d rows for %s, want 1", table, count, ownerKey)
		}
	}
}

func openHolderStore(t *testing.T) (*Store, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sesame.db")
	return openSyncStore(t, path), rawDatabase(t, path)
}

func TestDeleteMemberDeletesTheirSyncedVault(t *testing.T) {
	store, db := openHolderStore(t)
	ctx := context.Background()
	leaving, err := store.CreateMember(ctx, holderTestActor, "Fictional Leaver")
	if err != nil {
		t.Fatal(err)
	}
	staying, err := store.CreateMember(ctx, holderTestActor, "Fictional Stayer")
	if err != nil {
		t.Fatal(err)
	}
	leavingKey, stayingKey := "member:"+leaving.ID, "member:"+staying.ID
	seedHolderVault(t, store, "vault-leaver", leavingKey)
	seedHolderVault(t, store, "vault-stayer", stayingKey)
	if _, err := store.DeleteMember(ctx, holderTestActor, leaving.ID); err != nil {
		t.Fatal(err)
	}
	requireNoHolderVault(t, db, "vault-leaver", leavingKey)
	requireWholeHolderVault(t, db, "vault-stayer", stayingKey)
}

func TestRemoveOwnerDeletesTheirSyncedVault(t *testing.T) {
	store, db := openHolderStore(t)
	ctx := context.Background()
	invited, err := store.InviteOwner(ctx, holderTestActor, "Fictional Co-owner")
	if err != nil {
		t.Fatal(err)
	}
	member, err := store.CreateMember(ctx, holderTestActor, "Fictional Member")
	if err != nil {
		t.Fatal(err)
	}
	ownerKey, memberKey := "owner:"+invited.Owner.ID, "member:"+member.ID
	seedHolderVault(t, store, "vault-owner", ownerKey)
	seedHolderVault(t, store, "vault-member", memberKey)
	if err := store.RemoveOwner(ctx, holderTestActor, invited.Owner.ID); err != nil {
		t.Fatal(err)
	}
	requireNoHolderVault(t, db, "vault-owner", ownerKey)
	requireWholeHolderVault(t, db, "vault-member", memberKey)
}

func TestFailedRemovalKeepsTheSyncedVault(t *testing.T) {
	store, db := openHolderStore(t)
	ctx := context.Background()
	member, err := store.CreateMember(ctx, holderTestActor, "Fictional Member")
	if err != nil {
		t.Fatal(err)
	}
	memberKey := "member:" + member.ID
	seedHolderVault(t, store, "vault-member", memberKey)
	if _, err := store.DeleteMember(ctx, holderTestActor, "member-unknown"); err == nil {
		t.Fatal("deleting an unknown member succeeded")
	}
	if err := store.RemoveOwner(ctx, holderTestActor, member.ID); err == nil {
		t.Fatal("removing a member ID as an owner succeeded")
	}
	requireWholeHolderVault(t, db, "vault-member", memberKey)
}

func TestMaintainDeletesOrphanedSyncedVaults(t *testing.T) {
	store, db := openHolderStore(t)
	ctx := context.Background()
	live, err := store.CreateMember(ctx, holderTestActor, "Fictional Live")
	if err != nil {
		t.Fatal(err)
	}
	removed, err := store.CreateMember(ctx, holderTestActor, "Fictional Removed")
	if err != nil {
		t.Fatal(err)
	}
	invited, err := store.InviteOwner(ctx, holderTestActor, "Fictional Invitee")
	if err != nil {
		t.Fatal(err)
	}
	liveKey, removedKey, invitedKey := "member:"+live.ID, "member:"+removed.ID, "owner:"+invited.Owner.ID
	seedHolderVault(t, store, "vault-live", liveKey)
	seedHolderVault(t, store, "vault-removed", removedKey)
	seedHolderVault(t, store, "vault-invited", invitedKey)
	seedHolderVault(t, store, "vault-vanished-owner", "owner:owner-vanished")
	seedHolderVault(t, store, "vault-vanished-member", "member:member-vanished")
	seedHolderVault(t, store, "vault-other-key", "owner-key-other")
	mustExec(t, db, `UPDATE members SET removed_at = ? WHERE id = ?`, time.Now().Unix(), removed.ID)
	if _, err := store.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	requireNoHolderVault(t, db, "vault-removed", removedKey)
	requireNoHolderVault(t, db, "vault-vanished-owner", "owner:owner-vanished")
	requireNoHolderVault(t, db, "vault-vanished-member", "member:member-vanished")
	requireWholeHolderVault(t, db, "vault-live", liveKey)
	requireWholeHolderVault(t, db, "vault-invited", invitedKey)
	requireWholeHolderVault(t, db, "vault-other-key", "owner-key-other")
}

func TestCompleteRecoveryDeletesVaultsOfDroppedOwners(t *testing.T) {
	env := newRecoveryTestEnv(t)
	env.seedRichSource()
	if _, err := env.prepare(); err != nil {
		t.Fatal(err)
	}
	store := env.open(func(options *Options) { options.Preserve = true })
	defer store.Close()
	keptKey, droppedKey, memberKey := "owner:"+recoveryOwnerA, "owner:"+recoveryOwnerB, "member:"+recoveryMemberActive
	seedHolderVault(t, store, "vault-kept", keptKey)
	seedHolderVault(t, store, "vault-dropped", droppedKey)
	seedHolderVault(t, store, "vault-member", memberKey)
	status, err := store.RecoveryStatus(env.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteRecovery(env.ctx, selfhost.SystemActor, status.Generation, selfhost.RecoverySelection{
		OwnerIDs:  []string{recoveryOwnerA},
		DeviceIDs: []string{recoveryDeviceA},
	}); err != nil {
		t.Fatal(err)
	}
	requireNoHolderVault(t, store.db, "vault-dropped", droppedKey)
	for vaultID, ownerKey := range map[string]string{"vault-kept": keptKey, "vault-member": memberKey} {
		if count := mustCount(t, store.db, `SELECT COUNT(*) FROM sync_vaults WHERE id = ? AND owner_key = ?`, vaultID, ownerKey); count != 1 {
			t.Fatalf("recovery deleted the vault for %s", ownerKey)
		}
	}
}
