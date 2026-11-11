//go:build linux

package diagnostics

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestSpaceRemainsUnknownWithoutDatabaseSize(t *testing.T) {
	cases := []string{"symlink", "wrong type", "path changed", "metadata unavailable"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			files := testFiles(1<<40, nil)
			switch name {
			case "symlink":
				target := filepath.Join(f.root, "fictional-database")
				if err := os.Rename(f.databasePath, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, f.databasePath); err != nil {
					t.Fatal(err)
				}
			case "wrong type":
				if err := os.Remove(f.databasePath); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(f.databasePath, 0o700); err != nil {
					t.Fatal(err)
				}
			case "path changed", "metadata unavailable":
				calls := 0
				files.lstat = func(path string) (fs.FileInfo, error) {
					if path == f.databasePath {
						calls++
						if name == "metadata unavailable" {
							return nil, fs.ErrPermission
						}
						if calls == 2 {
							return nil, fs.ErrNotExist
						}
					}
					return os.Lstat(path)
				}
			}
			before := snapshotTree(t, f.root)
			report := f.inspectWith(files)
			assertCheck(t, report, "space", StateUnknown, "space_database_unknown")
			assertSameTree(t, before, snapshotTree(t, f.root))
		})
	}
}

func TestSpaceUsesZeroForAnObservedMissingOrEmptyDatabase(t *testing.T) {
	for _, missing := range []bool{false, true} {
		name := "empty regular file"
		if missing {
			name = "missing file"
		}
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			if missing {
				if err := os.Remove(f.databasePath); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Truncate(f.databasePath, 0); err != nil {
				t.Fatal(err)
			}
			report := f.inspectWith(testFiles(256*mebibyte, nil))
			assertCheck(t, report, "space", StateOK, "space_sufficient")
		})
	}
}
