//go:build unix

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestSignedFeedIsWorldReadableEvenWithAStrictUmask(t *testing.T) {
	dir, keyPath, _ := generated(t)
	in := payloadFile(t, dir, nil)
	out := filepath.Join(dir, "signed.json")
	previous := syscall.Umask(0o077)
	defer syscall.Umask(previous)
	if code, _, stderr := invoke(t, "sign", "-key", keyPath, "-sequence", "1", "-in", in, "-out", out); code != 0 {
		t.Fatal(stderr)
	}
	if info, err := os.Stat(out); err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("signed feed mode: %v %v", info, err)
	}
}
