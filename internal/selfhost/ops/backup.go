package ops

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"usesesame.app/backend/internal/selfhost/secrets"
)

func Backup(ctx context.Context, dataDir, outPath string, options ...Option) (Manifest, error) {
	cfg, err := newConfig(options)
	if err != nil {
		return Manifest{}, err
	}
	return backup(ctx, dataDir, outPath, cfg)
}

func backup(ctx context.Context, dataDir, outPath string, cfg config) (Manifest, error) {
	if outPath == "" {
		return Manifest{}, errors.New("ops: backup path is required")
	}
	outPath, err := filepath.Abs(outPath)
	if err != nil {
		return Manifest{}, err
	}
	if present, err := exists(outPath); err != nil {
		return Manifest{}, err
	} else if present {
		return Manifest{}, fmt.Errorf("%w: %s", ErrBackupExists, outPath)
	}
	parent := filepath.Dir(outPath)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return Manifest{}, fmt.Errorf("backup directory %s cannot be created: %w", parent, err)
	}
	work, err := makePrivateDir(parent, ".backup-work-")
	if err != nil {
		return Manifest{}, err
	}
	defer os.RemoveAll(work)

	sources, err := gatherSources(ctx, dataDir, work, cfg)
	if err != nil {
		return Manifest{}, err
	}
	if err := ctx.Err(); err != nil {
		return Manifest{}, err
	}
	schema, err := storedSchemaVersion(ctx, sources[DatabaseName])
	if err != nil {
		return Manifest{}, fmt.Errorf("snapshot schema version cannot be read: %w", err)
	}
	if schema < 1 {
		return Manifest{}, errors.New("database has no schema, so there is nothing to back up")
	}
	manifest := Manifest{Format: FormatVersion, ServerVersion: cfg.version, SchemaVersion: schema, CreatedAt: cfg.now().UTC().Truncate(time.Second)}
	for _, name := range memberNames(sources[ConfigName] != "") {
		digest, size, err := hashFile(sources[name])
		if err != nil {
			return Manifest{}, err
		}
		manifest.Members = append(manifest.Members, ManifestMember{Name: name, Size: size, SHA256: digest})
	}
	if err := writeArchiveFile(manifest, sources, parent, outPath, cfg); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func gatherSources(ctx context.Context, dataDir, work string, cfg config) (map[string]string, error) {
	sources := map[string]string{}
	snapshotter := cfg.snapshotter
	if snapshotter == nil {
		snapshotter = vacuumSnapshotter{source: filepath.Join(dataDir, DatabaseName)}
	}
	snapshot := filepath.Join(work, DatabaseName)
	if err := snapshotter.Backup(ctx, snapshot); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(snapshot); err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("database snapshot was not written")
	}
	if err := os.Chmod(snapshot, 0o600); err != nil {
		return nil, err
	}
	sources[DatabaseName] = snapshot

	if _, err := secrets.Load(dataDir); err != nil {
		return nil, err
	}
	secretWork := filepath.Join(work, secrets.DirName)
	if err := os.Mkdir(secretWork, 0o700); err != nil {
		return nil, err
	}
	for _, file := range secrets.Files() {
		copied := filepath.Join(secretWork, file)
		if err := copyFileLimited(filepath.Join(secrets.Dir(dataDir), file), copied, secrets.KeySize); err != nil {
			return nil, fmt.Errorf("secret %s cannot be copied: %w", file, err)
		}
		sources[secretMemberName(file)] = copied
	}

	configSource := filepath.Join(dataDir, ConfigName)
	switch _, err := os.Lstat(configSource); {
	case err == nil:
		copied := filepath.Join(work, ConfigName)
		if err := copyFileLimited(configSource, copied, cfg.limits.MaxConfigBytes); err != nil {
			return nil, fmt.Errorf("config file cannot be copied: %w", err)
		}
		sources[ConfigName] = copied
	case errors.Is(err, fs.ErrNotExist):
	default:
		return nil, err
	}
	return sources, nil
}

func writeArchiveFile(manifest Manifest, sources map[string]string, parent, outPath string, cfg config) error {
	suffix, err := randomSuffix()
	if err != nil {
		return err
	}
	temp := filepath.Join(parent, ".backup-"+suffix+".tmp")
	file, err := os.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	cleanup := func() { _ = os.Remove(temp) }
	if err := writeArchive(file, manifest, sources); err != nil {
		file.Close()
		cleanup()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		cleanup()
		return err
	}
	if err := file.Close(); err != nil {
		cleanup()
		return err
	}
	if _, err := extractArchive(temp, "", cfg.limits); err != nil {
		cleanup()
		return fmt.Errorf("backup failed verification after writing: %w", err)
	}
	if err := cfg.step("backup-before-publish"); err != nil {
		cleanup()
		return err
	}
	if err := publishNoReplace(temp, outPath); err != nil {
		cleanup()
		return err
	}
	return syncDir(parent)
}
