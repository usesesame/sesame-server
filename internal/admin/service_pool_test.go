package admin

import (
	"bytes"
	"context"
	"net/url"
	"regexp"
	"testing"

	"usesesame.app/backend/internal/testdb"
)

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

func TestTheAdminPoolBoundsStatementsAndIdleTransactions(t *testing.T) {
	databaseURL := testdb.URL(t)
	store, err := Open(context.Background(), withoutSessionTimeouts(databaseURL), bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatalf("open admin store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for name, want := range map[string]string{"statement_timeout": "1min", "idle_in_transaction_session_timeout": "1min"} {
		var got string
		if err := store.db.QueryRowContext(context.Background(), "SHOW "+name).Scan(&got); err != nil {
			t.Fatalf("show %s: %v", name, err)
		}
		if got != want {
			t.Fatalf("admin %s = %q, want %q", name, got, want)
		}
	}
}
