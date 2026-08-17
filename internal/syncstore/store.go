// Package syncstore is the persistence behind Sesame Sync's control plane.
// Sync is not enabled. The rule this package exists to
// keep: the service stores opaque bytes, never decoded, parsed, or logged.
package syncstore

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgconn"
	"math/big"
	"time"

	"usesesame.app/backend/internal/syncproto"
)

var (
	// Must be surfaced to the user, never retried by overwriting.
	ErrConflict = errors.New("sync revision conflict")
	ErrNotFound = errors.New("sync record not found")
	// Deliberately indistinguishable: no challenge enumeration.
	ErrChallengeUnusable = errors.New("sync enrollment challenge is unusable")
	ErrApprovalRejected  = errors.New("sync device approval rejected")
	ErrQuotaExceeded     = errors.New("sync quota exceeded")
	// Binding prevents a revoked device from reading and any account token from revoking.
	ErrDesktopBindingRequired = errors.New("sync device binding is required")
	ErrNotApproved            = errors.New("sync device is not approved")
)

const (
	DevicePending  = "pending"
	DeviceApproved = "approved"
	DeviceRevoked  = "revoked"
)

const ChallengeTTL = 5 * time.Minute

type Store struct {
	db *sql.DB
	// Zero value means no receipt is issued.
	receiptKey ed25519.PrivateKey
	// Zero means syncproto.MaxStoredBytesPerVault; settable so tests can reach the boundary.
	storedByteBudget int64
}

func New(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) WithStoredByteBudget(bytes int64) *Store {
	copy := *s
	copy.storedByteBudget = bytes
	return &copy
}

func (s *Store) byteBudget() int64 {
	if s.storedByteBudget > 0 {
		return s.storedByteBudget
	}
	return syncproto.MaxStoredBytesPerVault
}

func (s *Store) WithReceiptKey(key ed25519.PrivateKey) *Store {
	copy := *s
	copy.receiptKey = key
	return &copy
}

func nowUTC() time.Time { return time.Now().UTC() }

// The id is client-generated.
type Vault struct {
	ID              string
	AccountID       string
	VaultEpoch      uint64
	CurrentRevision uint64
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type Device struct {
	ID                  string
	VaultID             string
	SigningPublicKey    []byte
	EncryptionPublicKey []byte
	State               string
	DeviceEpoch         uint64
	// A device is fully live only when this equals DeviceEpoch.
	ActivatedEpoch uint64
	Label          string
	CreatedAt      time.Time
	ApprovedAt     *time.Time
	RevokedAt      *time.Time
}

// Serializable isolation: approval and revision CAS both read a row then act,
// so read-committed could see the same pre-state twice. PostgreSQL requires
// retrying the whole transaction on 40001, so it is retried here. A real CAS
// mismatch is ErrConflict and is returned immediately, never retried.
func (s *Store) inTx(ctx context.Context, body func(*sql.Tx) error) error {
	const attempts = 5
	var err error
	for attempt := 0; attempt < attempts; attempt++ {
		err = s.runTx(ctx, body)
		if !isSerializationFailure(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff(attempt)):
		}
	}
	return err
}

func (s *Store) runTx(ctx context.Context, body func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return fmt.Errorf("begin sync transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after commit returns ErrTxDone
	if err := body(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit sync transaction: %w", err)
	}
	return nil
}

func isSerializationFailure(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "40001"
}

func backoff(attempt int) time.Duration {
	base := time.Duration(1<<attempt) * 2 * time.Millisecond
	jitter, err := rand.Int(rand.Reader, big.NewInt(int64(base)))
	if err != nil {
		return base
	}
	return base + time.Duration(jitter.Int64())
}

func (s *Store) EnsureVault(ctx context.Context, accountID, vaultID string) (Vault, error) {
	var vault Vault
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		// xmax is zero on a fresh insert, non-zero on the update path: it tells
		// the caller whether it created the vault without a second query.
		var created bool
		row := tx.QueryRowContext(ctx, `
			INSERT INTO sesame_sync_vaults (id, account_id)
			VALUES ($1, $2)
			ON CONFLICT (account_id) DO UPDATE SET updated_at = NOW()
			RETURNING id, account_id, vault_epoch, current_revision, created_at, updated_at, (xmax = 0)
		`, vaultID, accountID)
		if err := row.Scan(&vault.ID, &vault.AccountID, &vault.VaultEpoch, &vault.CurrentRevision, &vault.CreatedAt, &vault.UpdatedAt, &created); err != nil {
			return fmt.Errorf("ensure sync vault: %w", err)
		}
		if !created {
			return nil
		}
		return recordAudit(ctx, tx, vault.ID, "", "vault_created")
	})
	if err != nil {
		return Vault{}, err
	}
	return vault, nil
}

func (s *Store) VaultForAccount(ctx context.Context, accountID string) (Vault, error) {
	var vault Vault
	row := s.db.QueryRowContext(ctx, `
		SELECT id, account_id, vault_epoch, current_revision, created_at, updated_at
		FROM sesame_sync_vaults WHERE account_id = $1
	`, accountID)
	switch err := row.Scan(&vault.ID, &vault.AccountID, &vault.VaultEpoch, &vault.CurrentRevision, &vault.CreatedAt, &vault.UpdatedAt); {
	case errors.Is(err, sql.ErrNoRows):
		return Vault{}, ErrNotFound
	case err != nil:
		return Vault{}, fmt.Errorf("read sync vault: %w", err)
	}
	return vault, nil
}

// No size, ciphertext, or label: an audit row must not reconstruct vault contents.
func recordAudit(ctx context.Context, tx *sql.Tx, vaultID, deviceID, action string) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO sesame_sync_audit (vault_id, device_id, action) VALUES ($1, $2, $3)
	`, vaultID, deviceID, action); err != nil {
		return fmt.Errorf("record sync audit: %w", err)
	}
	return nil
}

func (s *Store) PurgeExpiredChallenges(ctx context.Context) (int64, error) {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM sesame_sync_challenges
		WHERE expires_at < NOW() OR consumed_at IS NOT NULL
	`)
	if err != nil {
		return 0, fmt.Errorf("purge sync challenges: %w", err)
	}
	removed, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("purge sync challenges: %w", err)
	}
	return removed, nil
}

func (s *Store) PurgeRevokedDevices(ctx context.Context, olderThan time.Duration) (int64, error) {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM sesame_sync_devices
		WHERE state = 'revoked' AND revoked_at IS NOT NULL AND revoked_at < $1
	`, time.Now().Add(-olderThan))
	if err != nil {
		return 0, fmt.Errorf("purge revoked sync devices: %w", err)
	}
	removed, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("purge revoked sync devices: %w", err)
	}
	return removed, nil
}

const RevokedDeviceRetention = 90 * 24 * time.Hour
