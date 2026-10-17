package syncproto

import (
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
)

var ErrRequestSignature = errors.New("sync request signature is invalid")

type RequestSignature struct {
	Method    string
	Target    string
	Body      []byte
	Token     string
	Timestamp uint64
	Nonce     string
}

func (request RequestSignature) SigningPayload() []byte {
	body := sha256.Sum256(request.Body)
	token := sha256.Sum256([]byte(request.Token))
	return newCanonical(LabelRequest).
		text(request.Method).
		text(request.Target).
		add(body[:]).
		add(token[:]).
		number(request.Timestamp).
		text(request.Nonce).
		bytes()
}

func (request RequestSignature) Verify(encodedKey, encodedSignature string, now uint64) error {
	if _, err := decodeCanonical(request.Nonce, RequestNonceBytes); err != nil {
		return ErrRequestSignature
	}
	if !withinSkew(request.Timestamp, now) {
		return ErrRequestSignature
	}
	key, err := decodeCanonical(encodedKey, Ed25519PublicKeyBytes)
	if err != nil {
		return ErrRequestSignature
	}
	signature, err := decodeCanonical(encodedSignature, Ed25519SignatureBytes)
	if err != nil || !ed25519.Verify(ed25519.PublicKey(key), request.SigningPayload(), signature) {
		return ErrRequestSignature
	}
	return nil
}

func withinSkew(timestamp, now uint64) bool {
	if timestamp > now {
		return timestamp-now <= RequestTimestampSkew
	}
	return now-timestamp <= RequestTimestampSkew
}

type ReceiptV3 struct {
	VaultID      string
	Revision     uint64
	HeaderDigest string
	VaultEpoch   uint64
	AcceptedAt   uint64
}

func (receipt ReceiptV3) SigningPayload() []byte {
	return newCanonical(LabelReceipt).
		text(receipt.VaultID).
		number(receipt.Revision).
		text(receipt.HeaderDigest).
		number(receipt.VaultEpoch).
		number(receipt.AcceptedAt).
		bytes()
}

func (receipt ReceiptV3) Sign(key ed25519.PrivateKey) []byte {
	return ed25519.Sign(key, receipt.SigningPayload())
}

func (receipt ReceiptV3) Verify(key ed25519.PublicKey, signature []byte) bool {
	return len(signature) == Ed25519SignatureBytes && ed25519.Verify(key, receipt.SigningPayload(), signature)
}

const LabelKeyPackage = "sesame-sync-v3-keypackage"

type KeyPackageV3 struct {
	VaultID           string
	SenderDeviceID    string
	RecipientDeviceID string
	VaultEpoch        uint64
	EntryDigest       string
	Ciphertext        string
	Signature         string
}

func (pkg KeyPackageV3) SigningPayload() []byte {
	return newCanonical(LabelKeyPackage).
		text(pkg.VaultID).
		text(pkg.SenderDeviceID).
		text(pkg.RecipientDeviceID).
		number(pkg.VaultEpoch).
		text(pkg.EntryDigest).
		text(pkg.Ciphertext).
		bytes()
}

func (pkg KeyPackageV3) Validate() error {
	if !opaqueID.MatchString(pkg.VaultID) || !opaqueID.MatchString(pkg.SenderDeviceID) || !opaqueID.MatchString(pkg.RecipientDeviceID) {
		return errors.New("sync key package is invalid")
	}
	if pkg.VaultEpoch == 0 || !validDigest(pkg.EntryDigest) {
		return errors.New("sync key package is invalid")
	}
	if _, err := decodeCanonicalRange(pkg.Ciphertext, 16, MaxEncryptedKeyPackageBytes); err != nil {
		return errors.New("sync key package is invalid")
	}
	if _, err := decodeCanonical(pkg.Signature, Ed25519SignatureBytes); err != nil {
		return errors.New("sync key package is invalid")
	}
	return nil
}

func (pkg KeyPackageV3) Verify(encodedKey string) error {
	if err := pkg.Validate(); err != nil {
		return err
	}
	key, err := decodeCanonical(encodedKey, Ed25519PublicKeyBytes)
	if err != nil {
		return errors.New("sync key package is invalid")
	}
	signature, err := decodeCanonical(pkg.Signature, Ed25519SignatureBytes)
	if err != nil || !ed25519.Verify(ed25519.PublicKey(key), pkg.SigningPayload(), signature) {
		return errors.New("sync key package signature is invalid")
	}
	return nil
}

func (pkg KeyPackageV3) Digest() string {
	return newCanonical(LabelKeyPackage + "-digest").
		text(pkg.VaultID).
		text(pkg.SenderDeviceID).
		text(pkg.RecipientDeviceID).
		number(pkg.VaultEpoch).
		text(pkg.EntryDigest).
		text(pkg.Ciphertext).
		text(pkg.Signature).
		digest()
}
