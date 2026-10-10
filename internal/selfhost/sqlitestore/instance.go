package sqlitestore

import (
	"context"
	"database/sql"
	"errors"

	"usesesame.app/backend/internal/selfhost"
)

const defaultInstanceName = "Sesame"

func (s *Store) ensureInstance(ctx context.Context, tx *sql.Tx, name string) error {
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM instance`).Scan(&exists); err != nil {
		return err
	}
	if exists > 0 {
		return nil
	}
	normalized, ok := selfhost.NormalizeName(name)
	if !ok {
		normalized = defaultInstanceName
	}
	id, err := newID()
	if err != nil {
		return err
	}
	now := s.clock().Unix()
	_, err = tx.ExecContext(ctx, `INSERT INTO instance (singleton, id, name, created_at, updated_at) VALUES (1, ?, ?, ?, ?)`, id, normalized, now, now)
	return err
}

func (s *Store) Instance(ctx context.Context) (selfhost.Instance, error) {
	db, err := s.read()
	if err != nil {
		return selfhost.Instance{}, err
	}
	return readInstance(ctx, db)
}

func readInstance(ctx context.Context, db queryer) (selfhost.Instance, error) {
	var instance selfhost.Instance
	var created, updated int64
	err := db.QueryRowContext(ctx, `SELECT id, name, public_url, created_at, updated_at FROM instance WHERE singleton = 1`).
		Scan(&instance.ID, &instance.Name, &instance.PublicURL, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return selfhost.Instance{}, selfhost.ErrNotFound
	}
	if err != nil {
		return selfhost.Instance{}, err
	}
	instance.CreatedAt = unixTime(created)
	instance.UpdatedAt = unixTime(updated)
	return instance, nil
}

func (s *Store) UpdateInstance(ctx context.Context, by selfhost.Actor, update selfhost.InstanceUpdate) (selfhost.Instance, error) {
	var result selfhost.Instance
	err := s.write(ctx, func(tx *sql.Tx) error {
		current, err := readInstance(ctx, tx)
		if err != nil {
			return err
		}
		detail := map[string]string{}
		if update.Name != nil {
			name, ok := selfhost.NormalizeName(*update.Name)
			if !ok {
				return selfhost.ErrInvalidInput
			}
			if name != current.Name {
				current.Name = name
				detail["name"] = name
			}
		}
		if update.PublicURL != nil {
			publicURL, ok := selfhost.NormalizePublicURL(*update.PublicURL)
			if !ok {
				return selfhost.ErrInvalidInput
			}
			if publicURL != current.PublicURL {
				current.PublicURL = publicURL
				detail["publicUrl"] = publicURL
			}
		}
		if len(detail) == 0 {
			result = current
			return nil
		}
		current.UpdatedAt = s.clock()
		if _, err := tx.ExecContext(ctx, `UPDATE instance SET name = ?, public_url = ?, updated_at = ? WHERE singleton = 1`, current.Name, current.PublicURL, current.UpdatedAt.Unix()); err != nil {
			return err
		}
		if err := s.audit(ctx, tx, by, "instance.updated", current.ID, detail); err != nil {
			return err
		}
		result = current
		return nil
	})
	if err != nil {
		return selfhost.Instance{}, err
	}
	return result, nil
}

func (s *Store) SetupRequired(ctx context.Context) (bool, error) {
	db, err := s.read()
	if err != nil {
		return false, err
	}
	count, err := countActiveOwners(ctx, db, "")
	return count == 0, err
}
