package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"usesesame.app/backend/internal/accounts"
	adminstore "usesesame.app/backend/internal/admin"
)

func supportEmailDatabase(t *testing.T) *sql.DB {
	t.Helper()
	databaseURL := os.Getenv("SESAME_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("SESAME_TEST_DATABASE_URL is required")
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatalf("ping test database: %v", err)
	}
	return db
}

func newSupportMigrationSchema(t *testing.T, db *sql.DB) *sql.Conn {
	t.Helper()
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("reserve test connection: %v", err)
	}
	schema := fmt.Sprintf("sesame_support_email_%d", time.Now().UnixNano())
	if _, err := conn.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		_ = conn.Close()
		t.Fatalf("create test schema: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "SET search_path TO "+schema); err != nil {
		_, _ = conn.ExecContext(ctx, "DROP SCHEMA "+schema+" CASCADE")
		_ = conn.Close()
		t.Fatalf("set test search path: %v", err)
	}
	t.Cleanup(func() {
		_, _ = conn.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		_ = conn.Close()
	})
	return conn
}

func applySupportMigrationsThrough(t *testing.T, conn *sql.Conn, version string) {
	t.Helper()
	dir := filepath.Join("..", "accounts", "migrations")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".sql") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		if len(name) < 4 || name[:4] > version {
			break
		}
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read migration %s: %v", name, err)
		}
		if _, err := conn.ExecContext(context.Background(), string(body)); err != nil {
			t.Fatalf("apply migration %s: %v", name, err)
		}
	}
}

func seedSupportMigrationAccount(t *testing.T, conn *sql.Conn, accountID string, supportReplies bool) {
	t.Helper()
	ctx := context.Background()
	if _, err := conn.ExecContext(ctx, `INSERT INTO sesame_accounts (id, email, password_hash) VALUES ($1, $2, 'fictional-unused-hash')`, accountID, accountID+"@example.invalid"); err != nil {
		t.Fatalf("create account %s: %v", accountID, err)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO sesame_account_notification_preferences (account_id, support_replies) VALUES ($1, $2)`, accountID, supportReplies); err != nil {
		t.Fatalf("create preferences %s: %v", accountID, err)
	}
}

func TestSupportEmailMigrationOnFreshDatabase(t *testing.T) {
	conn := newSupportMigrationSchema(t, supportEmailDatabase(t))
	applySupportMigrationsThrough(t, conn, "0039")
	ctx := context.Background()

	var columnDefault string
	if err := conn.QueryRowContext(ctx, `
		SELECT column_default FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND table_name = 'sesame_account_notification_preferences'
		  AND column_name = 'support_replies'
	`).Scan(&columnDefault); err != nil {
		t.Fatalf("read support_replies default: %v", err)
	}
	if !strings.Contains(strings.ToLower(columnDefault), "true") {
		t.Fatalf("support_replies default = %q, want true", columnDefault)
	}

	seedSupportMigrationAccount(t, conn, "fresh-account-default", true)
	if _, err := conn.ExecContext(ctx, `INSERT INTO sesame_accounts (id, email, password_hash) VALUES ('default-account', 'default@example.invalid', 'fictional-unused-hash')`); err != nil {
		t.Fatalf("create default account: %v", err)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO sesame_account_notification_preferences (account_id) VALUES ('default-account')`); err != nil {
		t.Fatalf("create default preferences: %v", err)
	}
	var enabled bool
	if err := conn.QueryRowContext(ctx, `SELECT support_replies FROM sesame_account_notification_preferences WHERE account_id = 'default-account'`).Scan(&enabled); err != nil {
		t.Fatalf("read default preferences: %v", err)
	}
	if !enabled {
		t.Fatal("a fresh preference row must default support replies on")
	}

	for _, kind := range []string{"support-receipt", "support-staff-notify", "support-reply"} {
		if _, err := conn.ExecContext(ctx, `
			INSERT INTO sesame_email_outbox (kind, to_email, action_url, expires_at, subject, body)
			VALUES ($1, 'recipient@example.invalid', 'https://account.example.invalid/support', NOW() + INTERVAL '7 days', 'Subject', 'Body')
		`, kind); err != nil {
			t.Fatalf("outbox message kind %s: %v", kind, err)
		}
	}
}

func TestSupportEmailMigrationUpgradesExplicitOptOut(t *testing.T) {
	conn := newSupportMigrationSchema(t, supportEmailDatabase(t))
	applySupportMigrationsThrough(t, conn, "0038")
	seedSupportMigrationAccount(t, conn, "opt-out-account", false)
	seedSupportMigrationAccount(t, conn, "opt-in-account", true)

	body, err := os.ReadFile(filepath.Join("..", "accounts", "migrations", "0039_support_email_delivery.sql"))
	if err != nil {
		t.Fatalf("read migration 0039: %v", err)
	}
	if _, err := conn.ExecContext(context.Background(), string(body)); err != nil {
		t.Fatalf("apply migration 0039: %v", err)
	}

	rows, err := conn.QueryContext(context.Background(), `SELECT account_id, support_replies FROM sesame_account_notification_preferences ORDER BY account_id`)
	if err != nil {
		t.Fatalf("read preferences: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var accountID string
		var supportReplies bool
		if err := rows.Scan(&accountID, &supportReplies); err != nil {
			t.Fatalf("scan preferences: %v", err)
		}
		if !supportReplies {
			t.Fatalf("account %s still has support replies off after migration 0039", accountID)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate preferences: %v", err)
	}
}

func TestSupportReplyEmailEnqueueFailureRollsBack(t *testing.T) {
	databaseURL := os.Getenv("SESAME_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("SESAME_TEST_DATABASE_URL is required")
	}
	ctx := context.Background()
	accountStore, err := accounts.Open(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open account store: %v", err)
	}
	t.Cleanup(func() { _ = accountStore.Close() })
	lockDatabaseTests(t, accountStore.DB())
	if _, err := accountStore.DB().ExecContext(ctx, `
		TRUNCATE sesame_email_outbox, sesame_support_requests, sesame_support_messages, sesame_support_notes,
		         sesame_admin_audit_log, sesame_admin_sessions, sesame_admin_accounts
		RESTART IDENTITY CASCADE
	`); err != nil {
		t.Fatalf("clear support tables: %v", err)
	}
	adminService, err := adminstore.Open(ctx, databaseURL, bytes.Repeat([]byte{5}, 32))
	if err != nil {
		t.Fatalf("open admin store: %v", err)
	}
	t.Cleanup(func() { _ = adminService.Close() })
	if _, err := accountStore.DB().ExecContext(ctx, `INSERT INTO sesame_admin_accounts (id, email, password_hash, role, totp_verified) VALUES ('support-email', 'staff@example.invalid', 'test', 'support', TRUE)`); err != nil {
		t.Fatalf("create admin: %v", err)
	}
	if _, err := accountStore.DB().ExecContext(ctx, `INSERT INTO sesame_support_requests (id, email, subject, message) VALUES ('ticket-support-email', 'requester@example.invalid', 'Fictional subject', 'Fictional message')`); err != nil {
		t.Fatalf("create ticket: %v", err)
	}
	actor := adminstore.Account{ID: "support-email", Email: "staff@example.invalid", Role: adminstore.RoleSupport}
	replyEmail := &adminstore.TicketReplyEmail{
		To:        "requester@example.invalid",
		Subject:   "Sesame support replied to your request",
		Body:      "A Sesame support specialist replied to your request.",
		ActionURL: "https://account.example.invalid/support",
		ExpiresAt: time.Now().UTC().Add(7 * 24 * time.Hour),
	}
	insertOutbox := func(ctx context.Context, tx *sql.Tx, messageID string) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO sesame_email_outbox (kind, to_email, action_url, expires_at, subject, body, support_message_id)
			VALUES ('support-reply', $1, $2, $3, $4, $5, $6)
		`, replyEmail.To, replyEmail.ActionURL, replyEmail.ExpiresAt, replyEmail.Subject, replyEmail.Body, messageID)
		return err
	}

	failing := func(ctx context.Context, tx *sql.Tx, messageID string) error {
		if err := insertOutbox(ctx, tx, messageID); err != nil {
			return err
		}
		return errors.New("simulated enqueue failure")
	}
	if _, err := adminService.ReplyTicket(ctx, actor, "ticket-support-email", "Fictional staff reply", replyEmail, failing, "test-ip"); err == nil {
		t.Fatal("a reply must fail when its enqueue hook fails")
	}
	assertSupportCounts(t, accountStore.DB(), 0, 0)
	var status string
	var firstResponse sql.NullTime
	if err := accountStore.DB().QueryRowContext(ctx, `SELECT status, first_response_at FROM sesame_support_requests WHERE id = 'ticket-support-email'`).Scan(&status, &firstResponse); err != nil {
		t.Fatalf("read ticket after failed reply: %v", err)
	}
	if status != "open" || firstResponse.Valid {
		t.Fatalf("failed reply changed the ticket: status %q firstResponse %v", status, firstResponse)
	}

	if _, err := adminService.ReplyTicket(ctx, actor, "ticket-support-email", "Fictional staff reply", replyEmail, insertOutbox, "test-ip"); err != nil {
		t.Fatalf("reply with a working enqueue hook: %v", err)
	}
	assertSupportCounts(t, accountStore.DB(), 1, 1)
	var sentViaEmail bool
	if err := accountStore.DB().QueryRowContext(ctx, `SELECT sent_via_email FROM sesame_support_messages WHERE ticket_id = 'ticket-support-email'`).Scan(&sentViaEmail); err != nil {
		t.Fatalf("read message: %v", err)
	}
	if !sentViaEmail {
		t.Fatal("a queued reply email must mark the message sent_via_email")
	}

	if _, err := adminService.ReplyTicket(ctx, actor, "ticket-support-email", "Fictional follow-up", nil, nil, "test-ip"); err != nil {
		t.Fatalf("reply without email: %v", err)
	}
	assertSupportCounts(t, accountStore.DB(), 2, 1)
	if err := accountStore.DB().QueryRowContext(ctx, `SELECT sent_via_email FROM sesame_support_messages WHERE ticket_id = 'ticket-support-email' ORDER BY created_at DESC LIMIT 1`).Scan(&sentViaEmail); err != nil {
		t.Fatalf("read follow-up message: %v", err)
	}
	if sentViaEmail {
		t.Fatal("a reply with no email queued must keep sent_via_email false")
	}
}

func assertSupportCounts(t *testing.T, db *sql.DB, wantMessages, wantOutbox int) {
	t.Helper()
	var messages, outbox int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sesame_support_messages WHERE ticket_id = 'ticket-support-email'`).Scan(&messages); err != nil {
		t.Fatalf("count support messages: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sesame_email_outbox`).Scan(&outbox); err != nil {
		t.Fatalf("count outbox messages: %v", err)
	}
	if messages != wantMessages || outbox != wantOutbox {
		t.Fatalf("messages = %d outbox = %d, want %d and %d", messages, outbox, wantMessages, wantOutbox)
	}
}
