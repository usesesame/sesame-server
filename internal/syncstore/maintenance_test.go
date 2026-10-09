package syncstore

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakePurger struct {
	mu             sync.Mutex
	challengeCalls int
	deviceCalls    int
	retentions     []time.Duration
	challengeErr   error
	deviceErr      error
	deviceRemoved  int64
}

func (f *fakePurger) PurgeExpiredChallenges(context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.challengeCalls++
	return 0, f.challengeErr
}

func (f *fakePurger) PurgeRevokedDevices(_ context.Context, olderThan time.Duration) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deviceCalls++
	f.retentions = append(f.retentions, olderThan)
	return f.deviceRemoved, f.deviceErr
}

func (f *fakePurger) calls() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.challengeCalls, f.deviceCalls
}

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buffer bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buffer, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &buffer
}

func TestParseMaintenanceConfigDefaultsAndBounds(t *testing.T) {
	defaults, err := ParseMaintenanceConfig("", "  ")
	if err != nil {
		t.Fatalf("defaults: %v", err)
	}
	if defaults.Interval != time.Hour || defaults.DeviceRetention != RevokedDeviceRetention {
		t.Fatalf("defaults = %s and %s, want 1h and %s", defaults.Interval, defaults.DeviceRetention, RevokedDeviceRetention)
	}
	custom, err := ParseMaintenanceConfig("15m", "720h")
	if err != nil || custom.Interval != 15*time.Minute || custom.DeviceRetention != 720*time.Hour {
		t.Fatalf("custom = %+v, %v", custom, err)
	}
	for _, rejected := range [][2]string{
		{"soon", ""}, {"0s", ""}, {"-1h", ""}, {"10s", ""}, {"48h", ""},
		{"", "forever"}, {"", "0s"}, {"", "-24h"}, {"", "1h"},
	} {
		if _, err := ParseMaintenanceConfig(rejected[0], rejected[1]); err == nil {
			t.Fatalf("interval %q and retention %q were accepted", rejected[0], rejected[1])
		}
	}
}

func TestRunMaintenanceOnceRunsBothPurgesWithTheConfiguredRetention(t *testing.T) {
	store := &fakePurger{}
	RunMaintenanceOnce(context.Background(), store, MaintenanceConfig{DeviceRetention: 48 * time.Hour})
	challenges, devices := store.calls()
	if challenges != 1 || devices != 1 || store.retentions[0] != 48*time.Hour {
		t.Fatalf("calls = %d challenge, %d device, retentions %v", challenges, devices, store.retentions)
	}
}

func TestRunMaintenanceOnceDoesNothingWhileSyncIsOff(t *testing.T) {
	store := &fakePurger{}
	RunMaintenanceOnce(context.Background(), store, MaintenanceConfig{
		DeviceRetention: RevokedDeviceRetention,
		Enabled:         func(context.Context) bool { return false },
	})
	if challenges, devices := store.calls(); challenges != 0 || devices != 0 {
		t.Fatalf("calls = %d and %d, want none while the flag is off", challenges, devices)
	}
}

func TestRunMaintenanceOnceLogsAFailureAndStillRunsTheOtherPurge(t *testing.T) {
	logs := captureLogs(t)
	store := &fakePurger{challengeErr: errors.New("challenge table unavailable"), deviceRemoved: 2}
	RunMaintenanceOnce(context.Background(), store, MaintenanceConfig{DeviceRetention: RevokedDeviceRetention})
	if _, devices := store.calls(); devices != 1 {
		t.Fatalf("device purge calls = %d, want 1 after a challenge purge failure", devices)
	}
	output := logs.String()
	if !strings.Contains(output, "could not purge enrollment challenges") || !strings.Contains(output, "challenge table unavailable") {
		t.Fatalf("logs do not report the challenge failure: %s", output)
	}
	if !strings.Contains(output, "purged revoked Sesame Sync devices") || !strings.Contains(output, "count=2") {
		t.Fatalf("logs do not report the removed devices: %s", output)
	}
}

func TestRunMaintenanceRepeatsAndStopsWhenTheContextIsCancelled(t *testing.T) {
	store := &fakePurger{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		RunMaintenance(ctx, store, MaintenanceConfig{Interval: 5 * time.Millisecond, DeviceRetention: RevokedDeviceRetention})
	}()
	deadline := time.After(5 * time.Second)
	for {
		if challenges, devices := store.calls(); challenges >= 3 && devices >= 3 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the job did not repeat")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunMaintenance did not stop after cancellation")
	}
}

func TestRunMaintenanceOnceSkipsACancelledContext(t *testing.T) {
	store := &fakePurger{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	RunMaintenanceOnce(ctx, store, MaintenanceConfig{DeviceRetention: RevokedDeviceRetention})
	if challenges, devices := store.calls(); challenges != 0 || devices != 0 {
		t.Fatalf("calls = %d and %d, want none after cancellation", challenges, devices)
	}
}
