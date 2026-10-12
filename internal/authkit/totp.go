package authkit

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func NewTOTPSecret() (string, error) {
	raw := make([]byte, 20)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw), nil
}

func TOTPURI(issuer, account, secret string) string {
	label := url.PathEscape(issuer + ":" + strings.ToLower(strings.TrimSpace(account)))
	return "otpauth://totp/" + label + "?secret=" + url.QueryEscape(secret) + "&issuer=" + url.PathEscape(issuer) + "&algorithm=SHA1&digits=6&period=30"
}

func VerifyTOTP(secret, code string, now time.Time) (counter int64, ok bool) {
	code = strings.TrimSpace(code)
	if len(code) != 6 {
		return -1, false
	}
	want, err := strconv.Atoi(code)
	if err != nil {
		return -1, false
	}
	decoded, ok := decodeSecret(secret)
	if !ok {
		return -1, false
	}
	baseCounter := now.Unix() / 30
	for offset := int64(-1); offset <= 1; offset++ {
		candidate := baseCounter + offset
		if truncate(decoded, candidate) == want {
			return candidate, true
		}
	}
	return -1, false
}

func TOTPCode(secret string, at time.Time) (string, bool) {
	decoded, ok := decodeSecret(secret)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("%06d", truncate(decoded, at.Unix()/30)), true
}

func decodeSecret(secret string) ([]byte, bool) {
	decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(secret))
	if err != nil || len(decoded) < 16 {
		return nil, false
	}
	return decoded, true
}

func truncate(key []byte, counter int64) int {
	var message [8]byte
	binary.BigEndian.PutUint64(message[:], uint64(counter))
	mac := hmac.New(sha1.New, key)
	_, _ = mac.Write(message[:])
	digest := mac.Sum(nil)
	index := digest[len(digest)-1] & 0x0f
	value := (uint32(digest[index])&0x7f)<<24 | uint32(digest[index+1])<<16 | uint32(digest[index+2])<<8 | uint32(digest[index+3])
	return int(value % 1_000_000)
}
