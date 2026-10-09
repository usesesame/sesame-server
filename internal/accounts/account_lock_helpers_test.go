package accounts

import (
	"bytes"
	"context"
	"database/sql"
	"testing"
	"time"
)

func seedSecurityAccount(t *testing.T, db *sql.DB, id string, verified bool) {
	t.Helper()
	clearFixtureAccount(t, db, id)
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO sesame_accounts (id, email, password_hash, email_verified_at)
		VALUES ($1, $2, 'fictional-current-hash', CASE WHEN $3::boolean THEN NOW() ELSE NULL END)
	`, id, id+"@example.invalid", verified); err != nil {
		t.Fatalf("seed account %s: %v", id, err)
	}
}

func waitForBlockedStatement(t *testing.T, db *sql.DB, fragment string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var waiting int
		if err := db.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM pg_stat_activity
			WHERE datname = current_database() AND pid <> pg_backend_pid() AND wait_event_type = 'Lock' AND query LIKE $1
		`, "%"+fragment+"%").Scan(&waiting); err != nil {
			t.Fatalf("inspect waiting statements: %v", err)
		}
		if waiting > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no statement containing %q is waiting on a lock", fragment)
}

func awaitResult[T any](t *testing.T, results <-chan T) T {
	t.Helper()
	select {
	case value := <-results:
		return value
	case <-time.After(10 * time.Second):
		t.Fatal("the blocked operation did not finish after the lock was released")
		var zero T
		return zero
	}
}

func holdAccountLock(t *testing.T, db *sql.DB, accountID, statement string) *sql.Tx {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin the competing transaction: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	if _, err := tx.ExecContext(context.Background(), statement, accountID); err != nil {
		t.Fatalf("take the account lock: %v", err)
	}
	return tx
}

func waitForBlockedCount(t *testing.T, db *sql.DB, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var waiting int
		if err := db.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM pg_stat_activity
			WHERE datname = current_database() AND pid <> pg_backend_pid() AND wait_event_type = 'Lock'
		`).Scan(&waiting); err != nil {
			t.Fatalf("inspect waiting statements: %v", err)
		}
		if waiting >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("fewer than %d statements are waiting on a lock", want)
}

func confirmEmailChange(store *PostgresStore, tokenHash []byte, fill byte) (EmailChangeResult, error) {
	now := time.Now().UTC()
	return store.ConfirmEmailChangeAndRotateSession(context.Background(), TokenSessionRotation{
		TokenHash: tokenHash, SessionTokenHash: bytes.Repeat([]byte{fill}, 32),
		SessionExpiresAt: now.Add(time.Hour), SessionLabel: "Browser", AuthenticatedAt: now,
	})
}

func accountEmailOf(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var email string
	if err := db.QueryRowContext(context.Background(), `SELECT email FROM sesame_accounts WHERE id = $1`, id).Scan(&email); err != nil {
		t.Fatalf("read account email: %v", err)
	}
	return email
}
