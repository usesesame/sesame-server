package accounts

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"
)

func lifecycleTestStore(t *testing.T) (*PostgresStore, *sql.DB) {
	t.Helper()
	databaseURL := os.Getenv("SESAME_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("SESAME_TEST_DATABASE_URL is required")
	}
	accountStore, err := Open(context.Background(), databaseURL)
	if err != nil {
		t.Fatalf("open account store: %v", err)
	}
	t.Cleanup(func() { _ = accountStore.Close() })
	const lockID int64 = 762374924
	conn, err := accountStore.DB().Conn(context.Background())
	if err != nil {
		t.Fatalf("reserve test database connection: %v", err)
	}
	if _, err := conn.ExecContext(context.Background(), `SELECT pg_advisory_lock($1)`, lockID); err != nil {
		_ = conn.Close()
		t.Fatalf("lock test database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, lockID)
		_ = conn.Close()
	})
	if _, err := accountStore.DB().ExecContext(context.Background(), `
		TRUNCATE sesame_support_requests, sesame_support_messages, sesame_support_notes,
		         sesame_accounts, sesame_sessions, sesame_email_outbox
		RESTART IDENTITY CASCADE
	`); err != nil {
		t.Fatalf("clear lifecycle tables: %v", err)
	}
	return accountStore, accountStore.DB()
}

func TestCloseAndReopenSupportTicketLifecycle(t *testing.T) {
	store, db := lifecycleTestStore(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `INSERT INTO sesame_accounts (id, email, password_hash) VALUES ('acct-close', 'close-user@example.invalid', 'test')`); err != nil {
		t.Fatalf("create account: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO sesame_support_requests (id, account_id, email, subject, message) VALUES ('ticket-acct', 'acct-close', 'close-user@example.invalid', 'Subject', 'Body')`); err != nil {
		t.Fatalf("create ticket: %v", err)
	}
	detail, err := store.CloseSupportTicket(ctx, "acct-close", "ticket-acct", time.Now().UTC())
	if err != nil {
		t.Fatalf("close ticket: %v", err)
	}
	if detail.Status != "closed" {
		t.Fatalf("status = %q, want closed", detail.Status)
	}
	if !detail.CanReopen {
		t.Fatal("a freshly closed ticket should be reopenable inside the window")
	}
	detail, err = store.ReopenSupportTicket(ctx, "acct-close", "ticket-acct", time.Now().UTC())
	if err != nil {
		t.Fatalf("reopen ticket: %v", err)
	}
	if detail.Status != "open" {
		t.Fatalf("status = %q, want open", detail.Status)
	}
	if _, err := store.CloseSupportTicket(ctx, "acct-other", "ticket-acct", time.Now().UTC()); err == nil {
		t.Fatal("a different account closed someone else's ticket")
	}
}
