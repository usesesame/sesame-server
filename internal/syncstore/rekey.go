package syncstore

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"

	"usesesame.app/backend/internal/syncproto"
)

// Revocation must carry new key material: an epoch alone does not remove knowledge of a key.

// One new key package per survivor; the service never sees the vault key.
type SurvivorPackage struct {
	RecipientDeviceID string
	Ciphertext        []byte
	Signature         []byte
}

type Rekey struct {
	VaultID string
	// Empty means rotate the key without removing anyone.
	RevokedDeviceID   string
	InitiatorDeviceID string
	Envelope          Envelope
	Survivors         []SurvivorPackage
}

// One atomic transition: revoke, advance the epoch, append the re-encrypted
// head, and re-wrap every survivor. A failure at any step rolls back the lot.
func (s *Store) RevokeAndRekey(ctx context.Context, rekey Rekey) (Vault, error) {
	if rekey.InitiatorDeviceID == "" || rekey.VaultID == "" {
		return Vault{}, ErrApprovalRejected
	}
	if rekey.RevokedDeviceID == rekey.InitiatorDeviceID {
		return Vault{}, ErrApprovalRejected
	}
	var vault Vault
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			SELECT id FROM sesame_sync_vaults WHERE id = $1 FOR UPDATE
		`, rekey.VaultID); err != nil {
			return fmt.Errorf("lock sync vault: %w", err)
		}

		var initiatorState string
		var initiatorEpoch, initiatorActivated uint64
		err := tx.QueryRowContext(ctx, `
			SELECT state, device_epoch, activated_epoch
			FROM sesame_sync_devices WHERE id = $1 AND vault_id = $2
		`, rekey.InitiatorDeviceID, rekey.VaultID).Scan(&initiatorState, &initiatorEpoch, &initiatorActivated)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return ErrNotFound
		case err != nil:
			return fmt.Errorf("read rekey initiator: %w", err)
		case initiatorState != DeviceApproved || initiatorActivated != initiatorEpoch:
			// Only an activated device can prove it holds the current vault key, so only it can rotate.
			return ErrApprovalRejected
		}

		if rekey.RevokedDeviceID != "" {
			result, err := tx.ExecContext(ctx, `
				UPDATE sesame_sync_devices
				SET state = 'revoked', revoked_at = NOW()
				WHERE id = $1 AND vault_id = $2 AND state <> 'revoked'
			`, rekey.RevokedDeviceID, rekey.VaultID)
			if err != nil {
				return fmt.Errorf("revoke sync device: %w", err)
			}
			revoked, err := result.RowsAffected()
			if err != nil {
				return fmt.Errorf("revoke sync device: %w", err)
			}
			if revoked != 1 {
				return ErrNotFound
			}
			if _, err := tx.ExecContext(ctx, `
				DELETE FROM sesame_sync_key_packages WHERE vault_id = $1 AND recipient_device_id = $2
			`, rekey.VaultID, rekey.RevokedDeviceID); err != nil {
				return fmt.Errorf("delete revoked sync key package: %w", err)
			}
		}

		var newEpoch uint64
		if err := tx.QueryRowContext(ctx, `
			UPDATE sesame_sync_vaults
			SET vault_epoch = vault_epoch + 1, rekeyed_at = NOW(), updated_at = NOW()
			WHERE id = $1
			RETURNING vault_epoch
		`, rekey.VaultID).Scan(&newEpoch); err != nil {
			return fmt.Errorf("advance sync vault epoch: %w", err)
		}

		if _, err := tx.ExecContext(ctx, `
			UPDATE sesame_sync_devices
			SET device_epoch = $3, activated_epoch = $3
			WHERE id = $1 AND vault_id = $2
		`, rekey.InitiatorDeviceID, rekey.VaultID, newEpoch); err != nil {
			return fmt.Errorf("activate rekey initiator: %w", err)
		}

		if err := s.appendEnvelopeTx(ctx, tx, &rekey.Envelope); err != nil {
			return err
		}

		// Survivors stay un-activated until they prove they opened the package.
		for _, survivor := range rekey.Survivors {
			if survivor.RecipientDeviceID == rekey.RevokedDeviceID {
				return ErrApprovalRejected
			}
			var state string
			var signingKey []byte
			err := tx.QueryRowContext(ctx, `
				SELECT state, signing_public_key FROM sesame_sync_devices WHERE id = $1 AND vault_id = $2
			`, survivor.RecipientDeviceID, rekey.VaultID).Scan(&state, &signingKey)
			switch {
			case errors.Is(err, sql.ErrNoRows):
				return ErrNotFound
			case err != nil:
				return fmt.Errorf("read surviving sync device: %w", err)
			case state != DeviceApproved:
				return ErrApprovalRejected
			}

			var initiatorKey []byte
			if err := tx.QueryRowContext(ctx, `
				SELECT signing_public_key FROM sesame_sync_devices WHERE id = $1 AND vault_id = $2
			`, rekey.InitiatorDeviceID, rekey.VaultID).Scan(&initiatorKey); err != nil {
				return fmt.Errorf("read rekey initiator key: %w", err)
			}
			pkg := syncproto.EncryptedKeyPackage{
				VaultID:           rekey.VaultID,
				SenderDeviceID:    rekey.InitiatorDeviceID,
				RecipientDeviceID: survivor.RecipientDeviceID,
				Ciphertext:        base64.RawURLEncoding.EncodeToString(survivor.Ciphertext),
				Signature:         base64.RawURLEncoding.EncodeToString(survivor.Signature),
				CreatedAt:         nowUTC(),
			}
			if err := pkg.VerifySignature(base64.RawURLEncoding.EncodeToString(initiatorKey)); err != nil {
				return ErrApprovalRejected
			}

			if _, err := tx.ExecContext(ctx, `
				INSERT INTO sesame_sync_key_packages (vault_id, recipient_device_id, sender_device_id, ciphertext, signature)
				VALUES ($1, $2, $3, $4, $5)
				ON CONFLICT (vault_id, recipient_device_id) DO UPDATE
				SET sender_device_id = EXCLUDED.sender_device_id,
				    ciphertext = EXCLUDED.ciphertext,
				    signature = EXCLUDED.signature,
				    created_at = NOW()
			`, rekey.VaultID, survivor.RecipientDeviceID, rekey.InitiatorDeviceID,
				survivor.Ciphertext, survivor.Signature); err != nil {
				return fmt.Errorf("store surviving sync key package: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `
				UPDATE sesame_sync_devices SET device_epoch = $3
				WHERE id = $1 AND vault_id = $2
			`, survivor.RecipientDeviceID, rekey.VaultID, newEpoch); err != nil {
				return fmt.Errorf("carry surviving sync device: %w", err)
			}
		}

		if rekey.RevokedDeviceID != "" {
			if err := recordAudit(ctx, tx, rekey.VaultID, rekey.RevokedDeviceID, "device_revoked"); err != nil {
				return err
			}
		}
		if err := recordAudit(ctx, tx, rekey.VaultID, rekey.InitiatorDeviceID, "vault_rekeyed"); err != nil {
			return err
		}
		if err := recordAudit(ctx, tx, rekey.VaultID, "", "vault_epoch_advanced"); err != nil {
			return err
		}

		row := tx.QueryRowContext(ctx, `
			SELECT id, account_id, vault_epoch, current_revision, created_at, updated_at
			FROM sesame_sync_vaults WHERE id = $1
		`, rekey.VaultID)
		return row.Scan(&vault.ID, &vault.AccountID, &vault.VaultEpoch, &vault.CurrentRevision, &vault.CreatedAt, &vault.UpdatedAt)
	})
	if err != nil {
		return Vault{}, err
	}
	return vault, nil
}

// Two-phase activation: the proof is a signature over the sealed package's
// digest, which only a device that could unseal it and holds its own signing
// key can produce.
func (s *Store) ActivateDevice(ctx context.Context, vaultID, deviceID string, proof []byte) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var state string
		var deviceEpoch uint64
		var signingKey []byte
		err := tx.QueryRowContext(ctx, `
			SELECT state, device_epoch, signing_public_key
			FROM sesame_sync_devices WHERE id = $1 AND vault_id = $2
		`, deviceID, vaultID).Scan(&state, &deviceEpoch, &signingKey)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return ErrNotFound
		case err != nil:
			return fmt.Errorf("read activating sync device: %w", err)
		case state != DeviceApproved:
			return ErrApprovalRejected
		}

		var ciphertext []byte
		if err := tx.QueryRowContext(ctx, `
			SELECT ciphertext FROM sesame_sync_key_packages
			WHERE vault_id = $1 AND recipient_device_id = $2
		`, vaultID, deviceID).Scan(&ciphertext); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("read sync key package for activation: %w", err)
		}

		if !syncproto.VerifyActivation(
			base64.RawURLEncoding.EncodeToString(signingKey),
			vaultID, deviceID, deviceEpoch,
			base64.RawURLEncoding.EncodeToString(ciphertext),
			base64.RawURLEncoding.EncodeToString(proof),
		) {
			return ErrApprovalRejected
		}

		if _, err := tx.ExecContext(ctx, `
			UPDATE sesame_sync_devices SET activated_epoch = device_epoch
			WHERE id = $1 AND vault_id = $2
		`, deviceID, vaultID); err != nil {
			return fmt.Errorf("activate sync device: %w", err)
		}
		return recordAudit(ctx, tx, vaultID, deviceID, "device_activated")
	})
}

func (s *Store) DenyDevice(ctx context.Context, vaultID, deviceID string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			DELETE FROM sesame_sync_devices
			WHERE id = $1 AND vault_id = $2 AND state = 'pending'
		`, deviceID, vaultID)
		if err != nil {
			return fmt.Errorf("deny sync device: %w", err)
		}
		denied, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("deny sync device: %w", err)
		}
		if denied != 1 {
			return ErrApprovalRejected
		}
		return recordAudit(ctx, tx, vaultID, deviceID, "device_denied")
	})
}

func (s *Store) ApprovedDeviceCount(ctx context.Context, vaultID string) (int, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sesame_sync_devices WHERE vault_id = $1 AND state = 'approved'
	`, vaultID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count approved sync devices: %w", err)
	}
	return count, nil
}

// Destructive: empties a vault with no approved device left so a new first
// device can enroll. Refuses while any approved device remains, so it cannot
// eject working devices.
func (s *Store) ResetVault(ctx context.Context, vaultID string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			SELECT id FROM sesame_sync_vaults WHERE id = $1 FOR UPDATE
		`, vaultID); err != nil {
			return fmt.Errorf("lock sync vault: %w", err)
		}
		var approved int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM sesame_sync_devices WHERE vault_id = $1 AND state = 'approved'
		`, vaultID).Scan(&approved); err != nil {
			return fmt.Errorf("count approved sync devices: %w", err)
		}
		if approved > 0 {
			return ErrApprovalRejected
		}
		for _, statement := range []string{
			`DELETE FROM sesame_sync_key_packages WHERE vault_id = $1`,
			`DELETE FROM sesame_sync_envelopes WHERE vault_id = $1`,
			`DELETE FROM sesame_sync_challenges WHERE vault_id = $1`,
			`DELETE FROM sesame_sync_devices WHERE vault_id = $1`,
		} {
			if _, err := tx.ExecContext(ctx, statement, vaultID); err != nil {
				return fmt.Errorf("reset sync vault: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE sesame_sync_vaults
			SET vault_epoch = vault_epoch + 1, current_revision = 0, rekeyed_at = NOW(), updated_at = NOW()
			WHERE id = $1
		`, vaultID); err != nil {
			return fmt.Errorf("reset sync vault: %w", err)
		}
		return recordAudit(ctx, tx, vaultID, "", "vault_reset")
	})
}
