package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	urlVariable = "SESAME_TEST_DATABASE_URL"
	timeout     = 30 * time.Second
)

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

func main() {
	if err := run(os.Args[1:], os.Getenv(urlVariable)); err != nil {
		fmt.Fprintln(os.Stderr, "scratchdb:", err)
		os.Exit(1)
	}
}

func run(args []string, serverURL string) error {
	if len(args) != 2 || (args[0] != "create" && args[0] != "drop") {
		return fmt.Errorf("usage: scratchdb <create|drop> <name>")
	}
	if serverURL == "" {
		return fmt.Errorf("%s is not set", urlVariable)
	}
	name := args[1]
	if !namePattern.MatchString(name) {
		return fmt.Errorf("%q is not a plain lowercase database name", name)
	}
	parsed, err := url.Parse(serverURL)
	if err != nil {
		return fmt.Errorf("%s is not a URL", urlVariable)
	}
	for _, key := range []string{"dbname", "database"} {
		if parsed.Query().Has(key) {
			return fmt.Errorf("%s must not select a database with %q", urlVariable, key)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	server, err := sql.Open("pgx", serverURL)
	if err != nil {
		return err
	}
	defer server.Close()
	quoted := pgx.Identifier{name}.Sanitize()
	if args[0] == "create" {
		if _, err := server.ExecContext(ctx, "CREATE DATABASE "+quoted); err != nil { //nolint:gosec
			return err
		}
		parsed.Path = "/" + name
		fmt.Println(parsed.String())
		return nil
	}
	if _, err := server.ExecContext(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()`, name); err != nil {
		return err
	}
	_, err = server.ExecContext(ctx, "DROP DATABASE IF EXISTS "+quoted) //nolint:gosec
	return err
}
