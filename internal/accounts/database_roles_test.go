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

func seedAuditChainTables(ctx context.Context, db *sql.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS sesame_admin_audit_chain_head (
			singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
			head_seq BIGINT NOT NULL,
			head_hash BYTEA NOT NULL,
			CONSTRAINT sesame_admin_audit_chain_head_hash_length CHECK (octet_length(head_hash) = 32)
		)`,
		`INSERT INTO sesame_admin_audit_chain_head (singleton, head_seq, head_hash)
		 VALUES (TRUE, 0, decode(repeat('00', 32), 'hex'))
		 ON CONFLICT (singleton) DO NOTHING`,
		`CREATE TABLE IF NOT EXISTS sesame_admin_audit_checkpoints (
			id BIGSERIAL PRIMARY KEY,
			cover_seq BIGINT NOT NULL UNIQUE,
			chain_hash BYTEA NOT NULL,
			key_id TEXT NOT NULL,
			signed_at TIMESTAMPTZ NOT NULL,
			signature BYTEA NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			CONSTRAINT sesame_admin_audit_checkpoints_chain_hash_length CHECK (octet_length(chain_hash) = 32),
			CONSTRAINT sesame_admin_audit_checkpoints_signature_length CHECK (octet_length(signature) = 64)
		)`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
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
	lockConnection, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatalf("reserve the fixture lock connection: %v", err)
	}
	const fixtureLockID int64 = 762374923
	if _, err := lockConnection.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, fixtureLockID); err != nil {
		_ = lockConnection.Close()
		t.Fatalf("lock the shared test database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = lockConnection.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, fixtureLockID)
		_ = lockConnection.Close()
	})
	if err := ensureApplicationRole(ctx, store.db); err != nil {
		t.Fatalf("ensure the application role: %v", err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate with the application role present: %v", err)
	}
	if err := store.ReconcileRuntimeRole(ctx); err != nil {
		t.Fatalf("reconcile the application role on the migrated database: %v", err)
	}
	if err := seedAuditChainTables(ctx, store.db); err != nil {
		t.Fatalf("seed the audit chain tables: %v", err)
	}
	if err := store.ReconcileRuntimeRole(ctx); err != nil {
		t.Fatalf("reconcile the application role after seeding the audit chain tables: %v", err)
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
		_, _ = store.db.ExecContext(context.Background(), `DELETE FROM sesame_admin_audit_checkpoints WHERE key_id = 'fixture-key'`)
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
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO sesame_admin_audit_checkpoints (cover_seq, chain_hash, key_id, signed_at, signature)
		VALUES (0, decode(repeat('ab', 32), 'hex'), 'fixture-key', NOW(), decode(repeat('cd', 64), 'hex'))
		ON CONFLICT (cover_seq) DO NOTHING`); err != nil {
		t.Fatalf("the application role must still insert checkpoints: %v", err)
	}
	for _, statement := range []string{
		`UPDATE sesame_admin_audit_log SET action = 'rewritten' WHERE admin_email = 'fixture@example.test'`,
		`DELETE FROM sesame_admin_audit_log WHERE admin_email = 'fixture@example.test'`,
		`TRUNCATE sesame_admin_audit_log`,
		`TRUNCATE sesame_sync_audit`,
		`DROP TABLE sesame_admin_audit_log`,
		`UPDATE sesame_admin_audit_chain_head SET head_seq = 99 WHERE singleton`,
		`DELETE FROM sesame_admin_audit_chain_head WHERE singleton`,
		`TRUNCATE sesame_admin_audit_chain_head`,
		`UPDATE sesame_admin_audit_checkpoints SET key_id = 'rewritten' WHERE key_id = 'fixture-key'`,
		`DELETE FROM sesame_admin_audit_checkpoints WHERE key_id = 'fixture-key'`,
	} {
		if _, err := conn.ExecContext(ctx, statement); err == nil {
			t.Fatalf("the application role ran %q", statement)
		} else if !insufficientPrivilege(err) {
			t.Fatalf("%q failed without a privilege error: %v", statement, err)
		}
	}
}
