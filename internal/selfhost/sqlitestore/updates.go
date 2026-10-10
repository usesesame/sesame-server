package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"usesesame.app/backend/internal/selfhost"
)

const updatesTarget = "instance"

func (s *Store) UpdateSettings(ctx context.Context) (selfhost.UpdateSettings, error) {
	db, err := s.read()
	if err != nil {
		return selfhost.UpdateSettings{}, err
	}
	return readUpdateSettings(ctx, db)
}

func readUpdateSettings(ctx context.Context, db queryer) (selfhost.UpdateSettings, error) {
	var settings selfhost.UpdateSettings
	var choice, feed string
	var checked sql.NullInt64
	err := db.QueryRowContext(ctx, `SELECT enabled, channel, last_feed, checked_at, last_error FROM update_settings WHERE singleton = 1`).
		Scan(&choice, &settings.Channel, &feed, &checked, &settings.LastError)
	if errors.Is(err, sql.ErrNoRows) {
		return selfhost.UpdateSettings{}, selfhost.ErrNotFound
	}
	if err != nil {
		return selfhost.UpdateSettings{}, err
	}
	settings.Choice = selfhost.UpdateChoice(choice)
	if feed != "" {
		settings.Feed = []byte(feed)
	}
	settings.CheckedAt = optionalTime(checked)
	settings.Sequences, err = readUpdateSequences(ctx, db)
	if err != nil {
		return selfhost.UpdateSettings{}, err
	}
	return settings, nil
}

func readUpdateSequences(ctx context.Context, db queryer) (map[string]int64, error) {
	rows, err := db.QueryContext(ctx, `SELECT key_id, sequence FROM update_sequences`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sequences := map[string]int64{}
	for rows.Next() {
		var keyID string
		var sequence int64
		if err := rows.Scan(&keyID, &sequence); err != nil {
			return nil, err
		}
		sequences[keyID] = sequence
	}
	return sequences, rows.Err()
}

func choiceOf(enabled bool) selfhost.UpdateChoice {
	if enabled {
		return selfhost.UpdatesOn
	}
	return selfhost.UpdatesOff
}

func (s *Store) ConfigureUpdates(ctx context.Context, by selfhost.Actor, change selfhost.UpdateChange) (selfhost.UpdateSettings, error) {
	var result selfhost.UpdateSettings
	err := s.write(ctx, func(tx *sql.Tx) error {
		current, err := readUpdateSettings(ctx, tx)
		if err != nil {
			return err
		}
		detail := map[string]string{}
		if change.Enabled != nil {
			if choice := choiceOf(*change.Enabled); choice != current.Choice {
				current.Choice = choice
				detail["enabled"] = string(choice)
			}
		}
		if change.Channel != nil {
			if !selfhost.ValidUpdateChannel(*change.Channel) {
				return selfhost.ErrInvalidInput
			}
			if *change.Channel != current.Channel {
				current.Channel = *change.Channel
				detail["channel"] = current.Channel
			}
		}
		result = current
		if len(detail) == 0 {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE update_settings SET enabled = ?, channel = ? WHERE singleton = 1`, string(current.Choice), current.Channel); err != nil {
			return err
		}
		return s.audit(ctx, tx, by, "updates.updated", updatesTarget, detail)
	})
	if err != nil {
		return selfhost.UpdateSettings{}, err
	}
	return result, nil
}

func (s *Store) chooseUpdatesAtSetup(ctx context.Context, tx *sql.Tx, by selfhost.Actor, enabled bool) error {
	choice := choiceOf(enabled)
	if _, err := tx.ExecContext(ctx, `UPDATE update_settings SET enabled = ? WHERE singleton = 1`, string(choice)); err != nil {
		return err
	}
	return s.audit(ctx, tx, by, "updates.updated", updatesTarget, map[string]string{"enabled": string(choice), "source": "setup"})
}

func (s *Store) RecordUpdateFeed(ctx context.Context, feed selfhost.UpdateFeed) error {
	if feed.Sequence < 1 || len(feed.Raw) == 0 || feed.KeyID == "" || len(feed.KeyID) > 64 {
		return selfhost.ErrInvalidInput
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			INSERT INTO update_sequences (key_id, sequence) VALUES (?, ?)
			ON CONFLICT (key_id) DO UPDATE SET sequence = excluded.sequence
			WHERE update_sequences.sequence <= excluded.sequence
		`, feed.KeyID, feed.Sequence)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return selfhost.ErrConflict
		}
		_, err = tx.ExecContext(ctx, `UPDATE update_settings SET last_feed = ?, checked_at = ?, last_error = '' WHERE singleton = 1`, string(feed.Raw), feed.CheckedAt.UTC().Unix())
		return err
	})
}

func (s *Store) RecordUpdateFailure(ctx context.Context, code string, at time.Time) error {
	if !selfhost.ValidUpdateError(code) {
		return selfhost.ErrInvalidInput
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		current, err := readUpdateSettings(ctx, tx)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE update_settings SET last_error = ? WHERE singleton = 1`, code); err != nil {
			return err
		}
		if code != selfhost.UpdateErrorRollback || current.LastError == code {
			return nil
		}
		var highest int64
		for _, sequence := range current.Sequences {
			highest = max(highest, sequence)
		}
		return s.audit(ctx, tx, selfhost.SystemActor, "updates.rollback_rejected", updatesTarget, map[string]string{"highestSequence": strconv.FormatInt(highest, 10)})
	})
}

func (s *Store) ResetUpdateSequences(ctx context.Context, by selfhost.Actor) (int, error) {
	var cleared int
	err := s.write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `DELETE FROM update_sequences`)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		cleared = int(count)
		if _, err := tx.ExecContext(ctx, `UPDATE update_settings SET last_error = '' WHERE last_error = ?`, selfhost.UpdateErrorRollback); err != nil {
			return err
		}
		return s.audit(ctx, tx, by, "updates.sequences_reset", updatesTarget, map[string]string{"keys": strconv.Itoa(cleared)})
	})
	return cleared, err
}
