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

const runtimeRoleName = "sesame_app"

const migrationLockID int64 = 0x534553414D45

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
	if err := lockMigration(ctx, conn); err != nil {
		return err
	}
	defer unlockMigration(conn)

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

func lockMigration(ctx context.Context, conn *sql.Conn) error {
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, migrationLockID); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	return nil
}

func unlockMigration(conn *sql.Conn) {
	unlockContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = conn.ExecContext(unlockContext, `SELECT pg_advisory_unlock($1)`, migrationLockID)
}

func (s *PostgresStore) ReconcileRuntimeRole(ctx context.Context) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := lockMigration(ctx, conn); err != nil {
		return err
	}
	defer unlockMigration(conn)
	return reconcileRuntimeRole(ctx, conn)
}

func reconcileRuntimeRole(ctx context.Context, conn *sql.Conn) error {
	var exists bool
	if err := conn.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, runtimeRoleName).Scan(&exists); err != nil {
		return fmt.Errorf("check the application role: %w", err)
	}
	if !exists {
		return nil
	}
	statements := []string{
		`GRANT USAGE ON SCHEMA public TO ` + runtimeRoleName,
		`GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO ` + runtimeRoleName,
		`GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO ` + runtimeRoleName,
		`REVOKE CREATE ON SCHEMA public FROM ` + runtimeRoleName,
		`REVOKE UPDATE, DELETE, TRUNCATE ON TABLE sesame_admin_audit_log FROM ` + runtimeRoleName,
		`REVOKE UPDATE, DELETE, TRUNCATE ON TABLE sesame_sync_audit FROM ` + runtimeRoleName,
		`DO $$
		BEGIN
			IF to_regclass('public.sesame_admin_audit_chain_head') IS NOT NULL THEN
				REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON TABLE sesame_admin_audit_chain_head FROM ` + runtimeRoleName + `;
			END IF;
			IF to_regclass('public.sesame_admin_audit_checkpoints') IS NOT NULL THEN
				REVOKE UPDATE, DELETE ON TABLE sesame_admin_audit_checkpoints FROM ` + runtimeRoleName + `;
			END IF;
		END $$`,
		`ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO ` + runtimeRoleName,
		`ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO ` + runtimeRoleName,
	}
	for _, statement := range statements {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("reconcile the application role: %w", err)
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
