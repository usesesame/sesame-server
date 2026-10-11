package syncstore

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

const (
	DefaultMaintenanceInterval = time.Hour
	MinimumMaintenanceInterval = time.Minute
	MaximumMaintenanceInterval = 24 * time.Hour
	MinimumDeviceRetention     = 24 * time.Hour
	maintenanceRunTimeout      = 30 * time.Second
)

type Purger interface {
	PurgeExpiredChallenges(ctx context.Context) (int64, error)
	PurgeRevokedDevices(ctx context.Context, olderThan time.Duration) (int64, error)
}

type MaintenanceConfig struct {
	Interval        time.Duration
	DeviceRetention time.Duration
	Enabled         func(ctx context.Context) bool
}

func ParseMaintenanceConfig(interval, retention string) (MaintenanceConfig, error) {
	config := MaintenanceConfig{Interval: DefaultMaintenanceInterval, DeviceRetention: RevokedDeviceRetention}
	if value := strings.TrimSpace(interval); value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil {
			return MaintenanceConfig{}, fmt.Errorf("SESAME_SYNC_MAINTENANCE_INTERVAL %q is not a duration such as 1h", value)
		}
		if parsed < MinimumMaintenanceInterval || parsed > MaximumMaintenanceInterval {
			return MaintenanceConfig{}, fmt.Errorf("SESAME_SYNC_MAINTENANCE_INTERVAL must be between %s and %s", MinimumMaintenanceInterval, MaximumMaintenanceInterval)
		}
		config.Interval = parsed
	}
	if value := strings.TrimSpace(retention); value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil {
			return MaintenanceConfig{}, fmt.Errorf("SESAME_SYNC_REVOKED_DEVICE_RETENTION %q is not a duration such as 2160h", value)
		}
		if parsed < MinimumDeviceRetention {
			return MaintenanceConfig{}, fmt.Errorf("SESAME_SYNC_REVOKED_DEVICE_RETENTION must be at least %s", MinimumDeviceRetention)
		}
		config.DeviceRetention = parsed
	}
	return config, nil
}

func RunMaintenance(ctx context.Context, store Purger, config MaintenanceConfig) {
	if config.Interval <= 0 {
		config.Interval = DefaultMaintenanceInterval
	}
	if config.DeviceRetention < MinimumDeviceRetention {
		config.DeviceRetention = RevokedDeviceRetention
	}
	RunMaintenanceOnce(ctx, store, config)
	ticker := time.NewTicker(config.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			RunMaintenanceOnce(ctx, store, config)
		}
	}
}

func RunMaintenanceOnce(ctx context.Context, store Purger, config MaintenanceConfig) {
	if ctx.Err() != nil {
		return
	}
	runContext, cancel := context.WithTimeout(ctx, maintenanceRunTimeout)
	defer cancel()
	if config.Enabled != nil && !config.Enabled(runContext) {
		return
	}
	if removed, err := store.PurgeExpiredChallenges(runContext); err != nil {
		slog.Warn("Sesame Sync could not purge enrollment challenges", "error", err)
	} else if removed > 0 {
		slog.Info("purged Sesame Sync enrollment challenges", "count", removed)
	}
	if removed, err := store.PurgeRevokedDevices(runContext, config.DeviceRetention); err != nil {
		slog.Warn("Sesame Sync could not purge every revoked device", "removed", removed, "error", err)
	} else if removed > 0 {
		slog.Info("purged revoked Sesame Sync devices", "count", removed)
	}
}
