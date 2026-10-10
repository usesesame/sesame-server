package sqlitestore

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"usesesame.app/backend/internal/selfhost"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type migration struct {
	version int
	name    string
	script  string
}

var defaultMigrations = mustLoadMigrations()

func mustLoadMigrations() []migration {
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		panic(err)
	}
	var loaded []migration
	for _, entry := range entries {
		name := strings.TrimSuffix(entry.Name(), ".sql")
		prefix, _, found := strings.Cut(name, "_")
		version, err := strconv.Atoi(prefix)
		if !found || err != nil || version < 1 {
			panic("invalid migration file name " + entry.Name())
		}
		script, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			panic(err)
		}
		loaded = append(loaded, migration{version: version, name: name, script: string(script)})
	}
	sort.Slice(loaded, func(i, j int) bool { return loaded[i].version < loaded[j].version })
	for index, item := range loaded {
		if item.version != index+1 {
			panic("migration versions must be contiguous from 1")
		}
	}
	return loaded
}

func (s *Store) storedVersion(ctx context.Context, db queryer) (int, error) {
	var exists int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'`).Scan(&exists); err != nil {
		return 0, err
	}
	if exists == 0 {
		return 0, nil
	}
	var version sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		return 0, err
	}
	return int(version.Int64), nil
}

func (s *Store) requireCurrentSchema(ctx context.Context, migrations []migration) error {
	stored, err := s.storedVersion(ctx, s.db)
	if err != nil {
		return fmt.Errorf("sqlitestore: read schema version: %w", err)
	}
	supported := len(migrations)
	if stored > supported {
		return selfhost.SchemaTooNewError{Stored: stored, Supported: supported}
	}
	if stored < supported {
		return fmt.Errorf("sqlitestore: database schema version %d is older than %d and needs a migration before it can be opened read only", stored, supported)
	}
	return nil
}

func (s *Store) migrate(ctx context.Context, migrations []migration, backupDir string) error {
	supported := len(migrations)
	stored, err := s.storedVersion(ctx, s.db)
	if err != nil {
		return fmt.Errorf("sqlitestore: read schema version: %w", err)
	}
	if stored > supported {
		return selfhost.SchemaTooNewError{Stored: stored, Supported: supported}
	}
	if stored == supported {
		return nil
	}
	if stored > 0 && backupDir != "" {
		name := fmt.Sprintf("pre-migration-v%d-%d.db", stored, s.clock().Unix())
		if err := s.backupTo(ctx, filepath.Join(backupDir, name)); err != nil {
			return fmt.Errorf("sqlitestore: back up before migration: %w", err)
		}
	}
	for _, item := range migrations {
		if err := s.applyMigration(ctx, item, supported); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) applyMigration(ctx context.Context, item migration, supported int) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at INTEGER NOT NULL) STRICT`); err != nil {
			return fmt.Errorf("sqlitestore: prepare migrations table: %w", err)
		}
		current, err := s.storedVersion(ctx, tx)
		if err != nil {
			return fmt.Errorf("sqlitestore: read schema version: %w", err)
		}
		if current > supported {
			return selfhost.SchemaTooNewError{Stored: current, Supported: supported}
		}
		if current >= item.version {
			return nil
		}
		if current != item.version-1 {
			return errors.New("sqlitestore: migration versions are not contiguous")
		}
		if _, err := tx.ExecContext(ctx, item.script); err != nil {
			return fmt.Errorf("sqlitestore: apply migration %s: %w", item.name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`, item.version, item.name, s.clock().Unix()); err != nil {
			return fmt.Errorf("sqlitestore: record migration %s: %w", item.name, err)
		}
		return nil
	})
}
