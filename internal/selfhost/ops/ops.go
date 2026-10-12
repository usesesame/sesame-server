package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"usesesame.app/backend/internal/selfhost/secrets"
)

const (
	FormatVersion = "sesame-selfhost-backup-v1"
	ManifestName  = "manifest.json"
	DatabaseName  = secrets.DatabaseFile
	ConfigName    = "config.json"
	LockFileName  = "sesame.lock"
	BackupDirName = "backups"

	DefaultKeep = 7

	maxManifestBytes = 64 << 10
	maxMembers       = 8
)

var (
	ErrInvalidArchive  = errors.New("ops: invalid backup archive")
	ErrSecretsMismatch = errors.New("ops: secrets do not match the database")
	ErrInstanceRunning = errors.New("ops: an instance is using this data directory")
	ErrBackupExists    = errors.New("ops: backup file already exists")
	ErrDatabaseDamaged = errors.New("ops: database failed its checks")
)

type Limits struct {
	MaxDatabaseBytes int64
	MaxConfigBytes   int64
}

func DefaultLimits() Limits {
	return Limits{MaxDatabaseBytes: 4 << 30, MaxConfigBytes: 1 << 20}
}

func (l Limits) maxArchiveBytes() int64 {
	return l.MaxDatabaseBytes + l.MaxConfigBytes + maxManifestBytes + int64(len(secrets.Files()))*secrets.KeySize + int64(maxMembers)*4096
}

type Snapshotter interface {
	Backup(ctx context.Context, destination string) error
}

type config struct {
	version     string
	snapshotter Snapshotter
	now         func() time.Time
	limits      Limits
	workDir     string
	keep        int
	logger      *slog.Logger
	hook        func(step string) error
}

type Option func(*config)

func WithVersion(version string) Option { return func(c *config) { c.version = version } }

func WithSnapshotter(snapshotter Snapshotter) Option {
	return func(c *config) { c.snapshotter = snapshotter }
}

func WithClock(now func() time.Time) Option { return func(c *config) { c.now = now } }

func WithLimits(limits Limits) Option { return func(c *config) { c.limits = limits } }

func WithWorkDir(dir string) Option { return func(c *config) { c.workDir = dir } }

func WithKeep(keep int) Option { return func(c *config) { c.keep = keep } }

func WithLogger(logger *slog.Logger) Option { return func(c *config) { c.logger = logger } }

func newConfig(options []Option) (config, error) {
	cfg := config{version: "dev", now: time.Now, limits: DefaultLimits(), keep: DefaultKeep, logger: slog.Default()}
	for _, option := range options {
		option(&cfg)
	}
	if cfg.keep < 1 {
		return cfg, fmt.Errorf("ops: keep must be at least 1, got %d", cfg.keep)
	}
	if cfg.limits.MaxDatabaseBytes < 1 || cfg.limits.MaxConfigBytes < 1 {
		return cfg, errors.New("ops: limits must be positive")
	}
	return cfg, nil
}

func (c config) step(name string) error {
	if c.hook == nil {
		return nil
	}
	return c.hook(name)
}

type ManifestMember struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	Format        string           `json:"format"`
	ServerVersion string           `json:"serverVersion"`
	SchemaVersion int              `json:"schemaVersion"`
	CreatedAt     time.Time        `json:"createdAt"`
	Members       []ManifestMember `json:"members"`
}

func memberNames(withConfig bool) []string {
	names := []string{DatabaseName}
	for _, name := range secrets.Files() {
		names = append(names, secretMemberName(name))
	}
	if withConfig {
		names = append(names, ConfigName)
	}
	return names
}

func secretMemberName(file string) string { return secrets.DirName + "/" + file }

func allowedMember(name string) bool {
	if name == DatabaseName || name == ConfigName {
		return true
	}
	for _, file := range secrets.Files() {
		if name == secretMemberName(file) {
			return true
		}
	}
	return false
}

func requiredMembers() []string { return memberNames(false) }
