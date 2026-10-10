package ops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
	"usesesame.app/backend/internal/selfhost/sqlitestore"
)

const snapshotBusyTimeout = 10 * time.Second

func openRaw(path string, readOnly bool, busy time.Duration) (*sql.DB, error) {
	query := url.Values{}
	query.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busy.Milliseconds()))
	if readOnly {
		query.Add("mode", "ro")
	}
	location := url.URL{Path: path}
	db, err := sql.Open("sqlite", "file:"+location.EscapedPath()+"?"+query.Encode())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

type vacuumSnapshotter struct{ source string }

func (v vacuumSnapshotter) Backup(ctx context.Context, destination string) error {
	if _, err := os.Stat(v.source); err != nil {
		return fmt.Errorf("database %s cannot be read: %w", v.source, err)
	}
	db, err := openRaw(v.source, true, snapshotBusyTimeout)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, destination); err != nil {
		return fmt.Errorf("database snapshot failed: %w", err)
	}
	return nil
}

func storedSchemaVersion(ctx context.Context, path string) (int, error) {
	db, err := openRaw(path, true, time.Second)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	var tables int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'`).Scan(&tables); err != nil {
		return 0, err
	}
	if tables == 0 {
		return 0, nil
	}
	var version sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		return 0, err
	}
	return int(version.Int64), nil
}

var (
	supportedOnce  sync.Mutex
	supportedValue int
)

func supportedSchema(ctx context.Context) (int, error) {
	supportedOnce.Lock()
	defer supportedOnce.Unlock()
	if supportedValue > 0 {
		return supportedValue, nil
	}
	dir, err := os.MkdirTemp("", "sesame-schema-probe-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(dir)
	store, err := sqlitestore.Open(ctx, sqlitestore.Options{Path: filepath.Join(dir, DatabaseName), AdminKey: make([]byte, 32)})
	if err != nil {
		return 0, fmt.Errorf("supported schema version cannot be determined: %w", err)
	}
	info, err := store.System(ctx)
	closeErr := store.Close()
	if err != nil {
		return 0, err
	}
	if closeErr != nil {
		return 0, closeErr
	}
	supportedValue = info.SchemaVersion
	return supportedValue, nil
}

func writeLockBusy(ctx context.Context, path string) (bool, error) {
	present, err := exists(path)
	if err != nil {
		return false, err
	}
	if !present {
		return false, nil
	}
	db, err := openRaw(path, false, 0)
	if err != nil {
		return false, nil
	}
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		return sqliteBusy(err), nil
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return sqliteBusy(err), nil
	}
	_, _ = conn.ExecContext(ctx, `ROLLBACK`)
	return false, nil
}

func sqliteBusy(err error) bool {
	var failure *sqlite.Error
	if !errors.As(err, &failure) {
		return false
	}
	switch failure.Code() & 0xff {
	case sqlite3.SQLITE_BUSY, sqlite3.SQLITE_LOCKED:
		return true
	}
	return false
}
