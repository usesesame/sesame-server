//go:build linux

package diagnostics

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"usesesame.app/backend/internal/selfhost/config"
)

func TestCancellationIsInterruptedWithoutWrites(t *testing.T) {
	f := newFixture(t)
	before := snapshotTree(t, f.root)

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	report := Inspect(canceled, Options{
		Lookup:      f.lookup(),
		ToolVersion: "0.4.1-test",
		Now:         fixedNow,
	})
	if report.Overall != StateUnknown {
		t.Fatalf("overall = %q", report.Overall)
	}
	if report.Config != nil {
		t.Fatal("config is not null")
	}
	if len(report.Checks) != 1 {
		t.Fatalf("checks = %v", checkIDs(report))
	}
	assertCheck(t, report, "inspection", StateUnknown, "interrupted")
	assertSameTree(t, before, snapshotTree(t, f.root))
}

func TestCancellationDuringConfigurationIsInterruptedWithoutWrites(t *testing.T) {
	f := newFixture(t)
	before := snapshotTree(t, f.root)
	ctx, cancel := context.WithCancel(context.Background())
	lookup := func(name string) (string, bool) {
		if name == config.EnvDataDir {
			cancel()
		}
		return f.lookup()(name)
	}
	report := inspect(ctx, Options{
		Lookup:      lookup,
		ToolVersion: "0.4.1-test",
		Now:         fixedNow,
	}, testFiles(1<<40, nil))
	if report.Overall != StateUnknown {
		t.Fatalf("overall = %q", report.Overall)
	}
	if report.Config != nil {
		t.Fatal("config is not null")
	}
	if len(report.Checks) != 1 {
		t.Fatalf("checks = %v", checkIDs(report))
	}
	assertCheck(t, report, "inspection", StateUnknown, "interrupted")
	assertSameTree(t, before, snapshotTree(t, f.root))
}

func TestReadOnlyTreeIsUnchanged(t *testing.T) {
	f := newFixture(t)
	if err := os.Chmod(f.configPath, 0o400); err != nil {
		t.Fatal(err)
	}
	for _, name := range instanceKeyFiles {
		if err := os.Chmod(filepath.Join(f.secretsDir, name), 0o400); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(f.databasePath, 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(f.secretsDir, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(f.dataDir, 0o500); err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, f.root)
	report := f.inspectWith(testFiles(1<<40, nil))
	after := snapshotTree(t, f.root)
	assertSameTree(t, before, after)
	assertCheck(t, report, "config_file", StateOK, "config_file_private")
	assertCheck(t, report, "data_directory", StateOK, "data_directory_private")
	assertCheck(t, report, "secrets", StateOK, "secrets_metadata_ok")
	assertCheck(t, report, "database", StateUnknown, "database_not_opened")
	for _, name := range []string{"sesame.db-wal", "sesame.db-shm", "sesame.lock"} {
		if _, err := os.Lstat(filepath.Join(f.dataDir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s exists after inspection", name)
		}
	}
	entries, err := os.ReadDir(f.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != "config.json" && entry.Name() != "secrets" && entry.Name() != "sesame.db" {
			t.Fatalf("unexpected data directory entry %q", entry.Name())
		}
	}
}
