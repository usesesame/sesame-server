package accounts

import (
	"context"
	"database/sql"
	"time"

	"usesesame.app/backend/internal/support"
)

func purgeSupportHistory(ctx context.Context, db *sql.DB) error {
	now := time.Now().UTC()
	retireBefore := now.Add(-support.RetentionAfter)
	if _, err := db.ExecContext(ctx, `
		DELETE FROM sesame_email_outbox
		WHERE support_message_id IN (
			SELECT message.id
			FROM sesame_support_messages message
			JOIN sesame_support_requests request ON request.id = message.ticket_id
			WHERE request.status = 'closed' AND request.closed_at IS NOT NULL AND request.closed_at <= $1
		)
	`, retireBefore); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `
		DELETE FROM sesame_support_requests
		WHERE status = 'closed' AND closed_at IS NOT NULL AND closed_at <= $1
	`, retireBefore); err != nil {
		return err
	}
	return autoCloseWaitingSupportRequests(ctx, db, now)
}

func autoCloseWaitingSupportRequests(ctx context.Context, db *sql.DB, now time.Time) error {
	staleBefore := now.Add(-support.AutoCloseAfter)
	ids, err := waitingSupportTicketIDs(ctx, db, staleBefore)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids {
		if _, err := support.CloseAutomatically(ctx, tx, id, staleBefore, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func waitingSupportTicketIDs(ctx context.Context, db *sql.DB, staleBefore time.Time) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id FROM sesame_support_requests
		WHERE status = 'waiting' AND updated_at <= $1
	`, staleBefore)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
