package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"usesesame.app/backend/internal/accounts"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if databaseURL == "" {
		slog.Error("Sesame migrate configuration is invalid", "error", "DATABASE_URL is required")
		os.Exit(1)
	}

	store, err := accounts.OpenWithoutMigrate(ctx, databaseURL)
	if err != nil {
		slog.Error("Sesame migrate could not open the database", "error", err)
		os.Exit(1)
	}
	defer store.Close()

	if err := store.Migrate(ctx); err != nil {
		slog.Error("Sesame migrate failed", "error", err)
		os.Exit(1)
	}

	slog.Info("Sesame migrations applied successfully")
}
