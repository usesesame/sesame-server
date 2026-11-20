package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"

	"usesesame.app/backend/internal/testdb"
)

func TestRunRefusesBadArguments(t *testing.T) {
	const server = "postgres://fictional@db.example.invalid/postgres"
	cases := []struct {
		name   string
		args   []string
		server string
		want   string
	}{
		{"no arguments", nil, server, "usage"},
		{"unknown command", []string{"truncate", "scratch"}, server, "usage"},
		{"missing server", []string{"create", "scratch"}, "", "SESAME_TEST_DATABASE_URL is not set"},
		{"upper case name", []string{"create", "Scratch"}, server, "plain lowercase"},
		{"injected name", []string{"drop", "scratch; DROP DATABASE postgres"}, server, "plain lowercase"},
		{"empty name", []string{"create", ""}, server, "plain lowercase"},
		{"dbname selector", []string{"create", "scratch"}, server + "?dbname=postgres", "must not select a database"},
		{"database selector", []string{"drop", "scratch"}, server + "?sslmode=disable&database=postgres", "must not select a database"},
	}
	for _, c := range cases {
		err := run(c.args, c.server)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error = %v, want it to contain %q", c.name, err, c.want)
		}
	}
}

func TestCreateAndDropAScratchDatabase(t *testing.T) {
	serverURL := testdb.URL(t)
	name := fmt.Sprintf("sesame_scratch_%d", os.Getpid())
	t.Cleanup(func() { _ = run([]string{"drop", name}, serverURL) })
	if err := run([]string{"create", name}, serverURL); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := run([]string{"create", name}, serverURL); err == nil {
		t.Fatal("creating the same database twice must fail")
	}
	if !databaseExists(t, serverURL, name) {
		t.Fatal("the database was not created")
	}
	if err := run([]string{"drop", name}, serverURL); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if databaseExists(t, serverURL, name) {
		t.Fatal("the database survived the drop")
	}
	if err := run([]string{"drop", name}, serverURL); err != nil {
		t.Fatalf("dropping a missing database must succeed: %v", err)
	}
}

func databaseExists(t *testing.T, serverURL, name string) bool {
	t.Helper()
	parsed, err := url.Parse(serverURL)
	if err != nil {
		t.Fatal(err)
	}
	server, err := sql.Open("pgx", parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	var exists bool
	if err := server.QueryRowContext(context.Background(), `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, name).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists
}
