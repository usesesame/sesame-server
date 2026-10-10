package sqlitestore

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"usesesame.app/backend/internal/selfhost"
	"usesesame.app/backend/internal/selfhost/storetest"
)

func options(config storetest.Config) Options {
	return Options{
		Path:     config.Path,
		AdminKey: config.AdminKey,
		Now:      config.Now,
		Flags:    config.Flags,
		ReadOnly: config.ReadOnly,
	}
}

func rawDatabase(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func harness() storetest.Harness {
	return storetest.Harness{
		Open: func(t *testing.T, config storetest.Config) selfhost.Store {
			t.Helper()
			store, err := Open(context.Background(), options(config))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			return store
		},
		TryOpen: func(config storetest.Config) (selfhost.Store, error) {
			store, err := Open(context.Background(), options(config))
			if err != nil {
				return nil, err
			}
			return store, nil
		},
		Raw:                  rawDatabase,
		InterruptedMigration: interruptedMigration,
	}
}

func TestStoreBehaviour(t *testing.T) {
	storetest.Run(t, harness())
}

var failingSecondMigration = migration{
	version: len(defaultMigrations) + 1,
	name:    "0099_broken",
	script:  `CREATE TABLE partial_table (id INTEGER PRIMARY KEY); INSERT INTO partial_table (id) VALUES (1); INSERT INTO missing_table (id) VALUES (1);`,
}

var workingSecondMigration = migration{
	version: len(defaultMigrations) + 1,
	name:    "0099_extra",
	script:  `CREATE TABLE extra_table (id INTEGER PRIMARY KEY); INSERT INTO extra_table (id) VALUES (1);`,
}

func tableCount(t *testing.T, db *sql.DB, name string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func storedVersion(t *testing.T, db *sql.DB) int {
	t.Helper()
	var version int
	if err := db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

func interruptedMigration(t *testing.T, path string, key []byte) {
	t.Helper()
	ctx := context.Background()
	config := Options{Path: path, AdminKey: key}
	t.Run("failure on an existing database rolls back", func(t *testing.T) {
		store, err := Open(ctx, config)
		if err != nil {
			t.Fatal(err)
		}
		member, err := store.CreateMember(ctx, selfhost.SystemActor, "Sam")
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		broken := append(append([]migration(nil), defaultMigrations...), failingSecondMigration)
		if _, err := openWith(ctx, config, broken); err == nil || !strings.Contains(err.Error(), "0099_broken") {
			t.Fatalf("expected the broken migration to fail, got %v", err)
		}
		db := rawDatabase(t, path)
		if tableCount(t, db, "partial_table") != 0 || storedVersion(t, db) != len(defaultMigrations) {
			t.Fatal("a failed migration left partial changes")
		}
		reopened, err := Open(ctx, config)
		if err != nil {
			t.Fatalf("database is not usable after the failed migration: %v", err)
		}
		members, err := reopened.ListMembers(ctx)
		if err != nil || len(members) != 1 || members[0].ID != member.ID {
			t.Fatalf("data lost: %v %+v", err, members)
		}
		if err := reopened.Close(); err != nil {
			t.Fatal(err)
		}
		fixed := append(append([]migration(nil), defaultMigrations...), workingSecondMigration)
		upgraded, err := openWith(ctx, config, fixed)
		if err != nil {
			t.Fatal(err)
		}
		defer upgraded.Close()
		if tableCount(t, db, "extra_table") != 1 || storedVersion(t, db) != len(defaultMigrations)+1 {
			t.Fatal("the repaired migration did not apply")
		}
		if _, err := Open(ctx, config); !errors.Is(err, selfhost.ErrSchemaTooNew) {
			t.Fatalf("the older binary must refuse the newer schema, got %v", err)
		}
	})
	t.Run("failure on a fresh database leaves nothing behind", func(t *testing.T) {
		fresh := filepath.Join(filepath.Dir(path), "fresh.db")
		broken := []migration{{version: 1, name: "0001_broken", script: `CREATE TABLE half (id INTEGER); INSERT INTO nowhere VALUES (1);`}}
		if _, err := openWith(ctx, Options{Path: fresh, AdminKey: key}, broken); err == nil {
			t.Fatal("expected failure")
		}
		db := rawDatabase(t, fresh)
		if tableCount(t, db, "half") != 0 || tableCount(t, db, "schema_migrations") != 0 && storedVersion(t, db) != 0 {
			t.Fatal("a failed first migration left partial changes")
		}
		store, err := Open(ctx, Options{Path: fresh, AdminKey: key})
		if err != nil {
			t.Fatalf("fresh database is not usable after a failed first migration: %v", err)
		}
		defer store.Close()
	})
	t.Run("a cancelled context stops the migration cleanly", func(t *testing.T) {
		cancelled := filepath.Join(filepath.Dir(path), "cancelled.db")
		stop, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := Open(stop, Options{Path: cancelled, AdminKey: key}); err == nil {
			t.Fatal("expected failure")
		}
		store, err := Open(ctx, Options{Path: cancelled, AdminKey: key})
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
	})
}

func TestMigrationBacksUpBeforeUpgrading(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "sesame.db")
	key := bytes.Repeat([]byte{1}, 32)
	store, err := Open(ctx, Options{Path: path, AdminKey: key})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateMember(ctx, selfhost.SystemActor, "Sam"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	backups := filepath.Join(dir, "backups")
	fixed := append(append([]migration(nil), defaultMigrations...), workingSecondMigration)
	upgraded, err := openWith(ctx, Options{Path: path, AdminKey: key, MigrationBackupDir: backups}, fixed)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	entries, err := os.ReadDir(backups)
	if err != nil || len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), fmt.Sprintf("pre-migration-v%d-", len(defaultMigrations))) {
		t.Fatalf("unexpected backups %v %v", entries, err)
	}
	copyDB := rawDatabase(t, filepath.Join(backups, entries[0].Name()))
	if storedVersion(t, copyDB) != len(defaultMigrations) || tableCount(t, copyDB, "extra_table") != 0 {
		t.Fatal("the pre migration backup must hold the old schema")
	}
	fresh := filepath.Join(dir, "fresh.db")
	again, err := openWith(ctx, Options{Path: fresh, AdminKey: key, MigrationBackupDir: filepath.Join(dir, "none")}, fixed)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if _, err := os.Stat(filepath.Join(dir, "none")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a fresh database needs no pre migration backup")
	}
}

func TestConnectionSettings(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sesame.db")
	store, err := Open(ctx, Options{Path: path, AdminKey: bytes.Repeat([]byte{1}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	pragmas := map[string]string{
		"journal_mode":   "wal",
		"foreign_keys":   "1",
		"busy_timeout":   "5000",
		"synchronous":    "2",
		"trusted_schema": "0",
	}
	for name, want := range pragmas {
		var got string
		if err := store.db.QueryRowContext(ctx, "PRAGMA "+name).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("PRAGMA %s = %s, want %s", name, got, want)
		}
	}
	if stats := store.db.Stats(); stats.MaxOpenConnections != 1 {
		t.Fatalf("expected a single connection, got %d", stats.MaxOpenConnections)
	}
	var integrity string
	if err := store.db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("integrity check: %v %s", err, integrity)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO devices (id, token_hash, name, owner_id, member_id, created_at, expires_at, last_seen_at) VALUES ('d', X'01', 'n', 'missing', NULL, 0, 0, 0)`); err == nil {
		t.Fatal("foreign keys are not enforced")
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO members (id, name, name_key, created_at) VALUES ('m', 'n', 'n', 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO devices (id, token_hash, name, owner_id, member_id, created_at, expires_at, last_seen_at) VALUES ('d', X'01', 'n', 'x', 'm', 0, 0, 0)`); err == nil {
		t.Fatal("a device with two holders was accepted")
	}
}

func TestEmbeddedMigrationsAreContiguous(t *testing.T) {
	if len(defaultMigrations) == 0 {
		t.Fatal("no migrations embedded")
	}
	for index, item := range defaultMigrations {
		if item.version != index+1 || strings.TrimSpace(item.script) == "" {
			t.Fatalf("bad migration %+v", item)
		}
	}
}

func TestUpdateSettingsMigrationKeepsExistingData(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "sesame.db")
	key := bytes.Repeat([]byte{1}, 32)
	old, err := openWith(ctx, Options{Path: path, AdminKey: key}, defaultMigrations[:1])
	if err != nil {
		t.Fatal(err)
	}
	member, err := old.CreateMember(ctx, selfhost.SystemActor, "Sam")
	if err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	backups := filepath.Join(dir, "backups")
	store, err := Open(ctx, Options{Path: path, AdminKey: key, MigrationBackupDir: backups})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	members, err := store.ListMembers(ctx)
	if err != nil || len(members) != 1 || members[0].ID != member.ID {
		t.Fatalf("members after migration = %+v, %v", members, err)
	}
	settings, err := store.UpdateSettings(ctx)
	if err != nil || settings.Choice != selfhost.UpdatesUnset || settings.Channel != "stable" || len(settings.Sequences) != 0 {
		t.Fatalf("settings after migration = %+v, %v", settings, err)
	}
	if entries, err := os.ReadDir(backups); err != nil || len(entries) != 1 {
		t.Fatalf("migration backups = %v, %v", entries, err)
	}
	report, err := store.Check(ctx)
	if err != nil || !report.OK() {
		t.Fatalf("check = %+v, %v", report, err)
	}
}

func TestUpdateSettingsTableRejectsBadValues(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, Options{Path: filepath.Join(t.TempDir(), "sesame.db"), AdminKey: bytes.Repeat([]byte{1}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, statement := range []string{
		`UPDATE update_settings SET enabled = 'maybe'`,
		`UPDATE update_settings SET highest_sequence = -1`,
		`UPDATE update_settings SET channel = ''`,
		`INSERT INTO update_settings (singleton) VALUES (2)`,
	} {
		if _, err := store.db.ExecContext(ctx, statement); err == nil {
			t.Errorf("%s was accepted", statement)
		}
	}
}
