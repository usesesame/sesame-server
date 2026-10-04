package accounts

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"usesesame.app/backend/internal/support"
)

func seedMaintenanceTicket(t *testing.T, db *sql.DB, id, status string, closedAt, updatedAt time.Time) {
	t.Helper()
	var closed any
	if !closedAt.IsZero() {
		closed = closedAt.UTC()
	}
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO sesame_support_requests (id, email, subject, message, status, closed_at, updated_at)
		VALUES ($1, 'maintenance@example.invalid', 'Maintenance subject', 'Maintenance body', $2, $3, $4)
	`, id, status, closed, updatedAt.UTC()); err != nil {
		t.Fatalf("seed ticket %s: %v", id, err)
	}
}

func seedMaintenanceReply(t *testing.T, db *sql.DB, ticketID, messageID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO sesame_support_messages (id, ticket_id, author_role, admin_email, body, sent_via_email)
		VALUES ($1, $2, 'staff', 'staff@example.invalid', 'Fictional staff reply', TRUE)
	`, messageID, ticketID); err != nil {
		t.Fatalf("seed staff message %s: %v", messageID, err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO sesame_email_outbox (kind, to_email, action_url, expires_at, subject, body, support_message_id, status)
		VALUES ('support-reply', 'maintenance@example.invalid', 'https://account.example.invalid/support', NOW() + INTERVAL '7 days', 'Subject', 'Body', $1, 'delivered')
	`, messageID); err != nil {
		t.Fatalf("seed outbox row for %s: %v", messageID, err)
	}
}

func countRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	return count
}

func TestPurgeExpiredRetiresClosedSupportRequestsAndTheirOutboxRows(t *testing.T) {
	store, db := lifecycleTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	oldClosed := now.Add(-support.RetentionAfter - time.Hour)
	recentClosed := now.Add(-support.RetentionAfter + time.Hour)

	seedMaintenanceTicket(t, db, "ticket-retire-old", "closed", oldClosed, oldClosed)
	seedMaintenanceTicket(t, db, "ticket-retire-recent", "closed", recentClosed, recentClosed)
	seedMaintenanceTicket(t, db, "ticket-retire-open", "open", time.Time{}, now)
	seedMaintenanceTicket(t, db, "ticket-retire-waiting", "waiting", time.Time{}, now.Add(-time.Hour))
	seedMaintenanceReply(t, db, "ticket-retire-old", "message-retire-old")
	seedMaintenanceReply(t, db, "ticket-retire-recent", "message-retire-recent")

	if err := store.PurgeExpired(ctx); err != nil {
		t.Fatalf("purge expired: %v", err)
	}

	for id, want := range map[string]int{
		"ticket-retire-old": 0, "ticket-retire-recent": 1,
		"ticket-retire-open": 1, "ticket-retire-waiting": 1,
	} {
		if got := countRows(t, db, `SELECT COUNT(*) FROM sesame_support_requests WHERE id = $1`, id); got != want {
			t.Fatalf("ticket %s count = %d, want %d", id, got, want)
		}
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM sesame_support_messages WHERE ticket_id = 'ticket-retire-old'`); got != 0 {
		t.Fatalf("retired ticket kept %d messages", got)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM sesame_email_outbox WHERE support_message_id = 'message-retire-old'`); got != 0 {
		t.Fatalf("retired ticket kept %d outbox rows", got)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM sesame_email_outbox WHERE support_message_id = 'message-retire-recent'`); got != 1 {
		t.Fatalf("recent closed ticket outbox rows = %d, want 1", got)
	}
	if status := readMaintenanceStatus(t, db, "ticket-retire-waiting"); status != "waiting" {
		t.Fatalf("recent waiting ticket status = %q, want waiting", status)
	}

	if err := store.PurgeExpired(ctx); err != nil {
		t.Fatalf("second purge: %v", err)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM sesame_support_requests WHERE id = 'ticket-retire-recent'`); got != 1 {
		t.Fatalf("second purge changed the recent closed ticket: count %d", got)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM sesame_email_outbox WHERE support_message_id = 'message-retire-recent'`); got != 1 {
		t.Fatalf("second purge changed the recent outbox row: count %d", got)
	}
}

func TestPurgeExpiredClosesOnlyStaleWaitingRequests(t *testing.T) {
	store, db := lifecycleTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	stale := now.Add(-support.AutoCloseAfter - time.Hour)

	seedMaintenanceTicket(t, db, "ticket-auto-stale", "waiting", time.Time{}, stale)
	seedMaintenanceTicket(t, db, "ticket-auto-recent", "waiting", time.Time{}, now.Add(-time.Hour))
	seedMaintenanceTicket(t, db, "ticket-auto-open", "open", time.Time{}, stale)

	if err := store.PurgeExpired(ctx); err != nil {
		t.Fatalf("purge expired: %v", err)
	}

	var status string
	var closedAt, reopenUntil sql.NullTime
	var closedBy sql.NullString
	var closedBySystem bool
	if err := db.QueryRowContext(ctx, `
		SELECT status, closed_at, closed_by, closed_by_system, account_reopen_until
		FROM sesame_support_requests WHERE id = 'ticket-auto-stale'
	`).Scan(&status, &closedAt, &closedBy, &closedBySystem, &reopenUntil); err != nil {
		t.Fatalf("read auto-closed ticket: %v", err)
	}
	if status != "closed" || !closedBySystem || closedBy.Valid {
		t.Fatalf("auto-closed ticket = status %q closedBy %v system %v, want closed by system", status, closedBy, closedBySystem)
	}
	if !closedAt.Valid || closedAt.Time.Sub(now).Abs() > time.Minute {
		t.Fatalf("auto-closed at %v, want about %v", closedAt, now)
	}
	if !reopenUntil.Valid || reopenUntil.Time.Sub(closedAt.Time) < 29*24*time.Hour {
		t.Fatalf("auto-closed reopen window = %v, want about %s after close", reopenUntil, support.ReopenWindow)
	}

	if status := readMaintenanceStatus(t, db, "ticket-auto-recent"); status != "waiting" {
		t.Fatalf("recent waiting ticket status = %q, want waiting", status)
	}
	if status := readMaintenanceStatus(t, db, "ticket-auto-open"); status != "open" {
		t.Fatalf("open ticket status = %q, want open", status)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM sesame_support_requests WHERE closed_by_system AND id <> 'ticket-auto-stale'`); got != 0 {
		t.Fatalf("%d other tickets were marked system-closed", got)
	}

	if err := store.PurgeExpired(ctx); err != nil {
		t.Fatalf("second purge: %v", err)
	}
	var secondClosedAt sql.NullTime
	if err := db.QueryRowContext(ctx, `SELECT closed_at FROM sesame_support_requests WHERE id = 'ticket-auto-stale'`).Scan(&secondClosedAt); err != nil {
		t.Fatalf("read auto-closed ticket after second purge: %v", err)
	}
	if !secondClosedAt.Valid || !secondClosedAt.Time.Equal(closedAt.Time) {
		t.Fatalf("second purge changed closed_at: %v then %v", closedAt, secondClosedAt)
	}
}

func readMaintenanceStatus(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var status string
	if err := db.QueryRowContext(context.Background(), `SELECT status FROM sesame_support_requests WHERE id = $1`, id).Scan(&status); err != nil {
		t.Fatalf("read ticket %s: %v", id, err)
	}
	return status
}
