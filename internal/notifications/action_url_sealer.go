package notifications

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

const (
	actionURLSealedPrefix  = "v1:"
	actionURLAADDomain     = "sesame-email-outbox-action-v1"
	actionURLSealerKeyInfo = "sesame-email-outbox-action-url-sealer-v1"
)

type ActionURLSealer struct {
	aead cipher.AEAD
}

func NewActionURLSealer(key []byte) (*ActionURLSealer, error) {
	if _, err := aes.NewCipher(key); err != nil {
		return nil, fmt.Errorf("action URL sealer: %w", err)
	}
	derived, err := hkdf.Key(sha256.New, key, nil, actionURLSealerKeyInfo, 32)
	if err != nil {
		return nil, fmt.Errorf("action URL sealer: %w", err)
	}
	block, err := aes.NewCipher(derived)
	if err != nil {
		return nil, fmt.Errorf("action URL sealer: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("action URL sealer: %w", err)
	}
	return &ActionURLSealer{aead: aead}, nil
}

func (s *ActionURLSealer) Seal(kind, to, actionURL string) (string, error) {
	if actionURL == "" {
		return "", nil
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("seal action URL: %w", err)
	}
	sealed := s.aead.Seal(nil, nonce, []byte(actionURL), actionURLAAD(kind, to))
	return actionURLSealedPrefix + base64.RawURLEncoding.EncodeToString(append(nonce, sealed...)), nil
}

func (s *ActionURLSealer) Open(kind, to, sealed string) (string, error) {
	if sealed == "" {
		return "", nil
	}
	if s == nil {
		return "", errors.New("action URL sealer is not configured")
	}
	encoded, ok := strings.CutPrefix(sealed, actionURLSealedPrefix)
	if !ok {
		return "", errors.New("action URL is not sealed")
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(raw) <= s.aead.NonceSize() {
		return "", errors.New("action URL is not sealed")
	}
	actionURL, err := s.aead.Open(nil, raw[:s.aead.NonceSize()], raw[s.aead.NonceSize():], actionURLAAD(kind, to))
	if err != nil {
		return "", errors.New("action URL cannot be decrypted")
	}
	return string(actionURL), nil
}

func actionURLAAD(kind, to string) []byte {
	return []byte(actionURLAADDomain + "\x00" + kind + "\x00" + to)
}
