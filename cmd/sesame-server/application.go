package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"usesesame.app/backend/internal/selfhost"
	"usesesame.app/backend/internal/selfhost/config"
	"usesesame.app/backend/internal/selfhost/console"
	"usesesame.app/backend/internal/selfhost/ops"
	"usesesame.app/backend/internal/selfhost/secrets"
	"usesesame.app/backend/internal/selfhost/server"
	"usesesame.app/backend/internal/selfhost/sqlitestore"
	"usesesame.app/backend/internal/selfhost/updates"
)

const maintenanceInterval = time.Hour

func databasePath(cfg config.Config) string {
	return filepath.Join(cfg.DataDir, ops.DatabaseName)
}

func openStore(ctx context.Context, cfg config.Config, loaded *secrets.Secrets, readOnly bool) (*sqlitestore.Store, error) {
	store, err := sqlitestore.Open(ctx, sqlitestore.Options{
		Path:     databasePath(cfg),
		AdminKey: loaded.AdminEncryptionKey,
		Flags:    server.FlagDefinitions(),
		ReadOnly: readOnly,
	})
	if errors.Is(err, selfhost.ErrSchemaTooNew) {
		return nil, fmt.Errorf("%s was written by a newer Sesame server and this one refuses to open it: %w", databasePath(cfg), err)
	}
	if err != nil {
		return nil, fmt.Errorf("the database %s cannot be opened: %w", databasePath(cfg), err)
	}
	return store, nil
}

func buildApplication(ctx context.Context, input BuildInput) (*Application, error) {
	cfg, logger := input.Config, input.Logger
	options := []ops.Option{ops.WithVersion(input.Version), ops.WithLogger(logger)}
	if path, err := ops.PreMigration(ctx, cfg.DataDir, options...); err != nil {
		return nil, err
	} else if path != "" {
		logger.Info("Sesame wrote a backup before upgrading the database", "path", path)
	}
	store, err := openStore(ctx, cfg, input.Secrets, false)
	if err != nil {
		return nil, err
	}
	if err := announceFirstSetup(ctx, store, cfg, logger); err != nil {
		store.Close()
		return nil, err
	}
	updateService, err := updates.New(updates.Options{
		Store:       store,
		Version:     input.Version,
		Keys:        cfg.Updates.Keys,
		FeedURL:     cfg.Updates.FeedURL,
		InstallKind: updates.DetectInstallKind(cfg.Updates.InstallKind, pathExists),
		Logger:      logger,
	})
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("update checks cannot be set up: %w", err)
	}
	handler := server.New(server.Config{
		Store:          store,
		Version:        input.Version,
		Commit:         input.Commit,
		PublicURL:      cfg.PublicURL,
		TrustedProxies: cfg.TrustedProxies,
		SigningKey:     input.Secrets.SigningKey,
		IPPepper:       input.Secrets.IPPepper,
		Console:        console.FS(),
		Metrics:        cfg.Metrics,
		BackupInterval: cfg.BackupInterval,
		Warnings:       cfg.Warnings,
		Updates:        updateService,
	})
	backups := &ops.Runner{DataDir: cfg.DataDir, Interval: cfg.BackupInterval, Options: options, Recorder: store.RecordBackup, Logger: logger}
	return &Application{
		Handler: handler,
		Jobs: []func(context.Context){
			func(jobCtx context.Context) { _ = backups.Run(jobCtx) },
			func(jobCtx context.Context) { maintain(jobCtx, store, logger) },
			updateService.Run,
		},
		Close: store.Close,
	}, nil
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func announceFirstSetup(ctx context.Context, store selfhost.Store, cfg config.Config, logger *slog.Logger) error {
	required, err := store.SetupRequired(ctx)
	if err != nil {
		return fmt.Errorf("the database could not tell whether setup is needed: %w", err)
	}
	if !required {
		return nil
	}
	issue, err := store.StartFirstSetup(ctx)
	if err != nil {
		return fmt.Errorf("the first setup link could not be created: %w", err)
	}
	logger.Info(fmt.Sprintf("Open %s/setup#token=%s to create the first owner", cfg.PublicURL.Origin, issue.Token), "expires", issue.ExpiresAt.UTC().Format(time.RFC3339))
	return nil
}

func maintain(ctx context.Context, store selfhost.Store, logger *slog.Logger) {
	run := func(runCtx context.Context) {
		if _, err := store.Maintain(runCtx); err != nil && runCtx.Err() == nil {
			logger.Warn("Sesame maintenance failed", "error", err)
		}
	}
	run(ctx)
	runEvery(ctx, maintenanceInterval, run)
}
