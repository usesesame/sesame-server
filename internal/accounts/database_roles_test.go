package accounts

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func ensureApplicationRole(ctx context.Context, db *sql.DB) error {
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, runtimeRoleName).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	_, err := db.ExecContext(ctx, `CREATE ROLE `+runtimeRoleName+` LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE`)
	return err
}

func insufficientPrivilege(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && postgresError.Code == "42501"
}

func TestApplicationRoleCannotRewriteTheAuditLog(t *testing.T) {
	databaseURL := os.Getenv("SESAME_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("SESAME_TEST_DATABASE_URL is required")
	}
	ctx := context.Background()
	store, err := OpenWithoutMigrate(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := ensureApplicationRole(ctx, store.db); err != nil {
		t.Fatalf("ensure the application role: %v", err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate with the application role present: %v", err)
	}
	if err := store.ReconcileRuntimeRole(ctx); err != nil {
		t.Fatalf("reconcile the application role: %v", err)
	}
	conn, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatalf("reserve a test connection: %v", err)
	}
	t.Cleanup(func() {
		_, _ = conn.ExecContext(context.Background(), `RESET ROLE`)
		_ = conn.Close()
	})
	t.Cleanup(func() {
		_, _ = store.db.ExecContext(context.Background(), `ALTER TABLE sesame_admin_audit_log DISABLE TRIGGER USER`)
		_, _ = store.db.ExecContext(context.Background(), `DELETE FROM sesame_admin_audit_log WHERE admin_email = 'fixture@example.test'`)
		_, _ = store.db.ExecContext(context.Background(), `ALTER TABLE sesame_admin_audit_log ENABLE TRIGGER USER`)
	})
	if _, err := conn.ExecContext(ctx, `SET ROLE `+runtimeRoleName); err != nil {
		t.Fatalf("assume the application role: %v", err)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO sesame_admin_audit_log (admin_email, action, target_type) VALUES ('fixture@example.test', 'fixture.action', 'fixture')`); err != nil {
		t.Fatalf("the application role must still insert audit rows: %v", err)
	}
	var count int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM sesame_admin_audit_log WHERE admin_email = 'fixture@example.test'`).Scan(&count); err != nil {
		t.Fatalf("the application role must still read audit rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("audit row count = %d, want 1", count)
	}
	for _, statement := range []string{
		`UPDATE sesame_admin_audit_log SET action = 'rewritten' WHERE admin_email = 'fixture@example.test'`,
		`DELETE FROM sesame_admin_audit_log WHERE admin_email = 'fixture@example.test'`,
		`TRUNCATE sesame_admin_audit_log`,
		`TRUNCATE sesame_sync_audit`,
		`DROP TABLE sesame_admin_audit_log`,
	} {
		if _, err := conn.ExecContext(ctx, statement); err == nil {
			t.Fatalf("the application role ran %q", statement)
		} else if !insufficientPrivilege(err) {
			t.Fatalf("%q failed without a privilege error: %v", statement, err)
		}
	}
}
