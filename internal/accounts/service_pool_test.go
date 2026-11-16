package accounts

import (
	"context"
	"errors"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestServiceTimeoutsFillOnlyWhatTheOperatorLeftUnset(t *testing.T) {
	config, err := pgx.ParseConfig("postgres://fictional@127.0.0.1:5432/sesame?statement_timeout=5s")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ApplyServiceTimeouts(config)
	if config.RuntimeParams["statement_timeout"] != "5s" || config.RuntimeParams["idle_in_transaction_session_timeout"] != serviceIdleInTransaction {
		t.Fatalf("runtime parameters = %v", config.RuntimeParams)
	}
	plain, err := pgx.ParseConfig("postgres://fictional@127.0.0.1:5432/sesame")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	plain.RuntimeParams = nil
	ApplyServiceTimeouts(plain)
	if plain.RuntimeParams["statement_timeout"] != serviceStatementTimeout || plain.RuntimeParams["idle_in_transaction_session_timeout"] != serviceIdleInTransaction {
		t.Fatalf("runtime parameters = %v", plain.RuntimeParams)
	}
}

func serviceTestDatabaseURL(t *testing.T) string {
	t.Helper()
	databaseURL := os.Getenv("SESAME_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("SESAME_TEST_DATABASE_URL is required")
	}
	return databaseURL
}

func withoutSessionTimeouts(databaseURL string) string {
	names := []string{"statement_timeout", "idle_in_transaction_session_timeout"}
	if parsed, err := url.Parse(databaseURL); err == nil && parsed.Scheme != "" {
		query := parsed.Query()
		for _, name := range names {
			query.Del(name)
		}
		parsed.RawQuery = query.Encode()
		return parsed.String()
	}
	return sessionTimeoutSetting.ReplaceAllString(databaseURL, "")
}

var sessionTimeoutSetting = regexp.MustCompile(`(^|\s)(statement_timeout|idle_in_transaction_session_timeout)\s*=\s*('(\\.|[^'\\])*'|\S*)`)

func TestRemovingSessionTimeoutsLeavesQuotedValuesAlone(t *testing.T) {
	got := withoutSessionTimeouts(`host=127.0.0.1 password='two  spaces' statement_timeout=5s idle_in_transaction_session_timeout='7s' dbname=sesame`)
	if got != `host=127.0.0.1 password='two  spaces' dbname=sesame` {
		t.Fatalf("connection string = %q", got)
	}
}

func plainConnectionSetting(t *testing.T, databaseURL, name string) string {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), databaseURL)
	if err != nil {
		t.Fatalf("open a plain connection: %v", err)
	}
	defer conn.Close(context.Background())
	var value string
	if err := conn.QueryRow(context.Background(), "SHOW "+name).Scan(&value); err != nil {
		t.Fatalf("show %s on a plain connection: %v", name, err)
	}
	return value
}

func showSetting(t *testing.T, store *PostgresStore, name string) string {
	t.Helper()
	var value string
	if err := store.DB().QueryRowContext(context.Background(), "SHOW "+name).Scan(&value); err != nil {
		t.Fatalf("show %s: %v", name, err)
	}
	return value
}

func TestTheServicePoolBoundsStatementsAndIdleTransactions(t *testing.T) {
	databaseURL := withoutSessionTimeouts(serviceTestDatabaseURL(t))
	ctx := context.Background()
	service, err := OpenService(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open service store: %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	if got := showSetting(t, service, "statement_timeout"); got != "1min" {
		t.Fatalf("service statement_timeout = %q, want 1min", got)
	}
	if got := showSetting(t, service, "idle_in_transaction_session_timeout"); got != "1min" {
		t.Fatalf("service idle_in_transaction_session_timeout = %q, want 1min", got)
	}
	if stats := service.DB().Stats(); stats.MaxOpenConnections != servicePoolConnections {
		t.Fatalf("service pool allows %d connections, want %d", stats.MaxOpenConnections, servicePoolConnections)
	}

	migrations, err := OpenWithoutMigrate(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open migration store: %v", err)
	}
	t.Cleanup(func() { _ = migrations.Close() })
	for _, name := range []string{"statement_timeout", "idle_in_transaction_session_timeout"} {
		if got, want := showSetting(t, migrations, name), plainConnectionSetting(t, databaseURL, name); got != want {
			t.Fatalf("migration %s = %q, want the connection's own %q so long migrations finish", name, got, want)
		}
	}
}

func TestTheServicePoolCancelsAStatementPastItsTimeout(t *testing.T) {
	databaseURL := withoutSessionTimeouts(serviceTestDatabaseURL(t))
	separator := "?"
	if strings.Contains(databaseURL, "?") {
		separator = "&"
	}
	service, err := OpenService(context.Background(), databaseURL+separator+"statement_timeout=100ms")
	if err != nil {
		t.Fatalf("open service store: %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	_, err = service.DB().ExecContext(context.Background(), `SELECT pg_sleep(2)`)
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) || postgresError.Code != "57014" {
		t.Fatalf("a statement past the timeout = %v, want query_canceled", err)
	}
}

func TestAccountEventPurgeHasAnExpiryIndex(t *testing.T) {
	databaseURL := serviceTestDatabaseURL(t)
	store, err := Open(context.Background(), databaseURL)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	var definition string
	if err := store.DB().QueryRowContext(context.Background(), `
		SELECT indexdef FROM pg_indexes
		WHERE tablename = 'sesame_account_events' AND indexname = 'idx_account_events_expires_at'`).Scan(&definition); err != nil {
		t.Fatalf("find the expiry index: %v", err)
	}
	if !strings.Contains(definition, "(expires_at)") {
		t.Fatalf("expiry index = %q", definition)
	}
	if err := store.PurgeExpired(context.Background()); err != nil {
		t.Fatalf("purge expired records: %v", err)
	}
}
