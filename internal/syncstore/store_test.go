package syncstore

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"usesesame.app/backend/internal/syncproto"
)

type testEnvelopeSpec struct {
	signer            rekeyTestDevice
	revision          uint64
	previousDigest    string
	vaultEpoch        uint64
	deviceEpoch       uint64
	ciphertextByte    byte
	signWithDifferent *rekeyTestDevice
}

func buildTestEnvelope(t *testing.T, spec testEnvelopeSpec) Envelope {
	t.Helper()
	if spec.vaultEpoch == 0 {
		spec.vaultEpoch = 1
	}
	if spec.deviceEpoch == 0 {
		spec.deviceEpoch = spec.vaultEpoch
	}
	if spec.ciphertextByte == 0 {
		spec.ciphertextByte = 6
	}
	nonce := bytes.Repeat([]byte{5}, syncproto.XChaChaNonceBytes)
	ciphertext := bytes.Repeat([]byte{spec.ciphertextByte}, 32)
	protocol := syncproto.Envelope{
		Version:          syncproto.Version,
		VaultID:          rekeyTestVaultID,
		DeviceID:         spec.signer.id,
		Revision:         spec.revision,
		PreviousRevision: spec.revision - 1,
		VaultEpoch:       spec.vaultEpoch,
		DeviceEpoch:      spec.deviceEpoch,
		Operation:        "snapshot",
		PreviousDigest:   spec.previousDigest,
		Nonce:            base64.RawURLEncoding.EncodeToString(nonce),
		Ciphertext:       base64.RawURLEncoding.EncodeToString(ciphertext),
	}
	payload, err := protocol.SigningPayload()
	if err != nil {
		t.Fatalf("envelope payload: %v", err)
	}
	signingKey := spec.signer.priv
	if spec.signWithDifferent != nil {
		signingKey = spec.signWithDifferent.priv
	}
	return Envelope{
		VaultID:          rekeyTestVaultID,
		Revision:         spec.revision,
		PreviousRevision: spec.revision - 1,
		DeviceID:         spec.signer.id,
		VaultEpoch:       spec.vaultEpoch,
		DeviceEpoch:      spec.deviceEpoch,
		Operation:        "snapshot",
		PreviousDigest:   spec.previousDigest,
		Nonce:            nonce,
		Ciphertext:       ciphertext,
		Signature:        ed25519.Sign(signingKey, payload),
	}
}

func countRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	return count
}

func markDeviceRevoked(t *testing.T, db *sql.DB, deviceID string, revokedAt time.Time) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `
		UPDATE sesame_sync_devices SET state = 'revoked', revoked_at = $2 WHERE id = $1
	`, deviceID, revokedAt); err != nil {
		t.Fatalf("mark device revoked: %v", err)
	}
}

func insertChallenge(t *testing.T, db *sql.DB, seed byte, expiresAt time.Time, consumedAt *time.Time) []byte {
	t.Helper()
	value := bytes.Repeat([]byte{seed}, 32)
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO sesame_sync_challenges (value, vault_id, expires_at, consumed_at) VALUES ($1, $2, $3, $4)
	`, value, rekeyTestVaultID, expiresAt, consumedAt); err != nil {
		t.Fatalf("insert challenge: %v", err)
	}
	return value
}

func TestPurgeExpiredChallengesRemovesOnlyExpiredAndConsumedChallenges(t *testing.T) {
	store, db := rekeyTestStore(t)
	seedRekeyVault(t, store, db)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `DELETE FROM sesame_sync_challenges`); err != nil {
		t.Fatalf("clear challenges: %v", err)
	}
	consumedAt := time.Now().Add(-time.Minute)
	expired := insertChallenge(t, db, 1, time.Now().Add(-time.Hour), nil)
	consumed := insertChallenge(t, db, 2, time.Now().Add(time.Hour), &consumedAt)
	live := insertChallenge(t, db, 3, time.Now().Add(time.Hour), nil)

	removed, err := store.PurgeExpiredChallenges(ctx)
	if err != nil {
		t.Fatalf("purge challenges: %v", err)
	}
	if removed != 2 {
		t.Fatalf("removed = %d, want 2", removed)
	}
	for name, value := range map[string][]byte{"expired": expired, "consumed": consumed} {
		if countRows(t, db, `SELECT COUNT(*) FROM sesame_sync_challenges WHERE value = $1`, value) != 0 {
			t.Fatalf("%s challenge survived the purge", name)
		}
	}
	if countRows(t, db, `SELECT COUNT(*) FROM sesame_sync_challenges WHERE value = $1`, live) != 1 {
		t.Fatal("the live challenge was removed")
	}

	removed, err = store.PurgeExpiredChallenges(ctx)
	if err != nil || removed != 0 {
		t.Fatalf("second purge = %d, %v, want 0 and no error", removed, err)
	}
}

func TestEnrollDeviceRefusesExpiredUnknownAndReusedChallenges(t *testing.T) {
	store, db := rekeyTestStore(t)
	seedRekeyVault(t, store, db)
	ctx := context.Background()
	newcomer := newRekeyTestDevice(t, "deviceEEEEEEEEEEE01")
	enrollment := func(challenge []byte, desktopID string) Enrollment {
		return Enrollment{
			VaultID:             rekeyTestVaultID,
			DeviceID:            newcomer.id,
			SigningPublicKey:    newcomer.pub,
			EncryptionPublicKey: bytes.Repeat([]byte{9}, 32),
			Challenge:           challenge,
			Label:               "Fictional device",
			DesktopDeviceID:     desktopID,
		}
	}

	expired := insertChallenge(t, db, 11, time.Now().Add(-time.Second), nil)
	if _, err := store.EnrollDevice(ctx, enrollment(expired, "desktopEEEEEEEEEE01")); !errors.Is(err, ErrChallengeUnusable) {
		t.Fatalf("expired challenge error = %v, want ErrChallengeUnusable", err)
	}
	unknown := bytes.Repeat([]byte{12}, 32)
	if _, err := store.EnrollDevice(ctx, enrollment(unknown, "desktopEEEEEEEEEE01")); !errors.Is(err, ErrChallengeUnusable) {
		t.Fatalf("unknown challenge error = %v, want ErrChallengeUnusable", err)
	}
	if _, err := store.EnrollDevice(ctx, enrollment(nil, "")); !errors.Is(err, ErrDesktopBindingRequired) {
		t.Fatalf("missing desktop binding error = %v, want ErrDesktopBindingRequired", err)
	}

	challenge, err := store.IssueChallenge(ctx, rekeyTestVaultID)
	if err != nil {
		t.Fatalf("issue challenge: %v", err)
	}
	device, err := store.EnrollDevice(ctx, enrollment(challenge, "desktopEEEEEEEEEE01"))
	if err != nil {
		t.Fatalf("enroll with a live challenge: %v", err)
	}
	if device.State != DevicePending {
		t.Fatalf("enrolled device state = %q, want pending because the vault already has devices", device.State)
	}
	other := newRekeyTestDevice(t, "deviceFFFFFFFFFFF01")
	reuse := enrollment(challenge, "desktopFFFFFFFFFF01")
	reuse.DeviceID, reuse.SigningPublicKey = other.id, other.pub
	if _, err := store.EnrollDevice(ctx, reuse); !errors.Is(err, ErrChallengeUnusable) {
		t.Fatalf("reused challenge error = %v, want ErrChallengeUnusable", err)
	}
	if countRows(t, db, `SELECT COUNT(*) FROM sesame_sync_devices WHERE id = $1`, other.id) != 0 {
		t.Fatal("a reused challenge still created a device")
	}
}

func TestAppendEnvelopeChainsRevisionsAndRefusesConflicts(t *testing.T) {
	store, db := rekeyTestStore(t)
	deviceA, _, _ := seedRekeyVault(t, store, db)
	ctx := context.Background()

	if _, accepted, err := store.AppendEnvelopeAccepted(ctx, buildTestEnvelope(t, testEnvelopeSpec{signer: deviceA, revision: 1})); err != nil {
		t.Fatalf("append revision 1: %v", err)
	} else if accepted.Digest == "" {
		t.Fatal("the accepted envelope carries no digest")
	}

	latest, err := store.LatestEnvelope(ctx, rekeyTestVaultID)
	if err != nil {
		t.Fatalf("latest envelope: %v", err)
	}
	if latest.Revision != 1 || latest.Digest == "" || latest.DeviceID != deviceA.id {
		t.Fatalf("latest envelope = revision %d digest %q device %q", latest.Revision, latest.Digest, latest.DeviceID)
	}

	second := buildTestEnvelope(t, testEnvelopeSpec{signer: deviceA, revision: 2, previousDigest: latest.Digest, ciphertextByte: 7})
	appended, err := store.AppendEnvelope(ctx, second)
	if err != nil {
		t.Fatalf("append revision 2: %v", err)
	}
	if appended.CurrentRevision != 2 {
		t.Fatalf("vault revision = %d, want 2", appended.CurrentRevision)
	}

	stale := buildTestEnvelope(t, testEnvelopeSpec{signer: deviceA, revision: 2, previousDigest: latest.Digest, ciphertextByte: 8})
	if _, err := store.AppendEnvelope(ctx, stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale revision error = %v, want ErrConflict", err)
	}

	wrongChain := buildTestEnvelope(t, testEnvelopeSpec{signer: deviceA, revision: 3, previousDigest: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)), ciphertextByte: 9})
	if _, err := store.AppendEnvelope(ctx, wrongChain); !errors.Is(err, ErrConflict) {
		t.Fatalf("mismatched chain error = %v, want ErrConflict", err)
	}

	skipped := buildTestEnvelope(t, testEnvelopeSpec{signer: deviceA, revision: 4, previousDigest: latest.Digest})
	skipped.PreviousRevision = 2
	if _, err := store.AppendEnvelope(ctx, skipped); !errors.Is(err, ErrConflict) {
		t.Fatalf("revision gap error = %v, want ErrConflict", err)
	}

	if got := countRows(t, db, `SELECT COUNT(*) FROM sesame_sync_envelopes WHERE vault_id = $1`, rekeyTestVaultID); got != 2 {
		t.Fatalf("stored envelopes = %d, want 2 after the refused uploads", got)
	}
	vaultRow, err := store.VaultForAccount(ctx, "acct-rekey")
	if err != nil || vaultRow.CurrentRevision != 2 {
		t.Fatalf("vault after conflicts = %+v, %v, want revision 2", vaultRow, err)
	}
}

func TestAppendEnvelopeRefusesUnauthorizedWriters(t *testing.T) {
	store, db := rekeyTestStore(t)
	deviceA, deviceB, _ := seedRekeyVault(t, store, db)
	ctx := context.Background()

	forged := buildTestEnvelope(t, testEnvelopeSpec{signer: deviceA, revision: 1, signWithDifferent: &deviceB})
	if _, err := store.AppendEnvelope(ctx, forged); !errors.Is(err, ErrApprovalRejected) {
		t.Fatalf("envelope signed by another device error = %v, want ErrApprovalRejected", err)
	}

	notActivated := buildTestEnvelope(t, testEnvelopeSpec{signer: deviceB, revision: 1})
	if _, err := store.AppendEnvelope(ctx, notActivated); !errors.Is(err, ErrApprovalRejected) {
		t.Fatalf("pending activation error = %v, want ErrApprovalRejected", err)
	}

	unknown := newRekeyTestDevice(t, "deviceGGGGGGGGGGG01")
	if _, err := store.AppendEnvelope(ctx, buildTestEnvelope(t, testEnvelopeSpec{signer: unknown, revision: 1})); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown device error = %v, want ErrNotFound", err)
	}

	staleEpoch := buildTestEnvelope(t, testEnvelopeSpec{signer: deviceA, revision: 1, vaultEpoch: 2, deviceEpoch: 2})
	if _, err := store.AppendEnvelope(ctx, staleEpoch); !errors.Is(err, ErrConflict) {
		t.Fatalf("epoch ahead of the vault error = %v, want ErrConflict", err)
	}

	if got := countRows(t, db, `SELECT COUNT(*) FROM sesame_sync_envelopes`); got != 0 {
		t.Fatalf("stored envelopes = %d, want 0 after every refused upload", got)
	}
}

func TestLeaveVaultRevokesTheDeviceAndStopsItsWrites(t *testing.T) {
	store, db := rekeyTestStore(t)
	deviceA, deviceB, _ := seedRekeyVault(t, store, db)
	ctx := context.Background()

	if err := store.LeaveVault(ctx, rekeyTestVaultID, deviceB.id); err != nil {
		t.Fatalf("leave vault: %v", err)
	}
	devices, err := store.Devices(ctx, rekeyTestVaultID)
	if err != nil {
		t.Fatalf("list devices: %v", err)
	}
	states := make(map[string]Device, len(devices))
	for _, device := range devices {
		states[device.ID] = device
	}
	if left := states[deviceB.id]; left.State != DeviceRevoked || left.RevokedAt == nil {
		t.Fatalf("departed device = %+v, want revoked with a revocation time", left)
	}
	if kept := states[deviceA.id]; kept.State != DeviceApproved || kept.DeviceEpoch != 2 || kept.ActivatedEpoch != 2 {
		t.Fatalf("remaining device = %+v, want approved and carried to epoch 2", kept)
	}
	if _, err := store.KeyPackageFor(ctx, rekeyTestVaultID, deviceB.id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked device key package error = %v, want ErrNotFound", err)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM sesame_sync_audit WHERE device_id = $1 AND action = 'device_revoked'`, deviceB.id); got != 1 {
		t.Fatalf("revocation audit rows = %d, want 1", got)
	}

	if _, err := store.AppendEnvelope(ctx, buildTestEnvelope(t, testEnvelopeSpec{signer: deviceB, revision: 1, vaultEpoch: 2, deviceEpoch: 2})); !errors.Is(err, ErrApprovalRejected) {
		t.Fatalf("revoked device upload error = %v, want ErrApprovalRejected", err)
	}
	if _, err := store.AppendEnvelope(ctx, buildTestEnvelope(t, testEnvelopeSpec{signer: deviceA, revision: 1})); !errors.Is(err, ErrConflict) {
		t.Fatalf("upload under the pre-revocation epoch error = %v, want ErrConflict", err)
	}
	if _, err := store.AppendEnvelope(ctx, buildTestEnvelope(t, testEnvelopeSpec{signer: deviceA, revision: 1, vaultEpoch: 2, deviceEpoch: 2})); err != nil {
		t.Fatalf("remaining device upload at the new epoch: %v", err)
	}
	if err := store.LeaveVault(ctx, rekeyTestVaultID, deviceB.id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second leave error = %v, want ErrNotFound", err)
	}
}

func appendOwnedEnvelope(t *testing.T, store *Store, signer rekeyTestDevice) {
	t.Helper()
	if _, err := store.AppendEnvelope(context.Background(), buildTestEnvelope(t, testEnvelopeSpec{signer: signer, revision: 1})); err != nil {
		t.Fatalf("append envelope for %s: %v", signer.id, err)
	}
}

func TestPurgeRevokedDevicesKeepsDevicesThatRetainedEnvelopesReference(t *testing.T) {
	store, db := rekeyTestStore(t)
	deviceA, deviceB, deviceC := seedRekeyVault(t, store, db)
	ctx := context.Background()
	appendOwnedEnvelope(t, store, deviceA)
	recent := newRekeyTestDevice(t, "deviceHHHHHHHHHHH01")
	enrollRekeyDevice(t, store, recent, "desktopHHHHHHHHHH01")

	old := time.Now().Add(-RevokedDeviceRetention - 24*time.Hour)
	markDeviceRevoked(t, db, deviceA.id, old)
	markDeviceRevoked(t, db, deviceB.id, old)
	markDeviceRevoked(t, db, recent.id, time.Now().Add(-time.Hour))

	removed, err := store.PurgeRevokedDevices(ctx, RevokedDeviceRetention)
	if err != nil {
		t.Fatalf("purge revoked devices: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want only the revoked device with no envelope", removed)
	}
	exists := func(id string) bool {
		return countRows(t, db, `SELECT COUNT(*) FROM sesame_sync_devices WHERE id = $1`, id) == 1
	}
	if exists(deviceB.id) {
		t.Fatal("a revoked device past retention with no envelope was kept")
	}
	if !exists(deviceA.id) {
		t.Fatal("a revoked device that authored a retained envelope was deleted")
	}
	if !exists(recent.id) {
		t.Fatal("a device revoked inside the retention window was deleted")
	}
	if !exists(deviceC.id) {
		t.Fatal("an approved device was deleted")
	}
	if countRows(t, db, `SELECT COUNT(*) FROM sesame_sync_envelopes WHERE device_id = $1`, deviceA.id) != 1 {
		t.Fatal("the retained envelope was removed")
	}

	removed, err = store.PurgeRevokedDevices(ctx, RevokedDeviceRetention)
	if err != nil || removed != 0 {
		t.Fatalf("second purge = %d, %v, want 0 and no error", removed, err)
	}
}

func TestPurgeRevokedDevicesRemovesADeviceOnceItsEnvelopesAreGone(t *testing.T) {
	store, db := rekeyTestStore(t)
	deviceA, _, _ := seedRekeyVault(t, store, db)
	ctx := context.Background()
	appendOwnedEnvelope(t, store, deviceA)
	markDeviceRevoked(t, db, deviceA.id, time.Now().Add(-RevokedDeviceRetention-time.Hour))

	if removed, err := store.PurgeRevokedDevices(ctx, RevokedDeviceRetention); err != nil || removed != 0 {
		t.Fatalf("purge with a retained envelope = %d, %v, want 0 and no error", removed, err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM sesame_sync_envelopes WHERE device_id = $1`, deviceA.id); err != nil {
		t.Fatalf("prune envelope: %v", err)
	}
	removed, err := store.PurgeRevokedDevices(ctx, RevokedDeviceRetention)
	if err != nil || removed != 1 {
		t.Fatalf("purge after the envelope was pruned = %d, %v, want 1 and no error", removed, err)
	}
}

func TestPurgeRevokedDevicesStopsWhenTheContextIsCancelled(t *testing.T) {
	store, db := rekeyTestStore(t)
	_, deviceB, _ := seedRekeyVault(t, store, db)
	markDeviceRevoked(t, db, deviceB.id, time.Now().Add(-RevokedDeviceRetention-time.Hour))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := store.PurgeRevokedDevices(ctx, RevokedDeviceRetention); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled purge error = %v, want context.Canceled", err)
	}
	if countRows(t, db, `SELECT COUNT(*) FROM sesame_sync_devices WHERE id = $1`, deviceB.id) != 1 {
		t.Fatal("a cancelled purge deleted a device")
	}
}

func TestPurgeRevokedDevicesContinuesPastADeviceThatCannotBeDeleted(t *testing.T) {
	store, db := rekeyTestStore(t)
	_, deviceB, deviceC := seedRekeyVault(t, store, db)
	ctx := context.Background()
	old := time.Now().Add(-RevokedDeviceRetention - time.Hour)
	markDeviceRevoked(t, db, deviceB.id, old.Add(-time.Hour))
	markDeviceRevoked(t, db, deviceC.id, old)
	if _, err := db.ExecContext(ctx, `
		CREATE FUNCTION sesame_test_block_device_delete() RETURNS TRIGGER AS $$
		BEGIN
		  IF OLD.id = '`+deviceB.id+`' THEN
		    RAISE EXCEPTION 'blocked test device';
		  END IF;
		  RETURN OLD;
		END;
		$$ LANGUAGE plpgsql;
		CREATE TRIGGER sesame_test_block_device_delete BEFORE DELETE ON sesame_sync_devices
		FOR EACH ROW EXECUTE FUNCTION sesame_test_block_device_delete();
	`); err != nil {
		t.Fatalf("install blocking trigger: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `
			DROP TRIGGER IF EXISTS sesame_test_block_device_delete ON sesame_sync_devices;
			DROP FUNCTION IF EXISTS sesame_test_block_device_delete();
		`)
	})

	removed, err := store.PurgeRevokedDevices(ctx, RevokedDeviceRetention)
	if err == nil {
		t.Fatal("a device the database refused to delete was not reported")
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1 because the later device must still be purged", removed)
	}
	if countRows(t, db, `SELECT COUNT(*) FROM sesame_sync_devices WHERE id = $1`, deviceB.id) != 1 {
		t.Fatal("the blocked device was deleted")
	}
	if countRows(t, db, `SELECT COUNT(*) FROM sesame_sync_devices WHERE id = $1`, deviceC.id) != 0 {
		t.Fatal("a failure on one device stopped the purge of the next")
	}
}
