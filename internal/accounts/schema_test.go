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
