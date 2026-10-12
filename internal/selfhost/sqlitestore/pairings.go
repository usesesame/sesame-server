package sqlitestore

import (
	"context"
	"database/sql"
	"errors"

	"usesesame.app/backend/internal/authkit"
	"usesesame.app/backend/internal/selfhost"
)

func holderColumns(holder selfhost.Holder) (ownerID, memberID any, column string, ok bool) {
	switch holder.Kind {
	case selfhost.HolderOwner:
		return holder.ID, nil, "owner_id", holder.ID != ""
	case selfhost.HolderMember:
		return nil, holder.ID, "member_id", holder.ID != ""
	}
	return nil, nil, "", false
}

func resolveHolder(ctx context.Context, tx *sql.Tx, holder selfhost.Holder) (selfhost.Holder, error) {
	var query string
	switch holder.Kind {
	case selfhost.HolderOwner:
		query = `SELECT name FROM owners WHERE id = ? AND removed_at IS NULL AND password_hash IS NOT NULL`
	case selfhost.HolderMember:
		query = `SELECT name FROM members WHERE id = ? AND removed_at IS NULL`
	default:
		return selfhost.Holder{}, selfhost.ErrInvalidInput
	}
	err := tx.QueryRowContext(ctx, query, holder.ID).Scan(&holder.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return selfhost.Holder{}, selfhost.ErrNotFound
	}
	return holder, err
}

func (s *Store) CreatePairing(ctx context.Context, by selfhost.Actor, input selfhost.PairingInput) (selfhost.IssuedPairing, error) {
	ownerID, memberID, column, ok := holderColumns(input.Holder)
	if !ok {
		return selfhost.IssuedPairing{}, selfhost.ErrInvalidInput
	}
	hint := ""
	if input.DeviceNameHint != "" {
		hint, ok = selfhost.NormalizeDeviceName(input.DeviceNameHint)
		if !ok {
			return selfhost.IssuedPairing{}, selfhost.ErrInvalidInput
		}
	}
	code, codeHash, err := authkit.NewToken()
	if err != nil {
		return selfhost.IssuedPairing{}, err
	}
	var issued selfhost.IssuedPairing
	err = s.write(ctx, func(tx *sql.Tx) error {
		holder, err := resolveHolder(ctx, tx, input.Holder)
		if err != nil {
			return err
		}
		id, err := newID()
		if err != nil {
			return err
		}
		now := s.clock()
		if err := cancelPairingsWhere(ctx, tx, now.Unix(), column, holder.ID); err != nil {
			return err
		}
		expires := now.Add(selfhost.PairingTTL)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO pairings (id, code_hash, owner_id, member_id, device_name_hint, created_by, created_at, expires_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`, id, codeHash, ownerID, memberID, hint, by.String(), now.Unix(), expires.Unix()); err != nil {
			return err
		}
		issued = selfhost.IssuedPairing{
			Code:    code,
			Pairing: selfhost.Pairing{ID: id, Holder: holder, DeviceNameHint: hint, CreatedBy: by.String(), CreatedAt: now, ExpiresAt: expires},
		}
		return s.audit(ctx, tx, by, "pairing.created", id, map[string]string{"holderKind": string(holder.Kind), "holderId": holder.ID})
	})
	return issued, err
}

func (s *Store) ListPairings(ctx context.Context) ([]selfhost.Pairing, error) {
	db, err := s.read()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT p.id, p.owner_id, p.member_id, COALESCE(o.name, m.name), p.device_name_hint, p.created_by, p.created_at, p.expires_at
		FROM pairings p
		LEFT JOIN owners o ON o.id = p.owner_id
		LEFT JOIN members m ON m.id = p.member_id
		WHERE p.used_at IS NULL AND p.cancelled_at IS NULL AND p.expires_at > ?
		ORDER BY p.created_at DESC, p.id
	`, s.clock().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	pairings := []selfhost.Pairing{}
	for rows.Next() {
		var pairing selfhost.Pairing
		var ownerID, memberID sql.NullString
		var created, expires int64
		if err := rows.Scan(&pairing.ID, &ownerID, &memberID, &pairing.Holder.Name, &pairing.DeviceNameHint, &pairing.CreatedBy, &created, &expires); err != nil {
			return nil, err
		}
		if ownerID.Valid {
			pairing.Holder.Kind, pairing.Holder.ID = selfhost.HolderOwner, ownerID.String
		} else {
			pairing.Holder.Kind, pairing.Holder.ID = selfhost.HolderMember, memberID.String
		}
		pairing.CreatedAt = unixTime(created)
		pairing.ExpiresAt = unixTime(expires)
		pairings = append(pairings, pairing)
	}
	return pairings, rows.Err()
}

func (s *Store) CancelPairing(ctx context.Context, by selfhost.Actor, id string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		now := s.clock().Unix()
		result, err := tx.ExecContext(ctx, `
			UPDATE pairings SET cancelled_at = ?
			WHERE id = ? AND used_at IS NULL AND cancelled_at IS NULL AND expires_at > ?
		`, now, id, now)
		if err != nil {
			return err
		}
		if changed, err := result.RowsAffected(); err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return selfhost.ErrNotFound
		}
		return s.audit(ctx, tx, by, "pairing.cancelled", id, nil)
	})
}

func (s *Store) RedeemPairing(ctx context.Context, input selfhost.RedeemInput) (selfhost.IssuedDevice, error) {
	if !selfhost.ValidPairingCode(input.Code) {
		return selfhost.IssuedDevice{}, selfhost.ErrPairingInvalid
	}
	name, ok := selfhost.NormalizeDeviceName(input.DeviceName)
	if !ok || !selfhost.ValidDeviceMeta(input.Meta) {
		return selfhost.IssuedDevice{}, selfhost.ErrInvalidInput
	}
	meta := input.Meta
	if meta.ProtocolVersion == 0 {
		meta.ProtocolVersion = 1
	}
	token, tokenHash, err := authkit.NewToken()
	if err != nil {
		return selfhost.IssuedDevice{}, err
	}
	var issued selfhost.IssuedDevice
	err = s.write(ctx, func(tx *sql.Tx) error {
		now := s.clock()
		var pairingID string
		var ownerID, memberID sql.NullString
		err := tx.QueryRowContext(ctx, `
			UPDATE pairings SET used_at = ?
			WHERE code_hash = ? AND used_at IS NULL AND cancelled_at IS NULL AND expires_at > ?
			RETURNING id, owner_id, member_id
		`, now.Unix(), authkit.HashToken(input.Code), now.Unix()).Scan(&pairingID, &ownerID, &memberID)
		if errors.Is(err, sql.ErrNoRows) {
			return selfhost.ErrPairingInvalid
		}
		if err != nil {
			return err
		}
		deviceID, err := newID()
		if err != nil {
			return err
		}
		expires := now.Add(selfhost.DeviceTokenTTL)
		var owner, member any
		holder := selfhost.Holder{Kind: selfhost.HolderMember, ID: memberID.String}
		if ownerID.Valid {
			owner = ownerID.String
			holder = selfhost.Holder{Kind: selfhost.HolderOwner, ID: ownerID.String}
		} else {
			member = memberID.String
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO devices (id, token_hash, name, owner_id, member_id, app_version, platform, architecture, update_channel,
				protocol_version, browser_helper_capable, created_at, expires_at, last_seen_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, deviceID, tokenHash, name, owner, member, meta.AppVersion, meta.Platform, meta.Architecture, meta.UpdateChannel,
			meta.ProtocolVersion, boolInt(meta.BrowserHelperCapable), now.Unix(), expires.Unix(), now.Unix()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE pairings SET device_id = ? WHERE id = ?`, deviceID, pairingID); err != nil {
			return err
		}
		device, err := readDevice(ctx, tx, `d.id = ?`, deviceID)
		if err != nil {
			return err
		}
		issued = selfhost.IssuedDevice{Device: device, Token: token}
		return s.audit(ctx, tx, selfhost.Actor{Kind: selfhost.ActorDevice, ID: deviceID}, "device.paired", deviceID,
			map[string]string{"holderKind": string(holder.Kind), "holderId": holder.ID, "name": name, "pairingId": pairingID})
	})
	return issued, err
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
