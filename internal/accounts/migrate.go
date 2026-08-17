package accounts

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type migration struct {
	version string
	sql     string
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	migrations := make([]migration, 0, len(names))
	for _, name := range names {
		body, err := migrationFiles.ReadFile("migrations/" + name)
		if err != nil {
			return nil, err
		}
		migrations = append(migrations, migration{
			version: strings.TrimSuffix(name, ".sql"),
			sql:     string(body),
		})
	}
	return migrations, nil
}

func (s *PostgresStore) Migrate(ctx context.Context) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	const migrationLockID int64 = 0x534553414D45
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, migrationLockID); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() {
		unlockContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = conn.ExecContext(unlockContext, `SELECT pg_advisory_unlock($1)`, migrationLockID)
	}()

	if _, err := conn.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS sesame_schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`); err != nil {
		return err
	}
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	for _, m := range migrations {
		applied, err := migrationApplied(ctx, conn, m.version)
		if err != nil {
			return err
		}
		if applied {
			continue
		}
		if err := applyMigration(ctx, conn, m); err != nil {
			return fmt.Errorf("apply migration %s: %w", m.version, err)
		}
	}
	return nil
}

func migrationApplied(ctx context.Context, conn *sql.Conn, version string) (bool, error) {
	var exists bool
	err := conn.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM sesame_schema_migrations WHERE version = $1)`, version).Scan(&exists)
	return exists, err
}

func applyMigration(ctx context.Context, conn *sql.Conn, m migration) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO sesame_schema_migrations (version) VALUES ($1)`, m.version); err != nil {
		return err
	}
	return tx.Commit()
}
