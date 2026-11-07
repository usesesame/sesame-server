package ops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"usesesame.app/backend/internal/selfhost/secrets"
	"usesesame.app/backend/internal/selfhost/sqlitestore"
)

func prepareRestoreDatabase(ctx context.Context, staging string, now func() time.Time) (int, error) {
	loaded, err := secrets.Load(staging)
	if err != nil {
		return 0, fmt.Errorf("the restored instance secrets cannot be read: %w", err)
	}
	installed, err := sqlitestore.PrepareRestore(ctx, sqlitestore.Options{
		Path:     filepath.Join(staging, DatabaseName),
		AdminKey: loaded.AdminEncryptionKey,
		Preserve: true,
		Now:      now,
	})
	if err != nil {
		return 0, fmt.Errorf("the restored database cannot be prepared for recovery approval: %w", err)
	}
	if err := checkpointStagedDatabase(ctx, filepath.Join(staging, DatabaseName)); err != nil {
		return 0, err
	}
	return installed, nil
}

func checkpointStagedDatabase(ctx context.Context, path string) error {
	db, err := openRaw(path, false, 0)
	if err != nil {
		return fmt.Errorf("the restored database cannot be checkpointed: %w", err)
	}
	var busy, logFrames, checkpointed int
	if err := db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &checkpointed); err != nil {
		_ = db.Close()
		return fmt.Errorf("the restored database checkpoint failed: %w", err)
	}
	if busy != 0 {
		_ = db.Close()
		return errors.New("the restored database checkpoint is busy")
	}
	if checkpointed != logFrames {
		_ = db.Close()
		return fmt.Errorf("the restored database checkpoint copied %d of %d frames", checkpointed, logFrames)
	}
	if err := db.Close(); err != nil {
		return fmt.Errorf("the restored database checkpoint could not close: %w", err)
	}
	return nil
}

func removeStagedDatabaseSidecars(staging string) error {
	for _, name := range []string{DatabaseName + "-wal", DatabaseName + "-shm"} {
		if err := os.Remove(filepath.Join(staging, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("the restored database sidecar %s cannot be removed: %w", name, err)
		}
	}
	return nil
}

func VerifyLiveSecrets(ctx context.Context, dataDir string) error {
	loaded, err := secrets.Load(dataDir)
	if err != nil {
		return fmt.Errorf("the instance secrets in %s cannot be read: %w", secrets.Dir(dataDir), err)
	}
	if _, err := verifyOwnerSecrets(ctx, filepath.Join(dataDir, DatabaseName), loaded.AdminEncryptionKey); err != nil {
		return err
	}
	return nil
}
