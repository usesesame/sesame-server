package notifications

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"strings"
	"testing"
)

const (
	actionURLSealerTestToken     = "fictional-token-abc123"
	actionURLSealerTestKind      = "verify-email"
	actionURLSealerTestRecipient = "sealer-test@example.invalid"
)

func testActionURLSealer(t *testing.T) *ActionURLSealer {
	t.Helper()
	sealer, err := NewActionURLSealer([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("create test action URL sealer: %v", err)
	}
	return sealer
}

func otherActionURLSealer(t *testing.T) *ActionURLSealer {
	t.Helper()
	sealer, err := NewActionURLSealer([]byte("fedcba9876543210fedcba9876543210"))
	if err != nil {
		t.Fatalf("create second test action URL sealer: %v", err)
	}
	return sealer
}

func TestActionURLSealerRoundTrips(t *testing.T) {
	sealer := testActionURLSealer(t)
	actionURL := "https://account.example.invalid/verify-email#token=" + actionURLSealerTestToken

	sealed, err := sealer.Seal(actionURLSealerTestKind, actionURLSealerTestRecipient, actionURL)
	if err != nil {
		t.Fatalf("seal action URL: %v", err)
	}
	if sealed == actionURL || strings.Contains(sealed, actionURLSealerTestToken) || strings.Contains(sealed, "account.example.invalid") {
		t.Fatalf("sealed action URL kept plaintext: %q", sealed)
	}
	opened, err := sealer.Open(actionURLSealerTestKind, actionURLSealerTestRecipient, sealed)
	if err != nil || opened != actionURL {
		t.Fatalf("opened action URL = %q, %v; want the original", opened, err)
	}

	empty, err := sealer.Seal(actionURLSealerTestKind, actionURLSealerTestRecipient, "")
	if err != nil || empty != "" {
		t.Fatalf("sealed empty action URL = %q, %v; want an empty value", empty, err)
	}
	if opened, err := sealer.Open(actionURLSealerTestKind, actionURLSealerTestRecipient, ""); err != nil || opened != "" {
		t.Fatalf("opened empty action URL = %q, %v; want an empty value", opened, err)
	}
}

func TestActionURLSealerBindsSealedLinksToKindAndRecipient(t *testing.T) {
	sealer := testActionURLSealer(t)
	actionURL := "https://account.example.invalid/reset-password#token=" + actionURLSealerTestToken
	sealed, err := sealer.Seal("recover-password", "owner@example.invalid", actionURL)
	if err != nil {
		t.Fatalf("seal action URL: %v", err)
	}
	if opened, err := sealer.Open("recover-password", "owner@example.invalid", sealed); err != nil || opened != actionURL {
		t.Fatalf("opened the bound link to %q, %v; want the original", opened, err)
	}
	for _, tc := range []struct {
		name string
		kind string
		to   string
	}{
		{name: "another recipient", kind: "recover-password", to: "other@example.invalid"},
		{name: "another kind", kind: "change-email", to: "owner@example.invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opened, err := sealer.Open(tc.kind, tc.to, sealed)
			if err == nil || opened != "" {
				t.Fatalf("opened a link sealed for recover-password/owner@example.invalid as %s/%s to %q with error %v, want a failure", tc.kind, tc.to, opened, err)
			}
		})
	}
}

func TestActionURLSealerFailsClosed(t *testing.T) {
	sealer := testActionURLSealer(t)
	actionURL := "https://account.example.invalid/verify-email#token=" + actionURLSealerTestToken
	sealed, err := sealer.Seal(actionURLSealerTestKind, actionURLSealerTestRecipient, actionURL)
	if err != nil {
		t.Fatalf("seal action URL: %v", err)
	}
	wrongKey, err := otherActionURLSealer(t).Seal(actionURLSealerTestKind, actionURLSealerTestRecipient, actionURL)
	if err != nil {
		t.Fatalf("seal with the second key: %v", err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(sealed, actionURLSealedPrefix))
	if err != nil {
		t.Fatalf("decode sealed action URL: %v", err)
	}
	raw[len(raw)-1] ^= 0xff
	corrupted := actionURLSealedPrefix + base64.RawURLEncoding.EncodeToString(raw)

	for _, tc := range []struct {
		name  string
		value string
	}{
		{name: "wrong key", value: wrongKey},
		{name: "plaintext URL", value: actionURL},
		{name: "unknown version", value: "v2:" + strings.TrimPrefix(sealed, actionURLSealedPrefix)},
		{name: "corrupted ciphertext", value: corrupted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opened, err := sealer.Open(actionURLSealerTestKind, actionURLSealerTestRecipient, tc.value)
			if err == nil || opened != "" {
				t.Fatalf("opened %q to %q with error %v, want a failure", tc.value, opened, err)
			}
		})
	}
}

func TestActionURLSealerDerivesASubkeyFromTheSourceKey(t *testing.T) {
	sourceKey := []byte("0123456789abcdef0123456789abcdef")
	sealer, err := NewActionURLSealer(sourceKey)
	if err != nil {
		t.Fatalf("create action URL sealer: %v", err)
	}
	actionURL := "https://account.example.invalid/verify-email#token=" + actionURLSealerTestToken
	sealed, err := sealer.Seal(actionURLSealerTestKind, actionURLSealerTestRecipient, actionURL)
	if err != nil {
		t.Fatalf("seal action URL: %v", err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(sealed, actionURLSealedPrefix))
	if err != nil {
		t.Fatalf("decode sealed action URL: %v", err)
	}
	block, err := aes.NewCipher(sourceKey)
	if err != nil {
		t.Fatalf("create raw-key cipher: %v", err)
	}
	rawAEAD, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("create raw-key GCM: %v", err)
	}
	if _, err := rawAEAD.Open(nil, raw[:rawAEAD.NonceSize()], raw[rawAEAD.NonceSize():], actionURLAAD(actionURLSealerTestKind, actionURLSealerTestRecipient)); err == nil {
		t.Fatal("the raw source key opened a sealed action URL, want a purpose-derived subkey")
	}
}

func TestNewActionURLSealerRejectsAKeyThatIsNotAESShaped(t *testing.T) {
	if _, err := NewActionURLSealer([]byte("short")); err == nil {
		t.Fatal("a short key must be rejected")
	}
}
