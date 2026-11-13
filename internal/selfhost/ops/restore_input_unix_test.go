//go:build unix

package ops

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecoverDoesNotFollowRestoreInputLinks(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "private-backup.tar")
	writeFile(t, outside, []byte("operator archive"), 0o600)
	before := fileDigest(t, outside)
	link := filepath.Join(dir, restoreInputPrefix+"123")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := stageRestoreInput(context.Background(), dir, strings.NewReader("interrupted archive"), DefaultLimits()); err != nil {
		t.Fatal(err)
	}
	if _, err := Recover(dir); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("recovery changed the link: %v, %v", info, err)
	}
	if fileDigest(t, outside) != before {
		t.Fatal("recovery changed the external archive")
	}
}
