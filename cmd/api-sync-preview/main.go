package main

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"usesesame.app/backend/internal/accounts"
	adminstore "usesesame.app/backend/internal/admin"
	"usesesame.app/backend/internal/httpapi"
	"usesesame.app/backend/internal/syncstore"
)

func main() {
	if strings.TrimSpace(os.Getenv("SESAME_ENV")) != "development" {
		slog.Error("api-sync-preview refuses to start",
			"reason", "SESAME_ENV must be development",
			"detail", "this binary wires Sesame Sync, which has not passed its security review")
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	webOrigin := strings.TrimSuffix(strings.TrimSpace(os.Getenv("SESAME_WEB_ORIGIN")), "/")
	adminOrigin := strings.TrimSuffix(strings.TrimSpace(os.Getenv("SESAME_ADMIN_ORIGIN")), "/")
	adminKeyValue := strings.TrimSpace(os.Getenv("SESAME_ADMIN_ENCRYPTION_KEY"))
	adminPepper := strings.TrimSpace(os.Getenv("SESAME_ADMIN_IP_PEPPER"))
	if databaseURL == "" || webOrigin == "" || adminOrigin == "" || adminKeyValue == "" || adminPepper == "" {
		slog.Error("api-sync-preview configuration is incomplete",
			"required", "DATABASE_URL, SESAME_WEB_ORIGIN, SESAME_ADMIN_ORIGIN, SESAME_ADMIN_ENCRYPTION_KEY, SESAME_ADMIN_IP_PEPPER")
		os.Exit(1)
	}

	store, err := accounts.OpenWithoutMigrate(ctx, databaseURL)
	if err != nil {
		slog.Error("could not open the account database", "error", err)
		os.Exit(1)
	}
	defer store.Close()

	adminKey, err := adminstore.ParseEncryptionKey(adminKeyValue)
	if err != nil {
		slog.Error("admin encryption key is invalid", "error", err)
		os.Exit(1)
	}
	admin, err := adminstore.Open(ctx, databaseURL, adminKey)
	if err != nil {
		slog.Error("could not open the admin database", "error", err)
		os.Exit(1)
	}
	defer admin.Close()

	syncDB, err := openDatabase(databaseURL)
	if err != nil {
		slog.Error("could not open the Sync database", "error", err)
		os.Exit(1)
	}
	defer syncDB.Close()

	enabled, flagErr := admin.FeatureFlag(ctx, "cloud_sync_available")
	if flagErr != nil || enabled != "true" {
		slog.Warn("Sesame Sync is wired but the flag is off",
			"flag", "cloud_sync_available",
			"effect", "every /v1/sync route will answer 403 until an administrator turns it on")
	}

	config := httpapi.Config{
		Version:              "0.1.0-sync-preview",
		AllowedOrigin:        webOrigin,
		SessionSecure:        false,
		Accounts:             store,
		Admin:                admin,
		Sync:                 syncstore.New(syncDB).WithReceiptKey(capabilityKey()),
		AdminOrigin:          adminOrigin,
		AdminSecure:          false,
		AdminIPPepper:        adminPepper,
		CapabilitySigningKey: capabilityKey(),
		CapabilityKeyID:      "sync-preview",
		RegistrationMode:     "invite",
		WebBaseURL:           webOrigin,
	}

	address := env("SESAME_API_ADDR", "127.0.0.1:8787")
	if err := requireLoopback(address); err != nil {
		slog.Error("api-sync-preview refuses to start", "reason", err.Error())
		os.Exit(1)
	}
	server := &http.Server{
		Addr:              address,
		Handler:           httpapi.New(config),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       20 * time.Second,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			slog.Error("shutdown failed", "error", err)
		}
	}()
	slog.Warn("Sesame Sync preview API listening",
		"address", address,
		"warning", "development only. Sync has not passed its security review.")
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func requireLoopback(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("SESAME_API_ADDR %q is not a host:port address", address)
	}
	if host == "" {
		return fmt.Errorf("SESAME_API_ADDR %q listens on every interface", address)
	}
	if host == "localhost" {
		return nil
	}
	parsed := net.ParseIP(host)
	if parsed == nil || !parsed.IsLoopback() {
		return fmt.Errorf("SESAME_API_ADDR %q is not a loopback address; the Sync preview must not be reachable off this machine", address)
	}
	return nil
}

func openDatabase(databaseURL string) (*sql.DB, error) {
	config, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	db := stdlib.OpenDB(*config)
	db.SetMaxOpenConns(6)
	db.SetConnMaxLifetime(30 * time.Minute)
	return db, nil
}

func capabilityKey() ed25519.PrivateKey {
	value := strings.TrimSpace(os.Getenv("SESAME_CAPABILITY_SIGNING_KEY"))
	if decoded, err := base64.RawURLEncoding.DecodeString(value); err == nil {
		switch len(decoded) {
		case ed25519.SeedSize:
			return ed25519.NewKeyFromSeed(decoded)
		case ed25519.PrivateKeySize:
			return ed25519.PrivateKey(decoded)
		}
	}
	_, generated, err := ed25519.GenerateKey(nil)
	if err != nil {
		return nil
	}
	return generated
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
