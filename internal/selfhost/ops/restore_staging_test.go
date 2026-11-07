package ops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func abandonedRestoreStaging(t *testing.T, dir string) string {
	t.Helper()
	staging := filepath.Join(dir, stagingPrefix+"012345abcdef")
	if err := os.Mkdir(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(staging, "unfinished"), []byte("fictional staged data"), 0o600)
	return staging
}

func TestRecoverRemovesAbandonedRestoreStaging(t *testing.T) {
	dir := t.TempDir()
	staging := abandonedRestoreStaging(t, dir)
	if _, err := Recover(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(staging); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("abandoned staging remains: %v", err)
	}
}

func TestRecoverPreservesLockedRestoreStaging(t *testing.T) {
	dir := t.TempDir()
	staging := abandonedRestoreStaging(t, dir)
	lock, err := AcquireInstanceLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	if _, err := Recover(dir); !errors.Is(err, ErrInstanceRunning) {
		t.Fatalf("staging recovery ignored the active lock: %v", err)
	}
	if _, err := os.Lstat(staging); err != nil {
		t.Fatalf("locked staging changed: %v", err)
	}
}

func TestNextRestoreRemovesAbandonedStaging(t *testing.T) {
	oldInst, _, archive, _, _ := restoreFixture(t)
	staging := abandonedRestoreStaging(t, oldInst.dir)
	if _, err := Restore(context.Background(), archive, oldInst.dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(staging); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the next restore left abandoned staging: %v", err)
	}
}

func TestRecoverPreservesEntriesOutsideStagingPattern(t *testing.T) {
	dir := t.TempDir()
	abandonedRestoreStaging(t, dir)
	names := []string{stagingPrefix + "notes", stagingPrefix + "012345abcde", stagingPrefix + "012345abcdeg", stagingPrefix + "012345abcdef0"}
	for _, name := range names {
		if err := os.Mkdir(filepath.Join(dir, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	regular := stagingPrefix + "abcdef012345"
	writeFile(t, filepath.Join(dir, regular), []byte("operator file"), 0o600)
	names = append(names, regular)
	if _, err := Recover(dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if _, err := os.Lstat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("recovery changed unrelated entry %s: %v", name, err)
		}
	}
}
