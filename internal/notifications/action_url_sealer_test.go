package notifications

import (
	"encoding/base64"
	"strings"
	"testing"
)

const actionURLSealerTestToken = "fictional-token-abc123"

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

	sealed, err := sealer.Seal(actionURL)
	if err != nil {
		t.Fatalf("seal action URL: %v", err)
	}
	if sealed == actionURL || strings.Contains(sealed, actionURLSealerTestToken) || strings.Contains(sealed, "account.example.invalid") {
		t.Fatalf("sealed action URL kept plaintext: %q", sealed)
	}
	opened, err := sealer.Open(sealed)
	if err != nil || opened != actionURL {
		t.Fatalf("opened action URL = %q, %v; want the original", opened, err)
	}

	empty, err := sealer.Seal("")
	if err != nil || empty != "" {
		t.Fatalf("sealed empty action URL = %q, %v; want an empty value", empty, err)
	}
	if opened, err := sealer.Open(""); err != nil || opened != "" {
		t.Fatalf("opened empty action URL = %q, %v; want an empty value", opened, err)
	}
}

func TestActionURLSealerFailsClosed(t *testing.T) {
	sealer := testActionURLSealer(t)
	actionURL := "https://account.example.invalid/verify-email#token=" + actionURLSealerTestToken
	sealed, err := sealer.Seal(actionURL)
	if err != nil {
		t.Fatalf("seal action URL: %v", err)
	}
	wrongKey, err := otherActionURLSealer(t).Seal(actionURL)
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
			opened, err := sealer.Open(tc.value)
			if err == nil || opened != "" {
				t.Fatalf("opened %q to %q with error %v, want a failure", tc.value, opened, err)
			}
		})
	}
}

func TestNewActionURLSealerRejectsAKeyThatIsNotAESShaped(t *testing.T) {
	if _, err := NewActionURLSealer([]byte("short")); err == nil {
		t.Fatal("a short key must be rejected")
	}
}
