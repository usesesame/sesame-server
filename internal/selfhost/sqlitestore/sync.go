package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"time"

	"usesesame.app/backend/internal/syncproto"
	"usesesame.app/backend/internal/syncsvc"
)

const (
	syncVaultColumns      = `id, owner_key, current_revision, head_index, epoch, created_at, updated_at`
	syncDeviceColumns     = `vault_id, device_id, token_device_id, signing_key, package_key, state, added_index, activated_epoch, created_at`
	syncEntryColumns      = `vault_id, idx, previous_digest, kind, signer_device_id, subject_device_id, subject_signing_key, subject_package_key, sealed_label, epoch_after, snapshot_floor, signature`
	syncKeyPackageColumns = `vault_id, recipient_device_id, sender_device_id, vault_epoch, entry_digest, ciphertext, signature, created_at`
	syncHeaderColumns     = `vault_id, revision, version, device_id, vault_epoch, device_list_index, device_list_digest, previous_digest, ciphertext_digest, ciphertext_length, nonce, signature, digest, receipt, accepted_at`
	syncCheckpointColumns = `vault_id, device_id, version, device_list_index, device_list_digest, header_revision, header_digest, observed_at, signature, accepted_at`
	syncPairingColumns    = `vault_id, device_id, token_device_id, signing_key, package_key, offer, answer, confirm, state, expires_at, created_at`
)

var (
	errSyncValueTooLarge = errors.New("sqlitestore: sync value does not fit in a signed 64-bit column")
	errSyncValueNegative = errors.New("sqlitestore: stored sync value is negative")
)

type syncStorage struct {
	store *Store
}

func (s *Store) SyncStorage() syncsvc.Storage {
	return syncStorage{store: s}
}

func (s syncStorage) Update(ctx context.Context, fn func(syncsvc.Tx) error) error {
	return s.store.write(ctx, func(tx *sql.Tx) error {
		return fn(&syncTx{ctx: ctx, db: tx})
	})
}

func (s syncStorage) View(ctx context.Context, fn func(syncsvc.Tx) error) error {
	return s.store.view(ctx, func(tx *sql.Tx) error {
		return fn(&syncTx{ctx: ctx, db: tx})
	})
}

func (s *Store) view(ctx context.Context, fn func(tx *sql.Tx) error) error {
	if _, err := s.read(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	return fn(tx)
}

type syncTx struct {
	ctx context.Context
	db  queryer
}

var _ syncsvc.Tx = (*syncTx)(nil)

type integerColumns struct {
	err error
}

func (c *integerColumns) store(value uint64) int64 {
	if value > math.MaxInt64 && c.err == nil {
		c.err = errSyncValueTooLarge
	}
	return int64(value)
}

func (c *integerColumns) load(value int64) uint64 {
	if value < 0 && c.err == nil {
		c.err = errSyncValueNegative
	}
	return uint64(value)
}

func storedTime(moment time.Time) int64 {
	return moment.Unix()
}

func expiryUnix(moment time.Time) int64 {
	if moment.Nanosecond() > 0 {
		return moment.Unix() + 1
	}
	return moment.Unix()
}

func syncError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return syncsvc.ErrNotFound
	}
	if isUnique(err) {
		return syncsvc.ErrConflict
	}
	return err
}

func queryOne[T any](ctx context.Context, db queryer, scan func(func(...any) error) (T, error), query string, args ...any) (T, error) {
	var zero T
	item, err := scan(db.QueryRowContext(ctx, query, args...).Scan)
	if err != nil {
		return zero, syncError(err)
	}
	return item, nil
}

func collect[T any](ctx context.Context, db queryer, scan func(func(...any) error) (T, error), query string, args ...any) ([]T, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, syncError(err)
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		item, err := scan(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, syncError(rows.Err())
}

func (t *syncTx) exec(query string, args ...any) error {
	_, err := t.db.ExecContext(t.ctx, query, args...)
	return syncError(err)
}

func (t *syncTx) execCount(query string, args ...any) (int64, error) {
	result, err := t.db.ExecContext(t.ctx, query, args...)
	if err != nil {
		return 0, syncError(err)
	}
	return result.RowsAffected()
}

func scanVault(scan func(...any) error) (syncsvc.Vault, error) {
	var vault syncsvc.Vault
	var revision, head, epoch, created, updated int64
	if err := scan(&vault.ID, &vault.OwnerKey, &revision, &head, &epoch, &created, &updated); err != nil {
		return syncsvc.Vault{}, err
	}
	ints := &integerColumns{}
	vault.CurrentRevision = ints.load(revision)
	vault.HeadIndex = ints.load(head)
	vault.Epoch = ints.load(epoch)
	vault.CreatedAt = unixTime(created)
	vault.UpdatedAt = unixTime(updated)
	if ints.err != nil {
		return syncsvc.Vault{}, ints.err
	}
	return vault, nil
}

func scanSyncDevice(scan func(...any) error) (syncsvc.Device, error) {
	var device syncsvc.Device
	var added, activated, created int64
	if err := scan(&device.VaultID, &device.DeviceID, &device.TokenDeviceID, &device.SigningKey, &device.PackageKey,
		&device.State, &added, &activated, &created); err != nil {
		return syncsvc.Device{}, err
	}
	ints := &integerColumns{}
	device.AddedIndex = ints.load(added)
	device.ActivatedEpoch = ints.load(activated)
	device.CreatedAt = unixTime(created)
	if ints.err != nil {
		return syncsvc.Device{}, ints.err
	}
	return device, nil
}

func scanEntry(scan func(...any) error) (syncproto.DeviceListEntry, error) {
	var entry syncproto.DeviceListEntry
	var index, epochAfter, snapshotFloor int64
	if err := scan(&entry.VaultID, &index, &entry.PreviousDigest, &entry.Kind, &entry.SignerDeviceID, &entry.SubjectDeviceID,
		&entry.SubjectSigningKey, &entry.SubjectPackageKey, &entry.SealedLabel, &epochAfter, &snapshotFloor, &entry.Signature); err != nil {
		return syncproto.DeviceListEntry{}, err
	}
	ints := &integerColumns{}
	entry.Index = ints.load(index)
	entry.EpochAfter = ints.load(epochAfter)
	entry.SnapshotFloor = ints.load(snapshotFloor)
	if ints.err != nil {
		return syncproto.DeviceListEntry{}, ints.err
	}
	return entry, nil
}

func scanKeyPackage(scan func(...any) error) (syncsvc.KeyPackage, error) {
	var pkg syncsvc.KeyPackage
	var epoch, created int64
	if err := scan(&pkg.VaultID, &pkg.RecipientDeviceID, &pkg.SenderDeviceID, &epoch, &pkg.EntryDigest, &pkg.Ciphertext, &pkg.Signature, &created); err != nil {
		return syncsvc.KeyPackage{}, err
	}
	ints := &integerColumns{}
	pkg.VaultEpoch = ints.load(epoch)
	pkg.CreatedAt = unixTime(created)
	if ints.err != nil {
		return syncsvc.KeyPackage{}, ints.err
	}
	return pkg, nil
}

func scanHeader(scan func(...any) error) (syncsvc.StoredHeader, error) {
	var stored syncsvc.StoredHeader
	var revision, epoch, listIndex, length, accepted int64
	if err := scan(&stored.VaultID, &revision, &stored.Version, &stored.DeviceID, &epoch, &listIndex,
		&stored.DeviceListDigest, &stored.PreviousDigest, &stored.CiphertextDigest, &length,
		&stored.Nonce, &stored.Signature, &stored.Digest, &stored.Receipt, &accepted); err != nil {
		return syncsvc.StoredHeader{}, err
	}
	ints := &integerColumns{}
	stored.Revision = ints.load(revision)
	stored.VaultEpoch = ints.load(epoch)
	stored.DeviceListIndex = ints.load(listIndex)
	stored.CiphertextLength = ints.load(length)
	stored.AcceptedAt = unixTime(accepted)
	if ints.err != nil {
		return syncsvc.StoredHeader{}, ints.err
	}
	return stored, nil
}

func scanPairing(scan func(...any) error) (syncsvc.Pairing, error) {
	var pairing syncsvc.Pairing
	var expires, created int64
	if err := scan(&pairing.VaultID, &pairing.DeviceID, &pairing.TokenDeviceID, &pairing.SigningKey, &pairing.PackageKey,
		&pairing.Offer, &pairing.Answer, &pairing.Confirm, &pairing.State, &expires, &created); err != nil {
		return syncsvc.Pairing{}, err
	}
	pairing.ExpiresAt = unixTime(expires)
	pairing.CreatedAt = unixTime(created)
	return pairing, nil
}

func scanBytes(scan func(...any) error) ([]byte, error) {
	var body []byte
	err := scan(&body)
	return body, err
}

func (t *syncTx) VaultByOwner(ownerKey string) (syncsvc.Vault, error) {
	return queryOne(t.ctx, t.db, scanVault, `SELECT `+syncVaultColumns+` FROM sync_vaults WHERE owner_key = ?`, ownerKey)
}

func (t *syncTx) VaultByID(vaultID string) (syncsvc.Vault, error) {
	return queryOne(t.ctx, t.db, scanVault, `SELECT `+syncVaultColumns+` FROM sync_vaults WHERE id = ?`, vaultID)
}

func (t *syncTx) InsertVault(vault syncsvc.Vault) error {
	ints := &integerColumns{}
	revision, head, epoch := ints.store(vault.CurrentRevision), ints.store(vault.HeadIndex), ints.store(vault.Epoch)
	if ints.err != nil {
		return ints.err
	}
	return t.exec(`INSERT INTO sync_vaults (`+syncVaultColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		vault.ID, vault.OwnerKey, revision, head, epoch, storedTime(vault.CreatedAt), storedTime(vault.UpdatedAt))
}

func (t *syncTx) UpdateVault(vault syncsvc.Vault) error {
	ints := &integerColumns{}
	revision, head, epoch := ints.store(vault.CurrentRevision), ints.store(vault.HeadIndex), ints.store(vault.Epoch)
	if ints.err != nil {
		return ints.err
	}
	changed, err := t.execCount(`UPDATE sync_vaults SET owner_key = ?, current_revision = ?, head_index = ?, epoch = ?, created_at = ?, updated_at = ?
		WHERE id = ?`,
		vault.OwnerKey, revision, head, epoch, storedTime(vault.CreatedAt), storedTime(vault.UpdatedAt), vault.ID)
	if err != nil {
		return err
	}
	if changed == 0 {
		return syncsvc.ErrNotFound
	}
	return nil
}

func (t *syncTx) DeleteVault(vaultID string) error {
	return t.exec(`DELETE FROM sync_vaults WHERE id = ?`, vaultID)
}

func (t *syncTx) Devices(vaultID string) ([]syncsvc.Device, error) {
	return collect(t.ctx, t.db, scanSyncDevice, `SELECT `+syncDeviceColumns+` FROM sync_devices WHERE vault_id = ? ORDER BY added_index, device_id`, vaultID)
}

func (t *syncTx) Device(vaultID, deviceID string) (syncsvc.Device, error) {
	return queryOne(t.ctx, t.db, scanSyncDevice, `SELECT `+syncDeviceColumns+` FROM sync_devices WHERE vault_id = ? AND device_id = ?`, vaultID, deviceID)
}

func (t *syncTx) DeviceByToken(vaultID, tokenDeviceID string) (syncsvc.Device, error) {
	return queryOne(t.ctx, t.db, scanSyncDevice, `SELECT `+syncDeviceColumns+` FROM sync_devices WHERE vault_id = ? AND token_device_id = ? ORDER BY (state = 'revoked'), added_index DESC LIMIT 1`, vaultID, tokenDeviceID)
}

func (t *syncTx) PutDevice(device syncsvc.Device) error {
	ints := &integerColumns{}
	added, activated := ints.store(device.AddedIndex), ints.store(device.ActivatedEpoch)
	if ints.err != nil {
		return ints.err
	}
	return t.exec(`INSERT INTO sync_devices (`+syncDeviceColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (vault_id, device_id) DO UPDATE SET token_device_id = excluded.token_device_id, signing_key = excluded.signing_key,
			package_key = excluded.package_key, state = excluded.state, added_index = excluded.added_index,
			activated_epoch = excluded.activated_epoch, created_at = excluded.created_at`,
		device.VaultID, device.DeviceID, device.TokenDeviceID, device.SigningKey, device.PackageKey, device.State,
		added, activated, storedTime(device.CreatedAt))
}

func (t *syncTx) DeviceListEntries(vaultID string, after uint64, limit int) ([]syncproto.DeviceListEntry, error) {
	if limit <= 0 {
		return nil, nil
	}
	ints := &integerColumns{}
	afterIndex := ints.store(after)
	if ints.err != nil {
		return nil, ints.err
	}
	return collect(t.ctx, t.db, scanEntry, `SELECT `+syncEntryColumns+` FROM sync_device_list
		WHERE vault_id = ? AND idx > ? ORDER BY idx LIMIT ?`, vaultID, afterIndex, limit)
}

func (t *syncTx) AppendDeviceListEntry(entry syncproto.DeviceListEntry) error {
	ints := &integerColumns{}
	index, epochAfter, snapshotFloor := ints.store(entry.Index), ints.store(entry.EpochAfter), ints.store(entry.SnapshotFloor)
	if ints.err != nil {
		return ints.err
	}
	var last sql.NullInt64
	if err := t.db.QueryRowContext(t.ctx, `SELECT MAX(idx) FROM sync_device_list WHERE vault_id = ?`, entry.VaultID).Scan(&last); err != nil {
		return syncError(err)
	}
	if last.Int64+1 != index {
		return syncsvc.ErrConflict
	}
	return t.exec(`INSERT INTO sync_device_list (`+syncEntryColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.VaultID, index, entry.PreviousDigest, entry.Kind, entry.SignerDeviceID, entry.SubjectDeviceID,
		entry.SubjectSigningKey, entry.SubjectPackageKey, entry.SealedLabel, epochAfter, snapshotFloor, entry.Signature)
}

func (t *syncTx) PutKeyPackage(pkg syncsvc.KeyPackage) error {
	ints := &integerColumns{}
	epoch := ints.store(pkg.VaultEpoch)
	if ints.err != nil {
		return ints.err
	}
	return t.exec(`INSERT INTO sync_key_packages (`+syncKeyPackageColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (vault_id, recipient_device_id) DO UPDATE SET sender_device_id = excluded.sender_device_id,
			vault_epoch = excluded.vault_epoch, entry_digest = excluded.entry_digest, ciphertext = excluded.ciphertext, signature = excluded.signature,
			created_at = excluded.created_at`,
		pkg.VaultID, pkg.RecipientDeviceID, pkg.SenderDeviceID, epoch, pkg.EntryDigest, pkg.Ciphertext, pkg.Signature, storedTime(pkg.CreatedAt))
}

func (t *syncTx) KeyPackage(vaultID, recipientDeviceID string) (syncsvc.KeyPackage, error) {
	return queryOne(t.ctx, t.db, scanKeyPackage, `SELECT `+syncKeyPackageColumns+` FROM sync_key_packages WHERE vault_id = ? AND recipient_device_id = ?`,
		vaultID, recipientDeviceID)
}

func (t *syncTx) DeleteKeyPackages(vaultID string) error {
	return t.exec(`DELETE FROM sync_key_packages WHERE vault_id = ?`, vaultID)
}

func (t *syncTx) InsertHeader(header syncsvc.StoredHeader) error {
	ints := &integerColumns{}
	revision, epoch, listIndex := ints.store(header.Revision), ints.store(header.VaultEpoch), ints.store(header.DeviceListIndex)
	length := ints.store(header.CiphertextLength)
	if ints.err != nil {
		return ints.err
	}
	return t.exec(`INSERT INTO sync_headers (`+syncHeaderColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		header.VaultID, revision, header.Version, header.DeviceID, epoch, listIndex, header.DeviceListDigest, header.PreviousDigest,
		header.CiphertextDigest, length, header.Nonce, header.Signature, header.Digest, header.Receipt, storedTime(header.AcceptedAt))
}

func (t *syncTx) Headers(vaultID string, after uint64, limit int) ([]syncsvc.StoredHeader, error) {
	if limit <= 0 {
		return nil, nil
	}
	ints := &integerColumns{}
	afterRevision := ints.store(after)
	if ints.err != nil {
		return nil, ints.err
	}
	return collect(t.ctx, t.db, scanHeader, `SELECT `+syncHeaderColumns+` FROM sync_headers
		WHERE vault_id = ? AND revision > ? ORDER BY revision LIMIT ?`, vaultID, afterRevision, limit)
}

func (t *syncTx) Header(vaultID string, revision uint64) (syncsvc.StoredHeader, error) {
	ints := &integerColumns{}
	value := ints.store(revision)
	if ints.err != nil {
		return syncsvc.StoredHeader{}, ints.err
	}
	return queryOne(t.ctx, t.db, scanHeader, `SELECT `+syncHeaderColumns+` FROM sync_headers WHERE vault_id = ? AND revision = ?`, vaultID, value)
}

func (t *syncTx) LatestHeader(vaultID string) (syncsvc.StoredHeader, error) {
	return queryOne(t.ctx, t.db, scanHeader, `SELECT `+syncHeaderColumns+` FROM sync_headers WHERE vault_id = ? ORDER BY revision DESC LIMIT 1`, vaultID)
}

func (t *syncTx) PruneHeaders(vaultID string, keepFrom uint64) error {
	ints := &integerColumns{}
	floor := ints.store(keepFrom)
	if ints.err != nil {
		return ints.err
	}
	return t.exec(`DELETE FROM sync_headers WHERE vault_id = ? AND revision < ?`, vaultID, floor)
}

func (t *syncTx) PutCiphertext(vaultID string, revision uint64, ciphertext []byte) error {
	ints := &integerColumns{}
	value := ints.store(revision)
	if ints.err != nil {
		return ints.err
	}
	return t.exec(`INSERT INTO sync_ciphertexts (vault_id, revision, body) VALUES (?, ?, ?)
		ON CONFLICT (vault_id, revision) DO UPDATE SET body = excluded.body`, vaultID, value, ciphertext)
}

func (t *syncTx) Ciphertext(vaultID string, revision uint64) ([]byte, error) {
	ints := &integerColumns{}
	value := ints.store(revision)
	if ints.err != nil {
		return nil, ints.err
	}
	return queryOne(t.ctx, t.db, scanBytes, `SELECT body FROM sync_ciphertexts WHERE vault_id = ? AND revision = ?`, vaultID, value)
}

func (t *syncTx) CiphertextBytes(vaultID string) (int64, error) {
	var total int64
	err := t.db.QueryRowContext(t.ctx, `SELECT COALESCE(SUM(length(body)), 0) FROM sync_ciphertexts WHERE vault_id = ?`, vaultID).Scan(&total)
	return total, syncError(err)
}

func (t *syncTx) OldestCiphertext(vaultID string) (uint64, int64, error) {
	var revision, size int64
	err := t.db.QueryRowContext(t.ctx, `SELECT revision, length(body) FROM sync_ciphertexts WHERE vault_id = ? ORDER BY revision LIMIT 1`, vaultID).Scan(&revision, &size)
	if err != nil {
		return 0, 0, syncError(err)
	}
	ints := &integerColumns{}
	oldest := ints.load(revision)
	if ints.err != nil {
		return 0, 0, ints.err
	}
	return oldest, size, nil
}

func (t *syncTx) DeleteCiphertext(vaultID string, revision uint64) error {
	ints := &integerColumns{}
	value := ints.store(revision)
	if ints.err != nil {
		return ints.err
	}
	return t.exec(`DELETE FROM sync_ciphertexts WHERE vault_id = ? AND revision = ?`, vaultID, value)
}

func (t *syncTx) DeleteCiphertextsBefore(vaultID string, revision uint64) error {
	ints := &integerColumns{}
	value := ints.store(revision)
	if ints.err != nil {
		return ints.err
	}
	return t.exec(`DELETE FROM sync_ciphertexts WHERE vault_id = ? AND revision < ?`, vaultID, value)
}

func scanCheckpoint(scan func(...any) error) (syncsvc.StoredCheckpoint, error) {
	var stored syncsvc.StoredCheckpoint
	var listIndex, revision, observed, accepted int64
	if err := scan(&stored.VaultID, &stored.DeviceID, &stored.Version, &listIndex, &stored.DeviceListDigest,
		&revision, &stored.HeaderDigest, &observed, &stored.Signature, &accepted); err != nil {
		return syncsvc.StoredCheckpoint{}, err
	}
	ints := &integerColumns{}
	stored.DeviceListIndex, stored.HeaderRevision, stored.ObservedAt = ints.load(listIndex), ints.load(revision), ints.load(observed)
	if ints.err != nil {
		return syncsvc.StoredCheckpoint{}, ints.err
	}
	stored.AcceptedAt = unixTime(accepted)
	return stored, nil
}

func (t *syncTx) PutCheckpoint(checkpoint syncsvc.StoredCheckpoint) error {
	ints := &integerColumns{}
	listIndex, revision, observed := ints.store(checkpoint.DeviceListIndex), ints.store(checkpoint.HeaderRevision), ints.store(checkpoint.ObservedAt)
	if ints.err != nil {
		return ints.err
	}
	return t.exec(`INSERT INTO sync_checkpoints (`+syncCheckpointColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (vault_id, device_id) DO UPDATE SET version = excluded.version, device_list_index = excluded.device_list_index,
			device_list_digest = excluded.device_list_digest, header_revision = excluded.header_revision,
			header_digest = excluded.header_digest, observed_at = excluded.observed_at, signature = excluded.signature,
			accepted_at = excluded.accepted_at`,
		checkpoint.VaultID, checkpoint.DeviceID, checkpoint.Version, listIndex, checkpoint.DeviceListDigest,
		revision, checkpoint.HeaderDigest, observed, checkpoint.Signature, storedTime(checkpoint.AcceptedAt))
}

func (t *syncTx) Checkpoint(vaultID, deviceID string) (syncsvc.StoredCheckpoint, error) {
	return queryOne(t.ctx, t.db, scanCheckpoint, `SELECT `+syncCheckpointColumns+` FROM sync_checkpoints WHERE vault_id = ? AND device_id = ?`, vaultID, deviceID)
}

func (t *syncTx) Checkpoints(vaultID string) ([]syncsvc.StoredCheckpoint, error) {
	return collect(t.ctx, t.db, scanCheckpoint, `SELECT `+syncCheckpointColumns+` FROM sync_checkpoints WHERE vault_id = ? ORDER BY device_id`, vaultID)
}

func (t *syncTx) DeleteCheckpoint(vaultID, deviceID string) error {
	return t.exec(`DELETE FROM sync_checkpoints WHERE vault_id = ? AND device_id = ?`, vaultID, deviceID)
}

func (t *syncTx) PutPairing(pairing syncsvc.Pairing) error {
	return t.exec(`INSERT INTO sync_pairings (`+syncPairingColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (vault_id, device_id) DO UPDATE SET token_device_id = excluded.token_device_id, signing_key = excluded.signing_key,
			package_key = excluded.package_key, offer = excluded.offer, answer = excluded.answer, confirm = excluded.confirm,
			state = excluded.state, expires_at = excluded.expires_at, created_at = excluded.created_at`,
		pairing.VaultID, pairing.DeviceID, pairing.TokenDeviceID, pairing.SigningKey, pairing.PackageKey,
		pairing.Offer, pairing.Answer, pairing.Confirm, pairing.State, expiryUnix(pairing.ExpiresAt), storedTime(pairing.CreatedAt))
}

func (t *syncTx) Pairing(vaultID, deviceID string) (syncsvc.Pairing, error) {
	return queryOne(t.ctx, t.db, scanPairing, `SELECT `+syncPairingColumns+` FROM sync_pairings WHERE vault_id = ? AND device_id = ?`, vaultID, deviceID)
}

func (t *syncTx) PairingByToken(vaultID, tokenDeviceID string) (syncsvc.Pairing, error) {
	return queryOne(t.ctx, t.db, scanPairing, `SELECT `+syncPairingColumns+` FROM sync_pairings WHERE vault_id = ? AND token_device_id = ?`, vaultID, tokenDeviceID)
}

func (t *syncTx) Pairings(vaultID string) ([]syncsvc.Pairing, error) {
	return collect(t.ctx, t.db, scanPairing, `SELECT `+syncPairingColumns+` FROM sync_pairings WHERE vault_id = ? ORDER BY created_at, device_id`, vaultID)
}

func (t *syncTx) DeletePairing(vaultID, deviceID string) error {
	return t.exec(`DELETE FROM sync_pairings WHERE vault_id = ? AND device_id = ?`, vaultID, deviceID)
}

func (t *syncTx) RecordNonce(scope, nonce string, now, expiresAt time.Time) (bool, error) {
	changed, err := t.execCount(`INSERT INTO sync_nonces (scope, nonce, expires_at) VALUES (?, ?, ?)
		ON CONFLICT (scope, nonce) DO UPDATE SET expires_at = excluded.expires_at WHERE sync_nonces.expires_at <= ?`,
		scope, nonce, expiryUnix(expiresAt), storedTime(now))
	if err != nil {
		return false, err
	}
	return changed == 1, nil
}

func (t *syncTx) PurgeExpired(now time.Time) error {
	if err := t.exec(`DELETE FROM sync_nonces WHERE expires_at <= ?`, storedTime(now)); err != nil {
		return err
	}
	return t.exec(`DELETE FROM sync_pairings WHERE expires_at <= ?`, storedTime(now))
}
