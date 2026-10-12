package updates

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

type Keys map[string]ed25519.PublicKey

func ValidKeyID(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		switch {
		case character >= 'a' && character <= 'z', character >= 'A' && character <= 'Z', character >= '0' && character <= '9', character == '-', character == '_', character == '.':
		default:
			return false
		}
	}
	return true
}

func ParseKeys(list string) (Keys, error) {
	keys := Keys{}
	list = strings.TrimSpace(list)
	if list == "" {
		return keys, nil
	}
	if strings.ContainsAny(list, "\r\n") {
		return nil, errors.New("the key list must not hold line breaks")
	}
	for _, entry := range strings.Split(list, ",") {
		id, encoded, found := strings.Cut(strings.TrimSpace(entry), ":")
		if !found || !ValidKeyID(id) {
			return nil, errors.New("each key must be a key id of letters, digits, dot, dash or underscore, a colon and an unpadded base64url Ed25519 public key")
		}
		raw, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("key %q is not a 32 byte Ed25519 public key in unpadded base64url", id)
		}
		if _, duplicate := keys[id]; duplicate {
			return nil, fmt.Errorf("key id %q appears more than once", id)
		}
		keys[id] = ed25519.PublicKey(raw)
	}
	return keys, nil
}

func FormatKey(id string, key ed25519.PublicKey) string {
	return id + ":" + base64.RawURLEncoding.EncodeToString(key)
}
