package accounts

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestEmbeddedMigrationsParse(t *testing.T) {
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("load migrations: %v", err)
	}
	concurrent := map[string]string{}
	for _, m := range migrations {
		if m.concurrentIndex != "" {
			concurrent[m.version] = m.concurrentIndex
			continue
		}
		body, err := migrationFiles.ReadFile("migrations/" + m.version + ".sql")
		if err != nil {
			t.Fatalf("read migration %s: %v", m.version, err)
		}
		if m.sql != string(body) {
			t.Fatalf("ordinary migration %s must run its file unchanged", m.version)
		}
	}
	if len(concurrent) != 1 || concurrent["0053_account_events_expiry_index"] != "idx_account_events_expires_at" {
		t.Fatalf("concurrent index migrations = %v, want only 0053 building idx_account_events_expires_at", concurrent)
	}
}

func TestConcurrentIndexDirectiveIsParsedStrictly(t *testing.T) {
	const statement = "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_fixture_items_label\n    ON fixture_items (label);\n"
	valid := map[string]string{
		"with a semicolon":    concurrentIndexDirective + "\n" + statement,
		"without a semicolon": concurrentIndexDirective + "\nCREATE INDEX CONCURRENTLY IF NOT EXISTS idx_fixture_items_label ON fixture_items (label)\n",
		"unique":              concurrentIndexDirective + "\nCREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_fixture_items_label ON fixture_items (label);\n",
	}
	for name, body := range valid {
		m, err := parseMigration("0002_fixture", body)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if m.concurrentIndex != "idx_fixture_items_label" || strings.HasPrefix(m.sql, "--") || strings.HasSuffix(m.sql, ";") {
			t.Fatalf("%s parsed as %+v", name, m)
		}
	}

	malformed := map[string]string{
		"directive without the space":       "--sesame-migration: create-index-concurrently\n" + statement,
		"directive with a trailing space":   concurrentIndexDirective + " \n" + statement,
		"directive with a carriage return":  concurrentIndexDirective + "\r\n" + statement,
		"directive in upper case":           strings.ToUpper(concurrentIndexDirective) + "\n" + statement,
		"unknown directive":                 "-- sesame-migration: no-transaction\n" + statement,
		"misspelled directive":              "-- sesame-migration: create-index-concurrent\n" + statement,
		"indented directive":                " " + concurrentIndexDirective + "\n" + statement,
		"directive on the second line":      "\n" + concurrentIndexDirective + "\n" + statement,
		"directive after a statement":       "CREATE TABLE fixture_items (label TEXT);\n" + concurrentIndexDirective + "\n" + statement,
		"directive repeated":                concurrentIndexDirective + "\n" + concurrentIndexDirective + "\n" + statement,
		"plain index":                       concurrentIndexDirective + "\nCREATE INDEX IF NOT EXISTS idx_fixture_items_label ON fixture_items (label);\n",
		"index without IF NOT EXISTS":       concurrentIndexDirective + "\nCREATE INDEX CONCURRENTLY idx_fixture_items_label ON fixture_items (label);\n",
		"index without a name":              concurrentIndexDirective + "\nCREATE INDEX CONCURRENTLY IF NOT EXISTS ON fixture_items (label);\n",
		"quoted index name":                 concurrentIndexDirective + "\nCREATE INDEX CONCURRENTLY IF NOT EXISTS \"Fixture\" ON fixture_items (label);\n",
		"other statement":                   concurrentIndexDirective + "\nDROP TABLE fixture_items;\n",
		"empty body":                        concurrentIndexDirective + "\n",
		"line comment in the statement":     concurrentIndexDirective + "\nCREATE INDEX CONCURRENTLY IF NOT EXISTS idx_fixture_items_label ON fixture_items (label) -- fixture\n",
		"block comment in the statement":    concurrentIndexDirective + "\nCREATE INDEX CONCURRENTLY IF NOT EXISTS idx_fixture_items_label /* fixture */ ON fixture_items (label);\n",
		"lower case keywords":               concurrentIndexDirective + "\ncreate index concurrently if not exists idx_fixture_items_label on fixture_items (label);\n",
		"index name longer than PostgreSQL": concurrentIndexDirective + "\nCREATE INDEX CONCURRENTLY IF NOT EXISTS idx_" + strings.Repeat("a", 61) + " ON fixture_items (label);\n",
	}
	for name, body := range malformed {
		if m, err := parseMigration("0002_fixture", body); err == nil {
			t.Errorf("%s was accepted as %+v", name, m)
		}
	}
}

func TestConcurrentIndexMigrationRefusesTwoStatements(t *testing.T) {
	bodies := []string{
		concurrentIndexDirective + "\nCREATE INDEX CONCURRENTLY IF NOT EXISTS idx_fixture_items_label ON fixture_items (label);\nCREATE INDEX CONCURRENTLY IF NOT EXISTS idx_fixture_items_rank ON fixture_items (rank);\n",
		concurrentIndexDirective + "\nCREATE INDEX CONCURRENTLY IF NOT EXISTS idx_fixture_items_label ON fixture_items (label); DROP TABLE fixture_items\n",
		concurrentIndexDirective + "\nCREATE INDEX CONCURRENTLY IF NOT EXISTS idx_fixture_items_label ON fixture_items (label);;\n",
	}
	for _, body := range bodies {
		_, err := parseMigration("0002_fixture", body)
		if err == nil || !strings.Contains(err.Error(), "exactly one statement") {
			t.Errorf("two statements = %v, want the one statement refusal for %q", err, body)
		}
	}
}

func TestADirectiveBelowAnOrdinaryStatementIsRefused(t *testing.T) {
	body := "CREATE TABLE fixture_items (label TEXT);\n"
	m, err := parseMigration("0001_fixture", body)
	if err != nil || m.sql != body || m.concurrentIndex != "" {
		t.Fatalf("ordinary migration = %+v, %v", m, err)
	}
	if _, err := parseMigration("0001_fixture", body+"-- sesame-migration: create-index-concurrently\n"); err == nil {
		t.Fatal("a directive below an ordinary statement must be refused")
	}
}

func freshMigrationStore(t *testing.T) *PostgresStore {
	t.Helper()
	databaseURL := os.Getenv("SESAME_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("SESAME_TEST_DATABASE_URL is required")
	}
	ctx := context.Background()
	admin, err := OpenWithoutMigrate(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open the test database: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	databaseName := fmt.Sprintf("sesame_migrate_%d_%d", os.Getpid(), time.Now().UnixNano())
	if _, err := admin.db.ExecContext(ctx, `CREATE DATABASE `+databaseName); err != nil {
		t.Fatalf("create a fresh migration test database: %v", err)
	}
	t.Cleanup(func() {
		cleanupContext := context.Background()
		_, _ = admin.db.ExecContext(cleanupContext, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()`, databaseName)
		if _, err := admin.db.ExecContext(cleanupContext, `DROP DATABASE IF EXISTS `+databaseName); err != nil {
			t.Errorf("drop the fresh migration test database: %v", err)
		}
	})
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatalf("parse the test database URL: %v", err)
	}
	parsed.Path = "/" + databaseName
	store, err := OpenWithoutMigrate(ctx, parsed.String())
	if err != nil {
		t.Fatalf("open the fresh migration test database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func mustParseMigration(t *testing.T, version, body string) migration {
	t.Helper()
	m, err := parseMigration(version, body)
	if err != nil {
		t.Fatalf("parse migration %s: %v", version, err)
	}
	return m
}

func appliedCount(t *testing.T, store *PostgresStore, version string) int {
	t.Helper()
	var count int
	if err := store.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM sesame_schema_migrations WHERE version = $1`, version).Scan(&count); err != nil {
		t.Fatalf("count applied migration %s: %v", version, err)
	}
	return count
}

func fixtureIndex(t *testing.T, store *PostgresStore, name string) (definition string, valid bool) {
	t.Helper()
	err := store.db.QueryRowContext(context.Background(), `
		SELECT pg_get_indexdef(i.indexrelid), i.indisvalid
		FROM pg_index i WHERE i.indexrelid = to_regclass($1)`, name).Scan(&definition, &valid)
	if err != nil {
		t.Fatalf("inspect index %s: %v", name, err)
	}
	return definition, valid
}

const fixtureTableMigration = "CREATE TABLE fixture_items (id BIGSERIAL PRIMARY KEY, label TEXT NOT NULL, rank INTEGER NOT NULL);\n"

func TestConcurrentIndexMigrationIsRecordedOnce(t *testing.T) {
	store := freshMigrationStore(t)
	ctx := context.Background()
	migrations := []migration{
		mustParseMigration(t, "0001_fixture_items", fixtureTableMigration),
		mustParseMigration(t, "0002_fixture_items_label", concurrentIndexDirective+"\nCREATE INDEX CONCURRENTLY IF NOT EXISTS idx_fixture_items_label\n    ON fixture_items (label);\n"),
	}
	for run := 0; run < 2; run++ {
		if err := store.runMigrations(ctx, migrations); err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
	}
	if got := appliedCount(t, store, "0002_fixture_items_label"); got != 1 {
		t.Fatalf("concurrent index migration recorded %d times, want 1", got)
	}
	definition, valid := fixtureIndex(t, store, "idx_fixture_items_label")
	if !valid || !strings.Contains(definition, "(label)") {
		t.Fatalf("index = %q valid=%v", definition, valid)
	}
}

func TestConcurrentIndexMigrationRecoversAfterAFailedBuild(t *testing.T) {
	store := freshMigrationStore(t)
	ctx := context.Background()
	migrations := []migration{
		mustParseMigration(t, "0001_fixture_items", fixtureTableMigration),
		mustParseMigration(t, "0002_fixture_items_label", concurrentIndexDirective+"\nCREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_fixture_items_label ON fixture_items (label);\n"),
	}
	if err := store.runMigrations(ctx, migrations[:1]); err != nil {
		t.Fatalf("create the fixture table: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO fixture_items (label, rank) VALUES ('fixture-one', 1), ('fixture-one', 2)`); err != nil {
		t.Fatalf("seed duplicate labels: %v", err)
	}
	err := store.runMigrations(ctx, migrations)
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) || postgresError.Code != "23505" {
		t.Fatalf("build over duplicate labels = %v, want a unique violation", err)
	}
	if got := appliedCount(t, store, "0002_fixture_items_label"); got != 0 {
		t.Fatalf("a failed build was recorded %d times", got)
	}
	if _, valid := fixtureIndex(t, store, "idx_fixture_items_label"); valid {
		t.Fatal("the interrupted build must leave an invalid index for this test to mean anything")
	}

	if _, err := store.db.ExecContext(ctx, `DELETE FROM fixture_items WHERE rank = 2`); err != nil {
		t.Fatalf("remove the duplicate label: %v", err)
	}
	if err := store.runMigrations(ctx, migrations); err != nil {
		t.Fatalf("rerun after the failed build: %v", err)
	}
	if got := appliedCount(t, store, "0002_fixture_items_label"); got != 1 {
		t.Fatalf("recovered migration recorded %d times, want 1", got)
	}
	definition, valid := fixtureIndex(t, store, "idx_fixture_items_label")
	if !valid || !strings.Contains(definition, "UNIQUE") {
		t.Fatalf("recovered index = %q valid=%v", definition, valid)
	}
}

func TestConcurrentIndexMigrationReplacesAnInvalidLeftoverIndex(t *testing.T) {
	store := freshMigrationStore(t)
	ctx := context.Background()
	if err := store.runMigrations(ctx, []migration{mustParseMigration(t, "0001_fixture_items", fixtureTableMigration)}); err != nil {
		t.Fatalf("create the fixture table: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO fixture_items (label, rank) VALUES ('fixture-one', 1), ('fixture-one', 2)`); err != nil {
		t.Fatalf("seed duplicate labels: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `CREATE UNIQUE INDEX CONCURRENTLY idx_fixture_items_rank ON fixture_items (label)`); err == nil {
		t.Fatal("the leftover build must fail")
	}
	if definition, valid := fixtureIndex(t, store, "idx_fixture_items_rank"); valid || !strings.Contains(definition, "(label)") {
		t.Fatalf("leftover index = %q valid=%v, want an invalid index on label", definition, valid)
	}

	migrations := []migration{
		mustParseMigration(t, "0001_fixture_items", fixtureTableMigration),
		mustParseMigration(t, "0002_fixture_items_rank", concurrentIndexDirective+"\nCREATE INDEX CONCURRENTLY IF NOT EXISTS idx_fixture_items_rank ON fixture_items (rank);\n"),
	}
	if err := store.runMigrations(ctx, migrations); err != nil {
		t.Fatalf("migrate over the invalid leftover index: %v", err)
	}
	definition, valid := fixtureIndex(t, store, "idx_fixture_items_rank")
	if !valid || !strings.Contains(definition, "(rank)") || strings.Contains(definition, "UNIQUE") {
		t.Fatalf("rebuilt index = %q valid=%v, want a valid index on rank", definition, valid)
	}
	if got := appliedCount(t, store, "0002_fixture_items_rank"); got != 1 {
		t.Fatalf("migration recorded %d times, want 1", got)
	}
}

func TestConcurrentIndexMigrationKeepsAnIndexBuiltInATransaction(t *testing.T) {
	store := freshMigrationStore(t)
	ctx := context.Background()
	transactional := []migration{
		mustParseMigration(t, "0001_fixture_items", fixtureTableMigration),
		mustParseMigration(t, "0002_fixture_items_label", "CREATE INDEX IF NOT EXISTS idx_fixture_items_label\n    ON fixture_items (label);\n"),
	}
	if err := store.runMigrations(ctx, transactional); err != nil {
		t.Fatalf("apply the transactional index: %v", err)
	}
	var before string
	if err := store.db.QueryRowContext(ctx, `SELECT to_regclass('idx_fixture_items_label')::oid::text`).Scan(&before); err != nil {
		t.Fatalf("read the index identity: %v", err)
	}
	concurrent := []migration{
		transactional[0],
		mustParseMigration(t, "0002_fixture_items_label", concurrentIndexDirective+"\nCREATE INDEX CONCURRENTLY IF NOT EXISTS idx_fixture_items_label\n    ON fixture_items (label);\n"),
	}
	if err := store.runMigrations(ctx, concurrent); err != nil {
		t.Fatalf("run the concurrent form on a database that applied the transactional form: %v", err)
	}
	var after string
	if err := store.db.QueryRowContext(ctx, `SELECT to_regclass('idx_fixture_items_label')::oid::text`).Scan(&after); err != nil {
		t.Fatalf("read the index identity: %v", err)
	}
	if before != after || appliedCount(t, store, "0002_fixture_items_label") != 1 {
		t.Fatalf("index identity %s became %s, want the recorded migration skipped", before, after)
	}

	if _, err := store.db.ExecContext(ctx, `DELETE FROM sesame_schema_migrations WHERE version = '0002_fixture_items_label'`); err != nil {
		t.Fatalf("forget the migration record: %v", err)
	}
	if err := store.runMigrations(ctx, concurrent); err != nil {
		t.Fatalf("record an index that already exists: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT to_regclass('idx_fixture_items_label')::oid::text`).Scan(&after); err != nil {
		t.Fatalf("read the index identity: %v", err)
	}
	if before != after || appliedCount(t, store, "0002_fixture_items_label") != 1 {
		t.Fatalf("a valid existing index must be kept and recorded once, identity %s became %s", before, after)
	}
}

func TestConcurrentIndexMigrationRefusesARelationThatIsNotAnIndex(t *testing.T) {
	store := freshMigrationStore(t)
	migrations := []migration{
		mustParseMigration(t, "0001_fixture_items", fixtureTableMigration+"CREATE TABLE idx_fixture_items_label (id INTEGER);\n"),
		mustParseMigration(t, "0002_fixture_items_label", concurrentIndexDirective+"\nCREATE INDEX CONCURRENTLY IF NOT EXISTS idx_fixture_items_label ON fixture_items (label);\n"),
	}
	err := store.runMigrations(context.Background(), migrations)
	if err == nil || !strings.Contains(err.Error(), "missing or invalid") {
		t.Fatalf("a table in the index's place = %v, want a refusal", err)
	}
	if got := appliedCount(t, store, "0002_fixture_items_label"); got != 0 {
		t.Fatalf("the refused migration was recorded %d times", got)
	}
}

func TestConcurrentIndexMigrationRefusesAnIndexWithAnotherTableOrUniqueness(t *testing.T) {
	cases := map[string]string{
		"another table": "CREATE TABLE fixture_others (label TEXT);\nCREATE INDEX idx_fixture_items_label ON fixture_others (label);\n",
		"not unique":    "CREATE INDEX idx_fixture_items_label ON fixture_items (label);\n",
	}
	for name, existing := range cases {
		t.Run(name, func(t *testing.T) {
			store := freshMigrationStore(t)
			migrations := []migration{
				mustParseMigration(t, "0001_fixture_items", fixtureTableMigration+existing),
				mustParseMigration(t, "0002_fixture_items_label", concurrentIndexDirective+"\nCREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_fixture_items_label ON fixture_items (label);\n"),
			}
			err := store.runMigrations(context.Background(), migrations)
			if err == nil || !strings.Contains(err.Error(), "different table or uniqueness") {
				t.Fatalf("an index with %s = %v, want a refusal", name, err)
			}
			if got := appliedCount(t, store, "0002_fixture_items_label"); got != 0 {
				t.Fatalf("the refused migration was recorded %d times", got)
			}
		})
	}
}

func TestOrdinaryMigrationsStillRunInATransaction(t *testing.T) {
	store := freshMigrationStore(t)
	ctx := context.Background()
	failing := []migration{
		mustParseMigration(t, "0001_fixture_items", fixtureTableMigration),
		mustParseMigration(t, "0002_fixture_rollback", "CREATE TABLE fixture_rollback (id INTEGER);\nINSERT INTO fixture_items (label, rank) VALUES ('fixture-rollback', 1);\nSELECT 1 / 0;\n"),
	}
	err := store.runMigrations(ctx, failing)
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) || postgresError.Code != "22012" {
		t.Fatalf("failing migration = %v, want division_by_zero", err)
	}
	var table *string
	if err := store.db.QueryRowContext(ctx, `SELECT to_regclass('fixture_rollback')::text`).Scan(&table); err != nil {
		t.Fatalf("look for the rolled back table: %v", err)
	}
	var rows int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM fixture_items`).Scan(&rows); err != nil {
		t.Fatalf("count fixture rows: %v", err)
	}
	if table != nil || rows != 0 || appliedCount(t, store, "0002_fixture_rollback") != 0 {
		t.Fatalf("failed migration left table=%v rows=%d, want everything rolled back", table, rows)
	}
	if appliedCount(t, store, "0001_fixture_items") != 1 {
		t.Fatal("the migration before the failure must stay applied")
	}

	concurrentWithoutDirective := []migration{
		failing[0],
		mustParseMigration(t, "0002_fixture_items_label", "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_fixture_items_label ON fixture_items (label);\n"),
	}
	err = store.runMigrations(ctx, concurrentWithoutDirective)
	if !errors.As(err, &postgresError) || postgresError.Code != "25001" {
		t.Fatalf("a concurrent build without the directive = %v, want active_sql_transaction", err)
	}
}

func TestConcurrentIndexMigrationAndAWaitingMigratorBothFinish(t *testing.T) {
	store := freshMigrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	migrations := []migration{
		mustParseMigration(t, "0001_fixture_items", fixtureTableMigration),
		mustParseMigration(t, "0002_fixture_items_label", concurrentIndexDirective+"\nCREATE INDEX CONCURRENTLY IF NOT EXISTS idx_fixture_items_label ON fixture_items (label);\n"),
	}
	if err := store.runMigrations(ctx, migrations[:1]); err != nil {
		t.Fatalf("create the fixture table: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO fixture_items (label, rank) SELECT 'fixture-' || n, n FROM generate_series(1, 300000) AS n`); err != nil {
		t.Fatalf("seed fixture rows: %v", err)
	}
	holder, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatalf("reserve the lock holder: %v", err)
	}
	defer holder.Close()
	if err := lockMigration(ctx, holder); err != nil {
		t.Fatalf("hold the migration lock: %v", err)
	}
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- store.runMigrations(ctx, migrations)
		}()
	}
	time.Sleep(3 * migrationLockRetry)
	unlockMigration(holder)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("migrator: %v", err)
		}
	}
	if got := appliedCount(t, store, "0002_fixture_items_label"); got != 1 {
		t.Fatalf("concurrent index migration recorded %d times, want 1", got)
	}
	if _, valid := fixtureIndex(t, store, "idx_fixture_items_label"); !valid {
		t.Fatal("the index must be valid")
	}
}

func TestMigrationLockWaitStopsWithTheContext(t *testing.T) {
	store := freshMigrationStore(t)
	ctx := context.Background()
	holder, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatalf("reserve the lock holder: %v", err)
	}
	defer holder.Close()
	if err := lockMigration(ctx, holder); err != nil {
		t.Fatalf("hold the migration lock: %v", err)
	}
	defer unlockMigration(holder)
	waitContext, cancel := context.WithTimeout(ctx, 3*migrationLockRetry)
	defer cancel()
	err = store.runMigrations(waitContext, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting past the deadline = %v, want the context deadline", err)
	}
}

func TestConcurrentIndexMigrationRefusesASchemaQualifiedTable(t *testing.T) {
	for _, table := range []string{"archive.fixture_items", "archive .fixture_items", "archive. fixture_items", "archive . fixture_items"} {
		body := concurrentIndexDirective + "\nCREATE INDEX CONCURRENTLY IF NOT EXISTS idx_fixture_items_label ON " + table + " (label);\n"
		if _, err := parseMigration("0002_fixture", body); err == nil {
			t.Fatalf("an index on %q must be refused, because the runner looks the index up without a schema", table)
		}
	}
	using := concurrentIndexDirective + "\nCREATE INDEX CONCURRENTLY IF NOT EXISTS idx_fixture_items_label ON fixture_items USING btree (label);\n"
	if _, err := parseMigration("0002_fixture", using); err != nil {
		t.Fatalf("a table followed by USING = %v", err)
	}
	plain := concurrentIndexDirective + "\nCREATE INDEX CONCURRENTLY IF NOT EXISTS idx_fixture_items_label ON fixture_items(label);\n"
	if _, err := parseMigration("0002_fixture", plain); err != nil {
		t.Fatalf("a plain table name = %v", err)
	}
}
