//go:build unix

package ops

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func alternateGroup(t *testing.T) int {
	t.Helper()
	groups, err := os.Getgroups()
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range groups {
		if group != os.Getgid() {
			return group
		}
	}
	if os.Geteuid() == 0 {
		return os.Getgid() + 1
	}
	t.Fatal("the test user needs a supplementary group to observe ownership changes")
	return 0
}

func groupOf(t *testing.T, path string) int {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return int(info.Sys().(*syscall.Stat_t).Gid)
}

func outsideTree(t *testing.T) (staging, outsideFile, outsideDir string) {
	t.Helper()
	base := t.TempDir()
	staging = filepath.Join(base, "staging")
	outsideDir = filepath.Join(base, "outside")
	outsideFile = filepath.Join(outsideDir, "victim")
	for _, dir := range []string{staging, outsideDir} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(staging, "inside"), []byte("inside"), 0o600)
	writeFile(t, outsideFile, []byte("victim"), 0o600)
	return staging, outsideFile, outsideDir
}

func TestOwnTreeDoesNotFollowSymlinksOutOfTheTree(t *testing.T) {
	staging, outsideFile, outsideDir := outsideTree(t)
	if err := os.Symlink(outsideFile, filepath.Join(staging, "file-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(staging, "dir-link")); err != nil {
		t.Fatal(err)
	}
	group := alternateGroup(t)
	before := groupOf(t, outsideFile)

	if err := ownTree(staging, os.Getuid(), group); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{outsideFile, outsideDir} {
		if got := groupOf(t, path); got != before {
			t.Fatalf("%s changed group from %d to %d through a symlink", path, before, got)
		}
	}
	for _, name := range []string{"inside", "file-link", "dir-link"} {
		if got := groupOf(t, filepath.Join(staging, name)); got != group {
			t.Fatalf("%s has group %d, want %d", name, got, group)
		}
	}
}

func TestSyncTreeRefusesSymlinksOutOfTheTree(t *testing.T) {
	staging, outsideFile, outsideDir := outsideTree(t)
	if err := syncTree(staging); err != nil {
		t.Fatalf("a plain tree failed to sync: %v", err)
	}
	for name, target := range map[string]string{"file-link": outsideFile, "dir-link": outsideDir} {
		link := filepath.Join(staging, name)
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if err := syncTree(staging); err == nil {
			t.Fatalf("syncTree followed %s out of the tree", name)
		}
		if err := os.Remove(link); err != nil {
			t.Fatal(err)
		}
	}
}
