package syncstore

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"usesesame.app/backend/internal/syncproto"
)

// One opaque vault snapshot or tombstone; Nonce, Ciphertext, and Signature are
// bytes this package never interprets.
type Envelope struct {
	VaultID          string
	Revision         uint64
	PreviousRevision uint64
	DeviceID         string
	VaultEpoch       uint64
	DeviceEpoch      uint64
	Operation        string
	TombstoneID      string
	Nonce            []byte
	Ciphertext       []byte
	Signature        []byte
	CreatedAt        time.Time
	// PreviousDigest names the predecessor and is inside the signed bytes.
	PreviousDigest string
	Digest         string
	Receipt        string
}

func (envelope Envelope) protocolForm() syncproto.Envelope {
	return syncproto.Envelope{
		Version:          syncproto.Version,
		VaultID:          envelope.VaultID,
		DeviceID:         envelope.DeviceID,
		Revision:         envelope.Revision,
		PreviousRevision: envelope.PreviousRevision,
		VaultEpoch:       envelope.VaultEpoch,
		DeviceEpoch:      envelope.DeviceEpoch,
		Operation:        envelope.Operation,
		TombstoneID:      envelope.TombstoneID,
		PreviousDigest:   envelope.PreviousDigest,
		Nonce:            base64.RawURLEncoding.EncodeToString(envelope.Nonce),
		Ciphertext:       base64.RawURLEncoding.EncodeToString(envelope.Ciphertext),
		Signature:        base64.RawURLEncoding.EncodeToString(envelope.Signature),
	}
}

// Compare-and-swap upload: succeeds only when the vault is still at
// PreviousRevision, else ErrConflict, never a retry-by-overwrite. Enforced
// by the serializable transaction, the conditional UPDATE, and the PRIMARY
// KEY (vault_id, revision). The device epoch must also match the vault's, so
// a device revoked since its last upload cannot write under a stale key.
func (s *Store) AppendEnvelope(ctx context.Context, envelope Envelope) (Vault, error) {
	vault, _, err := s.appendEnvelope(ctx, envelope)
	return vault, err
}

func (s *Store) AppendEnvelopeAccepted(ctx context.Context, envelope Envelope) (Vault, Envelope, error) {
	return s.appendEnvelope(ctx, envelope)
}

func (s *Store) appendEnvelope(ctx context.Context, envelope Envelope) (Vault, Envelope, error) {
	if envelope.Revision != envelope.PreviousRevision+1 {
		return Vault{}, Envelope{}, fmt.Errorf("%w: revisions must be contiguous", ErrConflict)
	}
	var vault Vault
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if err := s.appendEnvelopeTx(ctx, tx, &envelope); err != nil {
			return err
		}
		row := tx.QueryRowContext(ctx, `
			SELECT id, account_id, vault_epoch, current_revision, created_at, updated_at
			FROM sesame_sync_vaults WHERE id = $1
		`, envelope.VaultID)
		if err := row.Scan(&vault.ID, &vault.AccountID, &vault.VaultEpoch, &vault.CurrentRevision, &vault.CreatedAt, &vault.UpdatedAt); err != nil {
			return fmt.Errorf("read sync vault after append: %w", err)
		}
		return nil
	})
	if err != nil {
		return Vault{}, Envelope{}, err
	}
	return vault, envelope, nil
}

func (s *Store) appendEnvelopeTx(ctx context.Context, tx *sql.Tx, envelope *Envelope) error {
	var deviceState string
	var deviceEpoch, activatedEpoch uint64
	err := tx.QueryRowContext(ctx, `
		SELECT state, device_epoch, activated_epoch FROM sesame_sync_devices WHERE id = $1 AND vault_id = $2
	`, envelope.DeviceID, envelope.VaultID).Scan(&deviceState, &deviceEpoch, &activatedEpoch)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return ErrNotFound
	case err != nil:
		return fmt.Errorf("read uploading sync device: %w", err)
	case deviceState != DeviceApproved:
		return ErrApprovalRejected
	case activatedEpoch != deviceEpoch:
		// Two-phase activation: a device that has not proven it can act on its
		// package must not write a revision others would have to read.
		return ErrApprovalRejected
	}

	// Verify the claimed device authored these bytes, in the same transaction.
	var signingKey []byte
	if err := tx.QueryRowContext(ctx, `
		SELECT signing_public_key FROM sesame_sync_devices WHERE id = $1 AND vault_id = $2
	`, envelope.DeviceID, envelope.VaultID).Scan(&signingKey); err != nil {
		return fmt.Errorf("read uploading sync device key: %w", err)
	}
	if err := envelope.protocolForm().VerifySignature(
		base64.RawURLEncoding.EncodeToString(signingKey),
	); err != nil {
		return ErrApprovalRejected
	}

	var vaultEpoch uint64
	if err := tx.QueryRowContext(ctx, `
		SELECT vault_epoch FROM sesame_sync_vaults WHERE id = $1
	`, envelope.VaultID).Scan(&vaultEpoch); err != nil {
		return fmt.Errorf("read sync vault epoch: %w", err)
	}
	// A stale device epoch means the device predates a revocation; it must
	// re-fetch a key package before writing. All three epochs must agree.
	if deviceEpoch != vaultEpoch ||
		envelope.VaultEpoch != vaultEpoch ||
		envelope.DeviceEpoch != deviceEpoch {
		return fmt.Errorf("%w: stale epoch", ErrConflict)
	}

	// Every revision after the first names its predecessor by digest, and the
	// digest is inside the signed bytes, so the service cannot rewrite the chain.
	var expectedPrevious string
	if envelope.Revision > 1 {
		err := tx.QueryRowContext(ctx, `
			SELECT digest FROM sesame_sync_envelopes WHERE vault_id = $1 AND revision = $2
		`, envelope.VaultID, envelope.PreviousRevision).Scan(&expectedPrevious)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			expectedPrevious = envelope.PreviousDigest
		case err != nil:
			return fmt.Errorf("read previous sync envelope digest: %w", err)
		}
		if expectedPrevious != "" && envelope.PreviousDigest != expectedPrevious {
			return fmt.Errorf("%w: revision chain does not match", ErrConflict)
		}
	} else if envelope.PreviousDigest != "" {
		return fmt.Errorf("%w: revision chain does not match", ErrConflict)
	}

	if _, err := tx.ExecContext(ctx, `
		DELETE FROM sesame_sync_envelopes
		WHERE vault_id = $1
		  AND revision <= (
		    SELECT MAX(revision) - ($2 - 1) FROM sesame_sync_envelopes WHERE vault_id = $1
		  )
	`, envelope.VaultID, syncproto.MaxSnapshotsPerVault); err != nil {
		return fmt.Errorf("prune sync envelopes: %w", err)
	}

	result, err := tx.ExecContext(ctx, `
		UPDATE sesame_sync_vaults
		SET current_revision = $2, updated_at = NOW()
		WHERE id = $1 AND current_revision = $3
	`, envelope.VaultID, envelope.Revision, envelope.PreviousRevision)
	if err != nil {
		return fmt.Errorf("advance sync revision: %w", err)
	}
	advanced, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("advance sync revision: %w", err)
	}
	if advanced != 1 {
		return ErrConflict
	}

	var storedBytes int64
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(LENGTH(ciphertext)), 0) FROM sesame_sync_envelopes WHERE vault_id = $1
	`, envelope.VaultID).Scan(&storedBytes); err != nil {
		return fmt.Errorf("measure stored sync bytes: %w", err)
	}
	if storedBytes+int64(len(envelope.Ciphertext)) > s.byteBudget() {
		return ErrQuotaExceeded
	}

	// Computed here, never accepted from the caller, so a client cannot point the chain anywhere.
	digest := syncproto.EnvelopeDigest(envelope.protocolForm())
	envelope.Digest = digest

	if len(s.receiptKey) == ed25519.PrivateKeySize {
		encoded, signature, err := syncproto.SignReceipt(s.receiptKey, syncproto.Receipt{
			VaultID:    envelope.VaultID,
			Revision:   envelope.Revision,
			Digest:     digest,
			VaultEpoch: envelope.VaultEpoch,
			AcceptedAt: nowUTC().Format(time.RFC3339),
		})
		if err != nil {
			return fmt.Errorf("sign sync acceptance receipt: %w", err)
		}
		envelope.Receipt = encoded + "." + signature
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO sesame_sync_envelopes (
			vault_id, revision, previous_revision, device_id, vault_epoch, device_epoch,
			operation, tombstone_id, nonce, ciphertext, signature,
			previous_digest, digest, receipt
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
	`,
		envelope.VaultID, envelope.Revision, envelope.PreviousRevision, envelope.DeviceID,
		envelope.VaultEpoch, envelope.DeviceEpoch, envelope.Operation, envelope.TombstoneID,
		envelope.Nonce, envelope.Ciphertext, envelope.Signature,
		envelope.PreviousDigest, digest, envelope.Receipt,
	); err != nil {
		return fmt.Errorf("store sync envelope: %w", err)
	}

	return nil
}

func (s *Store) LatestEnvelope(ctx context.Context, vaultID string) (Envelope, error) {
	var envelope Envelope
	row := s.db.QueryRowContext(ctx, `
		SELECT vault_id, revision, previous_revision, device_id, vault_epoch, device_epoch,
		       operation, tombstone_id, nonce, ciphertext, signature, created_at,
		       previous_digest, digest, receipt
		FROM sesame_sync_envelopes WHERE vault_id = $1 ORDER BY revision DESC LIMIT 1
	`, vaultID)
	switch err := scanEnvelope(row, &envelope); {
	case errors.Is(err, sql.ErrNoRows):
		return Envelope{}, ErrNotFound
	case err != nil:
		return Envelope{}, fmt.Errorf("read latest sync envelope: %w", err)
	}
	return envelope, nil
}

// Bounded: an unbounded read is a denial-of-service lever.
func (s *Store) EnvelopesSince(ctx context.Context, vaultID string, afterRevision uint64, limit int) ([]Envelope, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT vault_id, revision, previous_revision, device_id, vault_epoch, device_epoch,
		       operation, tombstone_id, nonce, ciphertext, signature, created_at,
		       previous_digest, digest, receipt
		FROM sesame_sync_envelopes
		WHERE vault_id = $1 AND revision > $2
		ORDER BY revision ASC LIMIT $3
	`, vaultID, afterRevision, limit)
	if err != nil {
		return nil, fmt.Errorf("list sync envelopes: %w", err)
	}
	defer rows.Close()
	envelopes := make([]Envelope, 0)
	for rows.Next() {
		var envelope Envelope
		if err := scanEnvelope(rows, &envelope); err != nil {
			return nil, fmt.Errorf("scan sync envelope: %w", err)
		}
		envelopes = append(envelopes, envelope)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list sync envelopes: %w", err)
	}
	return envelopes, nil
}

func scanEnvelope(src scanner, envelope *Envelope) error {
	return src.Scan(
		&envelope.VaultID, &envelope.Revision, &envelope.PreviousRevision, &envelope.DeviceID,
		&envelope.VaultEpoch, &envelope.DeviceEpoch, &envelope.Operation, &envelope.TombstoneID,
		&envelope.Nonce, &envelope.Ciphertext, &envelope.Signature, &envelope.CreatedAt,
		&envelope.PreviousDigest, &envelope.Digest, &envelope.Receipt,
	)
}
