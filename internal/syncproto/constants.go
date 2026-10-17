package syncproto

import "regexp"

const (
	MaxCiphertextBytes          = 10 * 1024 * 1024
	XChaChaNonceBytes           = 24
	Ed25519SignatureBytes       = 64
	Ed25519PublicKeyBytes       = 32
	X25519PublicKeyBytes        = 32
	MaxEncryptedKeyPackageBytes = 64 * 1024
	MaxDevicesPerVault          = 10
)

var opaqueID = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

func writeUint64(out *[8]byte, value uint64) {
	for index := 7; index >= 0; index-- {
		out[index] = byte(value)
		value >>= 8
	}
}
