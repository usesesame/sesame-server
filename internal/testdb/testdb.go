package testdb

import (
	"context"
	"os"
	"strings"
	"testing"
)

const (
	urlVariable = "SESAME_TEST_DATABASE_URL"
	ciVariable  = "CI"
)

func runsUnderCI(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "0", "false":
		return false
	}
	return true
}

func URL(t testing.TB) string {
	t.Helper()
	databaseURL := os.Getenv(urlVariable)
	if databaseURL != "" {
		return databaseURL
	}
	if runsUnderCI(os.Getenv(ciVariable)) {
		t.Fatalf("%s is not set, so the database tests cannot run, and CI must not pass without them", urlVariable)
	}
	t.Skip("the database tests did not run because " + urlVariable + " is not set, so set it to the URL of a throwaway PostgreSQL database and run go test ./... -p 1")
	return ""
}

func Open[Store interface{ Close() error }](t testing.TB, open func(context.Context, string) (Store, error)) Store {
	t.Helper()
	store, err := open(context.Background(), URL(t))
	if err != nil {
		t.Fatalf("open the test database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
