package syncproto

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The Go half of the cross-language framing check. Signatures cover a specific
// JSON encoding, not a logical structure, so this makes the encoding itself the
// thing under test.

type syncFixture struct {
	Input struct {
		VaultID          string `json:"vaultId"`
		DeviceID         string `json:"deviceId"`
		Revision         uint64 `json:"revision"`
		PreviousRevision uint64 `json:"previousRevision"`
		VaultEpoch       uint64 `json:"vaultEpoch"`
		DeviceEpoch      uint64 `json:"deviceEpoch"`
		NonceByte        byte   `json:"nonceByte"`
		NonceLength      int    `json:"nonceLength"`
		CiphertextUTF8   string `json:"ciphertextUtf8"`
		TombstoneID      string `json:"tombstoneId"`
		PreviousDigest   string `json:"previousDigest"`
	} `json:"input"`
	SnapshotSigningPayload string `json:"snapshotSigningPayload"`
	RustSignedSnapshot     struct {
		VerifyingKey string `json:"verifyingKey"`
		Signature    string `json:"signature"`
	} `json:"rustSignedSnapshot"`
}

func loadSyncFixture(t *testing.T) syncFixture {
	t.Helper()
	path := filepath.Join("testdata", "envelope-signing-payload.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read sync signing fixture: %v", err)
	}
	var fixture syncFixture
	if err := json.Unmarshal(body, &fixture); err != nil {
		t.Fatalf("parse sync signing fixture: %v", err)
	}
	return fixture
}

func (f syncFixture) envelope(operation, tombstoneID string) Envelope {
	nonce := make([]byte, f.Input.NonceLength)
	for i := range nonce {
		nonce[i] = f.Input.NonceByte
	}
	return Envelope{
		Version:          Version,
		VaultID:          f.Input.VaultID,
		DeviceID:         f.Input.DeviceID,
		Revision:         f.Input.Revision,
		PreviousRevision: f.Input.PreviousRevision,
		VaultEpoch:       f.Input.VaultEpoch,
		DeviceEpoch:      f.Input.DeviceEpoch,
		Operation:        operation,
		TombstoneID:      tombstoneID,
		PreviousDigest:   f.Input.PreviousDigest,
		Nonce:            base64.RawURLEncoding.EncodeToString(nonce),
		Ciphertext:       base64.RawURLEncoding.EncodeToString([]byte(f.Input.CiphertextUTF8)),
	}
}

func TestSigningPayloadMatchesTheCrossLanguageFixture(t *testing.T) {
	fixture := loadSyncFixture(t)

	snapshot, err := fixture.envelope("snapshot", "").signingPayload()
	if err != nil {
		t.Fatalf("snapshot signing payload: %v", err)
	}
	if string(snapshot) != fixture.SnapshotSigningPayload {
		t.Errorf("snapshot signing payload drifted from the shared fixture.\n got: %s\nwant: %s",
			snapshot, fixture.SnapshotSigningPayload)
	}

}

func TestASignatureFromTheRustClientVerifiesHere(t *testing.T) {
	fixture := loadSyncFixture(t)
	envelope := fixture.envelope("snapshot", "")
	envelope.Signature = fixture.RustSignedSnapshot.Signature

	if err := envelope.VerifySignature(fixture.RustSignedSnapshot.VerifyingKey); err != nil {
		t.Fatalf("a Rust-signed envelope failed verification in Go: %v", err)
	}

	tampered := envelope
	tampered.Revision = 5
	tampered.PreviousRevision = 4
	if err := tampered.VerifySignature(fixture.RustSignedSnapshot.VerifyingKey); err == nil {
		t.Fatal("a tampered revision verified, so the signature is not binding the routing fields")
	}
}

func TestEmptyTombstoneIDIsOmittedFromTheSignedPayload(t *testing.T) {
	fixture := loadSyncFixture(t)
	payload, err := fixture.envelope("snapshot", "").signingPayload()
	if err != nil {
		t.Fatalf("signing payload: %v", err)
	}
	if got := string(payload); contains(got, "tombstoneId") {
		t.Errorf("an empty tombstoneId must not appear in the signed payload: %s", got)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	}()
}
