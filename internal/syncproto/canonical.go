package syncproto

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
)

const (
	VersionV3 = 3

	LabelDeviceList       = "sesame-sync-v3-devicelist"
	LabelDeviceListDigest = "sesame-sync-v3-devicelist-digest"
	LabelHeader           = "sesame-sync-v3-header"
	LabelHeaderDigest     = "sesame-sync-v3-header-digest"
	LabelRequest          = "sesame-sync-v3-request"
	LabelReceipt          = "sesame-sync-v3-receipt"
	LabelAAD              = "sesame-sync-v3-aad"

	DigestBytes           = 32
	MaxSealedLabelBytes   = 256
	MinSealedLabelBytes   = 40
	RequestNonceBytes     = 16
	MaxHeaderRetention    = 10000
	RequestTimestampSkew  = 5 * 60
	RequestNonceRetention = 10 * 60
)

type canonical struct {
	out []byte
}

func newCanonical(label string) *canonical {
	return (&canonical{}).text(label)
}

func (c *canonical) add(part []byte) *canonical {
	var length [8]byte
	writeUint64(&length, uint64(len(part)))
	c.out = append(c.out, length[:]...)
	c.out = append(c.out, part...)
	return c
}

func (c *canonical) text(value string) *canonical {
	return c.add([]byte(value))
}

func (c *canonical) number(value uint64) *canonical {
	return c.text(strconv.FormatUint(value, 10))
}

func (c *canonical) bytes() []byte {
	return c.out
}

func (c *canonical) digest() string {
	sum := sha256.Sum256(c.out)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func decodeCanonical(value string, length int) ([]byte, error) {
	if strings.ContainsAny(value, "\r\n") {
		return nil, errors.New("sync value is not canonical base64url")
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || len(decoded) != length {
		return nil, errors.New("sync value is not canonical base64url of the expected length")
	}
	if base64.RawURLEncoding.EncodeToString(decoded) != value {
		return nil, errors.New("sync value is not canonical base64url")
	}
	return decoded, nil
}

func decodeCanonicalRange(value string, minimum, maximum int) ([]byte, error) {
	if strings.ContainsAny(value, "\r\n") {
		return nil, errors.New("sync value is not canonical base64url")
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || len(decoded) < minimum || len(decoded) > maximum {
		return nil, errors.New("sync value is not canonical base64url of an allowed length")
	}
	if base64.RawURLEncoding.EncodeToString(decoded) != value {
		return nil, errors.New("sync value is not canonical base64url")
	}
	return decoded, nil
}

func validDigest(value string) bool {
	_, err := decodeCanonical(value, DigestBytes)
	return err == nil
}

func checkedNext(value uint64) (uint64, bool) {
	if value == ^uint64(0) {
		return 0, false
	}
	return value + 1, true
}
