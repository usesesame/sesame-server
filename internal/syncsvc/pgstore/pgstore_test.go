package pgstore

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"math"
	"os"
	"testing"
	"time"

	"usesesame.app/backend/internal/accounts"
	"usesesame.app/backend/internal/syncproto"
	"usesesame.app/backend/internal/syncsvc"
	"usesesame.app/backend/internal/syncsvc/servicetest"
)

const testVaultID = "vaultAAAAAAAAAAAA01"

func openTestDatabase(t *testing.T) *sql.DB {
	t.Helper()
	databaseURL := os.Getenv("SESAME_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("SESAME_TEST_DATABASE_URL is required")
	}
	ctx := context.Background()
	accountStore, err := accounts.Open(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open account store: %v", err)
	}
	t.Cleanup(func() { _ = accountStore.Close() })
	db := accountStore.DB()
	const lockID int64 = 762374923
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("reserve test database connection: %v", err)
	}
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, lockID); err != nil {
		_ = conn.Close()
		t.Fatalf("lock test database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, lockID)
		_ = conn.Close()
	})
	return db
}

func truncateSyncTables(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `
		TRUNCATE sesame_sync3_vaults, sesame_sync3_devices, sesame_sync3_device_list,
		         sesame_sync3_key_packages, sesame_sync3_headers, sesame_sync3_ciphertexts,
		         sesame_sync3_pairings, sesame_sync3_checkpoints, sesame_sync3_nonces
		RESTART IDENTITY CASCADE
	`); err != nil {
		t.Fatalf("truncate sync tables: %v", err)
	}
}

func newTestStore(t *testing.T) (*Store, *sql.DB) {
	t.Helper()
	db := openTestDatabase(t)
	truncateSyncTables(t, db)
	return New(db), db
}

func insertTestVault(t *testing.T, store *Store, vaultID string) {
	t.Helper()
	now := time.Unix(1_800_000_000, 0).UTC()
	err := store.Update(context.Background(), func(tx syncsvc.Tx) error {
		return tx.InsertVault(syncsvc.Vault{ID: vaultID, OwnerKey: "owner-fixture", HeadIndex: 1, Epoch: 1, CreatedAt: now, UpdatedAt: now})
	})
	if err != nil {
		t.Fatalf("insert vault: %v", err)
	}
}

func TestServiceBehaviourOnPostgres(t *testing.T) {
	db := openTestDatabase(t)
	servicetest.Run(t, func(t *testing.T) syncsvc.Storage {
		truncateSyncTables(t, db)
		return New(db)
	})
}

func TestRecordNonceTakesOverOnlyAnExpiredRow(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	expires := now.Add(10 * time.Minute)
	record := func(at time.Time, nonce string) bool {
		t.Helper()
		var fresh bool
		err := store.Update(ctx, func(tx syncsvc.Tx) error {
			var err error
			fresh, err = tx.RecordNonce("scope-fixture", nonce, at, at.Add(10*time.Minute))
			return err
		})
		if err != nil {
			t.Fatalf("record nonce: %v", err)
		}
		return fresh
	}

	if !record(now, "nonce-one") {
		t.Fatal("a new nonce was not fresh")
	}
	if record(now.Add(time.Minute), "nonce-one") {
		t.Fatal("a live nonce was accepted twice")
	}
	if !record(expires, "nonce-one") {
		t.Fatal("an expired nonce did not take over")
	}

	if err := store.Update(ctx, func(tx syncsvc.Tx) error { return tx.PurgeExpired(expires.Add(time.Hour)) }); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if !record(expires.Add(time.Hour), "nonce-one") {
		t.Fatal("a purged nonce was not fresh")
	}
}

func TestAppendDeviceListEntryNeedsTheNextIndex(t *testing.T) {
	store, _ := newTestStore(t)
	insertTestVault(t, store, testVaultID)
	ctx := context.Background()
	entry := func(index uint64) syncproto.DeviceListEntry {
		return syncproto.DeviceListEntry{
			VaultID: testVaultID, Index: index, PreviousDigest: "digest-fixture", Kind: syncproto.EntryAdd,
			SignerDeviceID: "device-fixture", SubjectDeviceID: "device-subject", SubjectSigningKey: "key-fixture",
			SubjectPackageKey: "package-fixture", SealedLabel: "label-fixture", EpochAfter: 1, SnapshotFloor: 0,
			Signature: "signature-fixture",
		}
	}
	appendEntry := func(index uint64) error {
		return store.Update(ctx, func(tx syncsvc.Tx) error { return tx.AppendDeviceListEntry(entry(index)) })
	}

	if err := appendEntry(2); !errors.Is(err, syncsvc.ErrConflict) {
		t.Fatalf("index 2 on an empty list: %v, want ErrConflict", err)
	}
	if err := appendEntry(1); err != nil {
		t.Fatalf("index 1: %v", err)
	}
	if err := appendEntry(1); !errors.Is(err, syncsvc.ErrConflict) {
		t.Fatalf("repeated index 1: %v, want ErrConflict", err)
	}
	if err := appendEntry(3); !errors.Is(err, syncsvc.ErrConflict) {
		t.Fatalf("index 3 after 1: %v, want ErrConflict", err)
	}
	if err := appendEntry(2); err != nil {
		t.Fatalf("index 2: %v", err)
	}
}

func TestRejectsValuesOutsideTheStoredRange(t *testing.T) {
	store, _ := newTestStore(t)
	insertTestVault(t, store, testVaultID)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()

	overflow := store.Update(ctx, func(tx syncsvc.Tx) error {
		return tx.PutDevice(syncsvc.Device{
			VaultID: testVaultID, DeviceID: "device-overflow", TokenDeviceID: "token-overflow",
			SigningKey: "key-fixture", PackageKey: "package-fixture", State: syncsvc.DeviceApproved,
			AddedIndex: math.MaxInt64 + 1, CreatedAt: now,
		})
	})
	if overflow == nil || errors.Is(overflow, syncsvc.ErrConflict) {
		t.Fatalf("an index above MaxInt64 was stored: %v", overflow)
	}

	oversized := store.Update(ctx, func(tx syncsvc.Tx) error {
		return tx.PutCiphertext(testVaultID, 1, bytes.Repeat([]byte{1}, 10*1024*1024+1))
	})
	if oversized == nil {
		t.Fatal("a ciphertext above 10485760 bytes was stored")
	}

	tooLongOffer := store.Update(ctx, func(tx syncsvc.Tx) error {
		return tx.PutPairing(syncsvc.Pairing{
			VaultID: testVaultID, DeviceID: "device-offer", TokenDeviceID: "token-offer",
			SigningKey: "key-fixture", PackageKey: "package-fixture", Offer: bytes.Repeat([]byte{2}, 513),
			State: syncsvc.PairingOffered, ExpiresAt: now.Add(time.Minute), CreatedAt: now,
		})
	})
	if tooLongOffer == nil {
		t.Fatal("an offer above 512 bytes was stored")
	}
}

func TestTheOldSyncTablesAreDroppedAndTheCurrentOnesRemain(t *testing.T) {
	db := openTestDatabase(t)
	for _, table := range []string{
		"sesame_sync_vaults", "sesame_sync_devices", "sesame_sync_challenges",
		"sesame_sync_key_packages", "sesame_sync_envelopes", "sesame_sync_audit",
	} {
		var name sql.NullString
		if err := db.QueryRowContext(context.Background(), `SELECT to_regclass($1)::text`, "public."+table).Scan(&name); err != nil {
			t.Fatal(err)
		}
		if name.Valid {
			t.Fatalf("%s still exists", table)
		}
	}
	for _, table := range []string{"sesame_sync3_vaults", "sesame_sync3_checkpoints", "sesame_sync3_key_packages"} {
		var name sql.NullString
		if err := db.QueryRowContext(context.Background(), `SELECT to_regclass($1)::text`, "public."+table).Scan(&name); err != nil {
			t.Fatal(err)
		}
		if !name.Valid {
			t.Fatalf("%s is missing", table)
		}
	}
}
