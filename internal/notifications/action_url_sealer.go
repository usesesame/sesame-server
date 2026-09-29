package notifications

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

const (
	actionURLSealedPrefix = "v1:"
	actionURLSealedAAD    = "sesame-email-outbox-action-v1"
)

type ActionURLSealer struct {
	aead cipher.AEAD
}

func NewActionURLSealer(key []byte) (*ActionURLSealer, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("action URL sealer: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("action URL sealer: %w", err)
	}
	return &ActionURLSealer{aead: aead}, nil
}

func (s *ActionURLSealer) Seal(actionURL string) (string, error) {
	if actionURL == "" {
		return "", nil
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("seal action URL: %w", err)
	}
	sealed := s.aead.Seal(nil, nonce, []byte(actionURL), []byte(actionURLSealedAAD))
	return actionURLSealedPrefix + base64.RawURLEncoding.EncodeToString(append(nonce, sealed...)), nil
}

func (s *ActionURLSealer) Open(sealed string) (string, error) {
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
	actionURL, err := s.aead.Open(nil, raw[:s.aead.NonceSize()], raw[s.aead.NonceSize():], []byte(actionURLSealedAAD))
	if err != nil {
		return "", errors.New("action URL cannot be decrypted")
	}
	return string(actionURL), nil
}
