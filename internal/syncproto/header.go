package syncproto

import (
	"crypto/ed25519"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
)

var (
	ErrHeaderInvalid = errors.New("sync header is invalid")
	ErrHistoryBroken = errors.New("sync history does not verify")
)

type Header struct {
	Version          int    `json:"version"`
	VaultID          string `json:"vaultId"`
	DeviceID         string `json:"deviceId"`
	Revision         uint64 `json:"revision"`
	VaultEpoch       uint64 `json:"vaultEpoch"`
	DeviceListIndex  uint64 `json:"deviceListIndex"`
	DeviceListDigest string `json:"deviceListDigest"`
	PreviousDigest   string `json:"previousDigest"`
	CiphertextDigest string `json:"ciphertextDigest"`
	CiphertextLength uint64 `json:"ciphertextLength"`
	Nonce            string `json:"nonce"`
	Signature        string `json:"signature"`
}

func (header Header) fields(label string) *canonical {
	return newCanonical(label).
		number(uint64(header.Version)).
		text(header.VaultID).
		text(header.DeviceID).
		number(header.Revision).
		number(header.VaultEpoch).
		number(header.DeviceListIndex).
		text(header.DeviceListDigest).
		text(header.PreviousDigest).
		text(header.CiphertextDigest).
		number(header.CiphertextLength).
		text(header.Nonce)
}

func (header Header) SigningPayload() []byte {
	return header.fields(LabelHeader).bytes()
}

func (header Header) Digest() string {
	return header.fields(LabelHeaderDigest).text(header.Signature).digest()
}

func (header Header) Validate() error {
	if header.Version != VersionV3 {
		return fmt.Errorf("%w: version", ErrHeaderInvalid)
	}
	if !opaqueID.MatchString(header.VaultID) || !opaqueID.MatchString(header.DeviceID) {
		return fmt.Errorf("%w: identifiers", ErrHeaderInvalid)
	}
	if header.Revision == 0 || header.VaultEpoch == 0 || header.DeviceListIndex == 0 {
		return fmt.Errorf("%w: revision, epoch or device list index", ErrHeaderInvalid)
	}
	if (header.Revision == 1) != (header.PreviousDigest == "") {
		return fmt.Errorf("%w: previous digest", ErrHeaderInvalid)
	}
	if header.PreviousDigest != "" && !validDigest(header.PreviousDigest) {
		return fmt.Errorf("%w: previous digest", ErrHeaderInvalid)
	}
	if !validDigest(header.DeviceListDigest) || !validDigest(header.CiphertextDigest) {
		return fmt.Errorf("%w: digest", ErrHeaderInvalid)
	}
	if header.CiphertextLength < 16 || header.CiphertextLength > MaxCiphertextBytes {
		return fmt.Errorf("%w: ciphertext length", ErrHeaderInvalid)
	}
	if _, err := decodeCanonical(header.Nonce, XChaChaNonceBytes); err != nil {
		return fmt.Errorf("%w: nonce", ErrHeaderInvalid)
	}
	if _, err := decodeCanonical(header.Signature, Ed25519SignatureBytes); err != nil {
		return fmt.Errorf("%w: signature", ErrHeaderInvalid)
	}
	return nil
}

func (header Header) VerifyCiphertext(ciphertext []byte) error {
	if uint64(len(ciphertext)) != header.CiphertextLength {
		return fmt.Errorf("%w: ciphertext length", ErrHistoryBroken)
	}
	sum := sha256Bytes(ciphertext)
	expected, err := decodeCanonical(header.CiphertextDigest, DigestBytes)
	if err != nil || subtle.ConstantTimeCompare(sum[:], expected) != 1 {
		return fmt.Errorf("%w: ciphertext digest", ErrHistoryBroken)
	}
	return nil
}

func VerifyHeader(list *DeviceList, previous *Header, header Header) error {
	if err := header.Validate(); err != nil {
		return err
	}
	if header.VaultID != list.VaultID() {
		return fmt.Errorf("%w: vault", ErrHistoryBroken)
	}
	if header.DeviceListIndex > list.Head() {
		return fmt.Errorf("%w: header names a device list entry this device has not verified", ErrHistoryBroken)
	}
	if digest, _ := list.DigestAt(header.DeviceListIndex); digest != header.DeviceListDigest {
		return fmt.Errorf("%w: device list digest", ErrHistoryBroken)
	}
	if epoch, _ := list.EpochAt(header.DeviceListIndex); epoch != header.VaultEpoch {
		return fmt.Errorf("%w: epoch", ErrHistoryBroken)
	}
	if previous != nil {
		if err := verifyLink(*previous, header); err != nil {
			return err
		}
	}
	signer, ok := list.MemberAt(header.DeviceID, header.DeviceListIndex)
	if !ok {
		return fmt.Errorf("%w: signer was not a member", ErrHistoryBroken)
	}
	key, err := decodeCanonical(signer.SigningKey, Ed25519PublicKeyBytes)
	if err != nil {
		return fmt.Errorf("%w: signer key", ErrHistoryBroken)
	}
	signature, err := decodeCanonical(header.Signature, Ed25519SignatureBytes)
	if err != nil || !ed25519.Verify(ed25519.PublicKey(key), header.SigningPayload(), signature) {
		return fmt.Errorf("%w: signature", ErrHistoryBroken)
	}
	return nil
}

func verifyLink(previous, header Header) error {
	next, ok := checkedNext(previous.Revision)
	if !ok || header.Revision != next {
		return fmt.Errorf("%w: revision does not follow", ErrHistoryBroken)
	}
	if header.PreviousDigest != previous.Digest() {
		return fmt.Errorf("%w: previous digest", ErrHistoryBroken)
	}
	if header.DeviceListIndex < previous.DeviceListIndex || header.VaultEpoch < previous.VaultEpoch {
		return fmt.Errorf("%w: device list or epoch went backwards", ErrHistoryBroken)
	}
	return nil
}

func VerifyHeaderChain(list *DeviceList, previous *Header, headers []Header) error {
	for _, header := range headers {
		if err := VerifyHeader(list, previous, header); err != nil {
			return err
		}
		current := header
		previous = &current
	}
	return nil
}

func SignHeader(key ed25519.PrivateKey, header Header) Header {
	header.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, header.SigningPayload()))
	return header
}

func SnapshotAADV3(header Header) []byte {
	return newCanonical(LabelAAD).
		number(uint64(header.Version)).
		text(header.VaultID).
		text(header.DeviceID).
		number(header.Revision).
		number(header.VaultEpoch).
		number(header.DeviceListIndex).
		text(header.DeviceListDigest).
		text(header.PreviousDigest).
		bytes()
}
