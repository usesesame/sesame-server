package syncproto

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"
)

// The revision chain, activation proofs, and acceptance receipts. Each
// envelope carries the digest of its predecessor inside the signed bytes, so
// the history is a chain.
func EnvelopeDigest(envelope Envelope) string {
	digest := sha256.New()
	digest.Write([]byte("sesame-sync-envelope-digest-v1"))
	for _, part := range [][]byte{
		[]byte(envelope.VaultID),
		[]byte(envelope.DeviceID),
		[]byte(itoa(uint64(envelope.Version))),
		[]byte(itoa(envelope.Revision)),
		[]byte(itoa(envelope.PreviousRevision)),
		[]byte(itoa(envelope.VaultEpoch)),
		[]byte(itoa(envelope.DeviceEpoch)),
		[]byte(envelope.Operation),
		[]byte(envelope.TombstoneID),
		[]byte(envelope.PreviousDigest),
		[]byte(envelope.Nonce),
		[]byte(envelope.Ciphertext),
		[]byte(envelope.Signature),
	} {
		var length [8]byte
		writeUint64(&length, uint64(len(part)))
		digest.Write(length[:])
		digest.Write(part)
	}
	return base64.RawURLEncoding.EncodeToString(digest.Sum(nil))
}

func writeUint64(out *[8]byte, value uint64) {
	for index := 7; index >= 0; index-- {
		out[index] = byte(value)
		value >>= 8
	}
}

func itoa(value uint64) string {
	if value == 0 {
		return "0"
	}
	var buffer [20]byte
	position := len(buffer)
	for value > 0 {
		position--
		buffer[position] = byte('0' + value%10)
		value /= 10
	}
	return string(buffer[position:])
}

// Two-phase activation proof: signs the exact package ciphertext and binds the
// epoch, so a device that never received the package cannot produce it and an
// old proof cannot be replayed after rotation.
func ActivationPayload(vaultID, deviceID string, deviceEpoch uint64, ciphertext string) ([]byte, error) {
	return json.Marshal(struct {
		VaultID     string `json:"vaultId"`
		DeviceID    string `json:"deviceId"`
		DeviceEpoch uint64 `json:"deviceEpoch"`
		Ciphertext  string `json:"ciphertext"`
	}{vaultID, deviceID, deviceEpoch, ciphertext})
}

func VerifyActivation(encodedPublicKey, vaultID, deviceID string, deviceEpoch uint64, ciphertext, proof string) bool {
	publicKey, err := decodeDeviceSigningKey(encodedPublicKey)
	if err != nil {
		return false
	}
	signature, err := base64.RawURLEncoding.DecodeString(proof)
	if err != nil || len(signature) != Ed25519SignatureBytes {
		return false
	}
	payload, err := ActivationPayload(vaultID, deviceID, deviceEpoch, ciphertext)
	if err != nil {
		return false
	}
	return ed25519.Verify(publicKey, payload, signature)
}

// Signed acceptance evidence a service serving an older head cannot reproduce.
type Receipt struct {
	VaultID    string `json:"vaultId"`
	Revision   uint64 `json:"revision"`
	Digest     string `json:"digest"`
	VaultEpoch uint64 `json:"vaultEpoch"`
	AcceptedAt string `json:"acceptedAt"`
}

func (receipt Receipt) signingPayload() ([]byte, error) {
	return json.Marshal(receipt)
}

func SignReceipt(key ed25519.PrivateKey, receipt Receipt) (string, string, error) {
	if len(key) != ed25519.PrivateKeySize {
		return "", "", errors.New("sync receipt signing key is invalid")
	}
	payload, err := receipt.signingPayload()
	if err != nil {
		return "", "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload),
		base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, payload)), nil
}

func VerifyReceipt(encodedPublicKey, encodedReceipt, signature string) (Receipt, error) {
	publicKey, err := decodeDeviceSigningKey(encodedPublicKey)
	if err != nil {
		return Receipt{}, errors.New("sync receipt key is invalid")
	}
	payload, err := base64.RawURLEncoding.DecodeString(encodedReceipt)
	if err != nil {
		return Receipt{}, errors.New("sync receipt is invalid")
	}
	raw, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil || len(raw) != Ed25519SignatureBytes || !ed25519.Verify(publicKey, payload, raw) {
		return Receipt{}, errors.New("sync receipt signature is invalid")
	}
	var receipt Receipt
	if err := json.Unmarshal(payload, &receipt); err != nil {
		return Receipt{}, errors.New("sync receipt is invalid")
	}
	if !opaqueID.MatchString(receipt.VaultID) || receipt.Digest == "" {
		return Receipt{}, errors.New("sync receipt is invalid")
	}
	if _, err := time.Parse(time.RFC3339, receipt.AcceptedAt); err != nil {
		return Receipt{}, errors.New("sync receipt is invalid")
	}
	return receipt, nil
}

// Signed removal authorisation; the epoch is bound in so a captured intent
// cannot be replayed after rotation.
func RevocationIntentPayload(vaultID, callerDeviceID, targetDeviceID string, callerEpoch uint64) ([]byte, error) {
	return json.Marshal(struct {
		VaultID        string `json:"vaultId"`
		CallerDeviceID string `json:"callerDeviceId"`
		TargetDeviceID string `json:"targetDeviceId"`
		CallerEpoch    uint64 `json:"callerEpoch"`
	}{vaultID, callerDeviceID, targetDeviceID, callerEpoch})
}

func VerifyRevocationIntent(encodedPublicKey, vaultID, callerDeviceID, targetDeviceID string, callerEpoch uint64, intent string) bool {
	publicKey, err := decodeDeviceSigningKey(encodedPublicKey)
	if err != nil {
		return false
	}
	signature, err := base64.RawURLEncoding.DecodeString(intent)
	if err != nil || len(signature) != Ed25519SignatureBytes {
		return false
	}
	payload, err := RevocationIntentPayload(vaultID, callerDeviceID, targetDeviceID, callerEpoch)
	if err != nil {
		return false
	}
	return ed25519.Verify(publicKey, payload, signature)
}

// The canonical snapshot AAD. The service never encrypts or decrypts, so this
// exists for the cross-language fixture contract, not runtime use.
func SnapshotAAD(vaultID, deviceID string, revision, previousRevision, vaultEpoch, deviceEpoch uint64, operation string) []byte {
	aad := make([]byte, 0, 128)
	aad = append(aad, "sesame-sync-snapshot-v2"...)
	for _, part := range [][]byte{
		[]byte(vaultID),
		[]byte(deviceID),
		[]byte(itoa(uint64(Version))),
		[]byte(itoa(revision)),
		[]byte(itoa(previousRevision)),
		[]byte(itoa(vaultEpoch)),
		[]byte(itoa(deviceEpoch)),
		[]byte(operation),
	} {
		var length [8]byte
		writeUint64(&length, uint64(len(part)))
		aad = append(aad, length[:]...)
		aad = append(aad, part...)
	}
	return aad
}

func SnapshotAADFor(envelope Envelope) []byte {
	return SnapshotAAD(
		envelope.VaultID, envelope.DeviceID,
		envelope.Revision, envelope.PreviousRevision,
		envelope.VaultEpoch, envelope.DeviceEpoch,
		envelope.Operation,
	)
}
