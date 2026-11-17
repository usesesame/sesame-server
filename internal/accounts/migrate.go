package accounts

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

const runtimeRoleName = "sesame_app"

const migrationLockID int64 = 0x534553414D45

const migrationLockRetry = 200 * time.Millisecond

const concurrentIndexDirective = "-- sesame-migration: create-index-concurrently"

var (
	migrationDirectivePattern = regexp.MustCompile(`(?im)^[ \t]*--[ \t]*sesame-migration`)
	concurrentIndexPattern    = regexp.MustCompile(`^CREATE\s+(?:UNIQUE\s+)?INDEX\s+CONCURRENTLY\s+IF\s+NOT\s+EXISTS\s+([a-z_][a-z0-9_]{0,62})\s+ON\s+[a-z_][a-z0-9_]{0,62}(?:\s|\()`)
)

type migration struct {
	version         string
	sql             string
	concurrentIndex string
}

func parseMigration(version, body string) (migration, error) {
	m := migration{version: version, sql: body}
	directives := migrationDirectivePattern.FindAllStringIndex(body, -1)
	if len(directives) == 0 {
		return m, nil
	}
	firstLine, rest, _ := strings.Cut(body, "\n")
	if len(directives) != 1 || directives[0][0] != 0 || firstLine != concurrentIndexDirective {
		return migration{}, fmt.Errorf("migration %s: the only supported directive is %q on the first line", version, concurrentIndexDirective)
	}
	statement := strings.TrimSpace(rest)
	statement = strings.TrimSpace(strings.TrimSuffix(statement, ";"))
	if strings.Contains(statement, ";") {
		return migration{}, fmt.Errorf("migration %s: a concurrent index migration must hold exactly one statement", version)
	}
	if strings.Contains(statement, "--") || strings.Contains(statement, "/*") {
		return migration{}, fmt.Errorf("migration %s: a concurrent index migration must not hold comments", version)
	}
	match := concurrentIndexPattern.FindStringSubmatch(statement)
	if match == nil {
		return migration{}, fmt.Errorf("migration %s: a concurrent index migration must be one CREATE INDEX CONCURRENTLY IF NOT EXISTS statement", version)
	}
	m.sql = statement
	m.concurrentIndex = match[1]
	return m, nil
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
		m, err := parseMigration(strings.TrimSuffix(name, ".sql"), string(body))
		if err != nil {
			return nil, err
		}
		migrations = append(migrations, m)
	}
	return migrations, nil
}

func (s *PostgresStore) Migrate(ctx context.Context) error {
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	return s.runMigrations(ctx, migrations)
}

func (s *PostgresStore) runMigrations(ctx context.Context, migrations []migration) error {
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
	for {
		var locked bool
		if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, migrationLockID).Scan(&locked); err != nil {
			return fmt.Errorf("acquire migration lock: %w", err)
		}
		if locked {
			return nil
		}
		timer := time.NewTimer(migrationLockRetry)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("acquire migration lock: %w", ctx.Err())
		case <-timer.C:
		}
	}
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
	if m.concurrentIndex != "" {
		return applyConcurrentIndex(ctx, conn, m)
	}
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

func applyConcurrentIndex(ctx context.Context, conn *sql.Conn, m migration) error {
	exists, valid, err := indexState(ctx, conn, m.concurrentIndex)
	if err != nil {
		return err
	}
	if exists && !valid {
		if _, err := conn.ExecContext(ctx, `DROP INDEX CONCURRENTLY IF EXISTS `+pgx.Identifier{m.concurrentIndex}.Sanitize()); err != nil {
			return fmt.Errorf("drop the invalid index %s: %w", m.concurrentIndex, err)
		}
	}
	if _, err := conn.ExecContext(ctx, m.sql); err != nil {
		return err
	}
	exists, valid, err = indexState(ctx, conn, m.concurrentIndex)
	if err != nil {
		return err
	}
	if !exists || !valid {
		return fmt.Errorf("index %s is missing or invalid after the build", m.concurrentIndex)
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO sesame_schema_migrations (version) VALUES ($1)`, m.version)
	return err
}

func indexState(ctx context.Context, conn *sql.Conn, name string) (exists, valid bool, err error) {
	err = conn.QueryRowContext(ctx, `
		SELECT indisvalid AND indisready FROM pg_index
		WHERE indexrelid = to_regclass($1::text)`, pgx.Identifier{name}.Sanitize()).Scan(&valid)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("inspect index %s: %w", name, err)
	}
	return true, valid, nil
}
