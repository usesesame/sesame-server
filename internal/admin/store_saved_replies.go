package admin

import (
	"context"
	"database/sql"
	"errors"
)

func scanSavedReply(row interface{ Scan(...any) error }) (SavedReply, error) {
	var reply SavedReply
	var createdBy sql.NullString
	if err := row.Scan(&reply.ID, &reply.Title, &reply.Body, &createdBy, &reply.CreatedAt, &reply.UpdatedAt); err != nil {
		return SavedReply{}, err
	}
	if createdBy.Valid {
		value := createdBy.String
		reply.CreatedByAdminID = &value
	}
	return reply, nil
}

func (s *Store) SavedReplies(ctx context.Context) ([]SavedReply, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, title, body, created_by_admin_id, created_at, updated_at
		FROM sesame_support_saved_replies
		ORDER BY title, id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	replies := []SavedReply{}
	for rows.Next() {
		reply, err := scanSavedReply(rows)
		if err != nil {
			return nil, err
		}
		replies = append(replies, reply)
	}
	return replies, rows.Err()
}

func (s *Store) CreateSavedReply(ctx context.Context, actor Account, title, body, ipHash string) (SavedReply, error) {
	id, err := newID()
	if err != nil {
		return SavedReply{}, err
	}
	var reply SavedReply
	err = s.mutate(ctx, actor, "saved_reply.create", "saved_reply", id, ipHash, map[string]any{
		"titleLength": len(title),
		"bodyLength":  len(body),
	}, func(tx *sql.Tx) error {
		var scanErr error
		reply, scanErr = scanSavedReply(tx.QueryRowContext(ctx, `
			INSERT INTO sesame_support_saved_replies (id, title, body, created_by_admin_id)
			VALUES ($1, $2, $3, $4)
			RETURNING id, title, body, created_by_admin_id, created_at, updated_at
		`, id, title, body, actor.ID))
		return scanErr
	})
	if err != nil {
		return SavedReply{}, err
	}
	return reply, nil
}

func (s *Store) UpdateSavedReply(ctx context.Context, actor Account, replyID, title, body, ipHash string) (SavedReply, error) {
	var reply SavedReply
	err := s.mutate(ctx, actor, "saved_reply.update", "saved_reply", replyID, ipHash, map[string]any{
		"titleLength": len(title),
		"bodyLength":  len(body),
	}, func(tx *sql.Tx) error {
		var scanErr error
		reply, scanErr = scanSavedReply(tx.QueryRowContext(ctx, `
			UPDATE sesame_support_saved_replies
			SET title = $2, body = $3, updated_at = NOW()
			WHERE id = $1
			RETURNING id, title, body, created_by_admin_id, created_at, updated_at
		`, replyID, title, body))
		if errors.Is(scanErr, sql.ErrNoRows) {
			return ErrNotFound
		}
		return scanErr
	})
	if err != nil {
		return SavedReply{}, err
	}
	return reply, nil
}

func (s *Store) DeleteSavedReply(ctx context.Context, actor Account, replyID, ipHash string) error {
	return s.mutate(ctx, actor, "saved_reply.delete", "saved_reply", replyID, ipHash, map[string]any{}, func(tx *sql.Tx) error {
		return affected(tx.ExecContext(ctx, `DELETE FROM sesame_support_saved_replies WHERE id = $1`, replyID))
	})
}
