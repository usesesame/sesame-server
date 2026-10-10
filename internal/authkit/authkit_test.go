package authkit

import (
	"bytes"
	"encoding/base32"
	"strings"
	"testing"
	"time"
)

func TestPasswordRoundTripAndRejections(t *testing.T) {
	encoded, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(encoded, "correct horse battery") {
		t.Fatal("matching password was rejected")
	}
	if NeedsRehash(encoded) {
		t.Fatal("fresh hash reported as needing rehash")
	}
	for name, candidate := range map[string]string{
		"empty":        "",
		"plain text":   "correct horse battery",
		"wrong scheme": strings.Replace(encoded, "argon2id", "argon2i", 1),
		"zero memory":  "$argon2id$v=19$m=0,t=2,p=1$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"truncated":    encoded[:len(encoded)-4],
	} {
		if VerifyPassword(candidate, "correct horse battery") {
			t.Fatalf("%s accepted", name)
		}
	}
	if VerifyPassword(encoded, "correct horse batterx") {
		t.Fatal("wrong password accepted")
	}
	if !NeedsRehash("$argon2id$v=19$m=8,t=1,p=1$AAAA$AAAA") {
		t.Fatal("weak parameters not flagged for rehash")
	}
}

func TestTokenHashing(t *testing.T) {
	token, hash, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(token) != 43 || len(hash) != 32 {
		t.Fatalf("unexpected sizes %d %d", len(token), len(hash))
	}
	if !bytes.Equal(hash, HashToken(token)) {
		t.Fatal("hash does not match token")
	}
	other, _, _ := NewToken()
	if token == other {
		t.Fatal("tokens repeat")
	}
}

func TestTOTPMatchesRFC6238Vector(t *testing.T) {
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte("12345678901234567890"))
	moment := time.Unix(59, 0)
	code, ok := TOTPCode(secret, moment)
	if !ok || code != "287082" {
		t.Fatalf("code %q ok %v", code, ok)
	}
	counter, ok := VerifyTOTP(secret, code, moment)
	if !ok || counter != 1 {
		t.Fatalf("counter %d ok %v", counter, ok)
	}
	if _, ok := VerifyTOTP(secret, code, moment.Add(2*time.Minute)); ok {
		t.Fatal("code accepted outside the window")
	}
	for _, bad := range []string{"", "28708", "2870822", "abcdef", " 287083"} {
		if _, ok := VerifyTOTP(secret, bad, moment); ok {
			t.Fatalf("malformed code %q accepted", bad)
		}
	}
	if _, ok := VerifyTOTP("short", code, moment); ok {
		t.Fatal("short secret accepted")
	}
}

func TestTOTPURIKeepsHostedFormat(t *testing.T) {
	got := TOTPURI("Sesame Admin", " Person@Example.test ", "ABCDEFGHIJKLMNOP")
	want := "otpauth://totp/Sesame%20Admin:person@example.test?secret=ABCDEFGHIJKLMNOP&issuer=Sesame%20Admin&algorithm=SHA1&digits=6&period=30"
	if got != want {
		t.Fatalf("got %s", got)
	}
}

func TestSealBindsKeyAndContext(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	sealed, err := Seal(key, []byte("secret"), []byte("context-a"))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := Open(key, sealed, []byte("context-a"))
	if err != nil || string(plain) != "secret" {
		t.Fatalf("round trip failed: %v", err)
	}
	if _, err := Open(key, sealed, []byte("context-b")); err == nil {
		t.Fatal("wrong context accepted")
	}
	if _, err := Open(bytes.Repeat([]byte{8}, 32), sealed, []byte("context-a")); err == nil {
		t.Fatal("wrong key accepted")
	}
	flipped := append([]byte(nil), sealed...)
	flipped[len(flipped)-1] ^= 1
	if _, err := Open(key, flipped, []byte("context-a")); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}
	if _, err := Open(key, sealed[:8], []byte("context-a")); err == nil {
		t.Fatal("short ciphertext accepted")
	}
	if _, err := Seal([]byte("short"), []byte("x"), nil); err == nil {
		t.Fatal("short key accepted")
	}
}
