package testdb

import (
	"context"
	"os"
	"testing"
)

const urlVariable = "SESAME_TEST_DATABASE_URL"

func URL(t testing.TB) string {
	t.Helper()
	databaseURL := os.Getenv(urlVariable)
	if databaseURL == "" {
		t.Skip(urlVariable + " is required")
	}
	return databaseURL
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
