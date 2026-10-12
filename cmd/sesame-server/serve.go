package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"usesesame.app/backend/internal/selfhost/config"
	"usesesame.app/backend/internal/selfhost/ops"
	"usesesame.app/backend/internal/selfhost/secrets"
	selfhostserver "usesesame.app/backend/internal/selfhost/server"
)

const shutdownTimeout = 8 * time.Second

func serve(ctx context.Context, env Environment, invocation Invocation) error {
	cfg, logger := invocation.Config, invocation.Logger
	for _, setting := range cfg.Effective() {
		logger.Info("Sesame configuration", "name", setting.Name, "value", setting.Value, "source", string(setting.Source))
	}
	for _, warning := range cfg.Warnings {
		logger.Warn("Sesame configuration warning", "warning", warning)
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return fmt.Errorf("%s=%s cannot be created: %w", config.EnvDataDir, cfg.DataDir, err)
	}
	recovered, err := ops.Recover(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("an interrupted restore in %s could not be finished: %w", cfg.DataDir, err)
	}
	if recovered != "" {
		logger.Warn("Sesame finished an interrupted restore", "detail", recovered)
	}
	lock, err := ops.AcquireInstanceLock(cfg.DataDir)
	if errors.Is(err, ops.ErrInstanceRunning) {
		return fmt.Errorf("another Sesame server is already using %s=%s", config.EnvDataDir, cfg.DataDir)
	}
	if err != nil {
		return err
	}
	defer func() {
		if err := lock.Release(); err != nil {
			logger.Warn("Sesame could not release the instance lock cleanly", "error", err)
		}
	}()
	loaded, generated, err := secrets.LoadOrCreate(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("instance secrets under %s=%s are unusable: %w", config.EnvDataDir, cfg.DataDir, err)
	}
	for _, name := range generated {
		logger.Info("Sesame generated an instance secret", "file", name, "directory", secrets.Dir(cfg.DataDir))
	}
	application, err := env.Commands.Serve.Build(ctx, BuildInput{Config: cfg, Secrets: loaded, Logger: logger, Version: env.Version, Commit: env.Commit})
	if err != nil {
		return err
	}
	if application == nil || application.Handler == nil {
		return errors.New("the server application has no HTTP handler")
	}
	if application.Close != nil {
		defer func() {
			if err := application.Close(); err != nil {
				logger.Error("Sesame could not close its resources", "error", err)
			}
		}()
	}
	jobs := application.Jobs
	listener, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("%s %q cannot be used: %w", config.EnvAddr, cfg.Addr, err)
	}
	httpServer := selfhostserver.NewHTTPServer(application.Handler)
	jobCtx, cancelJobs := context.WithCancel(ctx)
	defer cancelJobs()
	var running sync.WaitGroup
	for _, job := range jobs {
		running.Add(1)
		go func() {
			defer running.Done()
			job(jobCtx)
		}()
	}
	logger.Info("Sesame server listening", "address", listener.Addr().String(), "publicUrl", cfg.PublicURL.Origin, "version", env.Version)
	return serveHTTP(ctx, httpServer, listener, cancelJobs, &running, shutdownTimeout, logger)
}

func serveHTTP(ctx context.Context, server *http.Server, listener net.Listener, stopJobs context.CancelFunc, jobs *sync.WaitGroup, timeout time.Duration, logger *slog.Logger) error {
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	select {
	case err := <-served:
		stopJobs()
		jobs.Wait()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("the HTTP server stopped: %w", err)
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := server.Shutdown(shutdown); err != nil {
		logger.Error("Sesame shutdown failed", "error", err)
	}
	stopJobs()
	if !waitForBackgroundJobs(shutdown, jobs) {
		logger.Error("Sesame background jobs did not stop before the shutdown deadline")
	}
	<-served
	return nil
}

func waitForBackgroundJobs(ctx context.Context, jobs *sync.WaitGroup) bool {
	done := make(chan struct{})
	go func() {
		jobs.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
}

func runEvery(ctx context.Context, interval time.Duration, fn func(context.Context)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fn(ctx)
		}
	}
}
