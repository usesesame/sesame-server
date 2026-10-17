package syncsvc

import (
	"context"
	"log/slog"
	"time"
)

const (
	DefaultMaintenanceInterval = time.Hour
	maintenanceRunTimeout      = 30 * time.Second
)

func RunMaintenance(ctx context.Context, service *Service, interval time.Duration, enabled func(context.Context) bool) {
	if interval <= 0 {
		interval = DefaultMaintenanceInterval
	}
	runMaintenanceOnce(ctx, service, enabled)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runMaintenanceOnce(ctx, service, enabled)
		}
	}
}

func runMaintenanceOnce(ctx context.Context, service *Service, enabled func(context.Context) bool) {
	if ctx.Err() != nil {
		return
	}
	runContext, cancel := context.WithTimeout(ctx, maintenanceRunTimeout)
	defer cancel()
	if enabled != nil && !enabled(runContext) {
		return
	}
	if err := service.Maintain(runContext); err != nil {
		slog.Warn("Sesame Sync could not purge expired records", "error", err)
	}
}
