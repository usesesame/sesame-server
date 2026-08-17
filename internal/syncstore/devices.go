package syncstore

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"usesesame.app/backend/internal/syncproto"
)

// Server-generated, so a joining device cannot choose a challenge it has precomputed a proof for.
func (s *Store) IssueChallenge(ctx context.Context, vaultID string) ([]byte, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return nil, fmt.Errorf("generate sync enrollment challenge: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO sesame_sync_challenges (value, vault_id, expires_at) VALUES ($1, $2, $3)
	`, value, vaultID, time.Now().Add(ChallengeTTL)); err != nil {
		return nil, fmt.Errorf("store sync enrollment challenge: %w", err)
	}
	return value, nil
}

type Enrollment struct {
	VaultID             string
	DeviceID            string
	SigningPublicKey    []byte
	EncryptionPublicKey []byte
	Challenge           []byte
	Label               string
	// Required: without it, a revoked device keeps its desktop token and keeps reading.
	DesktopDeviceID string
}

// Consume the challenge in the same transaction that inserts the device, so a
// checked-then-used challenge cannot be replayed by two concurrent callers. A
// pending device holds no key package, so it can decrypt and upload nothing.
func (s *Store) EnrollDevice(ctx context.Context, enrollment Enrollment) (Device, error) {
	if enrollment.DesktopDeviceID == "" {
		return Device{}, ErrDesktopBindingRequired
	}
	var device Device
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		// Serializable-isolation retry rationale: quota and first-device status
		// are read-then-write, so locking the vault row turns the race into a
		// queue and the decision is made once.
		if _, err := tx.ExecContext(ctx, `
			SELECT id FROM sesame_sync_vaults WHERE id = $1 FOR UPDATE
		`, enrollment.VaultID); err != nil {
			return fmt.Errorf("lock sync vault: %w", err)
		}

		var devices int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM sesame_sync_devices WHERE vault_id = $1 AND state <> 'revoked'
		`, enrollment.VaultID).Scan(&devices); err != nil {
			return fmt.Errorf("count sync devices: %w", err)
		}
		if devices >= syncproto.MaxDevicesPerVault {
			return ErrQuotaExceeded
		}

		result, err := tx.ExecContext(ctx, `
			UPDATE sesame_sync_challenges
			SET consumed_at = NOW()
			WHERE value = $1 AND vault_id = $2 AND consumed_at IS NULL AND expires_at > NOW()
		`, enrollment.Challenge, enrollment.VaultID)
		if err != nil {
			return fmt.Errorf("consume sync enrollment challenge: %w", err)
		}
		consumed, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("consume sync enrollment challenge: %w", err)
		}
		// Unknown, expired, wrong-vault, and used challenges are one answer: no probing.
		if consumed != 1 {
			return ErrChallengeUnusable
		}

		var existing int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM sesame_sync_devices WHERE vault_id = $1 AND id <> $2
		`, enrollment.VaultID, enrollment.DeviceID).Scan(&existing); err != nil {
			return fmt.Errorf("count sync devices: %w", err)
		}
		state := "pending"
		if existing == 0 {
			state = "approved"
		}

		row := tx.QueryRowContext(ctx, `
			INSERT INTO sesame_sync_devices (id, vault_id, signing_public_key, encryption_public_key, state, label, desktop_device_id, approved_at, device_epoch, activated_epoch)
			VALUES (
			  $1, $2, $3, $4, $6, $5, $7,
			  CASE WHEN $6 = 'approved' THEN NOW() END,
			  CASE WHEN $6 = 'approved' THEN (SELECT vault_epoch FROM sesame_sync_vaults WHERE id = $2) ELSE 0 END,
			  -- The first device generated the vault key itself, so it has nothing to prove opening a package.
			  CASE WHEN $6 = 'approved' THEN (SELECT vault_epoch FROM sesame_sync_vaults WHERE id = $2) ELSE 0 END
			)
			RETURNING id, vault_id, signing_public_key, encryption_public_key, state, device_epoch, label, created_at, approved_at, revoked_at, activated_epoch
		`, enrollment.DeviceID, enrollment.VaultID, enrollment.SigningPublicKey, enrollment.EncryptionPublicKey, enrollment.Label, state, enrollment.DesktopDeviceID)
		if err := scanDevice(row, &device); err != nil {
			return fmt.Errorf("enroll sync device: %w", err)
		}
		if err := recordAudit(ctx, tx, enrollment.VaultID, device.ID, "device_enrolled"); err != nil {
			return err
		}
		if state == "approved" {
			return recordAudit(ctx, tx, enrollment.VaultID, device.ID, "device_approved")
		}
		return nil
	})
	if err != nil {
		return Device{}, err
	}
	return device, nil
}

// The vault key wrapped to one device's X25519 key by another, approved device.
type KeyPackage struct {
	VaultID string
	// Rejects approval with an obsolete key package after a rekey wins the race.
	ExpectedVaultEpoch uint64
	SenderDeviceID     string
	RecipientDeviceID  string
	Ciphertext         []byte
	Signature          []byte
}

// The approver must be a different, currently approved device: the website can
// never produce a key package, so an account password never reaches this path.
func (s *Store) ApproveDevice(ctx context.Context, pkg KeyPackage) (Device, error) {
	if pkg.SenderDeviceID == pkg.RecipientDeviceID || pkg.ExpectedVaultEpoch == 0 {
		return Device{}, ErrApprovalRejected
	}
	var device Device
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var vaultEpoch uint64
		if err := tx.QueryRowContext(ctx, `
			SELECT vault_epoch FROM sesame_sync_vaults WHERE id = $1 FOR UPDATE
		`, pkg.VaultID).Scan(&vaultEpoch); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrApprovalRejected
			}
			return fmt.Errorf("read sync vault epoch for approval: %w", err)
		}
		if vaultEpoch != pkg.ExpectedVaultEpoch {
			return ErrApprovalRejected
		}
		var approverState string
		var approverEpoch uint64
		err := tx.QueryRowContext(ctx, `
			SELECT state, device_epoch FROM sesame_sync_devices WHERE id = $1 AND vault_id = $2
		`, pkg.SenderDeviceID, pkg.VaultID).Scan(&approverState, &approverEpoch)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return ErrApprovalRejected
		case err != nil:
			return fmt.Errorf("read approving sync device: %w", err)
		case approverState != DeviceApproved || approverEpoch == 0:
			return ErrApprovalRejected
		}

		// Verify the sender actually authored the package, in the same transaction.
		var approverSigningKey []byte
		if err := tx.QueryRowContext(ctx, `
			SELECT signing_public_key FROM sesame_sync_devices WHERE id = $1 AND vault_id = $2
		`, pkg.SenderDeviceID, pkg.VaultID).Scan(&approverSigningKey); err != nil {
			return fmt.Errorf("read approving sync device key: %w", err)
		}
		if err := (syncproto.EncryptedKeyPackage{
			VaultID:           pkg.VaultID,
			SenderDeviceID:    pkg.SenderDeviceID,
			RecipientDeviceID: pkg.RecipientDeviceID,
			Ciphertext:        base64.RawURLEncoding.EncodeToString(pkg.Ciphertext),
			Signature:         base64.RawURLEncoding.EncodeToString(pkg.Signature),
			CreatedAt:         time.Now().UTC(),
		}).VerifySignature(base64.RawURLEncoding.EncodeToString(approverSigningKey)); err != nil {
			return ErrApprovalRejected
		}

		result, err := tx.ExecContext(ctx, `
			UPDATE sesame_sync_devices
			SET state = 'approved', approved_at = NOW(),
			    device_epoch = (SELECT vault_epoch FROM sesame_sync_vaults WHERE id = $2)
			WHERE id = $1 AND vault_id = $2 AND state = 'pending'
		`, pkg.RecipientDeviceID, pkg.VaultID)
		if err != nil {
			return fmt.Errorf("approve sync device: %w", err)
		}
		approved, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("approve sync device: %w", err)
		}
		// A revoked device must never be resurrected by re-approval; it has to enroll again.
		if approved != 1 {
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
		`, pkg.VaultID, pkg.RecipientDeviceID, pkg.SenderDeviceID, pkg.Ciphertext, pkg.Signature); err != nil {
			return fmt.Errorf("store sync key package: %w", err)
		}

		row := tx.QueryRowContext(ctx, `
			SELECT id, vault_id, signing_public_key, encryption_public_key, state, device_epoch, label, created_at, approved_at, revoked_at, activated_epoch
			FROM sesame_sync_devices WHERE id = $1 AND vault_id = $2
		`, pkg.RecipientDeviceID, pkg.VaultID)
		if err := scanDevice(row, &device); err != nil {
			return fmt.Errorf("read approved sync device: %w", err)
		}
		return recordAudit(ctx, tx, pkg.VaultID, device.ID, "device_approved")
	})
	if err != nil {
		return Device{}, err
	}
	return device, nil
}

// No rotation here: a device leaving voluntarily already knows the vault key and no server-side action can take that back.
func (s *Store) LeaveVault(ctx context.Context, vaultID, deviceID string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			UPDATE sesame_sync_devices
			SET state = 'revoked', revoked_at = NOW()
			WHERE id = $1 AND vault_id = $2 AND state <> 'revoked'
		`, deviceID, vaultID)
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
		`, vaultID, deviceID); err != nil {
			return fmt.Errorf("delete revoked sync key package: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE sesame_sync_vaults SET vault_epoch = vault_epoch + 1, updated_at = NOW() WHERE id = $1
		`, vaultID); err != nil {
			return fmt.Errorf("advance sync vault epoch: %w", err)
		}
		// No key was rotated, so survivors carry activated_epoch with them; a
		// rekey is the case where they must re-activate.
		if _, err := tx.ExecContext(ctx, `
			UPDATE sesame_sync_devices
			SET device_epoch = (SELECT vault_epoch FROM sesame_sync_vaults WHERE id = $1),
			    activated_epoch = (SELECT vault_epoch FROM sesame_sync_vaults WHERE id = $1)
			WHERE vault_id = $1 AND state = 'approved'
		`, vaultID); err != nil {
			return fmt.Errorf("carry approved sync devices to the new epoch: %w", err)
		}
		if err := recordAudit(ctx, tx, vaultID, deviceID, "device_revoked"); err != nil {
			return err
		}
		return recordAudit(ctx, tx, vaultID, "", "vault_epoch_advanced")
	})
}

func (s *Store) Devices(ctx context.Context, vaultID string) ([]Device, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, vault_id, signing_public_key, encryption_public_key, state, device_epoch, label, created_at, approved_at, revoked_at, activated_epoch
		FROM sesame_sync_devices WHERE vault_id = $1 ORDER BY created_at DESC
	`, vaultID)
	if err != nil {
		return nil, fmt.Errorf("list sync devices: %w", err)
	}
	defer rows.Close()
	devices := make([]Device, 0)
	for rows.Next() {
		var device Device
		if err := scanDevice(rows, &device); err != nil {
			return nil, fmt.Errorf("scan sync device: %w", err)
		}
		devices = append(devices, device)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list sync devices: %w", err)
	}
	return devices, nil
}

func (s *Store) KeyPackageFor(ctx context.Context, vaultID, deviceID string) (KeyPackage, error) {
	var pkg KeyPackage
	row := s.db.QueryRowContext(ctx, `
		SELECT vault_id, sender_device_id, recipient_device_id, ciphertext, signature
		FROM sesame_sync_key_packages WHERE vault_id = $1 AND recipient_device_id = $2
	`, vaultID, deviceID)
	switch err := row.Scan(&pkg.VaultID, &pkg.SenderDeviceID, &pkg.RecipientDeviceID, &pkg.Ciphertext, &pkg.Signature); {
	case errors.Is(err, sql.ErrNoRows):
		return KeyPackage{}, ErrNotFound
	case err != nil:
		return KeyPackage{}, fmt.Errorf("read sync key package: %w", err)
	}
	return pkg, nil
}

type scanner interface{ Scan(dest ...any) error }

func scanDevice(src scanner, device *Device) error {
	var approvedAt, revokedAt sql.NullTime
	if err := src.Scan(
		&device.ID, &device.VaultID, &device.SigningPublicKey, &device.EncryptionPublicKey,
		&device.State, &device.DeviceEpoch, &device.Label, &device.CreatedAt, &approvedAt, &revokedAt,
		&device.ActivatedEpoch,
	); err != nil {
		return err
	}
	if approvedAt.Valid {
		device.ApprovedAt = &approvedAt.Time
	}
	if revokedAt.Valid {
		device.RevokedAt = &revokedAt.Time
	}
	return nil
}

func (s *Store) DeviceForDesktop(ctx context.Context, vaultID, desktopDeviceID string) (Device, error) {
	if desktopDeviceID == "" {
		return Device{}, ErrDesktopBindingRequired
	}
	var device Device
	row := s.db.QueryRowContext(ctx, `
		SELECT id, vault_id, signing_public_key, encryption_public_key, state, device_epoch, label, created_at, approved_at, revoked_at, activated_epoch
		FROM sesame_sync_devices
		WHERE vault_id = $1 AND desktop_device_id = $2
	`, vaultID, desktopDeviceID)
	if err := scanDevice(row, &device); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Device{}, ErrNotFound
		}
		return Device{}, fmt.Errorf("read sync device for desktop: %w", err)
	}
	return device, nil
}
