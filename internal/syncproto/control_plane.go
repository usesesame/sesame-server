package syncproto

import (
	"crypto/ed25519"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"
)

const (
	Ed25519PublicKeyBytes       = 32
	X25519PublicKeyBytes        = 32
	MaxEncryptedKeyPackageBytes = 64 * 1024
	MaxDevicesPerVault          = 10
	MaxSnapshotsPerVault        = 100
	MaxStoredBytesPerVault      = 128 * 1024 * 1024
)

type DeviceState string

const (
	DevicePending  DeviceState = "pending"
	DeviceApproved DeviceState = "approved"
	DeviceRevoked  DeviceState = "revoked"
)

// Public routing information only; the wrapped key package is opaque and
// never decrypted by a Sync service.
type DeviceEnrollment struct {
	VaultID             string
	DeviceID            string
	SigningPublicKey    string
	EncryptionPublicKey string
	Challenge           string
	Proof               string
	CreatedAt           time.Time
}

type EnrollmentChallenge struct {
	VaultID   string
	Value     string
	ExpiresAt time.Time
	used      bool
}

// Marking it used prevents a captured enrollment proof from being replayed
// into a second approval attempt.
func (challenge *EnrollmentChallenge) Consume(input DeviceEnrollment, now time.Time) error {
	if challenge == nil || !opaqueID.MatchString(challenge.VaultID) || challenge.VaultID != input.VaultID || challenge.used || challenge.ExpiresAt.IsZero() || !now.Before(challenge.ExpiresAt) {
		return errors.New("sync enrollment challenge is invalid")
	}
	if err := input.Validate(); err != nil {
		return err
	}
	expected, err := base64.RawURLEncoding.DecodeString(challenge.Value)
	if err != nil || len(expected) != 32 {
		return errors.New("sync enrollment challenge is invalid")
	}
	presented, err := base64.RawURLEncoding.DecodeString(input.Challenge)
	if err != nil || subtle.ConstantTimeCompare(expected, presented) != 1 {
		return errors.New("sync enrollment challenge is invalid")
	}
	challenge.used = true
	return nil
}

func (input DeviceEnrollment) Validate() error {
	if !opaqueID.MatchString(input.VaultID) || !opaqueID.MatchString(input.DeviceID) || input.CreatedAt.IsZero() {
		return errors.New("sync device enrollment is invalid")
	}
	publicKey, err := decodeDeviceSigningKey(input.SigningPublicKey)
	if err != nil {
		return err
	}
	encryptionKey, err := base64.RawURLEncoding.DecodeString(input.EncryptionPublicKey)
	if err != nil || len(encryptionKey) != X25519PublicKeyBytes {
		return errors.New("sync device encryption key is invalid")
	}
	challenge, err := base64.RawURLEncoding.DecodeString(input.Challenge)
	if err != nil || len(challenge) != 32 {
		return errors.New("sync enrollment challenge is invalid")
	}
	proof, err := base64.RawURLEncoding.DecodeString(input.Proof)
	if err != nil || len(proof) != Ed25519SignatureBytes {
		return errors.New("sync enrollment proof is invalid")
	}
	payload, err := input.signingPayload()
	if err != nil || !ed25519.Verify(publicKey, payload, proof) {
		return errors.New("sync enrollment proof is invalid")
	}
	return nil
}

// CreatedAt is deliberately absent: the handler stamps it on arrival, so a
// signer could never know the value it was supposed to sign.
func (input DeviceEnrollment) signingPayload() ([]byte, error) {
	return json.Marshal(struct {
		VaultID             string `json:"vaultId"`
		DeviceID            string `json:"deviceId"`
		SigningPublicKey    string `json:"signingPublicKey"`
		EncryptionPublicKey string `json:"encryptionPublicKey"`
		Challenge           string `json:"challenge"`
	}{input.VaultID, input.DeviceID, input.SigningPublicKey, input.EncryptionPublicKey, input.Challenge})
}

func decodeDeviceSigningKey(value string) (ed25519.PublicKey, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) != Ed25519PublicKeyBytes {
		return nil, errors.New("sync device signing key is invalid")
	}
	return ed25519.PublicKey(decoded), nil
}

type EncryptedKeyPackage struct {
	VaultID, SenderDeviceID, RecipientDeviceID, Ciphertext, Signature string
	CreatedAt                                                         time.Time
}

func (key EncryptedKeyPackage) Validate() error {
	if !opaqueID.MatchString(key.VaultID) || !opaqueID.MatchString(key.SenderDeviceID) || !opaqueID.MatchString(key.RecipientDeviceID) || key.CreatedAt.IsZero() {
		return errors.New("sync key package is invalid")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(key.Ciphertext)
	if err != nil || len(decoded) < 16 || len(decoded) > MaxEncryptedKeyPackageBytes {
		return errors.New("sync key package is invalid")
	}
	signature, err := base64.RawURLEncoding.DecodeString(key.Signature)
	if err != nil || len(signature) != Ed25519SignatureBytes {
		return errors.New("sync key package is invalid")
	}
	return nil
}

func (key EncryptedKeyPackage) VerifySignature(encodedPublicKey string) error {
	if err := key.Validate(); err != nil {
		return err
	}
	publicKey, err := decodeDeviceSigningKey(encodedPublicKey)
	if err != nil {
		return err
	}
	signature, _ := base64.RawURLEncoding.DecodeString(key.Signature)
	payload, err := key.signingPayload()
	if err != nil || !ed25519.Verify(publicKey, payload, signature) {
		return errors.New("sync key package signature is invalid")
	}
	return nil
}

// CreatedAt is deliberately absent: the handler stamps it on arrival.
func (key EncryptedKeyPackage) SigningPayload() ([]byte, error) {
	return key.signingPayload()
}

func (key EncryptedKeyPackage) signingPayload() ([]byte, error) {
	return json.Marshal(struct {
		VaultID           string `json:"vaultId"`
		SenderDeviceID    string `json:"senderDeviceId"`
		RecipientDeviceID string `json:"recipientDeviceId"`
		Ciphertext        string `json:"ciphertext"`
	}{key.VaultID, key.SenderDeviceID, key.RecipientDeviceID, key.Ciphertext})
}

type DeviceRecord struct {
	Enrollment DeviceEnrollment
	State      DeviceState
	Epoch      uint64
}

func (record DeviceRecord) Approve(packageForDevice EncryptedKeyPackage, approver DeviceRecord, challenge *EnrollmentChallenge, now time.Time) (DeviceRecord, error) {
	if err := record.Enrollment.Validate(); err != nil {
		return DeviceRecord{}, err
	}
	if record.State != DevicePending || packageForDevice.RecipientDeviceID != record.Enrollment.DeviceID || packageForDevice.VaultID != record.Enrollment.VaultID {
		return DeviceRecord{}, errors.New("sync device approval is invalid")
	}
	if approver.State != DeviceApproved || approver.Epoch == 0 || approver.Enrollment.VaultID != record.Enrollment.VaultID || approver.Enrollment.DeviceID == record.Enrollment.DeviceID || packageForDevice.SenderDeviceID != approver.Enrollment.DeviceID {
		return DeviceRecord{}, errors.New("sync device approval is invalid")
	}
	if err := approver.Enrollment.Validate(); err != nil {
		return DeviceRecord{}, err
	}
	if err := packageForDevice.VerifySignature(approver.Enrollment.SigningPublicKey); err != nil {
		return DeviceRecord{}, err
	}
	if err := challenge.Consume(record.Enrollment, now); err != nil {
		return DeviceRecord{}, err
	}
	record.State = DeviceApproved
	record.Epoch++
	return record, nil
}

func (record DeviceRecord) Revoke() (DeviceRecord, error) {
	if record.State != DeviceApproved {
		return DeviceRecord{}, errors.New("sync device revocation is invalid")
	}
	record.State = DeviceRevoked
	record.Epoch++
	return record, nil
}

type Quota struct{ DeviceLimit, SnapshotLimit, CiphertextBytesLimit int }

func DefaultQuota() Quota {
	return Quota{DeviceLimit: MaxDevicesPerVault, SnapshotLimit: MaxSnapshotsPerVault, CiphertextBytesLimit: MaxCiphertextBytes}
}
