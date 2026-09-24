package syncstore

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"os"
	"testing"

	"usesesame.app/backend/internal/accounts"
	"usesesame.app/backend/internal/syncproto"
)

const (
	rekeyTestVaultID = "vaultAAAAAAAAAAAA01"
	rekeyDeviceA     = "deviceAAAAAAAAAAA01"
	rekeyDeviceB     = "deviceBBBBBBBBBBB01"
	rekeyDeviceC     = "deviceCCCCCCCCCCC01"
)

type rekeyTestDevice struct {
	id   string
	priv ed25519.PrivateKey
	pub  []byte
}

func newRekeyTestDevice(t *testing.T, id string) rekeyTestDevice {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate device key: %v", err)
	}
	return rekeyTestDevice{id: id, priv: priv, pub: pub}
}

func signKeyPackage(t *testing.T, signer rekeyTestDevice, pkg syncproto.EncryptedKeyPackage) []byte {
	t.Helper()
	payload, err := pkg.SigningPayload()
	if err != nil {
		t.Fatalf("key package payload: %v", err)
	}
	return ed25519.Sign(signer.priv, payload)
}

// A rekey test needs a migrated database, so it reuses the account store's
// Open, which runs migrations under the same advisory lock the other database
// tests take.
func rekeyTestStore(t *testing.T) (*Store, *sql.DB) {
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
	if _, err := db.ExecContext(ctx, `
		TRUNCATE sesame_sync_audit, sesame_sync_envelopes, sesame_sync_key_packages,
		         sesame_sync_challenges, sesame_sync_devices, sesame_sync_vaults,
		         sesame_accounts
		RESTART IDENTITY CASCADE
	`); err != nil {
		t.Fatalf("clear sync tables: %v", err)
	}
	return New(db), db
}

func enrollRekeyDevice(t *testing.T, store *Store, device rekeyTestDevice, desktopID string) {
	t.Helper()
	ctx := context.Background()
	challenge, err := store.IssueChallenge(ctx, rekeyTestVaultID)
	if err != nil {
		t.Fatalf("issue challenge: %v", err)
	}
	if _, err := store.EnrollDevice(ctx, Enrollment{
		VaultID:             rekeyTestVaultID,
		DeviceID:            device.id,
		SigningPublicKey:    device.pub,
		EncryptionPublicKey: bytes.Repeat([]byte{9}, 32),
		Challenge:           challenge,
		Label:               "Fictional device",
		DesktopDeviceID:     desktopID,
	}); err != nil {
		t.Fatalf("enroll %s: %v", device.id, err)
	}
}

func approveRekeyDevice(t *testing.T, store *Store, approver, device rekeyTestDevice) {
	t.Helper()
	ctx := context.Background()
	ciphertext := bytes.Repeat([]byte{2}, 32)
	signature := signKeyPackage(t, approver, syncproto.EncryptedKeyPackage{
		VaultID:           rekeyTestVaultID,
		SenderDeviceID:    approver.id,
		RecipientDeviceID: device.id,
		Ciphertext:        base64.RawURLEncoding.EncodeToString(ciphertext),
	})
	if _, err := store.ApproveDevice(ctx, KeyPackage{
		VaultID:            rekeyTestVaultID,
		ExpectedVaultEpoch: 1,
		SenderDeviceID:     approver.id,
		RecipientDeviceID:  device.id,
		Ciphertext:         ciphertext,
		Signature:          signature,
	}); err != nil {
		t.Fatalf("approve %s: %v", device.id, err)
	}
}

// The first device is auto-approved and activated. B and C are approved by A.
func seedRekeyVault(t *testing.T, store *Store, db *sql.DB) (rekeyTestDevice, rekeyTestDevice, rekeyTestDevice) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `INSERT INTO sesame_accounts (id, email, password_hash) VALUES ('acct-rekey', 'rekey-user@example.invalid', 'test')`); err != nil {
		t.Fatalf("create account: %v", err)
	}
	if _, err := store.EnsureVault(ctx, "acct-rekey", rekeyTestVaultID); err != nil {
		t.Fatalf("ensure vault: %v", err)
	}
	deviceA := newRekeyTestDevice(t, rekeyDeviceA)
	deviceB := newRekeyTestDevice(t, rekeyDeviceB)
	deviceC := newRekeyTestDevice(t, rekeyDeviceC)
	enrollRekeyDevice(t, store, deviceA, "desktopAAAAAAAAAA01")
	enrollRekeyDevice(t, store, deviceB, "desktopBBBBBBBBBB01")
	approveRekeyDevice(t, store, deviceA, deviceB)
	enrollRekeyDevice(t, store, deviceC, "desktopCCCCCCCCCC01")
	approveRekeyDevice(t, store, deviceA, deviceC)
	return deviceA, deviceB, deviceC
}

func rekeyTestEnvelope(t *testing.T, signer rekeyTestDevice, epoch uint64) Envelope {
	t.Helper()
	protocol := syncproto.Envelope{
		Version:          syncproto.Version,
		VaultID:          rekeyTestVaultID,
		DeviceID:         signer.id,
		Revision:         1,
		PreviousRevision: 0,
		VaultEpoch:       epoch,
		DeviceEpoch:      epoch,
		Operation:        "snapshot",
		Nonce:            base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{5}, syncproto.XChaChaNonceBytes)),
		Ciphertext:       base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{6}, 32)),
	}
	payload, err := protocol.SigningPayload()
	if err != nil {
		t.Fatalf("envelope payload: %v", err)
	}
	return Envelope{
		VaultID:          rekeyTestVaultID,
		Revision:         1,
		PreviousRevision: 0,
		DeviceID:         signer.id,
		VaultEpoch:       epoch,
		DeviceEpoch:      epoch,
		Operation:        "snapshot",
		Nonce:            bytes.Repeat([]byte{5}, syncproto.XChaChaNonceBytes),
		Ciphertext:       bytes.Repeat([]byte{6}, 32),
		Signature:        ed25519.Sign(signer.priv, payload),
	}
}

func rekeySurvivor(t *testing.T, initiator, recipient rekeyTestDevice) SurvivorPackage {
	t.Helper()
	ciphertext := bytes.Repeat([]byte{3}, 32)
	signature := signKeyPackage(t, initiator, syncproto.EncryptedKeyPackage{
		VaultID:           rekeyTestVaultID,
		SenderDeviceID:    initiator.id,
		RecipientDeviceID: recipient.id,
		Ciphertext:        base64.RawURLEncoding.EncodeToString(ciphertext),
	})
	return SurvivorPackage{RecipientDeviceID: recipient.id, Ciphertext: ciphertext, Signature: signature}
}

func TestRevokeAndRekeyRefusesAnIncompleteOrDuplicatedSurvivorSet(t *testing.T) {
	store, db := rekeyTestStore(t)
	deviceA, deviceB, deviceC := seedRekeyVault(t, store, db)
	ctx := context.Background()
	envelope := rekeyTestEnvelope(t, deviceA, 2)
	unknown := newRekeyTestDevice(t, "deviceDDDDDDDDDDD01")
	unknownSurvivor := rekeySurvivor(t, deviceA, unknown)

	cases := []struct {
		name      string
		survivors []SurvivorPackage
	}{
		{"a missing approved device", nil},
		{"the revoked device", []SurvivorPackage{rekeySurvivor(t, deviceA, deviceB), rekeySurvivor(t, deviceA, deviceC)}},
		{"a duplicate recipient", []SurvivorPackage{rekeySurvivor(t, deviceA, deviceB), rekeySurvivor(t, deviceA, deviceB)}},
		{"an unknown recipient", []SurvivorPackage{rekeySurvivor(t, deviceA, deviceB), unknownSurvivor}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := store.RevokeAndRekey(ctx, Rekey{
				VaultID:           rekeyTestVaultID,
				RevokedDeviceID:   deviceC.id,
				InitiatorDeviceID: deviceA.id,
				Envelope:          envelope,
				Survivors:         testCase.survivors,
			})
			if !errors.Is(err, ErrApprovalRejected) {
				t.Fatalf("rekey error = %v, want ErrApprovalRejected", err)
			}
		})
	}

	var epoch uint64
	if err := db.QueryRowContext(ctx, `SELECT vault_epoch FROM sesame_sync_vaults WHERE id = $1`, rekeyTestVaultID).Scan(&epoch); err != nil {
		t.Fatalf("read vault epoch: %v", err)
	}
	if epoch != 1 {
		t.Fatalf("vault epoch = %d, want 1 after every rejected rekey", epoch)
	}
	var envelopes int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sesame_sync_envelopes WHERE vault_id = $1`, rekeyTestVaultID).Scan(&envelopes); err != nil {
		t.Fatalf("count envelopes: %v", err)
	}
	if envelopes != 0 {
		t.Fatalf("stored envelopes = %d, want 0 after every rejected rekey", envelopes)
	}
	var revoked int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sesame_sync_devices WHERE id = $1 AND state = 'revoked'`, deviceC.id).Scan(&revoked); err != nil {
		t.Fatalf("count revoked devices: %v", err)
	}
	if revoked != 0 {
		t.Fatalf("revoked devices = %d, want 0 after a rejected rekey", revoked)
	}
}

func TestRevokeAndRekeyCoversEveryApprovedDevice(t *testing.T) {
	store, db := rekeyTestStore(t)
	deviceA, deviceB, deviceC := seedRekeyVault(t, store, db)
	ctx := context.Background()
	vault, err := store.RevokeAndRekey(ctx, Rekey{
		VaultID:           rekeyTestVaultID,
		RevokedDeviceID:   deviceC.id,
		InitiatorDeviceID: deviceA.id,
		Envelope:          rekeyTestEnvelope(t, deviceA, 2),
		Survivors:         []SurvivorPackage{rekeySurvivor(t, deviceA, deviceB)},
	})
	if err != nil {
		t.Fatalf("rekey: %v", err)
	}
	if vault.VaultEpoch != 2 {
		t.Fatalf("vault epoch = %d, want 2", vault.VaultEpoch)
	}
	devices, err := store.Devices(ctx, rekeyTestVaultID)
	if err != nil {
		t.Fatalf("list devices: %v", err)
	}
	byID := make(map[string]Device, len(devices))
	for _, device := range devices {
		byID[device.ID] = device
	}
	if survivor := byID[deviceB.id]; survivor.State != DeviceApproved || survivor.DeviceEpoch != 2 || survivor.ActivatedEpoch != 0 {
		t.Fatalf("survivor = %+v, want approved at epoch 2 and awaiting activation", survivor)
	}
	if revoked := byID[deviceC.id]; revoked.State != DeviceRevoked {
		t.Fatalf("revoked device state = %q, want revoked", revoked.State)
	}
	pkg, err := store.KeyPackageFor(ctx, rekeyTestVaultID, deviceB.id)
	if err != nil {
		t.Fatalf("read survivor key package: %v", err)
	}
	if pkg.SenderDeviceID != deviceA.id {
		t.Fatalf("survivor package sender = %q, want the initiator", pkg.SenderDeviceID)
	}
	var envelopes int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sesame_sync_envelopes WHERE vault_id = $1`, rekeyTestVaultID).Scan(&envelopes); err != nil {
		t.Fatalf("count envelopes: %v", err)
	}
	if envelopes != 1 {
		t.Fatalf("stored envelopes = %d, want 1", envelopes)
	}
}
