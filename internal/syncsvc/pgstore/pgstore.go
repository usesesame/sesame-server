package pgstore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"usesesame.app/backend/internal/syncproto"
	"usesesame.app/backend/internal/syncsvc"
)

const maxUpdateAttempts = 5

var (
	_ syncsvc.Storage = (*Store)(nil)
	_ syncsvc.Tx      = (*transaction)(nil)
)

type Store struct {
	db *sql.DB
}

func New(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) Update(ctx context.Context, fn func(syncsvc.Tx) error) error {
	for attempt := 1; ; attempt++ {
		err := s.runTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable}, fn)
		if !isRetryable(err) {
			return err
		}
		if attempt == maxUpdateAttempts {
			return fmt.Errorf("sync update still conflicted after %d attempts: %w", attempt, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt) * time.Millisecond):
		}
	}
}

func (s *Store) View(ctx context.Context, fn func(syncsvc.Tx) error) error {
	return s.runTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead}, fn)
}

func (s *Store) runTx(ctx context.Context, options *sql.TxOptions, fn func(syncsvc.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, options)
	if err != nil {
		return fmt.Errorf("begin sync transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(&transaction{ctx: ctx, tx: tx}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit sync transaction: %w", err)
	}
	return nil
}

func isRetryable(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "40001" || pgErr.Code == "40P01")
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func mapError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, sql.ErrNoRows):
		return syncsvc.ErrNotFound
	case isUniqueViolation(err):
		return syncsvc.ErrConflict
	default:
		return fmt.Errorf("sync store: %w", err)
	}
}

type uint64Column uint64

func (c uint64Column) Value() (driver.Value, error) {
	if uint64(c) > math.MaxInt64 {
		return nil, fmt.Errorf("sync value %d is above the BIGINT range", uint64(c))
	}
	return int64(c), nil
}

func (c *uint64Column) Scan(src any) error {
	value, ok := src.(int64)
	if !ok || value < 0 {
		return fmt.Errorf("sync stored value %v is not a non-negative BIGINT", src)
	}
	*c = uint64Column(value)
	return nil
}

func asColumn(value *uint64) *uint64Column {
	return (*uint64Column)(value)
}

type rowScanner interface {
	Scan(dest ...any) error
}

type transaction struct {
	ctx context.Context
	tx  *sql.Tx
}

func one[T any](t *transaction, scan func(rowScanner) (T, error), query string, args ...any) (T, error) {
	var zero T
	item, err := scan(t.tx.QueryRowContext(t.ctx, query, args...))
	if err != nil {
		return zero, mapError(err)
	}
	return item, nil
}

func many[T any](t *transaction, scan func(rowScanner) (T, error), query string, args ...any) ([]T, error) {
	rows, err := t.tx.QueryContext(t.ctx, query, args...)
	if err != nil {
		return nil, mapError(err)
	}
	defer func() { _ = rows.Close() }()
	var out []T
	for rows.Next() {
		item, err := scan(rows)
		if err != nil {
			return nil, mapError(err)
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError(err)
	}
	return out, nil
}

func (t *transaction) exec(query string, args ...any) error {
	_, err := t.tx.ExecContext(t.ctx, query, args...)
	return mapError(err)
}

func (t *transaction) execCount(query string, args ...any) (int64, error) {
	result, err := t.tx.ExecContext(t.ctx, query, args...)
	if err != nil {
		return 0, mapError(err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("sync store rows affected: %w", err)
	}
	return count, nil
}

func (t *transaction) execExpectingOne(query string, notFound error, args ...any) error {
	count, err := t.execCount(query, args...)
	if err != nil {
		return err
	}
	if count == 0 {
		return notFound
	}
	return nil
}

const vaultColumns = `id, owner_key, current_revision, head_index, epoch, created_at, updated_at`

func scanVault(row rowScanner) (syncsvc.Vault, error) {
	var vault syncsvc.Vault
	err := row.Scan(&vault.ID, &vault.OwnerKey, asColumn(&vault.CurrentRevision), asColumn(&vault.HeadIndex),
		asColumn(&vault.Epoch), &vault.CreatedAt, &vault.UpdatedAt)
	return vault, err
}

func (t *transaction) VaultByOwner(ownerKey string) (syncsvc.Vault, error) {
	return one(t, scanVault, `SELECT `+vaultColumns+` FROM sesame_sync3_vaults WHERE owner_key = $1`, ownerKey)
}

func (t *transaction) VaultByID(vaultID string) (syncsvc.Vault, error) {
	return one(t, scanVault, `SELECT `+vaultColumns+` FROM sesame_sync3_vaults WHERE id = $1`, vaultID)
}

func (t *transaction) InsertVault(vault syncsvc.Vault) error {
	return t.exec(`
		INSERT INTO sesame_sync3_vaults (id, owner_key, current_revision, head_index, epoch, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, vault.ID, vault.OwnerKey, uint64Column(vault.CurrentRevision), uint64Column(vault.HeadIndex),
		uint64Column(vault.Epoch), vault.CreatedAt, vault.UpdatedAt)
}

func (t *transaction) UpdateVault(vault syncsvc.Vault) error {
	return t.execExpectingOne(`
		UPDATE sesame_sync3_vaults
		SET owner_key = $2, current_revision = $3, head_index = $4, epoch = $5, created_at = $6, updated_at = $7
		WHERE id = $1
	`, syncsvc.ErrNotFound, vault.ID, vault.OwnerKey, uint64Column(vault.CurrentRevision), uint64Column(vault.HeadIndex),
		uint64Column(vault.Epoch), vault.CreatedAt, vault.UpdatedAt)
}

func (t *transaction) DeleteVault(vaultID string) error {
	return t.exec(`DELETE FROM sesame_sync3_vaults WHERE id = $1`, vaultID)
}

const deviceColumns = `vault_id, device_id, token_device_id, signing_key, package_key, state, added_index, activated_epoch, created_at`

func scanDevice(row rowScanner) (syncsvc.Device, error) {
	var device syncsvc.Device
	err := row.Scan(&device.VaultID, &device.DeviceID, &device.TokenDeviceID, &device.SigningKey, &device.PackageKey,
		&device.State, asColumn(&device.AddedIndex), asColumn(&device.ActivatedEpoch), &device.CreatedAt)
	return device, err
}

func (t *transaction) Devices(vaultID string) ([]syncsvc.Device, error) {
	return many(t, scanDevice, `SELECT `+deviceColumns+` FROM sesame_sync3_devices
		WHERE vault_id = $1 ORDER BY added_index, device_id`, vaultID)
}

func (t *transaction) Device(vaultID, deviceID string) (syncsvc.Device, error) {
	return one(t, scanDevice, `SELECT `+deviceColumns+` FROM sesame_sync3_devices
		WHERE vault_id = $1 AND device_id = $2`, vaultID, deviceID)
}

func (t *transaction) DeviceByToken(vaultID, tokenDeviceID string) (syncsvc.Device, error) {
	return one(t, scanDevice, `SELECT `+deviceColumns+` FROM sesame_sync3_devices
		WHERE vault_id = $1 AND token_device_id = $2 ORDER BY (state = 'revoked'), added_index DESC LIMIT 1`, vaultID, tokenDeviceID)
}

func (t *transaction) PutDevice(device syncsvc.Device) error {
	return t.exec(`
		INSERT INTO sesame_sync3_devices (vault_id, device_id, token_device_id, signing_key, package_key, state, added_index, activated_epoch, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (vault_id, device_id) DO UPDATE SET
			token_device_id = EXCLUDED.token_device_id,
			signing_key = EXCLUDED.signing_key,
			package_key = EXCLUDED.package_key,
			state = EXCLUDED.state,
			added_index = EXCLUDED.added_index,
			activated_epoch = EXCLUDED.activated_epoch,
			created_at = EXCLUDED.created_at
	`, device.VaultID, device.DeviceID, device.TokenDeviceID, device.SigningKey, device.PackageKey, device.State,
		uint64Column(device.AddedIndex), uint64Column(device.ActivatedEpoch), device.CreatedAt)
}

const entryColumns = `vault_id, idx, previous_digest, kind, signer_device_id, subject_device_id, subject_signing_key, subject_package_key, sealed_label, epoch_after, snapshot_floor, signature`

func scanEntry(row rowScanner) (syncproto.DeviceListEntry, error) {
	var entry syncproto.DeviceListEntry
	err := row.Scan(&entry.VaultID, asColumn(&entry.Index), &entry.PreviousDigest, &entry.Kind, &entry.SignerDeviceID,
		&entry.SubjectDeviceID, &entry.SubjectSigningKey, &entry.SubjectPackageKey, &entry.SealedLabel,
		asColumn(&entry.EpochAfter), asColumn(&entry.SnapshotFloor), &entry.Signature)
	return entry, err
}

func (t *transaction) DeviceListEntries(vaultID string, after uint64, limit int) ([]syncproto.DeviceListEntry, error) {
	if limit <= 0 {
		return nil, nil
	}
	return many(t, scanEntry, `SELECT `+entryColumns+` FROM sesame_sync3_device_list
		WHERE vault_id = $1 AND idx > $2 ORDER BY idx LIMIT $3`, vaultID, uint64Column(after), limit)
}

func (t *transaction) AppendDeviceListEntry(entry syncproto.DeviceListEntry) error {
	var last uint64Column
	if err := t.tx.QueryRowContext(t.ctx, `
		SELECT COALESCE(MAX(idx), 0) FROM sesame_sync3_device_list WHERE vault_id = $1
	`, entry.VaultID).Scan(&last); err != nil {
		return mapError(err)
	}
	if uint64(last)+1 != entry.Index {
		return syncsvc.ErrConflict
	}
	return t.exec(`
		INSERT INTO sesame_sync3_device_list (`+entryColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
	`, entry.VaultID, uint64Column(entry.Index), entry.PreviousDigest, entry.Kind, entry.SignerDeviceID,
		entry.SubjectDeviceID, entry.SubjectSigningKey, entry.SubjectPackageKey, entry.SealedLabel,
		uint64Column(entry.EpochAfter), uint64Column(entry.SnapshotFloor), entry.Signature)
}

const keyPackageColumns = `vault_id, sender_device_id, recipient_device_id, vault_epoch, entry_digest, ciphertext, signature, created_at`

func scanKeyPackage(row rowScanner) (syncsvc.KeyPackage, error) {
	var pkg syncsvc.KeyPackage
	err := row.Scan(&pkg.VaultID, &pkg.SenderDeviceID, &pkg.RecipientDeviceID, asColumn(&pkg.VaultEpoch),
		&pkg.EntryDigest, &pkg.Ciphertext, &pkg.Signature, &pkg.CreatedAt)
	return pkg, err
}

func (t *transaction) PutKeyPackage(pkg syncsvc.KeyPackage) error {
	return t.exec(`
		INSERT INTO sesame_sync3_key_packages (`+keyPackageColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (vault_id, recipient_device_id) DO UPDATE SET
			sender_device_id = EXCLUDED.sender_device_id,
			vault_epoch = EXCLUDED.vault_epoch,
			entry_digest = EXCLUDED.entry_digest,
			ciphertext = EXCLUDED.ciphertext,
			signature = EXCLUDED.signature,
			created_at = EXCLUDED.created_at
	`, pkg.VaultID, pkg.SenderDeviceID, pkg.RecipientDeviceID, uint64Column(pkg.VaultEpoch),
		pkg.EntryDigest, pkg.Ciphertext, pkg.Signature, pkg.CreatedAt)
}

func (t *transaction) KeyPackage(vaultID, recipientDeviceID string) (syncsvc.KeyPackage, error) {
	return one(t, scanKeyPackage, `SELECT `+keyPackageColumns+` FROM sesame_sync3_key_packages
		WHERE vault_id = $1 AND recipient_device_id = $2`, vaultID, recipientDeviceID)
}

func (t *transaction) DeleteKeyPackages(vaultID string) error {
	return t.exec(`DELETE FROM sesame_sync3_key_packages WHERE vault_id = $1`, vaultID)
}

const headerColumns = `vault_id, revision, version, device_id, vault_epoch, device_list_index, device_list_digest,
	previous_digest, ciphertext_digest, ciphertext_length, nonce, signature, digest, receipt, accepted_at`

func scanStoredHeader(row rowScanner) (syncsvc.StoredHeader, error) {
	var stored syncsvc.StoredHeader
	err := row.Scan(&stored.VaultID, asColumn(&stored.Revision), &stored.Version, &stored.DeviceID,
		asColumn(&stored.VaultEpoch), asColumn(&stored.DeviceListIndex), &stored.DeviceListDigest,
		&stored.PreviousDigest, &stored.CiphertextDigest, asColumn(&stored.CiphertextLength), &stored.Nonce,
		&stored.Signature, &stored.Digest, &stored.Receipt, &stored.AcceptedAt)
	return stored, err
}

func (t *transaction) InsertHeader(header syncsvc.StoredHeader) error {
	return t.exec(`
		INSERT INTO sesame_sync3_headers (`+headerColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
	`, header.VaultID, uint64Column(header.Revision), header.Version, header.DeviceID,
		uint64Column(header.VaultEpoch), uint64Column(header.DeviceListIndex), header.DeviceListDigest,
		header.PreviousDigest, header.CiphertextDigest, uint64Column(header.CiphertextLength), header.Nonce,
		header.Signature, header.Digest, header.Receipt, header.AcceptedAt)
}

func (t *transaction) Headers(vaultID string, after uint64, limit int) ([]syncsvc.StoredHeader, error) {
	if limit <= 0 {
		return nil, nil
	}
	return many(t, scanStoredHeader, `SELECT `+headerColumns+` FROM sesame_sync3_headers
		WHERE vault_id = $1 AND revision > $2 ORDER BY revision LIMIT $3`, vaultID, uint64Column(after), limit)
}

func (t *transaction) Header(vaultID string, revision uint64) (syncsvc.StoredHeader, error) {
	return one(t, scanStoredHeader, `SELECT `+headerColumns+` FROM sesame_sync3_headers
		WHERE vault_id = $1 AND revision = $2`, vaultID, uint64Column(revision))
}

func (t *transaction) LatestHeader(vaultID string) (syncsvc.StoredHeader, error) {
	return one(t, scanStoredHeader, `SELECT `+headerColumns+` FROM sesame_sync3_headers
		WHERE vault_id = $1 ORDER BY revision DESC LIMIT 1`, vaultID)
}

func (t *transaction) PruneHeaders(vaultID string, keepFrom uint64) error {
	return t.exec(`DELETE FROM sesame_sync3_headers WHERE vault_id = $1 AND revision < $2`,
		vaultID, uint64Column(keepFrom))
}

func (t *transaction) PutCiphertext(vaultID string, revision uint64, ciphertext []byte) error {
	return t.exec(`
		INSERT INTO sesame_sync3_ciphertexts (vault_id, revision, body)
		VALUES ($1, $2, $3)
		ON CONFLICT (vault_id, revision) DO UPDATE SET body = EXCLUDED.body
	`, vaultID, uint64Column(revision), ciphertext)
}

func (t *transaction) Ciphertext(vaultID string, revision uint64) ([]byte, error) {
	var body []byte
	if err := t.tx.QueryRowContext(t.ctx, `
		SELECT body FROM sesame_sync3_ciphertexts WHERE vault_id = $1 AND revision = $2
	`, vaultID, uint64Column(revision)).Scan(&body); err != nil {
		return nil, mapError(err)
	}
	return body, nil
}

func (t *transaction) CiphertextBytes(vaultID string) (int64, error) {
	var total int64
	if err := t.tx.QueryRowContext(t.ctx, `
		SELECT COALESCE(SUM(octet_length(body)), 0)::BIGINT FROM sesame_sync3_ciphertexts WHERE vault_id = $1
	`, vaultID).Scan(&total); err != nil {
		return 0, mapError(err)
	}
	return total, nil
}

func (t *transaction) OldestCiphertext(vaultID string) (uint64, int64, error) {
	var revision uint64Column
	var size int64
	if err := t.tx.QueryRowContext(t.ctx, `
		SELECT revision, octet_length(body)::BIGINT FROM sesame_sync3_ciphertexts
		WHERE vault_id = $1 ORDER BY revision LIMIT 1
	`, vaultID).Scan(&revision, &size); err != nil {
		return 0, 0, mapError(err)
	}
	return uint64(revision), size, nil
}

func (t *transaction) DeleteCiphertext(vaultID string, revision uint64) error {
	return t.exec(`DELETE FROM sesame_sync3_ciphertexts WHERE vault_id = $1 AND revision = $2`,
		vaultID, uint64Column(revision))
}

func (t *transaction) DeleteCiphertextsBefore(vaultID string, revision uint64) error {
	return t.exec(`DELETE FROM sesame_sync3_ciphertexts WHERE vault_id = $1 AND revision < $2`,
		vaultID, uint64Column(revision))
}

const pairingColumns = `vault_id, device_id, token_device_id, signing_key, package_key, offer, answer, confirm, state, expires_at, created_at`

func scanPairing(row rowScanner) (syncsvc.Pairing, error) {
	var pairing syncsvc.Pairing
	err := row.Scan(&pairing.VaultID, &pairing.DeviceID, &pairing.TokenDeviceID, &pairing.SigningKey,
		&pairing.PackageKey, &pairing.Offer, &pairing.Answer, &pairing.Confirm, &pairing.State,
		&pairing.ExpiresAt, &pairing.CreatedAt)
	return pairing, err
}

const checkpointColumns = `vault_id, device_id, version, device_list_index, device_list_digest, header_revision,
	header_digest, observed_at, signature, accepted_at`

func scanCheckpoint(row rowScanner) (syncsvc.StoredCheckpoint, error) {
	var stored syncsvc.StoredCheckpoint
	err := row.Scan(&stored.VaultID, &stored.DeviceID, &stored.Version, asColumn(&stored.DeviceListIndex),
		&stored.DeviceListDigest, asColumn(&stored.HeaderRevision), &stored.HeaderDigest,
		asColumn(&stored.ObservedAt), &stored.Signature, &stored.AcceptedAt)
	return stored, err
}

func (t *transaction) PutCheckpoint(checkpoint syncsvc.StoredCheckpoint) error {
	return t.exec(`
		INSERT INTO sesame_sync3_checkpoints (`+checkpointColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (vault_id, device_id) DO UPDATE SET
			version = EXCLUDED.version,
			device_list_index = EXCLUDED.device_list_index,
			device_list_digest = EXCLUDED.device_list_digest,
			header_revision = EXCLUDED.header_revision,
			header_digest = EXCLUDED.header_digest,
			observed_at = EXCLUDED.observed_at,
			signature = EXCLUDED.signature,
			accepted_at = EXCLUDED.accepted_at
	`, checkpoint.VaultID, checkpoint.DeviceID, checkpoint.Version, uint64Column(checkpoint.DeviceListIndex),
		checkpoint.DeviceListDigest, uint64Column(checkpoint.HeaderRevision), checkpoint.HeaderDigest,
		uint64Column(checkpoint.ObservedAt), checkpoint.Signature, checkpoint.AcceptedAt)
}

func (t *transaction) Checkpoint(vaultID, deviceID string) (syncsvc.StoredCheckpoint, error) {
	return one(t, scanCheckpoint, `SELECT `+checkpointColumns+` FROM sesame_sync3_checkpoints
		WHERE vault_id = $1 AND device_id = $2`, vaultID, deviceID)
}

func (t *transaction) Checkpoints(vaultID string) ([]syncsvc.StoredCheckpoint, error) {
	return many(t, scanCheckpoint, `SELECT `+checkpointColumns+` FROM sesame_sync3_checkpoints
		WHERE vault_id = $1 ORDER BY device_id`, vaultID)
}

func (t *transaction) DeleteCheckpoint(vaultID, deviceID string) error {
	return t.exec(`DELETE FROM sesame_sync3_checkpoints WHERE vault_id = $1 AND device_id = $2`, vaultID, deviceID)
}

func (t *transaction) PutPairing(pairing syncsvc.Pairing) error {
	return t.exec(`
		INSERT INTO sesame_sync3_pairings (`+pairingColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (vault_id, device_id) DO UPDATE SET
			token_device_id = EXCLUDED.token_device_id,
			signing_key = EXCLUDED.signing_key,
			package_key = EXCLUDED.package_key,
			offer = EXCLUDED.offer,
			answer = EXCLUDED.answer,
			confirm = EXCLUDED.confirm,
			state = EXCLUDED.state,
			expires_at = EXCLUDED.expires_at,
			created_at = EXCLUDED.created_at
	`, pairing.VaultID, pairing.DeviceID, pairing.TokenDeviceID, pairing.SigningKey, pairing.PackageKey,
		pairing.Offer, nullableBytes(pairing.Answer), nullableBytes(pairing.Confirm), pairing.State,
		pairing.ExpiresAt, pairing.CreatedAt)
}

func nullableBytes(value []byte) []byte {
	if len(value) == 0 {
		return nil
	}
	return value
}

func (t *transaction) Pairing(vaultID, deviceID string) (syncsvc.Pairing, error) {
	return one(t, scanPairing, `SELECT `+pairingColumns+` FROM sesame_sync3_pairings
		WHERE vault_id = $1 AND device_id = $2`, vaultID, deviceID)
}

func (t *transaction) PairingByToken(vaultID, tokenDeviceID string) (syncsvc.Pairing, error) {
	return one(t, scanPairing, `SELECT `+pairingColumns+` FROM sesame_sync3_pairings
		WHERE vault_id = $1 AND token_device_id = $2`, vaultID, tokenDeviceID)
}

func (t *transaction) Pairings(vaultID string) ([]syncsvc.Pairing, error) {
	return many(t, scanPairing, `SELECT `+pairingColumns+` FROM sesame_sync3_pairings
		WHERE vault_id = $1 ORDER BY created_at, device_id`, vaultID)
}

func (t *transaction) DeletePairing(vaultID, deviceID string) error {
	return t.exec(`DELETE FROM sesame_sync3_pairings WHERE vault_id = $1 AND device_id = $2`, vaultID, deviceID)
}

func (t *transaction) RecordNonce(scope, nonce string, now, expiresAt time.Time) (bool, error) {
	count, err := t.execCount(`
		INSERT INTO sesame_sync3_nonces (scope, nonce, expires_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (scope, nonce) DO UPDATE SET expires_at = EXCLUDED.expires_at
		WHERE sesame_sync3_nonces.expires_at <= $4
	`, scope, nonce, expiresAt, now)
	if err != nil {
		return false, err
	}
	return count == 1, nil
}

func (t *transaction) PurgeExpired(now time.Time) error {
	if err := t.exec(`DELETE FROM sesame_sync3_nonces WHERE expires_at <= $1`, now); err != nil {
		return err
	}
	return t.exec(`DELETE FROM sesame_sync3_pairings WHERE expires_at <= $1`, now)
}
