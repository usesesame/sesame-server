package admin

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"usesesame.app/backend/internal/authkit"
)

func ParseEncryptionKey(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if decoded, err := hex.DecodeString(value); err == nil && len(decoded) == 32 {
		return decoded, nil
	}
	for _, encoding := range []*base64.Encoding{base64.RawStdEncoding, base64.StdEncoding, base64.RawURLEncoding, base64.URLEncoding} {
		if decoded, err := encoding.DecodeString(value); err == nil && len(decoded) == 32 {
			return decoded, nil
		}
	}
	return nil, errors.New("SESAME_ADMIN_ENCRYPTION_KEY must encode exactly 32 bytes")
}

var totpSealContext = []byte("sesame-admin-totp-v1")

func encryptSecret(key, plaintext []byte) ([]byte, error) {
	return authkit.Seal(key, plaintext, totpSealContext)
}

func decryptSecret(key, encoded []byte) ([]byte, error) {
	return authkit.Open(key, encoded, totpSealContext)
}

func NewTOTPSecret() (string, error) {
	return authkit.NewTOTPSecret()
}

func TOTPURI(email, secret string) string {
	return authkit.TOTPURI("Sesame Admin", email, secret)
}

// Returns the matched time-step counter for replay prevention; -1 and false when no window matches.
func VerifyTOTP(secret, code string, now time.Time) (counter int64, ok bool) {
	return authkit.VerifyTOTP(secret, code, now)
}

func HashIP(value, pepper string) string {
	digest := sha256.Sum256([]byte(pepper + "\x00" + value))
	return hex.EncodeToString(digest[:])
}

func NewToken() (string, []byte, error) {
	return authkit.NewToken()
}

func HashToken(token string) []byte {
	return authkit.HashToken(token)
}
