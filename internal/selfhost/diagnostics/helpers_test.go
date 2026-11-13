package diagnostics

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"usesesame.app/backend/internal/selfhost/config"
)

const (
	keyMarker         = "KEYMARKER"
	databaseMarker    = "DBMARKER"
	configMarker      = "PASSWORDMARKER"
	configUserMarker  = "FICTIONALUSERMARKER"
	environmentMarker = "ENVPASSWORDMARKER"
	fictionalDatabase = databaseMarker + "-FICTIONAL-DATABASE"
)

type fixture struct {
	t            *testing.T
	root         string
	dataDir      string
	secretsDir   string
	configPath   string
	databasePath string
	env          map[string]string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	mustMkdir(t, dataDir, 0o700)
	f := &fixture{
		t:            t,
		root:         root,
		dataDir:      dataDir,
		secretsDir:   filepath.Join(dataDir, "secrets"),
		configPath:   filepath.Join(dataDir, "config.json"),
		databasePath: filepath.Join(dataDir, "sesame.db"),
		env:          map[string]string{config.EnvDataDir: dataDir},
	}
	f.writeConfig(`{"publicUrl":"https://vault.example.test","smtp":{"addr":"smtp.example.test:587","from":"noreply@example.test","username":"` + configUserMarker + `","password":"` + configMarker + `"}}`)
	mustMkdir(t, f.secretsDir, 0o700)
	for index, name := range instanceKeyFiles {
		mustWrite(t, filepath.Join(f.secretsDir, name), markerKey(index), 0o600)
	}
	mustWrite(t, f.databasePath, []byte(fictionalDatabase), 0o600)
	t.Cleanup(func() {
		makeWritable(root)
	})
	return f
}

func markerKey(index int) []byte {
	prefix := []byte(fmt.Sprintf("%s-%d-", keyMarker, index))
	value := make([]byte, 32)
	copy(value, prefix)
	for i := len(prefix); i < len(value); i++ {
		value[i] = 'x'
	}
	return value
}

func (f *fixture) writeConfig(document string) {
	f.t.Helper()
	mustWrite(f.t, f.configPath, []byte(document), 0o600)
}

func (f *fixture) lookup() config.Lookup {
	return func(name string) (string, bool) {
		value, ok := f.env[name]
		return value, ok
	}
}

func (f *fixture) inspect() Report {
	f.t.Helper()
	return inspect(context.Background(), Options{
		Lookup:      f.lookup(),
		ToolVersion: "0.4.1-test",
		Now:         fixedNow,
	}, testFiles(1<<40, nil))
}

func (f *fixture) inspectWith(files fileSystem) Report {
	f.t.Helper()
	return inspect(context.Background(), Options{
		Lookup:      f.lookup(),
		ToolVersion: "0.4.1-test",
		Now:         fixedNow,
	}, files)
}

func fixedNow() time.Time {
	return time.Date(2026, 11, 7, 12, 34, 56, 789_000_000, time.FixedZone("CET", 2*60*60))
}

func testFiles(available uint64, spaceErr error) fileSystem {
	files := osFileSystem
	files.availableSpace = func(*os.File) (uint64, error) {
		return available, spaceErr
	}
	return files
}

func mustMkdir(t *testing.T, path string, mode fs.FileMode) {
	t.Helper()
	if err := os.Mkdir(path, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path string, data []byte, mode fs.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func makeWritable(root string) {
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			_ = os.Chmod(path, 0o700)
		}
		return nil
	})
}

func checkByID(t *testing.T, report Report, id string) Check {
	t.Helper()
	for _, check := range report.Checks {
		if check.ID == id {
			return check
		}
	}
	t.Fatalf("check %q is missing from %v", id, checkIDs(report))
	return Check{}
}

func checkIDs(report Report) []string {
	ids := make([]string, 0, len(report.Checks))
	for _, check := range report.Checks {
		ids = append(ids, check.ID)
	}
	return ids
}

func assertCheck(t *testing.T, report Report, id, state, code string) Check {
	t.Helper()
	check := checkByID(t, report, id)
	if check.State != state || check.Code != code {
		t.Fatalf("check %s state and code = %s/%s, want %s/%s", id, check.State, check.Code, state, code)
	}
	return check
}

func assertNoPrivateMarkers(t *testing.T, value string) {
	t.Helper()
	for _, marker := range []string{
		keyMarker,
		databaseMarker,
		configMarker,
		configUserMarker,
		environmentMarker,
	} {
		if strings.Contains(value, marker) {
			t.Fatalf("output contains private marker %q", marker)
		}
	}
}

type treeEntry struct {
	Mode    fs.FileMode
	Size    int64
	ModTime int64
	Content string
	Link    string
}

func snapshotTree(t *testing.T, root string) map[string]treeEntry {
	t.Helper()
	snapshot := map[string]treeEntry{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		item := treeEntry{Mode: info.Mode(), Size: info.Size(), ModTime: info.ModTime().UnixNano()}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			item.Link = target
		case info.Mode().IsRegular() && info.Mode().Perm()&0o400 != 0:
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(content)
			item.Content = hex.EncodeToString(sum[:])
		}
		snapshot[relative] = item
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func assertSameTree(t *testing.T, before, after map[string]treeEntry) {
	t.Helper()
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("filesystem tree changed\nbefore: %#v\nafter:  %#v", before, after)
	}
}
