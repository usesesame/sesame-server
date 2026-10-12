package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"usesesame.app/backend/internal/selfhost/updates"
)

var fixedNow = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

func invoke(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr, func() time.Time { return fixedNow })
	return code, stdout.String(), stderr.String()
}

func payloadFile(t *testing.T, dir string, change func(*updates.Payload)) string {
	t.Helper()
	payload := updates.Payload{
		SchemaVersion: 1,
		IssuedAt:      fixedNow.Add(-time.Hour),
		ExpiresAt:     fixedNow.Add(30 * 24 * time.Hour),
		Products: []updates.Product{{ID: updates.ServerProductID, Channels: map[string]updates.Release{"stable": {
			Version:     "0.2.0",
			PublishedAt: fixedNow.Add(-24 * time.Hour),
			NotesURL:    "https://example.net/notes/0.2.0",
			Images:      []updates.Image{{Ref: "registry.example.net/sesame/server@sha256:" + strings.Repeat("a", 64)}},
			Binaries:    []updates.Binary{{OS: "linux", Arch: "amd64", URL: "https://downloads.example.net/sesame-server", SHA256: strings.Repeat("b", 64)}},
		}}}},
	}
	if change != nil {
		change(&payload)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "feed.json")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func publicKeyFrom(t *testing.T, stdout string) string {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	return lines[len(lines)-1]
}

func TestKeygenWritesAPrivateKeyFileAndPrintsOnlyThePublicKey(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "feed.key")
	code, stdout, stderr := invoke(t, "keygen", "-out", keyPath, "-id", "test-key")
	if code != 0 || stderr != "" {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	info, err := os.Stat(keyPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("key file: %v %v", info, err)
	}
	content, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	id, seed, found := strings.Cut(strings.TrimSpace(string(content)), ":")
	if !found || id != "test-key" {
		t.Fatalf("key file = %q", content)
	}
	if strings.Contains(stdout, seed) || strings.Contains(stdout, string(content)) {
		t.Fatal("keygen printed the private key")
	}
	if _, err := updates.ParseKeys(publicKeyFrom(t, stdout)); err != nil {
		t.Fatalf("the printed public key does not parse: %v", err)
	}
	if code, _, stderr := invoke(t, "keygen", "-out", keyPath); code == 0 || !strings.Contains(stderr, "cannot be created") {
		t.Fatalf("keygen overwrote an existing key: exit %d %s", code, stderr)
	}
	after, _ := os.ReadFile(keyPath)
	if !bytes.Equal(after, content) {
		t.Fatal("the existing key file changed")
	}
	for _, args := range [][]string{{"keygen"}, {"keygen", "-out", filepath.Join(dir, "x"), "-id", "bad id"}, {"keygen", "-out", filepath.Join(dir, "x"), "extra"}, {"keygen", "-out", filepath.Join(dir, "missing", "x")}} {
		if code, _, _ := invoke(t, args...); code == 0 {
			t.Errorf("%v succeeded", args)
		}
	}
}

func generated(t *testing.T) (dir, keyPath, pub string) {
	t.Helper()
	dir = t.TempDir()
	keyPath = filepath.Join(dir, "feed.key")
	code, stdout, stderr := invoke(t, "keygen", "-out", keyPath, "-id", "test-key")
	if code != 0 {
		t.Fatal(stderr)
	}
	return dir, keyPath, publicKeyFrom(t, stdout)
}

func TestSignThenVerify(t *testing.T) {
	dir, keyPath, pub := generated(t)
	in := payloadFile(t, dir, nil)
	out := filepath.Join(dir, "signed.json")
	code, stdout, stderr := invoke(t, "sign", "-key", keyPath, "-sequence", "7", "-in", in, "-out", out)
	if code != 0 {
		t.Fatalf("sign exit %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "sequence 7") {
		t.Fatalf("stdout = %q", stdout)
	}
	code, stdout, stderr = invoke(t, "verify", "-pub", pub, out)
	if code != 0 || !strings.Contains(stdout, "sequence 7") || !strings.Contains(stdout, "sesame-server stable 0.2.0") {
		t.Fatalf("verify exit %d: %s %s", code, stdout, stderr)
	}
	keys, _ := updates.ParseKeys(pub)
	body, _ := os.ReadFile(out)
	document, err := (updates.Verifier{Keys: keys, Registry: updates.DefaultRegistry()}).Verify(body, fixedNow)
	if err != nil || document.Payload.Sequence != 7 {
		t.Fatalf("the library cannot verify the tool's output: %v", err)
	}
	if info, err := os.Stat(out); err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("signed feed mode: %v %v", info, err)
	}
}

func TestVerifyRejectsTamperedWrongKeyAndExpiredFeeds(t *testing.T) {
	dir, keyPath, pub := generated(t)
	in := payloadFile(t, dir, nil)
	out := filepath.Join(dir, "signed.json")
	if code, _, stderr := invoke(t, "sign", "-key", keyPath, "-sequence", "3", "-in", in, "-out", out); code != 0 {
		t.Fatal(stderr)
	}
	body, _ := os.ReadFile(out)
	var envelope updates.Envelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	original, _ := base64.RawURLEncoding.DecodeString(envelope.Payload)
	envelope.Payload = base64.RawURLEncoding.EncodeToString([]byte(strings.Replace(string(original), "0.2.0", "9.9.9", 1)))
	tampered, _ := json.Marshal(envelope)
	tamperedPath := filepath.Join(dir, "tampered.json")
	if err := os.WriteFile(tamperedPath, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := invoke(t, "verify", "-pub", pub, tamperedPath); code == 0 || !strings.Contains(stderr, "signature") {
		t.Fatalf("tampered feed exit %d: %s", code, stderr)
	}
	_, _, otherPub := generated(t)
	if code, _, _ := invoke(t, "verify", "-pub", otherPub, out); code == 0 {
		t.Fatal("a feed verified under a different key")
	}
	var stdout, stderr bytes.Buffer
	late := func() time.Time { return fixedNow.Add(60 * 24 * time.Hour) }
	if code := run([]string{"verify", "-pub", pub, out}, &stdout, &stderr, late); code == 0 || !strings.Contains(stderr.String(), "expired") {
		t.Fatalf("expired feed exit %d: %s", code, stderr.String())
	}
	for _, args := range [][]string{{"verify"}, {"verify", "-pub", pub}, {"verify", "-pub", "garbage", out}, {"verify", "-pub", pub, filepath.Join(dir, "missing.json")}} {
		if code, _, _ := invoke(t, args...); code == 0 {
			t.Errorf("%v succeeded", args)
		}
	}
}

func TestSignRefusesInvalidPayloads(t *testing.T) {
	dir, keyPath, _ := generated(t)
	bad := map[string]func(*updates.Payload){
		"http binary url": func(p *updates.Payload) {
			release := p.Products[0].Channels["stable"]
			release.Binaries[0].URL = "http://downloads.example.net/sesame-server"
			p.Products[0].Channels["stable"] = release
		},
		"bad sha256": func(p *updates.Payload) {
			release := p.Products[0].Channels["stable"]
			release.Binaries[0].SHA256 = "abc"
			p.Products[0].Channels["stable"] = release
		},
		"bad image ref": func(p *updates.Payload) {
			release := p.Products[0].Channels["stable"]
			release.Images[0].Ref = "registry.example.net/sesame/server:latest"
			p.Products[0].Channels["stable"] = release
		},
		"wrong product": func(p *updates.Payload) { p.Products[0].ID = "other-product" },
		"too long":      func(p *updates.Payload) { p.ExpiresAt = p.IssuedAt.Add(46 * 24 * time.Hour) },
		"already expired": func(p *updates.Payload) {
			p.IssuedAt, p.ExpiresAt = fixedNow.Add(-48*time.Hour), fixedNow.Add(-time.Hour)
		},
		"issued in the future": func(p *updates.Payload) {
			p.IssuedAt, p.ExpiresAt = fixedNow.Add(time.Hour), fixedNow.Add(48*time.Hour)
		},
		"wrong schema":   func(p *updates.Payload) { p.SchemaVersion = 2 },
		"other sequence": func(p *updates.Payload) { p.Sequence = 4 },
	}
	for name, change := range bad {
		t.Run(name, func(t *testing.T) {
			in := payloadFile(t, t.TempDir(), change)
			out := filepath.Join(dir, strings.ReplaceAll(name, " ", "-")+".json")
			if code, _, _ := invoke(t, "sign", "-key", keyPath, "-sequence", "7", "-in", in, "-out", out); code == 0 {
				t.Fatal("sign accepted the payload")
			}
			if _, err := os.Stat(out); err == nil {
				t.Fatal("a refused payload still produced a file")
			}
		})
	}
	same := payloadFile(t, t.TempDir(), func(p *updates.Payload) { p.Sequence = 7 })
	if code, _, stderr := invoke(t, "sign", "-key", keyPath, "-sequence", "7", "-in", same, "-out", filepath.Join(dir, "same.json")); code != 0 {
		t.Fatalf("a matching sequence in the payload was refused: %s", stderr)
	}
}

func TestSignRefusesBadArgumentsAndKeys(t *testing.T) {
	dir, keyPath, _ := generated(t)
	in := payloadFile(t, dir, nil)
	out := filepath.Join(dir, "signed.json")
	for _, args := range [][]string{
		{"sign"},
		{"sign", "-key", keyPath, "-in", in, "-out", out},
		{"sign", "-key", keyPath, "-sequence", "0", "-in", in, "-out", out},
		{"sign", "-key", keyPath, "-sequence", "-2", "-in", in, "-out", out},
		{"sign", "-key", keyPath, "-sequence", "x", "-in", in, "-out", out},
		{"sign", "-key", filepath.Join(dir, "missing.key"), "-sequence", "1", "-in", in, "-out", out},
		{"sign", "-key", keyPath, "-sequence", "1", "-in", filepath.Join(dir, "missing.json"), "-out", out},
		{"sign", "-key", keyPath, "-sequence", "1", "-in", in, "-out", out, "extra"},
	} {
		if code, _, _ := invoke(t, args...); code == 0 {
			t.Errorf("%v succeeded", args)
		}
	}
	if err := os.Chmod(keyPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := invoke(t, "sign", "-key", keyPath, "-sequence", "1", "-in", in, "-out", out); code == 0 || !strings.Contains(stderr, "only its owner") {
		t.Fatalf("a world readable key was used: exit %d %s", code, stderr)
	}
	if err := os.Chmod(keyPath, 0o600); err != nil {
		t.Fatal(err)
	}
	garbled := filepath.Join(dir, "garbled.key")
	if err := os.WriteFile(garbled, []byte("test-key:not-a-seed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := invoke(t, "sign", "-key", garbled, "-sequence", "1", "-in", in, "-out", out); code == 0 {
		t.Fatal("a garbled key file was used")
	}
	invalidJSON := filepath.Join(dir, "invalid.json")
	for name, content := range map[string]string{"not json": "nope", "unknown field": `{"extra":1}`, "trailing": `{} {}`} {
		if err := os.WriteFile(invalidJSON, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if code, _, _ := invoke(t, "sign", "-key", keyPath, "-sequence", "1", "-in", invalidJSON, "-out", out); code == 0 {
			t.Errorf("%s: sign accepted the payload file", name)
		}
	}
	if code, _, _ := invoke(t, "sign", "-key", keyPath, "-sequence", "1", "-in", in, "-out", out); code != 0 {
		t.Fatal("setup: the first signing failed")
	}
	if code, _, stderr := invoke(t, "sign", "-key", keyPath, "-sequence", "2", "-in", in, "-out", out); code == 0 || !strings.Contains(stderr, "cannot be created") {
		t.Fatalf("sign overwrote an existing feed: exit %d %s", code, stderr)
	}
}

func TestUsageAndUnknownCommands(t *testing.T) {
	if code, _, stderr := invoke(t); code != 2 || !strings.Contains(stderr, "Usage") {
		t.Fatalf("no arguments exit %d %q", code, stderr)
	}
	if code, stdout, _ := invoke(t, "help"); code != 0 || !strings.Contains(stdout, "feedsign keygen") {
		t.Fatalf("help exit %d", code)
	}
	if code, _, stderr := invoke(t, "publish"); code != 2 || !strings.Contains(stderr, "unknown command") {
		t.Fatalf("unknown command exit %d %q", code, stderr)
	}
	if code, stdout, _ := invoke(t, "keygen", "-help"); code != 0 || !strings.Contains(stdout, "feedsign sign") {
		t.Fatalf("keygen -help exit %d %q", code, stdout)
	}
}

func TestSignRefusesAKeyBehindASymbolicLink(t *testing.T) {
	dir, keyPath, _ := generated(t)
	link := filepath.Join(dir, "link.key")
	if err := os.Symlink(keyPath, link); err != nil {
		t.Fatal(err)
	}
	in := payloadFile(t, dir, nil)
	out := filepath.Join(dir, "signed.json")
	if code, _, stderr := invoke(t, "sign", "-key", link, "-sequence", "1", "-in", in, "-out", out); code == 0 || !strings.Contains(stderr, "symbolic link") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if _, err := os.Stat(out); err == nil {
		t.Fatal("a feed was signed with a key behind a symbolic link")
	}
}

func TestSignRefusesRunawaySequences(t *testing.T) {
	dir, keyPath, _ := generated(t)
	in := payloadFile(t, dir, nil)
	cases := []struct {
		name string
		args []string
		ok   bool
	}{
		{"next number", []string{"-sequence", "13", "-previous", "12"}, true},
		{"same number", []string{"-sequence", "12", "-previous", "12"}, true},
		{"exactly 1000 above", []string{"-sequence", "1012", "-previous", "12"}, true},
		{"1001 above", []string{"-sequence", "1013", "-previous", "12"}, false},
		{"lower than previous", []string{"-sequence", "11", "-previous", "12"}, false},
		{"first feed", []string{"-sequence", "1", "-previous", "0"}, true},
		{"first feed far ahead", []string{"-sequence", "2000", "-previous", "0"}, false},
		{"negative previous", []string{"-sequence", "5", "-previous", "-1"}, false},
		{"no previous given allows a jump", []string{"-sequence", "5000"}, true},
		{"one billion", []string{"-sequence", "1000000000"}, true},
		{"above one billion", []string{"-sequence", "1000000001"}, false},
		{"above one billion with previous", []string{"-sequence", "1000000001", "-previous", "1000000000"}, false},
		{"int64 maximum", []string{"-sequence", "9223372036854775807"}, false},
	}
	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := filepath.Join(dir, "signed-"+strings.ReplaceAll(tc.name, " ", "-")+".json")
			args := append([]string{"sign", "-key", keyPath, "-in", in, "-out", out}, tc.args...)
			code, _, stderr := invoke(t, args...)
			if (code == 0) != tc.ok {
				t.Fatalf("case %d: exit %d: %s", index, code, stderr)
			}
			if _, err := os.Stat(out); (err == nil) != tc.ok {
				t.Fatalf("output file present = %v, want %v", err == nil, tc.ok)
			}
		})
	}
}
