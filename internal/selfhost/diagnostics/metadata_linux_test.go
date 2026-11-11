//go:build linux

package diagnostics

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestConfigFileDisappearingMetadata(t *testing.T) {
	f := newFixture(t)
	files := testFiles(1<<40, nil)
	calls := 0
	files.lstat = func(path string) (fs.FileInfo, error) {
		if path == f.configPath {
			calls++
			if calls == 1 {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
		}
		return os.Lstat(path)
	}
	report := f.inspectWith(files)
	assertCheck(t, report, "config_file", StateUnknown, "path_changed")
}

func TestDataDirectoryChangingMetadata(t *testing.T) {
	f := newFixture(t)
	files := testFiles(1<<40, nil)
	calls := 0
	files.lstat = func(path string) (fs.FileInfo, error) {
		if path == f.dataDir {
			calls++
			if calls == 2 {
				if err := os.Chmod(path, 0o755); err != nil {
					t.Fatal(err)
				}
			}
		}
		return os.Lstat(path)
	}
	report := f.inspectWith(files)
	assertCheck(t, report, "data_directory", StateUnknown, "path_changed")
	assertCheck(t, report, "secrets", StateUnknown, "metadata_unavailable")
	assertCheck(t, report, "database", StateUnknown, "metadata_unavailable")
	assertCheck(t, report, "space", StateUnknown, "space_unavailable")
}

func TestSecretFileChangingMetadata(t *testing.T) {
	f := newFixture(t)
	files := testFiles(1<<40, nil)
	calls := 0
	keyPath := filepath.Join(f.secretsDir, "signing.key")
	files.lstat = func(path string) (fs.FileInfo, error) {
		if path == keyPath {
			calls++
			if calls == 2 {
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatal(err)
				}
			}
		}
		return os.Lstat(path)
	}
	report := f.inspectWith(files)
	assertCheck(t, report, "secrets", StateUnknown, "path_changed")
}

func TestDatabaseChangingMetadata(t *testing.T) {
	f := newFixture(t)
	files := testFiles(1<<40, nil)
	calls := 0
	files.lstat = func(path string) (fs.FileInfo, error) {
		if path == f.databasePath {
			calls++
			if calls == 2 {
				changed := time.Now().Add(time.Hour)
				if err := os.Chtimes(path, changed, changed); err != nil {
					t.Fatal(err)
				}
			}
		}
		return os.Lstat(path)
	}
	report := f.inspectWith(files)
	assertCheck(t, report, "database", StateUnknown, "path_changed")
}

func TestSecretFIFOIsNotOpened(t *testing.T) {
	f := newFixture(t)
	fifo := filepath.Join(f.secretsDir, "signing.key")
	if err := os.Remove(fifo); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan Report, 1)
	go func() {
		done <- inspect(context.Background(), Options{
			Lookup:      f.lookup(),
			ToolVersion: "0.4.1-test",
			Now:         fixedNow,
		}, testFiles(1<<40, nil))
	}()
	select {
	case report := <-done:
		assertCheck(t, report, "secrets", StateAction, "secrets_wrong_type")
	case <-time.After(2 * time.Second):
		t.Fatal("inspection blocked on a key FIFO")
	}
}

func TestDataDirectoryDisappearingMetadata(t *testing.T) {
	f := newFixture(t)
	files := testFiles(1<<40, nil)
	calls := 0
	files.lstat = func(path string) (fs.FileInfo, error) {
		if path == f.dataDir {
			calls++
			if calls == 2 {
				if err := os.RemoveAll(path); err != nil {
					t.Fatal(err)
				}
			}
		}
		return os.Lstat(path)
	}
	report := f.inspectWith(files)
	assertCheck(t, report, "data_directory", StateUnknown, "path_changed")
}

func TestSecretsDirectoryDisappearingMetadata(t *testing.T) {
	f := newFixture(t)
	files := testFiles(1<<40, nil)
	calls := 0
	files.lstat = func(path string) (fs.FileInfo, error) {
		if path == f.secretsDir {
			calls++
			if calls == 2 {
				if err := os.RemoveAll(path); err != nil {
					t.Fatal(err)
				}
			}
		}
		return os.Lstat(path)
	}
	report := f.inspectWith(files)
	assertCheck(t, report, "secrets", StateUnknown, "path_changed")
}

func TestSecretFileDisappearingMetadata(t *testing.T) {
	f := newFixture(t)
	files := testFiles(1<<40, nil)
	calls := 0
	keyPath := filepath.Join(f.secretsDir, "signing.key")
	files.lstat = func(path string) (fs.FileInfo, error) {
		if path == keyPath {
			calls++
			if calls == 2 {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
		}
		return os.Lstat(path)
	}
	report := f.inspectWith(files)
	assertCheck(t, report, "secrets", StateUnknown, "path_changed")
}

func TestDatabaseDisappearingMetadata(t *testing.T) {
	f := newFixture(t)
	files := testFiles(1<<40, nil)
	calls := 0
	files.lstat = func(path string) (fs.FileInfo, error) {
		if path == f.databasePath {
			calls++
			if calls == 2 {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
		}
		return os.Lstat(path)
	}
	report := f.inspectWith(files)
	assertCheck(t, report, "database", StateUnknown, "path_changed")
}

func TestConfigFileMetadataUnavailable(t *testing.T) {
	f := newFixture(t)
	files := testFiles(1<<40, nil)
	files.lstat = func(path string) (fs.FileInfo, error) {
		if path == f.configPath {
			return nil, fs.ErrPermission
		}
		return os.Lstat(path)
	}
	report := f.inspectWith(files)
	assertCheck(t, report, "config_file", StateUnknown, "metadata_unavailable")
}

func TestConfigFileSecondMetadataError(t *testing.T) {
	f := newFixture(t)
	files := testFiles(1<<40, nil)
	calls := 0
	files.lstat = func(path string) (fs.FileInfo, error) {
		if path == f.configPath {
			calls++
			if calls == 2 {
				return nil, fs.ErrPermission
			}
		}
		return os.Lstat(path)
	}
	report := f.inspectWith(files)
	assertCheck(t, report, "config_file", StateUnknown, "metadata_unavailable")
}

func TestDataDirectoryMetadataUnavailable(t *testing.T) {
	f := newFixture(t)
	files := testFiles(1<<40, nil)
	files.lstat = func(path string) (fs.FileInfo, error) {
		if path == f.dataDir {
			return nil, fs.ErrPermission
		}
		return os.Lstat(path)
	}
	report := f.inspectWith(files)
	assertCheck(t, report, "data_directory", StateUnknown, "metadata_unavailable")
	assertCheck(t, report, "secrets", StateUnknown, "metadata_unavailable")
	assertCheck(t, report, "database", StateUnknown, "metadata_unavailable")
	assertCheck(t, report, "space", StateUnknown, "space_unavailable")
}

func TestSecretsDirectoryMetadataUnavailable(t *testing.T) {
	f := newFixture(t)
	files := testFiles(1<<40, nil)
	files.lstat = func(path string) (fs.FileInfo, error) {
		if path == f.secretsDir {
			return nil, fs.ErrPermission
		}
		return os.Lstat(path)
	}
	report := f.inspectWith(files)
	assertCheck(t, report, "secrets", StateUnknown, "metadata_unavailable")
}

func TestSecretFileMetadataUnavailable(t *testing.T) {
	f := newFixture(t)
	files := testFiles(1<<40, nil)
	keyPath := filepath.Join(f.secretsDir, "signing.key")
	files.lstat = func(path string) (fs.FileInfo, error) {
		if path == keyPath {
			return nil, fs.ErrPermission
		}
		return os.Lstat(path)
	}
	report := f.inspectWith(files)
	assertCheck(t, report, "secrets", StateUnknown, "metadata_unavailable")
}

func TestDatabaseMetadataUnavailable(t *testing.T) {
	f := newFixture(t)
	files := testFiles(1<<40, nil)
	files.lstat = func(path string) (fs.FileInfo, error) {
		if path == f.databasePath {
			return nil, fs.ErrPermission
		}
		return os.Lstat(path)
	}
	report := f.inspectWith(files)
	assertCheck(t, report, "database", StateUnknown, "metadata_unavailable")
}

func TestMissingSecretsWithUnknownDatabase(t *testing.T) {
	f := newFixture(t)
	if err := os.RemoveAll(f.secretsDir); err != nil {
		t.Fatal(err)
	}
	files := testFiles(1<<40, nil)
	files.lstat = func(path string) (fs.FileInfo, error) {
		if path == f.databasePath {
			return nil, fs.ErrPermission
		}
		return os.Lstat(path)
	}
	report := f.inspectWith(files)
	assertCheck(t, report, "secrets", StateUnknown, "metadata_unavailable")
	assertCheck(t, report, "database", StateUnknown, "metadata_unavailable")
}

func TestMissingSecretFileWithUnknownDatabase(t *testing.T) {
	f := newFixture(t)
	if err := os.Remove(filepath.Join(f.secretsDir, "signing.key")); err != nil {
		t.Fatal(err)
	}
	files := testFiles(1<<40, nil)
	files.lstat = func(path string) (fs.FileInfo, error) {
		if path == f.databasePath {
			return nil, fs.ErrPermission
		}
		return os.Lstat(path)
	}
	report := f.inspectWith(files)
	assertCheck(t, report, "secrets", StateUnknown, "metadata_unavailable")
}
