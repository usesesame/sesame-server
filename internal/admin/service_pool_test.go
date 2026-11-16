package admin

import (
	"bytes"
	"context"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
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
	kept := []string{}
	for _, field := range strings.Fields(databaseURL) {
		if !slices.ContainsFunc(names, func(name string) bool { return strings.HasPrefix(field, name+"=") }) {
			kept = append(kept, field)
		}
	}
	return strings.Join(kept, " ")
}

func TestTheAdminPoolBoundsStatementsAndIdleTransactions(t *testing.T) {
	databaseURL := os.Getenv("SESAME_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("SESAME_TEST_DATABASE_URL is required")
	}
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
