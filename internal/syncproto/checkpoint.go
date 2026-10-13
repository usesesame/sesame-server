package syncproto

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
)

const LabelCheckpoint = "sesame-sync-v3-checkpoint"

var ErrCheckpointInvalid = errors.New("sync checkpoint is invalid")

type Checkpoint struct {
	Version          int    `json:"version"`
	VaultID          string `json:"vaultId"`
	DeviceID         string `json:"deviceId"`
	DeviceListIndex  uint64 `json:"deviceListIndex"`
	DeviceListDigest string `json:"deviceListDigest"`
	HeaderRevision   uint64 `json:"headerRevision"`
	HeaderDigest     string `json:"headerDigest"`
	ObservedAt       uint64 `json:"observedAt"`
	Signature        string `json:"signature"`
}

func (checkpoint Checkpoint) SigningPayload() []byte {
	return newCanonical(LabelCheckpoint).
		number(uint64(checkpoint.Version)).
		text(checkpoint.VaultID).
		text(checkpoint.DeviceID).
		number(checkpoint.DeviceListIndex).
		text(checkpoint.DeviceListDigest).
		number(checkpoint.HeaderRevision).
		text(checkpoint.HeaderDigest).
		number(checkpoint.ObservedAt).
		bytes()
}

func (checkpoint Checkpoint) Validate() error {
	if checkpoint.Version != VersionV3 {
		return fmt.Errorf("%w: version", ErrCheckpointInvalid)
	}
	if !opaqueID.MatchString(checkpoint.VaultID) || !opaqueID.MatchString(checkpoint.DeviceID) {
		return fmt.Errorf("%w: identifiers", ErrCheckpointInvalid)
	}
	if checkpoint.DeviceListIndex == 0 || !validDigest(checkpoint.DeviceListDigest) {
		return fmt.Errorf("%w: device list head", ErrCheckpointInvalid)
	}
	if (checkpoint.HeaderRevision == 0) != (checkpoint.HeaderDigest == "") {
		return fmt.Errorf("%w: header head", ErrCheckpointInvalid)
	}
	if checkpoint.HeaderDigest != "" && !validDigest(checkpoint.HeaderDigest) {
		return fmt.Errorf("%w: header head", ErrCheckpointInvalid)
	}
	if checkpoint.ObservedAt == 0 {
		return fmt.Errorf("%w: time", ErrCheckpointInvalid)
	}
	if _, err := decodeCanonical(checkpoint.Signature, Ed25519SignatureBytes); err != nil {
		return fmt.Errorf("%w: signature", ErrCheckpointInvalid)
	}
	return nil
}

func (checkpoint Checkpoint) Verify(encodedKey string) error {
	if err := checkpoint.Validate(); err != nil {
		return err
	}
	key, err := decodeCanonical(encodedKey, Ed25519PublicKeyBytes)
	if err != nil {
		return fmt.Errorf("%w: signer key", ErrCheckpointInvalid)
	}
	signature, err := decodeCanonical(checkpoint.Signature, Ed25519SignatureBytes)
	if err != nil || !ed25519.Verify(ed25519.PublicKey(key), checkpoint.SigningPayload(), signature) {
		return fmt.Errorf("%w: signature", ErrCheckpointInvalid)
	}
	return nil
}

func (checkpoint Checkpoint) VerifyAgainst(list *DeviceList) error {
	if err := checkpoint.Validate(); err != nil {
		return err
	}
	if checkpoint.VaultID != list.VaultID() {
		return fmt.Errorf("%w: vault", ErrCheckpointInvalid)
	}
	if checkpoint.DeviceListIndex > list.Head() {
		return fmt.Errorf("%w: names a device list entry this device has not verified", ErrHistoryBroken)
	}
	if digest, _ := list.DigestAt(checkpoint.DeviceListIndex); digest != checkpoint.DeviceListDigest {
		return fmt.Errorf("%w: device list digest", ErrHistoryBroken)
	}
	signer, ok := list.MemberAt(checkpoint.DeviceID, checkpoint.DeviceListIndex)
	if !ok {
		return fmt.Errorf("%w: signer was not a member", ErrCheckpointInvalid)
	}
	return checkpoint.Verify(signer.SigningKey)
}

func SignCheckpoint(key ed25519.PrivateKey, checkpoint Checkpoint) Checkpoint {
	checkpoint.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, checkpoint.SigningPayload()))
	return checkpoint
}

func WithinRequestSkew(timestamp, now uint64) bool {
	return withinSkew(timestamp, now)
}
