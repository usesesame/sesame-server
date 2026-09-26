package accounts

import (
	"bytes"
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
	const lockID int64 = 762374923
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
		TRUNCATE sesame_support_requests, sesame_support_messages, sesame_support_notes
		RESTART IDENTITY CASCADE
	`); err != nil {
		t.Fatalf("clear lifecycle tables: %v", err)
	}
	return accountStore, accountStore.DB()
}

func clearFixtureAccount(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `DELETE FROM sesame_accounts WHERE id = $1`, id); err != nil {
		t.Fatalf("clear fixture account %s: %v", id, err)
	}
	t.Cleanup(func() {
		if _, err := db.ExecContext(context.Background(), `DELETE FROM sesame_accounts WHERE id = $1`, id); err != nil {
			t.Error(err)
		}
	})
}

func TestCloseAndReopenSupportTicketLifecycle(t *testing.T) {
	store, db := lifecycleTestStore(t)
	ctx := context.Background()
	clearFixtureAccount(t, db, "acct-close")
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

func TestPurgeExpiredSupportAccessLinks(t *testing.T) {
	store, db := lifecycleTestStore(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `INSERT INTO sesame_support_requests (id, email, subject, message) VALUES ('ticket-purge', 'purge@example.invalid', 'Subject', 'Body')`); err != nil {
		t.Fatalf("create ticket: %v", err)
	}
	liveHash := bytes.Repeat([]byte{1}, 32)
	expiredHash := bytes.Repeat([]byte{2}, 32)
	revokedHash := bytes.Repeat([]byte{3}, 32)
	for _, link := range []struct {
		hash      []byte
		expiresAt string
		revoked   bool
	}{
		{hash: liveHash, expiresAt: "NOW() + INTERVAL '1 hour'"},
		{hash: expiredHash, expiresAt: "NOW() - INTERVAL '1 hour'"},
		{hash: revokedHash, expiresAt: "NOW() + INTERVAL '1 hour'", revoked: true},
	} {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO sesame_support_access_links (token_hash, ticket_id, requester_email, expires_at, revoked_at)
			VALUES ($1, 'ticket-purge', 'purge@example.invalid', `+link.expiresAt+`, CASE WHEN $2 THEN NOW() ELSE NULL END)
		`, link.hash, link.revoked); err != nil {
			t.Fatalf("insert access link: %v", err)
		}
	}
	if err := store.PurgeExpired(ctx); err != nil {
		t.Fatalf("purge expired: %v", err)
	}
	var links int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sesame_support_access_links`).Scan(&links); err != nil {
		t.Fatalf("count access links: %v", err)
	}
	if links != 1 {
		t.Fatalf("access links after purge = %d, want only the live one", links)
	}
	var live bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sesame_support_access_links WHERE token_hash = $1)`, liveHash).Scan(&live); err != nil {
		t.Fatalf("read live link: %v", err)
	}
	if !live {
		t.Fatal("the purge removed a live link")
	}
}
