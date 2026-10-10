package sqlitestore

import (
	"context"
	"database/sql"
	"errors"

	"usesesame.app/backend/internal/authkit"
	"usesesame.app/backend/internal/selfhost"
)

const deviceSelect = `
	SELECT d.id, d.name, d.owner_id, d.member_id, COALESCE(o.name, m.name, ''),
		d.app_version, d.platform, d.architecture, d.update_channel, d.protocol_version,
		d.browser_helper_capable, d.browser_helper_observed_at, d.created_at, d.expires_at, d.last_seen_at, d.revoked_at
	FROM devices d
	LEFT JOIN owners o ON o.id = d.owner_id
	LEFT JOIN members m ON m.id = d.member_id`

const activeDevice = `d.revoked_at IS NULL AND d.expires_at > ?`

func scanDevice(scan func(...any) error) (selfhost.Device, error) {
	var device selfhost.Device
	var ownerID, memberID sql.NullString
	var helperCapable int
	var helperObserved, revoked sql.NullInt64
	var created, expires, lastSeen int64
	if err := scan(&device.ID, &device.Name, &ownerID, &memberID, &device.Holder.Name,
		&device.Meta.AppVersion, &device.Meta.Platform, &device.Meta.Architecture, &device.Meta.UpdateChannel, &device.Meta.ProtocolVersion,
		&helperCapable, &helperObserved, &created, &expires, &lastSeen, &revoked); err != nil {
		return selfhost.Device{}, err
	}
	if ownerID.Valid {
		device.Holder.Kind, device.Holder.ID = selfhost.HolderOwner, ownerID.String
	} else {
		device.Holder.Kind, device.Holder.ID = selfhost.HolderMember, memberID.String
	}
	device.Meta.BrowserHelperCapable = helperCapable == 1
	device.BrowserHelperLastObservedAt = optionalTime(helperObserved)
	device.CreatedAt = unixTime(created)
	device.ExpiresAt = unixTime(expires)
	device.LastSeenAt = unixTime(lastSeen)
	device.RevokedAt = optionalTime(revoked)
	return device, nil
}

func readDevice(ctx context.Context, db queryer, where string, args ...any) (selfhost.Device, error) {
	device, err := scanDevice(db.QueryRowContext(ctx, deviceSelect+` WHERE `+where, args...).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return selfhost.Device{}, selfhost.ErrNotFound
	}
	return device, err
}

func validDeviceToken(token string) bool {
	return token != "" && len(token) <= 256
}

func (s *Store) AuthenticateDevice(ctx context.Context, token string) (selfhost.Device, error) {
	if !validDeviceToken(token) {
		return selfhost.Device{}, selfhost.ErrDeviceInvalid
	}
	db, err := s.read()
	if err != nil {
		return selfhost.Device{}, err
	}
	device, err := readDevice(ctx, db, `d.token_hash = ? AND `+activeDevice, authkit.HashToken(token), s.clock().Unix())
	if errors.Is(err, selfhost.ErrNotFound) {
		return selfhost.Device{}, selfhost.ErrDeviceInvalid
	}
	return device, err
}

func (s *Store) Heartbeat(ctx context.Context, token string, meta selfhost.DeviceMeta) (selfhost.Device, error) {
	if !validDeviceToken(token) {
		return selfhost.Device{}, selfhost.ErrDeviceInvalid
	}
	if meta.ProtocolVersion < 1 || !selfhost.ValidDeviceMeta(meta) {
		return selfhost.Device{}, selfhost.ErrInvalidInput
	}
	var device selfhost.Device
	err := s.write(ctx, func(tx *sql.Tx) error {
		now := s.clock().Unix()
		result, err := tx.ExecContext(ctx, `
			UPDATE devices SET app_version = ?, platform = ?, architecture = ?, update_channel = ?, protocol_version = ?,
				browser_helper_capable = ?,
				browser_helper_observed_at = CASE WHEN ? THEN ? ELSE browser_helper_observed_at END,
				last_seen_at = ?
			WHERE token_hash = ? AND revoked_at IS NULL AND expires_at > ?
		`, meta.AppVersion, meta.Platform, meta.Architecture, meta.UpdateChannel, meta.ProtocolVersion,
			boolInt(meta.BrowserHelperCapable), meta.BrowserHelperObserved, now, now, authkit.HashToken(token), now)
		if err != nil {
			return err
		}
		if changed, err := result.RowsAffected(); err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return selfhost.ErrDeviceInvalid
		}
		device, err = readDevice(ctx, tx, `d.token_hash = ?`, authkit.HashToken(token))
		return err
	})
	return device, err
}

func (s *Store) Disconnect(ctx context.Context, token string) error {
	if !validDeviceToken(token) {
		return selfhost.ErrDeviceInvalid
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		now := s.clock().Unix()
		var id string
		err := tx.QueryRowContext(ctx, `
			UPDATE devices SET revoked_at = ? WHERE token_hash = ? AND revoked_at IS NULL AND expires_at > ? RETURNING id
		`, now, authkit.HashToken(token), now).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return selfhost.ErrDeviceInvalid
		}
		if err != nil {
			return err
		}
		return s.audit(ctx, tx, selfhost.Actor{Kind: selfhost.ActorDevice, ID: id}, "device.disconnected", id, nil)
	})
}

func (s *Store) ListDevices(ctx context.Context, filter selfhost.DeviceFilter) ([]selfhost.Device, error) {
	db, err := s.read()
	if err != nil {
		return nil, err
	}
	query := deviceSelect
	args := []any{}
	if !filter.IncludeInactive {
		query += ` WHERE ` + activeDevice
		args = append(args, s.clock().Unix())
	}
	rows, err := db.QueryContext(ctx, query+` ORDER BY d.created_at DESC, d.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	devices := []selfhost.Device{}
	for rows.Next() {
		device, err := scanDevice(rows.Scan)
		if err != nil {
			return nil, err
		}
		devices = append(devices, device)
	}
	return devices, rows.Err()
}

func (s *Store) RevokeDevice(ctx context.Context, by selfhost.Actor, id string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE devices SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, s.clock().Unix(), id)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed == 1 {
			return s.audit(ctx, tx, by, "device.revoked", id, nil)
		}
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM devices WHERE id = ?`, id).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return selfhost.ErrNotFound
		}
		return nil
	})
}

func (s *Store) RevokeMemberDevices(ctx context.Context, by selfhost.Actor, memberID string) (int, error) {
	revoked := 0
	err := s.write(ctx, func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM members WHERE id = ? AND removed_at IS NULL`, memberID).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return selfhost.ErrNotFound
		}
		count, err := revokeDevicesWhere(ctx, tx, s.clock().Unix(), "member_id", memberID)
		if err != nil {
			return err
		}
		revoked = int(count)
		return s.audit(ctx, tx, by, "member.devices_revoked", memberID, map[string]string{"count": itoa(count)})
	})
	return revoked, err
}
