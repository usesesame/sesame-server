package sqlitestore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"testing"
	"time"

	"usesesame.app/backend/internal/syncproto"
	"usesesame.app/backend/internal/syncsvc"
	"usesesame.app/backend/internal/syncsvc/servicetest"
)

var syncTables = []string{"sync_vaults", "sync_devices", "sync_device_list", "sync_key_packages", "sync_headers", "sync_ciphertexts", "sync_pairings", "sync_checkpoints", "sync_nonces"}

func openSyncStore(t *testing.T, path string) *Store {
	t.Helper()
	store, err := Open(context.Background(), Options{Path: path, AdminKey: bytes.Repeat([]byte{7}, adminKeyLength)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func rowCount(t *testing.T, path, table string) int {
	t.Helper()
	db := rawDatabase(t, path)
	var count int
	if err := db.QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM %s`, table)).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestSyncStorageBehaviour(t *testing.T) {
	servicetest.Run(t, func(t *testing.T) syncsvc.Storage {
		return openSyncStore(t, filepath.Join(t.TempDir(), "sync.db")).SyncStorage()
	})
}

func seedSyncVault(t *testing.T, storage syncsvc.Storage, vaultID string) {
	t.Helper()
	now := time.Unix(1_800_000_000, 0).UTC()
	err := storage.Update(context.Background(), func(tx syncsvc.Tx) error {
		if err := tx.InsertVault(syncsvc.Vault{ID: vaultID, OwnerKey: "owner-fictional", CurrentRevision: 1, HeadIndex: 1, Epoch: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
			return err
		}
		if err := tx.AppendDeviceListEntry(syncproto.DeviceListEntry{
			VaultID: vaultID, Index: 1, PreviousDigest: "digest-zero", Kind: syncproto.EntryCreate,
			SignerDeviceID: "device-laptop", SubjectDeviceID: "device-laptop", SubjectSigningKey: "signing-laptop",
			SubjectPackageKey: "package-laptop", SealedLabel: "label-laptop", EpochAfter: 1, Signature: "signature-list",
		}); err != nil {
			return err
		}
		if err := tx.PutDevice(syncsvc.Device{
			VaultID: vaultID, DeviceID: "device-laptop", TokenDeviceID: "token-laptop", SigningKey: "signing-laptop",
			PackageKey: "package-laptop", State: syncsvc.DeviceApproved, AddedIndex: 1, ActivatedEpoch: 1, CreatedAt: now,
		}); err != nil {
			return err
		}
		if err := tx.PutKeyPackage(syncsvc.KeyPackage{KeyPackageV3: syncproto.KeyPackageV3{
			VaultID: vaultID, SenderDeviceID: "device-laptop", RecipientDeviceID: "device-phone", VaultEpoch: 1, EntryDigest: "digest-entry",
			Ciphertext: "ciphertext-package", Signature: "signature-package",
		}, CreatedAt: now}); err != nil {
			return err
		}
		if err := tx.InsertHeader(syncsvc.StoredHeader{Header: syncproto.Header{
			Version: 1, VaultID: vaultID, DeviceID: "device-laptop", Revision: 1, VaultEpoch: 1, DeviceListIndex: 1,
			DeviceListDigest: "digest-list", PreviousDigest: "digest-zero", CiphertextDigest: "digest-body",
			CiphertextLength: 16, Nonce: "nonce-header", Signature: "signature-header",
		}, Digest: "digest-header", Receipt: "receipt-header", AcceptedAt: now}); err != nil {
			return err
		}
		if err := tx.PutCiphertext(vaultID, 1, []byte("ciphertext-bytes")); err != nil {
			return err
		}
		if err := tx.PutPairing(syncsvc.Pairing{
			VaultID: vaultID, DeviceID: "device-tablet", TokenDeviceID: "token-tablet", SigningKey: "signing-tablet",
			PackageKey: "package-tablet", Offer: []byte("offer-tablet"), State: syncsvc.PairingOffered,
			ExpiresAt: now.Add(syncsvc.PairingLifetime), CreatedAt: now,
		}); err != nil {
			return err
		}
		if err := tx.PutCheckpoint(syncsvc.StoredCheckpoint{Checkpoint: syncproto.Checkpoint{
			Version: 3, VaultID: vaultID, DeviceID: "device-laptop", DeviceListIndex: 1, DeviceListDigest: "digest-list",
			HeaderRevision: 1, HeaderDigest: "digest-header", ObservedAt: 1_800_000_000, Signature: "signature-checkpoint",
		}, AcceptedAt: now}); err != nil {
			return err
		}
		fresh, err := tx.RecordNonce("scope-fictional", "nonce-fictional", now, now.Add(time.Minute))
		if err != nil {
			return err
		}
		if !fresh {
			return errors.New("the seeded nonce was not fresh")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSyncMigrationIsIdempotentOnReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sync.db")
	first := openSyncStore(t, path)
	seedSyncVault(t, first.SyncStorage(), "vault-fictional")
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openSyncStore(t, path)
	if rowCount(t, path, "sync_vaults") != 1 || rowCount(t, path, "sync_ciphertexts") != 1 {
		t.Fatal("reopening the database changed the synced rows")
	}
	db := rawDatabase(t, path)
	var applied int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE name = '0003_sync'`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != 1 || storedVersion(t, db) != len(defaultMigrations) {
		t.Fatalf("the sync migration was recorded %d times", applied)
	}
	var vault syncsvc.Vault
	if err := reopened.SyncStorage().View(context.Background(), func(tx syncsvc.Tx) error {
		var err error
		vault, err = tx.VaultByID("vault-fictional")
		return err
	}); err != nil {
		t.Fatalf("the vault is gone after reopen: %v", err)
	}
	if vault.OwnerKey != "owner-fictional" || vault.CurrentRevision != 1 {
		t.Fatalf("vault after reopen = %+v", vault)
	}
}

func TestDeletingVaultRemovesEveryDependentRow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sync.db")
	store := openSyncStore(t, path)
	storage := store.SyncStorage()
	seedSyncVault(t, storage, "vault-fictional")
	for _, table := range syncTables {
		if rowCount(t, path, table) != 1 {
			t.Fatalf("%s has %d seeded rows, want 1", table, rowCount(t, path, table))
		}
	}
	if err := storage.Update(context.Background(), func(tx syncsvc.Tx) error {
		return tx.DeleteVault("vault-fictional")
	}); err != nil {
		t.Fatal(err)
	}
	for _, table := range syncTables {
		want := 0
		if table == "sync_nonces" {
			want = 1
		}
		if got := rowCount(t, path, table); got != want {
			t.Fatalf("%s has %d rows after the vault was deleted, want %d", table, got, want)
		}
	}
}

func TestSyncNonceWindowAndPurge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sync.db")
	storage := openSyncStore(t, path).SyncStorage()
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	record := func(at, expires time.Time) bool {
		t.Helper()
		var fresh bool
		err := storage.Update(ctx, func(tx syncsvc.Tx) error {
			var err error
			fresh, err = tx.RecordNonce("scope-fictional", "nonce-fictional", at, expires)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return fresh
	}
	if !record(now, now.Add(time.Minute)) {
		t.Fatal("the first use of a nonce was refused")
	}
	if record(now.Add(30*time.Second), now.Add(90*time.Second)) {
		t.Fatal("a live nonce was accepted again")
	}
	if !record(now.Add(time.Minute), now.Add(2*time.Minute)) {
		t.Fatal("a nonce whose window ended was refused")
	}
	if err := storage.Update(ctx, func(tx syncsvc.Tx) error {
		return tx.PurgeExpired(now.Add(3 * time.Minute))
	}); err != nil {
		t.Fatal(err)
	}
	if rowCount(t, path, "sync_nonces") != 0 {
		t.Fatal("an expired nonce survived the purge")
	}
}

func TestSyncStoreRefusesValuesAboveTheSignedRange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sync.db")
	storage := openSyncStore(t, path).SyncStorage()
	err := storage.Update(context.Background(), func(tx syncsvc.Tx) error {
		return tx.InsertVault(syncsvc.Vault{ID: "vault-fictional", OwnerKey: "owner-fictional", CurrentRevision: uint64(math.MaxInt64) + 1})
	})
	if err == nil {
		t.Fatal("a revision above the signed 64-bit range was stored")
	}
	if rowCount(t, path, "sync_vaults") != 0 {
		t.Fatal("a refused vault write left a row behind")
	}
}

func TestSyncRejoinMigrationPreservesRevocationAndRollsBackFailure(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sync.db")
	config := Options{Path: path, AdminKey: bytes.Repeat([]byte{7}, adminKeyLength)}
	old, err := openWith(ctx, config, defaultMigrations[:4])
	if err != nil {
		t.Fatal(err)
	}
	seedSyncVault(t, old.SyncStorage(), "vault-fictional")
	var removed syncsvc.Device
	if err := old.SyncStorage().Update(ctx, func(tx syncsvc.Tx) error {
		var err error
		removed, err = tx.Device("vault-fictional", "device-laptop")
		if err != nil {
			return err
		}
		removed.State = syncsvc.DeviceRevoked
		return tx.PutDevice(removed)
	}); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	broken := append([]migration(nil), defaultMigrations...)
	broken[4].script += "INSERT INTO missing_table VALUES (1);"
	if _, err := openWith(ctx, config, broken); err == nil {
		t.Fatal("an interrupted table rebuild succeeded")
	}
	db := rawDatabase(t, path)
	if storedVersion(t, db) != 4 || tableCount(t, db, "sync_devices_rejoined") != 0 {
		t.Fatal("a failed rebuild left a partial schema")
	}
	upgraded := openSyncStore(t, path)
	if err := upgraded.SyncStorage().Update(ctx, func(tx syncsvc.Tx) error {
		saved, err := tx.Device(removed.VaultID, removed.DeviceID)
		if err != nil {
			return err
		}
		if saved != removed {
			t.Fatal("the migration changed the revoked identity")
		}
		replacement := removed
		replacement.DeviceID, replacement.State, replacement.AddedIndex = "device-replacement", syncsvc.DeviceApproved, 3
		return tx.PutDevice(replacement)
	}); err != nil {
		t.Fatalf("the old token could not bind to a replacement: %v", err)
	}
	if err := upgraded.SyncStorage().Update(ctx, func(tx syncsvc.Tx) error {
		duplicate := removed
		duplicate.DeviceID, duplicate.State = "device-duplicate", syncsvc.DeviceApproved
		return tx.PutDevice(duplicate)
	}); !errors.Is(err, syncsvc.ErrConflict) {
		t.Fatalf("two approved identities shared one connection: %v", err)
	}
	if rowCount(t, path, "sync_headers") != 1 || rowCount(t, path, "sync_ciphertexts") != 1 || rowCount(t, path, "sync_devices") != 2 {
		t.Fatal("the migration changed the retained history or accepted a duplicate binding")
	}
}
