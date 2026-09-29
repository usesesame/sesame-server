package accounts

import (
	"context"
	"testing"
)

func TestMissingMigrationVersionsReportsPendingInOrder(t *testing.T) {
	migrations := []migration{{version: "0001_accounts"}, {version: "0002_flags"}, {version: "0003_releases"}}
	pending := missingMigrationVersions(migrations, map[string]bool{"0001_accounts": true})
	if len(pending) != 2 || pending[0] != "0002_flags" || pending[1] != "0003_releases" {
		t.Fatalf("pending = %v, want the two unapplied versions in order", pending)
	}
	if got := missingMigrationVersions(migrations, map[string]bool{}); len(got) != 3 {
		t.Fatalf("an empty applied set must name every migration, got %v", got)
	}
	if got := missingMigrationVersions(migrations, map[string]bool{"0001_accounts": true, "0002_flags": true, "0003_releases": true}); len(got) != 0 {
		t.Fatalf("a fully migrated database must have no pending versions, got %v", got)
	}
}

func TestPendingMigrationsIsEmptyOnAMigratedDatabase(t *testing.T) {
	store, _ := lifecycleTestStore(t)
	pending, err := store.PendingMigrations(context.Background())
	if err != nil {
		t.Fatalf("pending migrations: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending = %v, want none after Migrate", pending)
	}
}

func TestEmailOutboxActionURLMigrationClearsLegacyPlaintext(t *testing.T) {
	_, db := lifecycleTestStore(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `TRUNCATE sesame_email_outbox`); err != nil {
		t.Fatalf("clear email outbox: %v", err)
	}
	seed := func(status, actionURL string) string {
		t.Helper()
		var id string
		if err := db.QueryRowContext(ctx, `
			INSERT INTO sesame_email_outbox (kind, to_email, action_url, expires_at, subject, body, status)
			VALUES ('verify-email', 'migration@example.invalid', $1, NOW() + INTERVAL '1 hour', 'Subject', 'Body', $2)
			RETURNING id`, actionURL, status).Scan(&id); err != nil {
			t.Fatalf("seed outbox row: %v", err)
		}
		return id
	}
	pendingID := seed("pending", "https://account.example.invalid/verify-email#token=fictional-migration")
	deliveredID := seed("delivered", "https://account.example.invalid/verify-email#token=fictional-migration")
	noticeID := seed("pending", "")

	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("load migrations: %v", err)
	}
	statement := ""
	for _, m := range migrations {
		if m.version == "0042_email_outbox_action_url_encryption" {
			statement = m.sql
		}
	}
	if statement == "" {
		t.Fatal("the email outbox action URL encryption migration is missing")
	}
	if _, err := db.ExecContext(ctx, statement); err != nil {
		t.Fatalf("apply the email outbox action URL encryption migration: %v", err)
	}

	rows, err := db.QueryContext(ctx, `
		SELECT id, status, COALESCE(error_message, ''), action_url
		FROM sesame_email_outbox ORDER BY id`)
	if err != nil {
		t.Fatalf("read migrated outbox rows: %v", err)
	}
	defer rows.Close()
	type migratedRow struct {
		status       string
		errorMessage string
	}
	got := map[string]migratedRow{}
	for rows.Next() {
		var id, status, errorMessage, actionURL string
		if err := rows.Scan(&id, &status, &errorMessage, &actionURL); err != nil {
			t.Fatalf("scan migrated outbox row: %v", err)
		}
		if actionURL != "" {
			t.Fatalf("outbox row %s kept a plaintext action URL: %q", id, actionURL)
		}
		got[id] = migratedRow{status: status, errorMessage: errorMessage}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate migrated outbox rows: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("migrated outbox rows = %d, want 3", len(got))
	}
	if row := got[pendingID]; row.status != "failed" || row.errorMessage != "action_url_encryption_upgrade" {
		t.Fatalf("pending legacy row = %+v, want failed with action_url_encryption_upgrade", row)
	}
	if row := got[deliveredID]; row.status != "delivered" {
		t.Fatalf("delivered legacy row = %+v, want delivered", row)
	}
	if row := got[noticeID]; row.status != "pending" {
		t.Fatalf("notice row = %+v, want pending", row)
	}
}
