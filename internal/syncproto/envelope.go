// Package syncproto defines the ciphertext-only Sync envelope. It has no
// vault model, so framing can be validated without reading vault data.
package syncproto

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"regexp"
)

const (
	Version               = 2
	MaxCiphertextBytes    = 10 * 1024 * 1024
	MaxEnvelopeBytes      = MaxCiphertextBytes * 2
	XChaChaNonceBytes     = 24
	Ed25519SignatureBytes = 64
)

var opaqueID = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

// An opaque, signed snapshot; the server must never decrypt it or accept
// plaintext fields alongside it.
type Envelope struct {
	Version          int    `json:"version"`
	VaultID          string `json:"vaultId"`
	DeviceID         string `json:"deviceId"`
	Revision         uint64 `json:"revision"`
	PreviousRevision uint64 `json:"previousRevision"`
	VaultEpoch       uint64 `json:"vaultEpoch"`
	DeviceEpoch      uint64 `json:"deviceEpoch"`
	Operation        string `json:"operation"`
	TombstoneID      string `json:"tombstoneId,omitempty"`
	// Inside the signed payload, so the service cannot rewrite the chain it stores.
	PreviousDigest string `json:"previousDigest"`
	Nonce          string `json:"nonce"`
	Ciphertext     string `json:"ciphertext"`
	Signature      string `json:"signature"`
}

// Binds the opaque ciphertext and every routing and epoch field to the device
// signing key without decrypting anything.
func (envelope Envelope) VerifySignature(encodedPublicKey string) error {
	if err := envelope.Validate(); err != nil {
		return err
	}
	publicKey, err := decodeDeviceSigningKey(encodedPublicKey)
	if err != nil {
		return err
	}
	signature, _ := base64.RawURLEncoding.DecodeString(envelope.Signature)
	payload, err := envelope.signingPayload()
	if err != nil || !ed25519.Verify(publicKey, payload, signature) {
		return errors.New("sync envelope signature is invalid")
	}
	return nil
}

func (envelope Envelope) SigningPayload() ([]byte, error) {
	return envelope.signingPayload()
}

func (envelope Envelope) signingPayload() ([]byte, error) {
	return json.Marshal(struct {
		Version          int    `json:"version"`
		VaultID          string `json:"vaultId"`
		DeviceID         string `json:"deviceId"`
		Revision         uint64 `json:"revision"`
		PreviousRevision uint64 `json:"previousRevision"`
		VaultEpoch       uint64 `json:"vaultEpoch"`
		DeviceEpoch      uint64 `json:"deviceEpoch"`
		Operation        string `json:"operation"`
		TombstoneID      string `json:"tombstoneId,omitempty"`
		PreviousDigest   string `json:"previousDigest"`
		Nonce            string `json:"nonce"`
		Ciphertext       string `json:"ciphertext"`
	}{envelope.Version, envelope.VaultID, envelope.DeviceID, envelope.Revision, envelope.PreviousRevision, envelope.VaultEpoch, envelope.DeviceEpoch, envelope.Operation, envelope.TombstoneID, envelope.PreviousDigest, envelope.Nonce, envelope.Ciphertext})
}

func Decode(reader io.Reader) (Envelope, error) {
	limited := io.LimitReader(reader, MaxEnvelopeBytes+1)
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	var envelope Envelope
	if err := decoder.Decode(&envelope); err != nil {
		return Envelope{}, errors.New("sync envelope could not be read")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Envelope{}, errors.New("sync envelope could not be read")
	}
	if err := envelope.Validate(); err != nil {
		return Envelope{}, err
	}
	return envelope, nil
}

func (envelope Envelope) Validate() error {
	if envelope.Version != Version {
		return errors.New("unsupported sync protocol version")
	}
	if !opaqueID.MatchString(envelope.VaultID) || !opaqueID.MatchString(envelope.DeviceID) {
		return errors.New("sync identifiers are invalid")
	}
	if envelope.Revision == 0 || envelope.PreviousRevision+1 != envelope.Revision || envelope.VaultEpoch == 0 || envelope.DeviceEpoch == 0 {
		return errors.New("sync revision is invalid")
	}
	// Tombstones are removed from the protocol; the field stays reserved and
	// must be empty, so an older client that sets it is refused rather than silently misunderstood.
	if envelope.Operation != "snapshot" {
		return errors.New("sync operation is invalid")
	}
	if envelope.TombstoneID != "" {
		return errors.New("sync tombstone is invalid")
	}
	nonce, err := base64.RawURLEncoding.DecodeString(envelope.Nonce)
	if err != nil || len(nonce) != XChaChaNonceBytes {
		return errors.New("sync nonce is invalid")
	}
	ciphertext, err := base64.RawURLEncoding.DecodeString(envelope.Ciphertext)
	if err != nil || len(ciphertext) < 16 || len(ciphertext) > MaxCiphertextBytes {
		return errors.New("sync ciphertext is invalid")
	}
	signature, err := base64.RawURLEncoding.DecodeString(envelope.Signature)
	if err != nil || len(signature) != Ed25519SignatureBytes {
		return errors.New("sync signature is invalid")
	}
	if envelope.Revision == 1 {
		if envelope.PreviousDigest != "" {
			return errors.New("sync revision chain is invalid")
		}
	} else {
		digest, err := base64.RawURLEncoding.DecodeString(envelope.PreviousDigest)
		if err != nil || len(digest) != sha256.Size {
			return errors.New("sync revision chain is invalid")
		}
	}
	return nil
}

// A stale device cannot overwrite a later snapshot, a removal epoch, or a recovered vault epoch.
func (envelope Envelope) CompareAndSwap(currentRevision, vaultEpoch, deviceEpoch uint64) error {
	if err := envelope.Validate(); err != nil {
		return err
	}
	if envelope.PreviousRevision != currentRevision || envelope.VaultEpoch != vaultEpoch || envelope.DeviceEpoch != deviceEpoch {
		return errors.New("sync compare-and-swap conflict")
	}
	return nil
}
