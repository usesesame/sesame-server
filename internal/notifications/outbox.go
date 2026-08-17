package notifications

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"usesesame.app/backend/internal/httpapi"
)

// Durable account-action email queue; the only component that mutates sesame_email_outbox.
type Outbox interface {
	Ping(ctx context.Context) error
	Enqueue(ctx context.Context, message httpapi.AccountEmail) (string, error)
	// Atomically leases due messages so concurrent workers cannot deliver the same row.
	Poll(ctx context.Context, limit int) ([]OutboxItem, error)
	MarkDelivered(ctx context.Context, id string) error
	MarkFailed(ctx context.Context, id string, attemptErr error) error
	PurgeDeliveredOlderThan(ctx context.Context, age time.Duration) (int64, error)
	PurgeFailedOlderThan(ctx context.Context, age time.Duration) (int64, error)
}

type OutboxItem struct {
	ID        string
	Kind      string
	To        string
	ActionURL string
	Subject   string
	Body      string
	ExpiresAt time.Time
	Attempts  int
	CreatedAt time.Time
	UpdatedAt time.Time
}

type PostgresOutbox struct {
	db               *sql.DB
	maxRetryInterval time.Duration
	leaseDuration    time.Duration
	maxAttempts      int
}

func NewPostgresOutbox(db *sql.DB) *PostgresOutbox {
	if db == nil {
		panic("outbox requires a non-nil *sql.DB")
	}
	return &PostgresOutbox{
		db:               db,
		maxRetryInterval: 24 * time.Hour,
		leaseDuration:    5 * time.Minute,
		maxAttempts:      8,
	}
}

func (o *PostgresOutbox) Ping(ctx context.Context) error {
	var available bool
	if err := o.db.QueryRowContext(ctx, `SELECT to_regclass('sesame_email_outbox') IS NOT NULL`).Scan(&available); err != nil {
		return fmt.Errorf("email outbox table is not available: %w", err)
	}
	if !available {
		return errors.New("email outbox table is not available")
	}
	return nil
}

func (o *PostgresOutbox) Enqueue(ctx context.Context, message httpapi.AccountEmail) (string, error) {
	var id string
	err := o.db.QueryRowContext(ctx, `
		INSERT INTO sesame_email_outbox (kind, to_email, action_url, expires_at, subject, body, support_message_id, status, next_attempt_at)
		VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, ''), 'pending', now())
		RETURNING id`,
		message.Kind, message.To, message.ActionURL, message.ExpiresAt.UTC(), message.Subject, message.Body, message.SupportMessageID,
	).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("enqueue email outbox message: %w", err)
	}
	return id, nil
}

func (o *PostgresOutbox) Poll(ctx context.Context, limit int) ([]OutboxItem, error) {
	if limit <= 0 {
		limit = 10
	}
	if _, err := o.db.ExecContext(ctx, `
		UPDATE sesame_email_outbox
		SET status = 'failed', error_message = 'message_expired', lease_until = NULL, updated_at = now()
		WHERE status IN ('pending', 'processing') AND expires_at <= now()`); err != nil {
		return nil, fmt.Errorf("expire email outbox messages: %w", err)
	}
	rows, err := o.db.QueryContext(ctx, `
		WITH candidates AS (
			SELECT id
			FROM sesame_email_outbox
			WHERE expires_at > now()
			  AND (
				(status = 'pending' AND next_attempt_at <= now())
				OR (status = 'processing' AND lease_until <= now())
			  )
			ORDER BY next_attempt_at ASC, created_at ASC
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE sesame_email_outbox AS message
		SET status = 'processing',
		    lease_until = now() + make_interval(secs => $2),
		    updated_at = now()
		FROM candidates
		WHERE message.id = candidates.id
		RETURNING message.id, message.kind, message.to_email, message.action_url, message.subject, message.body,
		          message.expires_at, message.attempts, message.created_at, message.updated_at`,
		limit, o.leaseDuration.Seconds())
	if err != nil {
		return nil, fmt.Errorf("poll email outbox: %w", err)
	}
	defer rows.Close()

	var items []OutboxItem
	for rows.Next() {
		var item OutboxItem
		if err := rows.Scan(&item.ID, &item.Kind, &item.To, &item.ActionURL, &item.Subject, &item.Body, &item.ExpiresAt, &item.Attempts, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan email outbox row: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate email outbox rows: %w", err)
	}
	return items, nil
}

func (o *PostgresOutbox) MarkDelivered(ctx context.Context, id string) error {
	res, err := o.db.ExecContext(ctx, `
		UPDATE sesame_email_outbox
		SET status = 'delivered', attempts = attempts + 1, error_message = NULL,
		    lease_until = NULL, updated_at = now()
		WHERE id = $1 AND status = 'processing'`, id)
	if err != nil {
		return fmt.Errorf("mark outbox message delivered: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("outbox message not found")
	}
	return nil
}

func (o *PostgresOutbox) MarkFailed(ctx context.Context, id string, attemptErr error) error {
	errorMessage := ""
	if attemptErr != nil {
		errorMessage = attemptErr.Error()
		if len(errorMessage) > 512 {
			errorMessage = errorMessage[:512]
		}
	}
	res, err := o.db.ExecContext(ctx, `
		UPDATE sesame_email_outbox
		SET status = CASE
		        WHEN attempts + 1 >= $3 OR expires_at <= now() THEN 'failed'
		        ELSE 'pending'
		    END,
		    attempts = attempts + 1,
		    error_message = $2,
		    next_attempt_at = now() + LEAST(
		        power(2, attempts) * interval '1 minute',
		        make_interval(secs => $4)
		    ),
		    lease_until = NULL,
		    updated_at = now()
		WHERE id = $1 AND status = 'processing'`,
		id, errorMessage, o.maxAttempts, o.maxRetryInterval.Seconds())
	if err != nil {
		return fmt.Errorf("mark outbox message failed: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("outbox message not found")
	}
	return nil
}

func (o *PostgresOutbox) PurgeDeliveredOlderThan(ctx context.Context, age time.Duration) (int64, error) {
	res, err := o.db.ExecContext(ctx, `
		DELETE FROM sesame_email_outbox
		WHERE status = 'delivered' AND updated_at < now() - $1::interval`, age.String())
	if err != nil {
		return 0, fmt.Errorf("purge delivered outbox messages: %w", err)
	}
	return res.RowsAffected()
}

func (o *PostgresOutbox) PurgeFailedOlderThan(ctx context.Context, age time.Duration) (int64, error) {
	res, err := o.db.ExecContext(ctx, `
		DELETE FROM sesame_email_outbox
		WHERE status = 'failed' AND updated_at < now() - $1::interval`, age.String())
	if err != nil {
		return 0, fmt.Errorf("purge failed outbox messages: %w", err)
	}
	return res.RowsAffected()
}

type OutboxEmailSender struct {
	outbox Outbox
}

func NewOutboxEmailSender(outbox Outbox) *OutboxEmailSender {
	return &OutboxEmailSender{outbox: outbox}
}

func (s *OutboxEmailSender) SendAccountEmail(ctx context.Context, message httpapi.AccountEmail) error {
	_, err := s.outbox.Enqueue(ctx, message)
	return err
}
