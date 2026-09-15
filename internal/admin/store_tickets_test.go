package admin

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"usesesame.app/backend/internal/accounts"
)

func ticketTestStore(t *testing.T) (*Store, *sql.DB) {
	t.Helper()
	databaseURL := os.Getenv("SESAME_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("SESAME_TEST_DATABASE_URL is required")
	}
	accountStore, err := accounts.Open(context.Background(), databaseURL)
	if err != nil {
		t.Fatalf("open account store: %v", err)
	}
	t.Cleanup(func() { _ = accountStore.Close() })
	lockReleaseTests(t, accountStore.DB())
	if _, err := accountStore.DB().ExecContext(context.Background(), `
		TRUNCATE sesame_support_requests, sesame_support_messages, sesame_support_notes,
		         sesame_admin_audit_log, sesame_admin_sessions, sesame_admin_accounts
		RESTART IDENTITY CASCADE
	`); err != nil {
		t.Fatalf("clear support tables: %v", err)
	}
	store, err := Open(context.Background(), databaseURL, bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatalf("open admin store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, accountStore.DB()
}

func TestSetTicketStatusClosesAndRestoresOpenState(t *testing.T) {
	store, db := ticketTestStore(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `INSERT INTO sesame_admin_accounts (id, email, password_hash, role, totp_verified) VALUES ('close-test', 'close@example.invalid', 'test', 'support', TRUE)`); err != nil {
		t.Fatalf("create admin: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO sesame_support_requests (id, email, subject, message) VALUES ('ticket-test', 'user@example.invalid', 'Subject', 'Body')`); err != nil {
		t.Fatalf("create ticket: %v", err)
	}
	actor := Account{ID: "close-test", Email: "close@example.invalid", Role: RoleSupport}

	if err := store.SetTicketStatus(ctx, actor, "ticket-test", TicketClosed, "test-ip"); err != nil {
		t.Fatalf("close ticket: %v", err)
	}
	var status string
	var closedAt, reopenUntil sql.NullTime
	var closedBy sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT status, closed_at, closed_by, account_reopen_until FROM sesame_support_requests WHERE id = 'ticket-test'`).Scan(&status, &closedAt, &closedBy, &reopenUntil); err != nil {
		t.Fatalf("read closed ticket: %v", err)
	}
	if status != string(TicketClosed) || !closedAt.Valid || !closedBy.Valid || closedBy.String != actor.ID || !reopenUntil.Valid {
		t.Fatalf("closed ticket = status %q closedAt %v closedBy %v reopenUntil %v", status, closedAt, closedBy, reopenUntil)
	}
	if reopenUntil.Time.Before(closedAt.Time.Add(29 * 24 * time.Hour)) {
		t.Fatalf("reopen window = %v, want about 30 days after close", reopenUntil.Time)
	}

	if err := store.SetTicketStatus(ctx, actor, "ticket-test", TicketOpen, "test-ip"); err != nil {
		t.Fatalf("reopen ticket: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT status, closed_at, closed_by, account_reopen_until FROM sesame_support_requests WHERE id = 'ticket-test'`).Scan(&status, &closedAt, &closedBy, &reopenUntil); err != nil {
		t.Fatalf("read reopened ticket: %v", err)
	}
	if status != string(TicketOpen) || closedAt.Valid || closedBy.Valid || reopenUntil.Valid {
		t.Fatalf("reopened ticket = status %q closedAt %v closedBy %v reopenUntil %v", status, closedAt, closedBy, reopenUntil)
	}

	if err := store.SetTicketStatus(ctx, actor, "ticket-test", "bogus", "test-ip"); err == nil {
		t.Fatal("invalid status accepted")
	}
}
