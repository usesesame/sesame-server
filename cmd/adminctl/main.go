package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/mail"
	"net/url"
	"os"
	"strings"
	"time"

	"usesesame.app/backend/internal/accounts"
	adminstore "usesesame.app/backend/internal/admin"
)

func main() {
	if len(os.Args) != 3 || (os.Args[1] != "bootstrap" && os.Args[1] != "reset") {
		fmt.Fprintln(os.Stderr, "usage: adminctl <bootstrap|reset> <email>")
		os.Exit(2)
	}
	email := strings.ToLower(strings.TrimSpace(os.Args[2]))
	parsedEmail, err := mail.ParseAddress(email)
	if err != nil || parsedEmail.Address != email {
		slog.Error("invalid admin email")
		os.Exit(2)
	}
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	keyValue := strings.TrimSpace(os.Getenv("SESAME_ADMIN_ENCRYPTION_KEY"))
	adminOrigin := strings.TrimSuffix(strings.TrimSpace(os.Getenv("SESAME_ADMIN_ORIGIN")), "/")
	if databaseURL == "" || keyValue == "" || adminOrigin == "" {
		slog.Error("DATABASE_URL, SESAME_ADMIN_ENCRYPTION_KEY, and SESAME_ADMIN_ORIGIN are required")
		os.Exit(1)
	}
	key, err := adminstore.ParseEncryptionKey(keyValue)
	if err != nil {
		slog.Error("invalid admin encryption key", "error", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	accountStore, err := accounts.Open(ctx, databaseURL)
	if err != nil {
		slog.Error("could not migrate account database", "error", err)
		os.Exit(1)
	}
	_ = accountStore.Close()
	store, err := adminstore.Open(ctx, databaseURL, key)
	if err != nil {
		slog.Error("could not open admin database", "error", err)
		os.Exit(1)
	}
	defer store.Close()
	var token string
	if os.Args[1] == "bootstrap" {
		token, err = store.BootstrapSuper(ctx, email, time.Now().UTC().Add(time.Hour))
	} else {
		if readable, checkErr := store.SecretReadable(ctx, email); checkErr == nil && !readable {
			fmt.Fprintln(os.Stderr, "Warning: this account's MFA secret does not decrypt with the configured")
			fmt.Fprintln(os.Stderr, "SESAME_ADMIN_ENCRYPTION_KEY. That, not a forgotten password, is why sign-in")
			fmt.Fprintln(os.Stderr, "was failing. Resetting now re-encrypts under the current key; if the key is")
			fmt.Fprintln(os.Stderr, "not the one the API runs with, the lockout will come back.")
		}
		token, err = store.ResetSetup(ctx, email, time.Now().UTC().Add(time.Hour))
	}
	if err != nil {
		slog.Error("could not prepare admin setup", "error", err)
		os.Exit(1)
	}
	fmt.Printf("Open this one-time setup link within one hour:\n%s/setup?token=%s\n", adminOrigin, url.QueryEscape(token))
}
