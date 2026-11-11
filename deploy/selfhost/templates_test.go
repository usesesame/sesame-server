package selfhostdeploy

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"
)

const (
	composeSHA256 = "e22c45dda88f6a4451797f8eb69d2674564f1601472820857c5edce7c5ac3793"
	caddySHA256   = "9e1800e104d44889a6935ec136f45ef4a8c87e725747e043427112609b73ed27"
)

func TestComposeMatchesCanonicalFile(t *testing.T) {
	raw, err := os.ReadFile("compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if embedded := Compose(); embedded != string(raw) {
		t.Fatal("embedded compose.yaml does not match its canonical file")
	}
	if got := digest(Compose()); got != composeSHA256 {
		t.Fatalf("embedded compose.yaml hash is %s, want %s", got, composeSHA256)
	}
	if Compose() != Compose() {
		t.Fatal("compose template changed between reads")
	}
}

func TestCaddyMatchesCanonicalFile(t *testing.T) {
	raw, err := os.ReadFile("Caddyfile")
	if err != nil {
		t.Fatal(err)
	}
	if embedded := Caddy(); embedded != string(raw) {
		t.Fatal("embedded Caddyfile does not match its canonical file")
	}
	if got := digest(Caddy()); got != caddySHA256 {
		t.Fatalf("embedded Caddyfile hash is %s, want %s", got, caddySHA256)
	}
	if Caddy() != Caddy() {
		t.Fatal("Caddy template changed between reads")
	}
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
