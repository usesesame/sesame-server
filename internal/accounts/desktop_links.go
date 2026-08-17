package accounts

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Adds cancellable, regenerable link requests to the one-time redemption methods in DesktopStore.
type DesktopLinkManager interface {
	CreateOrReplaceDesktopLink(context.Context, string, []byte, time.Time) (DesktopLink, error)
	DesktopLinkStatus(context.Context, string, time.Time) (DesktopLink, error)
	CancelDesktopLink(context.Context, string) error
}

var _ DesktopLinkManager = (*PostgresStore)(nil)

type DesktopLink struct {
	LinkID    string             `json:"linkId,omitempty"`
	State     string             `json:"state"`
	CreatedAt time.Time          `json:"createdAt,omitempty"`
	ExpiresAt time.Time          `json:"expiresAt,omitempty"`
	DeviceID  string             `json:"deviceId,omitempty"`
	Device    *DesktopConnection `json:"device,omitempty"`
}

func (s *PostgresStore) CreateOrReplaceDesktopLink(ctx context.Context, accountID string, codeHash []byte, expiresAt time.Time) (DesktopLink, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DesktopLink{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		UPDATE sesame_desktop_link_codes SET cancelled_at = NOW()
		WHERE account_id = $1 AND used_at IS NULL AND cancelled_at IS NULL AND expires_at > NOW()
	`, accountID); err != nil {
		return DesktopLink{}, err
	}
	id, err := newID()
	if err != nil {
		return DesktopLink{}, err
	}
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO sesame_desktop_link_codes (id, code_hash, account_id, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5)
	`, id, codeHash, accountID, now, expiresAt); err != nil {
		return DesktopLink{}, err
	}
	if err := tx.Commit(); err != nil {
		return DesktopLink{}, err
	}
	return DesktopLink{LinkID: id, State: "pending", CreatedAt: now, ExpiresAt: expiresAt.UTC()}, nil
}

func (s *PostgresStore) DesktopLinkStatus(ctx context.Context, accountID string, now time.Time) (DesktopLink, error) {
	var link DesktopLink
	var usedAt, cancelledAt sql.NullTime
	var connectedDeviceID sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT id, created_at, expires_at, used_at, cancelled_at, connected_device_id
		FROM sesame_desktop_link_codes WHERE account_id = $1
		ORDER BY created_at DESC LIMIT 1
	`, accountID).Scan(&link.LinkID, &link.CreatedAt, &link.ExpiresAt, &usedAt, &cancelledAt, &connectedDeviceID)
	if errors.Is(err, sql.ErrNoRows) {
		return DesktopLink{State: "none"}, nil
	}
	if err != nil {
		return DesktopLink{}, err
	}
	switch {
	case cancelledAt.Valid:
		return DesktopLink{State: "none"}, nil
	case usedAt.Valid && connectedDeviceID.Valid:
		link.State = "connected"
		link.DeviceID = connectedDeviceID.String
		device, err := scanDesktopConnection(s.db.QueryRowContext(ctx, `
			SELECT device_id, device_name, created_at, expires_at,
				app_version, platform, architecture, update_channel,
				last_seen_at, protocol_version, browser_helper_capable, browser_helper_last_observed_at
			FROM sesame_desktop_connections WHERE account_id = $1 AND device_id = $2
		`, accountID, connectedDeviceID.String).Scan, false, false)
		if err == nil {
			link.Device = &device
		} else if !errors.Is(err, sql.ErrNoRows) {
			return DesktopLink{}, err
		}
	case !link.ExpiresAt.After(now):
		link.State = "expired"
	default:
		link.State = "pending"
	}
	return link, nil
}

func (s *PostgresStore) CancelDesktopLink(ctx context.Context, accountID string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE sesame_desktop_link_codes SET cancelled_at = NOW()
		WHERE account_id = $1 AND used_at IS NULL AND cancelled_at IS NULL AND expires_at > NOW()
	`, accountID)
	return err
}
