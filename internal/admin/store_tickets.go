package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"usesesame.app/backend/internal/support"
)

func (s *Store) Tickets(ctx context.Context, filter TicketListFilter, page, size int) ([]TicketSummary, int, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 25
	}
	conditions := []string{"TRUE"}
	args := make([]any, 0, 5)
	if filter.Status != "" {
		args = append(args, filter.Status)
		conditions = append(conditions, fmt.Sprintf("t.status = $%d", len(args)))
	}
	if filter.Priority != "" {
		args = append(args, filter.Priority)
		conditions = append(conditions, fmt.Sprintf("t.priority = $%d", len(args)))
	}
	if filter.Category != "" {
		args = append(args, filter.Category)
		conditions = append(conditions, fmt.Sprintf("t.category = $%d", len(args)))
	}
	if filter.Assigned == "unassigned" {
		conditions = append(conditions, "t.assigned_admin_id IS NULL")
	} else if filter.Assigned != "" {
		args = append(args, filter.Assigned)
		conditions = append(conditions, fmt.Sprintf("t.assigned_admin_id = $%d", len(args)))
	}
	if filter.Query != "" {
		args = append(args, "%"+filter.Query+"%")
		conditions = append(conditions, fmt.Sprintf("(t.email ILIKE $%d OR t.subject ILIKE $%d)", len(args), len(args)))
	}
	where := strings.Join(conditions, " AND ")
	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sesame_support_requests t WHERE "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, size, (page-1)*size)
	query := fmt.Sprintf(`
		SELECT t.id, t.email, t.subject, t.status, t.priority, t.category, t.app_version, t.diagnostic_code, t.browser_integration, t.request_id,
		       t.assigned_admin_id, t.created_at, t.updated_at, t.first_response_at, t.closed_at,
		       (SELECT COUNT(*) FROM sesame_support_messages m WHERE m.ticket_id = t.id)
		FROM sesame_support_requests t
		WHERE %s
		ORDER BY
		  CASE t.priority WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'normal' THEN 2 WHEN 'low' THEN 3 END,
		  t.updated_at DESC
		LIMIT $%d OFFSET $%d
	`, where, len(args)-1, len(args))
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	tickets := []TicketSummary{}
	for rows.Next() {
		var t TicketSummary
		var assigned sql.NullString
		var firstResponse, closedAt sql.NullTime
		if err := rows.Scan(&t.ID, &t.Email, &t.Subject, &t.Status, &t.Priority, &t.Category, &t.AppVersion, &t.DiagnosticCode, &t.BrowserIntegration, &t.RequestID, &assigned, &t.CreatedAt, &t.UpdatedAt, &firstResponse, &closedAt, &t.MessageCount); err != nil {
			return nil, 0, err
		}
		if assigned.Valid {
			id := assigned.String
			t.AssignedAdminID = &id
		}
		if firstResponse.Valid {
			fr := firstResponse.Time
			t.FirstResponseAt = &fr
		}
		if closedAt.Valid {
			cl := closedAt.Time
			t.ClosedAt = &cl
		}
		t.SLADueAt = t.CreatedAt.Add(24 * time.Hour)
		t.SLABreached = t.FirstResponseAt == nil && time.Now().UTC().After(t.SLADueAt) && t.Status != TicketClosed
		if t.Status != TicketClosed {
			t.QueuePosition = len(tickets) + 1
		}
		tickets = append(tickets, t)
	}
	return tickets, total, rows.Err()
}

func (s *Store) Ticket(ctx context.Context, ticketID string) (TicketDetail, error) {
	var d TicketDetail
	var accountID, assigned sql.NullString
	var firstResponse, closedAt sql.NullTime
	err := s.db.QueryRowContext(ctx, `
		SELECT id, email, subject, status, priority, category, app_version, diagnostic_code, browser_integration, request_id,
		       account_id, assigned_admin_id, created_at, updated_at, first_response_at, closed_at
		FROM sesame_support_requests WHERE id = $1
	`, ticketID).Scan(&d.ID, &d.Email, &d.Subject, &d.Status, &d.Priority, &d.Category, &d.AppVersion, &d.DiagnosticCode, &d.BrowserIntegration, &d.RequestID, &accountID, &assigned, &d.CreatedAt, &d.UpdatedAt, &firstResponse, &closedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return TicketDetail{}, ErrNotFound
	}
	if err != nil {
		return TicketDetail{}, err
	}
	if accountID.Valid {
		d.AccountID = accountID.String
		devices, err := s.db.QueryContext(ctx, `SELECT device_id, device_name, created_at, expires_at FROM sesame_desktop_connections WHERE account_id = $1 AND expires_at > NOW() ORDER BY created_at DESC`, d.AccountID)
		if err != nil {
			return TicketDetail{}, err
		}
		defer devices.Close()
		d.LinkedDevices = []Device{}
		for devices.Next() {
			var device Device
			if err := devices.Scan(&device.ID, &device.Name, &device.ConnectedAt, &device.ExpiresAt); err != nil {
				return TicketDetail{}, err
			}
			d.LinkedDevices = append(d.LinkedDevices, device)
		}
		if err := devices.Err(); err != nil {
			return TicketDetail{}, err
		}
	}
	if assigned.Valid {
		id := assigned.String
		d.AssignedAdminID = &id
	}
	if firstResponse.Valid {
		fr := firstResponse.Time
		d.FirstResponseAt = &fr
	}
	if closedAt.Valid {
		cl := closedAt.Time
		d.ClosedAt = &cl
	}
	d.SLADueAt = d.CreatedAt.Add(24 * time.Hour)
	d.SLABreached = d.FirstResponseAt == nil && time.Now().UTC().After(d.SLADueAt) && d.Status != TicketClosed
	if d.Status != TicketClosed {
		if err := s.db.QueryRowContext(ctx, `
			SELECT COUNT(*) + 1 FROM sesame_support_requests queue
			WHERE queue.status IN ('open', 'in_progress', 'waiting')
			  AND (CASE queue.priority WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'normal' THEN 2 ELSE 3 END,
			       queue.updated_at, queue.id)
		      < (CASE $2 WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'normal' THEN 2 ELSE 3 END,
			         $3, $1)
		`, d.ID, d.Priority, d.UpdatedAt).Scan(&d.QueuePosition); err != nil {
			return TicketDetail{}, err
		}
	}

	msgRows, err := s.db.QueryContext(ctx, `
		SELECT message.id, message.author_role, message.admin_email, message.body, message.sent_via_email, message.created_at,
			COALESCE(email.status, ''), COALESCE(email.attempts, 0), email.next_attempt_at
		FROM sesame_support_messages message
		LEFT JOIN LATERAL (
			SELECT status, attempts, next_attempt_at FROM sesame_email_outbox
			WHERE support_message_id = message.id ORDER BY created_at DESC LIMIT 1
		) email ON TRUE
		WHERE message.ticket_id = $1 ORDER BY message.created_at
	`, ticketID)
	if err != nil {
		return TicketDetail{}, err
	}
	defer msgRows.Close()
	d.Messages = []TicketMessage{}
	for msgRows.Next() {
		var m TicketMessage
		var emailStatus sql.NullString
		var nextAttempt sql.NullTime
		if err := msgRows.Scan(&m.ID, &m.AuthorRole, &m.AdminEmail, &m.Body, &m.SentViaEmail, &m.CreatedAt, &emailStatus, &m.EmailAttempts, &nextAttempt); err != nil {
			return TicketDetail{}, err
		}
		if emailStatus.Valid && emailStatus.String != "" {
			m.EmailDeliveryStatus = emailStatus.String
		}
		if nextAttempt.Valid {
			value := nextAttempt.Time
			m.EmailNextAttemptAt = &value
		}
		d.Messages = append(d.Messages, m)
	}
	if err := msgRows.Err(); err != nil {
		return TicketDetail{}, err
	}

	noteRows, err := s.db.QueryContext(ctx, `
		SELECT id, admin_email, body, created_at
		FROM sesame_support_notes WHERE ticket_id = $1 ORDER BY created_at
	`, ticketID)
	if err != nil {
		return TicketDetail{}, err
	}
	defer noteRows.Close()
	d.Notes = []TicketNote{}
	for noteRows.Next() {
		var n TicketNote
		if err := noteRows.Scan(&n.ID, &n.AdminEmail, &n.Body, &n.CreatedAt); err != nil {
			return TicketDetail{}, err
		}
		d.Notes = append(d.Notes, n)
	}
	return d, noteRows.Err()
}

func (s *Store) ReplyTicket(ctx context.Context, actor Account, ticketID, body string, sendEmail bool, ipHash string) (TicketDetail, error) {
	msgID, err := newID()
	if err != nil {
		return TicketDetail{}, err
	}
	err = s.mutate(ctx, actor, "ticket.reply", "ticket", ticketID, ipHash, map[string]any{
		"sentViaEmail": sendEmail,
		"bodyLength":   len(body),
	}, func(tx *sql.Tx) error {
		var status string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM sesame_support_requests WHERE id = $1 FOR UPDATE`, ticketID).Scan(&status); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if status == string(TicketClosed) {
			return errors.New("this ticket is closed; reopen it before replying")
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO sesame_support_messages (id, ticket_id, author_role, admin_id, admin_email, body, sent_via_email)
			VALUES ($1, $2, 'staff', $3, $4, $5, $6)
		`, msgID, ticketID, actor.ID, actor.Email, body, sendEmail); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE sesame_support_requests
			SET status = 'waiting', updated_at = NOW(),
			    first_response_at = COALESCE(first_response_at, NOW())
			WHERE id = $1
		`, ticketID); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return TicketDetail{}, err
	}
	return s.Ticket(ctx, ticketID)
}

func (s *Store) AddTicketNote(ctx context.Context, actor Account, ticketID, body string, ipHash string) (TicketNote, error) {
	noteID, err := newID()
	if err != nil {
		return TicketNote{}, err
	}
	var note TicketNote
	err = s.mutate(ctx, actor, "ticket.note", "ticket", ticketID, ipHash, map[string]any{
		"bodyLength": len(body),
	}, func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sesame_support_requests WHERE id = $1`, ticketID).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return ErrNotFound
		}
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO sesame_support_notes (id, ticket_id, admin_id, admin_email, body)
			VALUES ($1, $2, $3, $4, $5)
			RETURNING id, admin_email, body, created_at
		`, noteID, ticketID, actor.ID, actor.Email, body).Scan(&note.ID, &note.AdminEmail, &note.Body, &note.CreatedAt); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return TicketNote{}, err
	}
	return note, nil
}

func (s *Store) AssignTicket(ctx context.Context, actor Account, ticketID, adminID string, ipHash string) error {
	return s.mutate(ctx, actor, "ticket.assign", "ticket", ticketID, ipHash, map[string]any{
		"to": adminID,
	}, func(tx *sql.Tx) error {
		if adminID == "" {
			if _, err := tx.ExecContext(ctx, `UPDATE sesame_support_requests SET assigned_admin_id = NULL, updated_at = NOW() WHERE id = $1`, ticketID); err != nil {
				return err
			}
			return nil
		}
		var role string
		if err := tx.QueryRowContext(ctx, `SELECT role FROM sesame_admin_accounts WHERE id = $1 AND suspended = FALSE`, adminID).Scan(&role); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return errors.New("the selected admin is not available")
			}
			return err
		}
		if role != string(RoleSuper) && role != string(RoleSupport) {
			return ErrNotAllowed
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE sesame_support_requests
			SET assigned_admin_id = $2, updated_at = NOW(),
			    status = CASE WHEN status = 'open' THEN 'in_progress' ELSE status END
			WHERE id = $1
		`, ticketID, adminID); err != nil {
			return err
		}
		return nil
	})
}

func (s *Store) SetTicketStatus(ctx context.Context, actor Account, ticketID string, status TicketStatus, ipHash string) error {
	return s.mutate(ctx, actor, "ticket.status", "ticket", ticketID, ipHash, map[string]any{
		"to": string(status),
	}, func(tx *sql.Tx) error {
		var oldStatus string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM sesame_support_requests WHERE id = $1 FOR UPDATE`, ticketID).Scan(&oldStatus); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if status == TicketClosed {
			_, err := support.Close(ctx, tx, ticketID, actor.ID, time.Now().UTC(), "")
			return err
		}
		_, err := support.SetOpenStatus(ctx, tx, ticketID, string(status), time.Now().UTC(), "")
		return err
	})
}

func (s *Store) SetTicketPriority(ctx context.Context, actor Account, ticketID string, priority TicketPriority, ipHash string) error {
	return s.mutate(ctx, actor, "ticket.priority", "ticket", ticketID, ipHash, map[string]any{
		"to": string(priority),
	}, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE sesame_support_requests SET priority = $2, updated_at = NOW() WHERE id = $1`, ticketID, string(priority))
		if err != nil {
			return err
		}
		return affected(result, nil)
	})
}
