package ops

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"time"
)

const (
	scheduledPrefix    = "sesame-backup-"
	preMigrationPrefix = "sesame-premigration-"
	archiveSuffix      = ".tar"
	nameTimeFormat     = "20060102T150405Z"
)

var (
	scheduledName    = regexp.MustCompile(`^sesame-backup-(\d{8}T\d{6}Z)(?:-(\d+))?\.tar$`)
	preMigrationName = regexp.MustCompile(`^sesame-premigration-v\d+-(\d{8}T\d{6}Z)(?:-(\d+))?\.tar$`)
)

type Runner struct {
	DataDir  string
	Interval time.Duration
	Keep     int
	Options  []Option
	Recorder func(ctx context.Context, at time.Time) error
	Logger   *slog.Logger
}

func BackupDir(dataDir string) string { return filepath.Join(dataDir, BackupDirName) }

func (r *Runner) options() []Option {
	options := append([]Option{}, r.Options...)
	if r.Keep != 0 {
		options = append(options, WithKeep(r.Keep))
	}
	if r.Logger != nil {
		options = append(options, WithLogger(r.Logger))
	}
	return options
}

func (r *Runner) Run(ctx context.Context) error {
	if r.Interval <= 0 {
		return nil
	}
	cfg, err := newConfig(r.options())
	if err != nil {
		return err
	}
	for {
		wait := r.untilDue(cfg)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		if path, err := r.RunOnce(ctx); err != nil {
			if backupEndedWithShutdown(ctx, cfg, path, err) {
				return nil
			}
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(minDuration(r.Interval, time.Hour)):
			}
		} else {
			cfg.logger.Info("scheduled backup written", "path", path)
		}
	}
}

func backupEndedWithShutdown(ctx context.Context, cfg config, path string, err error) bool {
	if ctx.Err() != nil {
		cfg.logger.Info("scheduled backup stopped by shutdown", "error", err, "path", path)
		return true
	}
	cfg.logger.Error("scheduled backup failed", "error", err, "path", path)
	return false
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func (r *Runner) untilDue(cfg config) time.Duration {
	newest, ok := newestScheduled(BackupDir(r.DataDir))
	if !ok {
		return 0
	}
	due := newest.Add(r.Interval).Sub(cfg.now())
	if due < 0 {
		return 0
	}
	return due
}

func (r *Runner) RunOnce(ctx context.Context) (string, error) {
	cfg, err := newConfig(r.options())
	if err != nil {
		return "", err
	}
	dir := BackupDir(r.DataDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path, manifest, err := writeNamed(ctx, r.DataDir, dir, scheduledPrefix, cfg)
	if err != nil {
		return "", err
	}
	var problems []error
	if r.Recorder != nil {
		if err := r.Recorder(ctx, manifest.CreatedAt); err != nil {
			problems = append(problems, fmt.Errorf("backup time could not be recorded: %w", err))
		}
	}
	if _, err := Rotate(dir, cfg.keep); err != nil {
		problems = append(problems, fmt.Errorf("old backups could not be rotated: %w", err))
	}
	return path, errors.Join(problems...)
}

func writeNamed(ctx context.Context, dataDir, dir, prefix string, cfg config) (string, Manifest, error) {
	stamp := cfg.now().UTC().Format(nameTimeFormat)
	for attempt := 0; attempt < 50; attempt++ {
		name := prefix + stamp + archiveSuffix
		if attempt > 0 {
			name = prefix + stamp + "-" + strconv.Itoa(attempt) + archiveSuffix
		}
		path := filepath.Join(dir, name)
		manifest, err := backup(ctx, dataDir, path, cfg)
		if errors.Is(err, ErrBackupExists) {
			continue
		}
		return path, manifest, err
	}
	return "", Manifest{}, errors.New("a free backup file name could not be found")
}

type ownedBackup struct {
	path  string
	stamp string
	count int
}

func ownedBackups(dir string, pattern *regexp.Regexp) ([]ownedBackup, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var owned []ownedBackup
	for _, entry := range entries {
		match := pattern.FindStringSubmatch(entry.Name())
		if match == nil || !entry.Type().IsRegular() {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if _, err := peekManifest(path); err != nil {
			continue
		}
		count := 0
		if match[2] != "" {
			count, _ = strconv.Atoi(match[2])
		}
		owned = append(owned, ownedBackup{path: path, stamp: match[1], count: count})
	}
	sort.Slice(owned, func(i, j int) bool {
		if owned[i].stamp != owned[j].stamp {
			return owned[i].stamp > owned[j].stamp
		}
		return owned[i].count > owned[j].count
	})
	return owned, nil
}

func newestScheduled(dir string) (time.Time, bool) {
	owned, err := ownedBackups(dir, scheduledName)
	if err != nil || len(owned) == 0 {
		return time.Time{}, false
	}
	moment, err := time.Parse(nameTimeFormat, owned[0].stamp)
	return moment, err == nil
}

func Rotate(dir string, keep int) ([]string, error) {
	if keep < 1 {
		return nil, fmt.Errorf("ops: keep must be at least 1, got %d", keep)
	}
	var removed []string
	var problems []error
	for _, pattern := range []*regexp.Regexp{scheduledName, preMigrationName} {
		owned, err := ownedBackups(dir, pattern)
		if err != nil {
			return removed, err
		}
		if len(owned) <= keep {
			continue
		}
		for _, stale := range owned[keep:] {
			info, err := os.Lstat(stale.path)
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			if err := os.Remove(stale.path); err != nil {
				problems = append(problems, err)
				continue
			}
			removed = append(removed, stale.path)
		}
	}
	return removed, errors.Join(problems...)
}

func PreMigration(ctx context.Context, dataDir string, options ...Option) (string, error) {
	cfg, err := newConfig(options)
	if err != nil {
		return "", err
	}
	databasePath := filepath.Join(dataDir, DatabaseName)
	present, err := exists(databasePath)
	if err != nil || !present {
		return "", err
	}
	stored, err := storedSchemaVersion(ctx, databasePath)
	if err != nil {
		return "", fmt.Errorf("schema version of %s cannot be read before the migration: %w", databasePath, err)
	}
	supported, err := supportedSchema(ctx)
	if err != nil {
		return "", err
	}
	if stored < 1 || stored >= supported {
		return "", nil
	}
	dir := BackupDir(dataDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path, _, err := writeNamed(ctx, dataDir, dir, fmt.Sprintf("%sv%d-", preMigrationPrefix, stored), cfg)
	if err != nil {
		return "", fmt.Errorf("backup before migration failed: %w", err)
	}
	if _, err := Rotate(dir, cfg.keep); err != nil {
		cfg.logger.Warn("old backups could not be rotated", "error", err)
	}
	return path, nil
}
