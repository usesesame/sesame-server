package accounts

import (
	"context"
	"encoding/json"
	"time"
)

type AccountActivityStore interface {
	RecordAccountEvent(context.Context, AccountEvent) error
	AccountEvents(context.Context, string, int) ([]AccountEvent, error)
}

var _ AccountActivityStore = (*PostgresStore)(nil)

type AccountEvent struct {
	ID        string            `json:"id"`
	AccountID string            `json:"-"`
	Type      string            `json:"type"`
	Label     string            `json:"label"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	CreatedAt time.Time         `json:"createdAt"`
	ExpiresAt time.Time         `json:"expiresAt"`
}

func (s *PostgresStore) RecordAccountEvent(ctx context.Context, event AccountEvent) error {
	if event.AccountID == "" || event.Type == "" {
		return nil
	}
	if event.ID == "" {
		id, err := newID()
		if err != nil {
			return err
		}
		event.ID = id
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	if event.ExpiresAt.IsZero() {
		event.ExpiresAt = event.CreatedAt.Add(180 * 24 * time.Hour)
	}
	metadata, err := json.Marshal(event.Metadata)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO sesame_account_events (id, account_id, event_type, label, metadata, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, event.ID, event.AccountID, event.Type, event.Label, metadata, event.CreatedAt, event.ExpiresAt)
	return err
}

func (s *PostgresStore) AccountEvents(ctx context.Context, accountID string, limit int) ([]AccountEvent, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, event_type, label, metadata, created_at, expires_at
		FROM sesame_account_events
		WHERE account_id = $1 AND expires_at > NOW()
		ORDER BY created_at DESC
		LIMIT $2
	`, accountID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]AccountEvent, 0)
	for rows.Next() {
		var event AccountEvent
		var metadata []byte
		if err := rows.Scan(&event.ID, &event.Type, &event.Label, &metadata, &event.CreatedAt, &event.ExpiresAt); err != nil {
			return nil, err
		}
		if len(metadata) > 0 {
			if err := json.Unmarshal(metadata, &event.Metadata); err != nil {
				return nil, err
			}
		}
		events = append(events, event)
	}
	return events, rows.Err()
}
