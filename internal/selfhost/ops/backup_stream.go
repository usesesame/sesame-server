package ops

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func BackupTo(ctx context.Context, dataDir string, output io.Writer, options ...Option) (Manifest, error) {
	cfg, err := newConfig(options)
	if err != nil {
		return Manifest{}, err
	}
	return backupTo(ctx, dataDir, output, cfg)
}

func backupTo(ctx context.Context, dataDir string, output io.Writer, cfg config) (Manifest, error) {
	if output == nil {
		return Manifest{}, errors.New("ops: backup output is required")
	}
	dataDir, err := filepath.Abs(dataDir)
	if err != nil {
		return Manifest{}, err
	}
	parent := dataDir
	if cfg.workDir != "" {
		parent, err = filepath.Abs(cfg.workDir)
		if err != nil {
			return Manifest{}, err
		}
	}
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return Manifest{}, fmt.Errorf("backup work directory %s cannot be created: %w", parent, err)
	}
	work, err := makePrivateDir(parent, ".backup-stream-")
	if err != nil {
		return Manifest{}, err
	}
	defer os.RemoveAll(work)

	archive := filepath.Join(work, "archive.tar")
	manifest, err := backup(ctx, dataDir, archive, cfg)
	if err != nil {
		return Manifest{}, err
	}
	if err := ctx.Err(); err != nil {
		return Manifest{}, err
	}
	file, info, err := openRegular(archive)
	if err != nil {
		return Manifest{}, err
	}
	defer file.Close()
	if info.Size() > cfg.limits.maxArchiveBytes() {
		return Manifest{}, invalid("backup archive is %d bytes, which is over the limit of %d", info.Size(), cfg.limits.maxArchiveBytes())
	}
	for _, member := range manifest.Members {
		if err := validMemberSize(member, cfg.limits); err != nil {
			return Manifest{}, err
		}
	}
	written, err := io.Copy(output, contextReader{ctx: ctx, reader: file})
	if err != nil {
		return Manifest{}, fmt.Errorf("the backup archive cannot be written to the output: %w", err)
	}
	if written != info.Size() {
		return Manifest{}, errors.New("the backup archive changed while it was being written to the output")
	}
	if err := ctx.Err(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}
