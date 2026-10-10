package sqlitestore

import (
	"context"
	"database/sql"

	"usesesame.app/backend/internal/selfhost"
)

func (s *Store) seedFlags(ctx context.Context, tx *sql.Tx) error {
	now := s.clock().Unix()
	for _, definition := range s.flags {
		enabled := 0
		if definition.Default {
			enabled = 1
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO flags (key, enabled, updated_at) VALUES (?, ?, ?)`, definition.Key, enabled, now); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) definition(key string) (selfhost.FlagDefinition, bool) {
	for _, definition := range s.flags {
		if definition.Key == key {
			return definition, true
		}
	}
	return selfhost.FlagDefinition{}, false
}

func (s *Store) Flags(ctx context.Context) ([]selfhost.Flag, error) {
	db, err := s.read()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT key, enabled, updated_at FROM flags ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	flags := []selfhost.Flag{}
	for rows.Next() {
		var flag selfhost.Flag
		var enabled int
		var updated int64
		if err := rows.Scan(&flag.Key, &enabled, &updated); err != nil {
			return nil, err
		}
		definition, known := s.definition(flag.Key)
		if !known {
			continue
		}
		flag.Description = definition.Description
		flag.Enabled = enabled == 1
		flag.UpdatedAt = unixTime(updated)
		flags = append(flags, flag)
	}
	return flags, rows.Err()
}

func (s *Store) SetFlag(ctx context.Context, by selfhost.Actor, key string, enabled bool) (selfhost.Flag, error) {
	definition, known := s.definition(key)
	if !known {
		return selfhost.Flag{}, selfhost.ErrNotFound
	}
	value := 0
	if enabled {
		value = 1
	}
	now := s.clock()
	err := s.write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			INSERT INTO flags (key, enabled, updated_at) VALUES (?, ?, ?)
			ON CONFLICT (key) DO UPDATE SET enabled = excluded.enabled, updated_at = excluded.updated_at
			WHERE flags.enabled <> excluded.enabled
		`, key, value, now.Unix())
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed == 0 {
			return err
		}
		state := "false"
		if enabled {
			state = "true"
		}
		return s.audit(ctx, tx, by, "flag.updated", key, map[string]string{"enabled": state})
	})
	if err != nil {
		return selfhost.Flag{}, err
	}
	flags, err := s.Flags(ctx)
	if err != nil {
		return selfhost.Flag{}, err
	}
	for _, flag := range flags {
		if flag.Key == definition.Key {
			return flag, nil
		}
	}
	return selfhost.Flag{}, selfhost.ErrNotFound
}
