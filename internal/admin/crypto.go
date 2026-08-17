package admin

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"
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

func encryptSecret(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return append(nonce, gcm.Seal(nil, nonce, plaintext, []byte("sesame-admin-totp-v1"))...), nil
}

func decryptSecret(key, encoded []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(encoded) <= gcm.NonceSize() {
		return nil, errors.New("encrypted secret is invalid")
	}
	return gcm.Open(nil, encoded[:gcm.NonceSize()], encoded[gcm.NonceSize():], []byte("sesame-admin-totp-v1"))
}

func NewTOTPSecret() (string, error) {
	raw := make([]byte, 20)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw), nil
}

func TOTPURI(email, secret string) string {
	label := url.PathEscape("Sesame Admin:" + strings.ToLower(strings.TrimSpace(email)))
	return "otpauth://totp/" + label + "?secret=" + url.QueryEscape(secret) + "&issuer=Sesame%20Admin&algorithm=SHA1&digits=6&period=30"
}

// Returns the matched time-step counter for replay prevention; -1 and false when no window matches.
func VerifyTOTP(secret, code string, now time.Time) (counter int64, ok bool) {
	code = strings.TrimSpace(code)
	if len(code) != 6 {
		return -1, false
	}
	want, err := strconv.Atoi(code)
	if err != nil {
		return -1, false
	}
	decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(secret))
	if err != nil || len(decoded) < 16 {
		return -1, false
	}
	baseCounter := now.Unix() / 30
	for offset := int64(-1); offset <= 1; offset++ {
		candidate := baseCounter + offset
		var message [8]byte
		binary.BigEndian.PutUint64(message[:], uint64(candidate))
		mac := hmac.New(sha1.New, decoded)
		_, _ = mac.Write(message[:])
		digest := mac.Sum(nil)
		index := digest[len(digest)-1] & 0x0f
		value := (uint32(digest[index])&0x7f)<<24 | uint32(digest[index+1])<<16 | uint32(digest[index+2])<<8 | uint32(digest[index+3])
		if int(value%1_000_000) == want {
			return candidate, true
		}
	}
	return -1, false
}

func HashIP(value, pepper string) string {
	digest := sha256.Sum256([]byte(pepper + "\x00" + value))
	return hex.EncodeToString(digest[:])
}

func NewToken() (string, []byte, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	hash := sha256.Sum256([]byte(token))
	return token, hash[:], nil
}

func HashToken(token string) []byte {
	hash := sha256.Sum256([]byte(token))
	return hash[:]
}
