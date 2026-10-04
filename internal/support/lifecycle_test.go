package support_test

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"usesesame.app/backend/internal/accounts"
	"usesesame.app/backend/internal/support"
)

const (
	lifecycleOwner     = "support-lifecycle-owner"
	lifecycleOther     = "support-lifecycle-other"
	lifecycleAdmin     = "support-lifecycle-admin"
	accountCloseGuard  = " AND account_id = $4 AND status <> 'closed'"
	accountReopenGuard = " AND account_id = $4 AND status = 'closed' AND account_reopen_until > $3"
)

func lifecycleTestDatabase(t *testing.T) *sql.DB {
	t.Helper()
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
	db := accountStore.DB()
	const lockID int64 = 762374923
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("reserve test database connection: %v", err)
	}
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, lockID); err != nil {
		_ = conn.Close()
		t.Fatalf("lock test database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, lockID)
		_ = conn.Close()
	})
	clearLifecycleFixtures(t, db)
	return db
}

func clearLifecycleFixtures(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	for _, id := range []string{lifecycleOwner, lifecycleOther} {
		if _, err := db.ExecContext(ctx, `DELETE FROM sesame_accounts WHERE id = $1`, id); err != nil {
			t.Fatalf("clear account %s: %v", id, err)
		}
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM sesame_admin_accounts WHERE id = $1`, lifecycleAdmin); err != nil {
		t.Fatalf("clear admin %s: %v", lifecycleAdmin, err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM sesame_support_requests WHERE id LIKE 'support-lifecycle-%'`); err != nil {
		t.Fatalf("clear tickets: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, `DELETE FROM sesame_support_requests WHERE id LIKE 'support-lifecycle-%'`)
		_, _ = db.ExecContext(ctx, `DELETE FROM sesame_admin_accounts WHERE id = $1`, lifecycleAdmin)
		_, _ = db.ExecContext(ctx, `DELETE FROM sesame_accounts WHERE id = $1`, lifecycleOwner)
		_, _ = db.ExecContext(ctx, `DELETE FROM sesame_accounts WHERE id = $1`, lifecycleOther)
	})
}

func seedLifecycleAccount(t *testing.T, db *sql.DB, id, email string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `INSERT INTO sesame_accounts (id, email, password_hash) VALUES ($1, $2, 'fictional-test-hash')`, id, email); err != nil {
		t.Fatalf("seed account %s: %v", id, err)
	}
}

func seedLifecycleAdmin(t *testing.T, db *sql.DB, id, email string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `INSERT INTO sesame_admin_accounts (id, email, password_hash, role, totp_verified) VALUES ($1, $2, 'fictional-test-hash', 'support', TRUE)`, id, email); err != nil {
		t.Fatalf("seed admin %s: %v", id, err)
	}
}

func seedLifecycleTicket(t *testing.T, db *sql.DB, id, accountID, email string) {
	t.Helper()
	var owner any
	if accountID != "" {
		owner = accountID
	}
	if _, err := db.ExecContext(context.Background(), `INSERT INTO sesame_support_requests (id, account_id, email, subject, message, status) VALUES ($1, $2, $3, 'Lifecycle subject', 'Lifecycle body', 'open')`, id, owner, email); err != nil {
		t.Fatalf("seed ticket %s: %v", id, err)
	}
}

type lifecycleRow struct {
	status        string
	closedAt      sql.NullTime
	closedBy      sql.NullString
	reopenUntil   sql.NullTime
	updatedAt     time.Time
	windowMatches bool
}

func readLifecycleRow(t *testing.T, db *sql.DB, id string) lifecycleRow {
	t.Helper()
	var row lifecycleRow
	if err := db.QueryRowContext(context.Background(), `
		SELECT status, closed_at, closed_by, account_reopen_until, updated_at,
		       COALESCE(account_reopen_until = closed_at + INTERVAL '30 days', FALSE)
		FROM sesame_support_requests WHERE id = $1`, id).
		Scan(&row.status, &row.closedAt, &row.closedBy, &row.reopenUntil, &row.updatedAt, &row.windowMatches); err != nil {
		t.Fatalf("read ticket %s: %v", id, err)
	}
	return row
}

func affectedRows(t *testing.T, result sql.Result) int64 {
	t.Helper()
	affected, err := result.RowsAffected()
	if err != nil {
		t.Fatalf("rows affected: %v", err)
	}
	return affected
}

func TestAccountCloseAndReopenTransitions(t *testing.T) {
	db := lifecycleTestDatabase(t)
	ctx := context.Background()
	seedLifecycleAccount(t, db, lifecycleOwner, "owner@example.invalid")
	seedLifecycleAccount(t, db, lifecycleOther, "other@example.invalid")
	seedLifecycleTicket(t, db, "support-lifecycle-a", lifecycleOwner, "owner@example.invalid")
	now := time.Now().UTC().Truncate(time.Millisecond)

	result, err := support.Close(ctx, db, "support-lifecycle-a", "", now, accountCloseGuard, lifecycleOther)
	if err != nil {
		t.Fatalf("close as a foreign account: %v", err)
	}
	if affectedRows(t, result) != 0 {
		t.Fatal("a foreign account closed someone else's ticket")
	}
	if row := readLifecycleRow(t, db, "support-lifecycle-a"); row.status != "open" || row.closedAt.Valid {
		t.Fatalf("foreign close changed the ticket: %+v", row)
	}

	result, err = support.Close(ctx, db, "support-lifecycle-a", "", now, accountCloseGuard, lifecycleOwner)
	if err != nil {
		t.Fatalf("close as the owner: %v", err)
	}
	if affectedRows(t, result) != 1 {
		t.Fatal("the owner could not close the ticket")
	}
	closed := readLifecycleRow(t, db, "support-lifecycle-a")
	if closed.status != "closed" {
		t.Fatalf("status after close = %q, want closed", closed.status)
	}
	if closed.closedBy.Valid {
		t.Fatalf("an account close recorded closed_by = %q", closed.closedBy.String)
	}
	if !closed.closedAt.Valid || closed.closedAt.Time.Sub(now).Abs() > 5*time.Second {
		t.Fatalf("closed_at = %v, want about %v", closed.closedAt, now)
	}
	if !closed.reopenUntil.Valid || !closed.reopenUntil.Time.After(closed.closedAt.Time) || !closed.windowMatches {
		t.Fatalf("reopen window = %v (closed_at + %s: %t), want %s after closed_at", closed.reopenUntil, support.ReopenWindow, closed.windowMatches, support.ReopenWindow)
	}

	result, err = support.Close(ctx, db, "support-lifecycle-a", "", now, accountCloseGuard, lifecycleOwner)
	if err != nil {
		t.Fatalf("close an already closed ticket: %v", err)
	}
	if affectedRows(t, result) != 0 {
		t.Fatal("a closed ticket was closed again")
	}

	result, err = support.SetOpenStatus(ctx, db, "support-lifecycle-a", "open", now.Add(31*24*time.Hour), accountReopenGuard, lifecycleOwner)
	if err != nil {
		t.Fatalf("reopen after the window: %v", err)
	}
	if affectedRows(t, result) != 0 {
		t.Fatal("a ticket was reopened after the reopen window")
	}
	if row := readLifecycleRow(t, db, "support-lifecycle-a"); row.status != "closed" {
		t.Fatalf("status after the expired reopen = %q, want closed", row.status)
	}

	result, err = support.SetOpenStatus(ctx, db, "support-lifecycle-a", "open", now, accountReopenGuard, lifecycleOwner)
	if err != nil {
		t.Fatalf("reopen inside the window: %v", err)
	}
	if affectedRows(t, result) != 1 {
		t.Fatal("the owner could not reopen inside the window")
	}
	reopened := readLifecycleRow(t, db, "support-lifecycle-a")
	if reopened.status != "open" || reopened.closedAt.Valid || reopened.closedBy.Valid || reopened.reopenUntil.Valid {
		t.Fatalf("reopened row = %+v, want open with cleared close bookkeeping", reopened)
	}

	result, err = support.SetOpenStatus(ctx, db, "support-lifecycle-a", "open", now, accountReopenGuard, lifecycleOther)
	if err != nil {
		t.Fatalf("reopen as a foreign account: %v", err)
	}
	if affectedRows(t, result) != 0 {
		t.Fatal("a foreign account reopened someone else's ticket")
	}
}

func TestAdminCloseAndWaitingTransitions(t *testing.T) {
	db := lifecycleTestDatabase(t)
	ctx := context.Background()
	seedLifecycleAdmin(t, db, lifecycleAdmin, "support-lifecycle@example.invalid")
	seedLifecycleTicket(t, db, "support-lifecycle-b", "", "guest@example.invalid")
	now := time.Now().UTC().Truncate(time.Millisecond)

	result, err := support.Close(ctx, db, "support-lifecycle-b", lifecycleAdmin, now, "")
	if err != nil {
		t.Fatalf("admin close: %v", err)
	}
	if affectedRows(t, result) != 1 {
		t.Fatal("the admin could not close the ticket")
	}
	closed := readLifecycleRow(t, db, "support-lifecycle-b")
	if closed.status != "closed" || !closed.closedBy.Valid || closed.closedBy.String != lifecycleAdmin {
		t.Fatalf("admin-closed row = %+v", closed)
	}
	if !closed.reopenUntil.Valid || !closed.reopenUntil.Time.After(closed.closedAt.Time) || !closed.windowMatches {
		t.Fatalf("admin reopen window = %v (closed_at + %s: %t)", closed.reopenUntil, support.ReopenWindow, closed.windowMatches)
	}

	result, err = support.SetOpenStatus(ctx, db, "support-lifecycle-b", "waiting", now, "")
	if err != nil {
		t.Fatalf("move to waiting: %v", err)
	}
	if affectedRows(t, result) != 1 {
		t.Fatal("the ticket did not move to waiting")
	}
	waiting := readLifecycleRow(t, db, "support-lifecycle-b")
	if waiting.status != "waiting" || waiting.closedAt.Valid || waiting.closedBy.Valid || waiting.reopenUntil.Valid {
		t.Fatalf("waiting row = %+v, want waiting with cleared close bookkeeping", waiting)
	}

	result, err = support.Close(ctx, db, "support-lifecycle-missing", "", now, "")
	if err != nil {
		t.Fatalf("close a missing ticket: %v", err)
	}
	if affectedRows(t, result) != 0 {
		t.Fatal("closing a missing ticket reported a change")
	}
}
