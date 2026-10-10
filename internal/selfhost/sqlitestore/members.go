package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"usesesame.app/backend/internal/selfhost"
)

func (s *Store) CreateMember(ctx context.Context, by selfhost.Actor, name string) (selfhost.Member, error) {
	normalized, ok := selfhost.NormalizeName(name)
	if !ok {
		return selfhost.Member{}, selfhost.ErrInvalidInput
	}
	var member selfhost.Member
	err := s.write(ctx, func(tx *sql.Tx) error {
		id, err := newID()
		if err != nil {
			return err
		}
		now := s.clock()
		if _, err := tx.ExecContext(ctx, `INSERT INTO members (id, name, name_key, created_at) VALUES (?, ?, ?, ?)`,
			id, normalized, selfhost.NameKey(normalized), now.Unix()); err != nil {
			if isUnique(err) {
				return selfhost.ErrConflict
			}
			return err
		}
		member = selfhost.Member{ID: id, Name: normalized, CreatedAt: now}
		return s.audit(ctx, tx, by, "member.created", id, map[string]string{"name": normalized})
	})
	return member, err
}

func (s *Store) ListMembers(ctx context.Context) ([]selfhost.Member, error) {
	db, err := s.read()
	if err != nil {
		return nil, err
	}
	now := s.clock().Unix()
	rows, err := db.QueryContext(ctx, `
		SELECT m.id, m.name, m.created_at,
			(SELECT COUNT(*) FROM devices d WHERE d.member_id = m.id AND d.revoked_at IS NULL AND d.expires_at > ?)
		FROM members m WHERE m.removed_at IS NULL ORDER BY m.created_at, m.name_key
	`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	members := []selfhost.Member{}
	for rows.Next() {
		var member selfhost.Member
		var created int64
		if err := rows.Scan(&member.ID, &member.Name, &created, &member.DeviceCount); err != nil {
			return nil, err
		}
		member.CreatedAt = unixTime(created)
		members = append(members, member)
	}
	return members, rows.Err()
}

func (s *Store) RenameMember(ctx context.Context, by selfhost.Actor, id, name string) (selfhost.Member, error) {
	normalized, ok := selfhost.NormalizeName(name)
	if !ok {
		return selfhost.Member{}, selfhost.ErrInvalidInput
	}
	err := s.write(ctx, func(tx *sql.Tx) error {
		var previous string
		err := tx.QueryRowContext(ctx, `SELECT name FROM members WHERE id = ? AND removed_at IS NULL`, id).Scan(&previous)
		if errors.Is(err, sql.ErrNoRows) {
			return selfhost.ErrNotFound
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE members SET name = ?, name_key = ? WHERE id = ?`, normalized, selfhost.NameKey(normalized), id); err != nil {
			if isUnique(err) {
				return selfhost.ErrConflict
			}
			return err
		}
		return s.audit(ctx, tx, by, "member.renamed", id, map[string]string{"from": previous, "to": normalized})
	})
	if err != nil {
		return selfhost.Member{}, err
	}
	members, err := s.ListMembers(ctx)
	if err != nil {
		return selfhost.Member{}, err
	}
	for _, member := range members {
		if member.ID == id {
			return member, nil
		}
	}
	return selfhost.Member{}, selfhost.ErrNotFound
}

func (s *Store) DeleteMember(ctx context.Context, by selfhost.Actor, id string) (int, error) {
	revoked := 0
	err := s.write(ctx, func(tx *sql.Tx) error {
		now := s.clock().Unix()
		result, err := tx.ExecContext(ctx, `UPDATE members SET removed_at = ? WHERE id = ? AND removed_at IS NULL`, now, id)
		if err != nil {
			return err
		}
		if changed, err := result.RowsAffected(); err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return selfhost.ErrNotFound
		}
		count, err := revokeDevicesWhere(ctx, tx, now, "member_id", id)
		if err != nil {
			return err
		}
		revoked = int(count)
		if err := cancelPairingsWhere(ctx, tx, now, "member_id", id); err != nil {
			return err
		}
		return s.audit(ctx, tx, by, "member.deleted", id, map[string]string{"revokedDevices": fmt.Sprint(count)})
	})
	if err != nil {
		return 0, err
	}
	return revoked, nil
}

func revokeDevicesWhere(ctx context.Context, tx *sql.Tx, now int64, column, id string) (int64, error) {
	result, err := tx.ExecContext(ctx, `UPDATE devices SET revoked_at = ? WHERE `+column+` = ? AND revoked_at IS NULL`, now, id)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func cancelPairingsWhere(ctx context.Context, tx *sql.Tx, now int64, column, id string) error {
	_, err := tx.ExecContext(ctx, `UPDATE pairings SET cancelled_at = ? WHERE `+column+` = ? AND used_at IS NULL AND cancelled_at IS NULL`, now, id)
	return err
}
