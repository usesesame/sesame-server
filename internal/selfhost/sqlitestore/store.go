package sqlitestore

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
	"usesesame.app/backend/internal/selfhost"
)

const (
	adminKeyLength = 32
	busyTimeout    = 5 * time.Second
)

type Options struct {
	Path               string
	AdminKey           []byte
	Now                func() time.Time
	InstanceName       string
	SessionTTL         time.Duration
	Flags              []selfhost.FlagDefinition
	ReadOnly           bool
	MigrationBackupDir string
}

type Store struct {
	db         *sql.DB
	path       string
	adminKey   []byte
	now        func() time.Time
	sessionTTL time.Duration
	flags      []selfhost.FlagDefinition
	closed     atomic.Bool
	auditMu    sync.Mutex
	auditState *auditState
}

var _ selfhost.Store = (*Store)(nil)

func Open(ctx context.Context, options Options) (*Store, error) {
	return openWith(ctx, options, defaultMigrations)
}

func openWith(ctx context.Context, options Options, migrations []migration) (*Store, error) {
	if options.Path == "" {
		return nil, errors.New("sqlitestore: database path is required")
	}
	if len(options.AdminKey) != adminKeyLength {
		return nil, fmt.Errorf("sqlitestore: admin key must be %d bytes", adminKeyLength)
	}
	for _, definition := range options.Flags {
		if !validFlagKey(definition.Key) {
			return nil, fmt.Errorf("sqlitestore: invalid flag key %q", definition.Key)
		}
	}
	if options.ReadOnly {
		if _, err := os.Stat(options.Path); err != nil {
			return nil, fmt.Errorf("sqlitestore: open database: %w", err)
		}
	} else if err := ensureFile(options.Path); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn(options.Path, options.ReadOnly))
	if err != nil {
		return nil, fmt.Errorf("sqlitestore: open database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	store := &Store{
		db:         db,
		path:       options.Path,
		adminKey:   append([]byte(nil), options.AdminKey...),
		now:        options.Now,
		sessionTTL: options.SessionTTL,
		flags:      append([]selfhost.FlagDefinition(nil), options.Flags...),
	}
	if store.now == nil {
		store.now = time.Now
	}
	if store.sessionTTL <= 0 {
		store.sessionTTL = selfhost.DefaultSessionTTL
	}
	if err := connect(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlitestore: open database: %w", err)
	}
	if err := store.prepare(ctx, options, migrations); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) prepare(ctx context.Context, options Options, migrations []migration) error {
	if options.ReadOnly {
		return s.requireCurrentSchema(ctx, migrations)
	}
	if err := s.migrate(ctx, migrations, options.MigrationBackupDir); err != nil {
		return err
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		if err := s.ensureInstance(ctx, tx, options.InstanceName); err != nil {
			return err
		}
		return s.seedFlags(ctx, tx)
	})
}

func connect(ctx context.Context, db *sql.DB) error {
	deadline := time.Now().Add(busyTimeout)
	for {
		err := db.PingContext(ctx)
		if err == nil || !isBusy(err) || time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func isBusy(err error) bool {
	var failure *sqlite.Error
	return errors.As(err, &failure) && failure.Code()&0xff == sqlite3.SQLITE_BUSY
}

func ensureFile(path string) error {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("sqlitestore: create database file: %w", err)
	}
	return file.Close()
}

func dsn(path string, readOnly bool) string {
	query := url.Values{}
	query.Add("_txlock", "immediate")
	query.Add("_busy_timeout", fmt.Sprint(busyTimeout.Milliseconds()))
	query.Add("_foreign_keys", "1")
	query.Add("_defensive", "1")
	query.Add("_pragma", "trusted_schema(0)")
	if readOnly {
		query.Add("mode", "ro")
		query.Add("_query_only", "1")
	} else {
		query.Add("_journal_mode", "WAL")
		query.Add("_synchronous", "FULL")
	}
	location := url.URL{Path: path}
	return "file:" + location.EscapedPath() + "?" + query.Encode()
}

func (s *Store) Close() error {
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	return s.db.Close()
}

func (s *Store) Ping(ctx context.Context) error {
	if s.closed.Load() {
		return selfhost.ErrClosed
	}
	return s.db.PingContext(ctx)
}

type queryer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (s *Store) write(ctx context.Context, fn func(tx *sql.Tx) error) error {
	if s.closed.Load() {
		return selfhost.ErrClosed
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *Store) read() (queryer, error) {
	if s.closed.Load() {
		return nil, selfhost.ErrClosed
	}
	return s.db, nil
}

func (s *Store) clock() time.Time {
	return s.now().UTC().Truncate(time.Second)
}

func newID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func unixTime(value int64) time.Time {
	return time.Unix(value, 0).UTC()
}

func optionalTime(value sql.NullInt64) *time.Time {
	if !value.Valid {
		return nil
	}
	moment := unixTime(value.Int64)
	return &moment
}

func isUnique(err error) bool {
	var failure *sqlite.Error
	if !errors.As(err, &failure) {
		return false
	}
	code := failure.Code()
	return code == sqlite3.SQLITE_CONSTRAINT_UNIQUE || code == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY
}

func validFlagKey(key string) bool {
	if key == "" || len(key) > 64 {
		return false
	}
	for _, character := range key {
		valid := character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '.' || character == '_' || character == '-'
		if !valid {
			return false
		}
	}
	return true
}
