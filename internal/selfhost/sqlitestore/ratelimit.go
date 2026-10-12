package sqlitestore

import (
	"context"
	"database/sql"
	"strconv"
	"time"

	"usesesame.app/backend/internal/selfhost"
)

const maxRateKeyLength = 256

func itoa(value int64) string {
	return strconv.FormatInt(value, 10)
}

func (s *Store) RateLimit(ctx context.Context, key string, limit int, window time.Duration) (selfhost.RateDecision, error) {
	if key == "" || len(key) > maxRateKeyLength || limit <= 0 || window < time.Second {
		return selfhost.RateDecision{}, selfhost.ErrInvalidInput
	}
	seconds := int64(window / time.Second)
	var decision selfhost.RateDecision
	err := s.write(ctx, func(tx *sql.Tx) error {
		now := s.clock().Unix()
		var attempts, started int64
		err := tx.QueryRowContext(ctx, `
			INSERT INTO rate_limits (key, window_started_at, window_seconds, attempts) VALUES (?1, ?2, ?3, 1)
			ON CONFLICT (key) DO UPDATE SET
				window_started_at = CASE WHEN window_started_at + window_seconds <= ?2 THEN ?2 ELSE window_started_at END,
				attempts = CASE WHEN window_started_at + window_seconds <= ?2 THEN 1 ELSE attempts + 1 END,
				window_seconds = ?3
			RETURNING attempts, window_started_at
		`, key, now, seconds).Scan(&attempts, &started)
		if err != nil {
			return err
		}
		retry := time.Duration(started+seconds-now) * time.Second
		if retry < time.Second {
			retry = time.Second
		}
		remaining := int64(limit) - attempts
		if remaining < 0 {
			remaining = 0
		}
		decision = selfhost.RateDecision{Allowed: attempts <= int64(limit), Remaining: int(remaining), RetryAfter: retry}
		return nil
	})
	return decision, err
}

func (s *Store) ResetRateLimit(ctx context.Context, key string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM rate_limits WHERE key = ?`, key)
		return err
	})
}
