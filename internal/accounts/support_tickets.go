package accounts

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"usesesame.app/backend/internal/support"
)

type SupportRequest struct {
	AccountID          string
	Email              string
	Subject            string
	Message            string
	Category           string
	AppVersion         string
	DiagnosticCode     string
	BrowserIntegration string
	RequestID          string
}

type SupportTicketSummary struct {
	ID                 string     `json:"id"`
	Subject            string     `json:"subject"`
	Status             string     `json:"status"`
	Category           string     `json:"category"`
	AppVersion         string     `json:"appVersion"`
	DiagnosticCode     string     `json:"diagnosticCode"`
	BrowserIntegration string     `json:"browserIntegration"`
	RequestID          string     `json:"requestId"`
	MessageCount       int        `json:"messageCount"`
	UnreadCount        int        `json:"unreadCount"`
	CreatedAt          time.Time  `json:"createdAt"`
	UpdatedAt          time.Time  `json:"updatedAt"`
	ClosedAt           *time.Time `json:"closedAt,omitempty"`
	AutoClosed         bool       `json:"autoClosed"`
	CanClose           bool       `json:"canClose"`
	CanReopen          bool       `json:"canReopen"`
}

type SupportTicketMessage struct {
	ID         string    `json:"id"`
	AuthorRole string    `json:"authorRole"`
	Body       string    `json:"body"`
	CreatedAt  time.Time `json:"createdAt"`
}

type SupportTicketDetail struct {
	SupportTicketSummary
	Messages []SupportTicketMessage `json:"messages"`
}

func (s *PostgresStore) CreateSupportRequest(ctx context.Context, input SupportRequest) (string, error) {
	id, err := newID()
	if err != nil {
		return "", err
	}
	messageID, err := newID()
	if err != nil {
		return "", err
	}
	var owner any
	if input.AccountID != "" {
		owner = input.AccountID
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	category := input.Category
	if category == "" {
		category = "general"
	}
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO sesame_support_requests (id, account_id, email, subject, message, category, app_version, diagnostic_code, browser_integration, request_id, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 'open')
	`, id, owner, input.Email, input.Subject, input.Message, category, input.AppVersion, input.DiagnosticCode, input.BrowserIntegration, input.RequestID); err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO sesame_support_messages (id, ticket_id, author_role, body)
		VALUES ($1, $2, 'user', $3)
	`, messageID, id, input.Message); err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return id, nil
}

func (s *PostgresStore) SupportTicketsForAccount(ctx context.Context, accountID string) ([]SupportTicketSummary, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT ticket.id, ticket.subject, ticket.status, ticket.category, ticket.app_version, ticket.diagnostic_code, ticket.browser_integration, ticket.request_id,
			ticket.created_at, ticket.updated_at, ticket.closed_at, ticket.closed_by_system, ticket.account_reopen_until,
			(SELECT COUNT(*) FROM sesame_support_messages message WHERE message.ticket_id = ticket.id),
			(SELECT COUNT(*) FROM sesame_support_messages message
			 WHERE message.ticket_id = ticket.id AND message.author_role = 'staff'
			   AND message.created_at > ticket.account_last_read_at)
		FROM sesame_support_requests ticket
		WHERE ticket.account_id = $1
		ORDER BY ticket.updated_at DESC
	`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tickets := make([]SupportTicketSummary, 0)
	for rows.Next() {
		var ticket SupportTicketSummary
		var closedAt, reopenUntil sql.NullTime
		if err := rows.Scan(&ticket.ID, &ticket.Subject, &ticket.Status, &ticket.Category, &ticket.AppVersion, &ticket.DiagnosticCode, &ticket.BrowserIntegration, &ticket.RequestID, &ticket.CreatedAt, &ticket.UpdatedAt, &closedAt, &ticket.AutoClosed, &reopenUntil, &ticket.MessageCount, &ticket.UnreadCount); err != nil {
			return nil, err
		}
		if closedAt.Valid {
			value := closedAt.Time
			ticket.ClosedAt = &value
		}
		ticket.CanClose = ticket.Status != "closed"
		ticket.CanReopen = ticket.Status == "closed" && reopenUntil.Valid && reopenUntil.Time.After(time.Now().UTC())
		tickets = append(tickets, ticket)
	}
	return tickets, rows.Err()
}

func (s *PostgresStore) SupportTicketForAccount(ctx context.Context, accountID, ticketID string) (SupportTicketDetail, error) {
	var ticket SupportTicketDetail
	var closedAt, reopenUntil sql.NullTime
	err := s.db.QueryRowContext(ctx, `
		SELECT id, subject, status, category, app_version, diagnostic_code, browser_integration, request_id, created_at, updated_at, closed_at, closed_by_system, account_reopen_until,
			(SELECT COUNT(*) FROM sesame_support_messages message WHERE message.ticket_id = sesame_support_requests.id),
			(SELECT COUNT(*) FROM sesame_support_messages message WHERE message.ticket_id = sesame_support_requests.id
			 AND message.author_role = 'staff' AND message.created_at > sesame_support_requests.account_last_read_at)
		FROM sesame_support_requests
		WHERE id = $1 AND account_id = $2
	`, ticketID, accountID).Scan(&ticket.ID, &ticket.Subject, &ticket.Status, &ticket.Category, &ticket.AppVersion, &ticket.DiagnosticCode, &ticket.BrowserIntegration, &ticket.RequestID, &ticket.CreatedAt, &ticket.UpdatedAt, &closedAt, &ticket.AutoClosed, &reopenUntil, &ticket.MessageCount, &ticket.UnreadCount)
	if errors.Is(err, sql.ErrNoRows) {
		return SupportTicketDetail{}, ErrNotFound
	}
	if err != nil {
		return SupportTicketDetail{}, err
	}
	if closedAt.Valid {
		value := closedAt.Time
		ticket.ClosedAt = &value
	}
	ticket.CanClose = ticket.Status != "closed"
	ticket.CanReopen = ticket.Status == "closed" && reopenUntil.Valid && reopenUntil.Time.After(time.Now().UTC())
	if _, err := s.db.ExecContext(ctx, `UPDATE sesame_support_requests SET account_last_read_at = NOW() WHERE id = $1 AND account_id = $2`, ticketID, accountID); err != nil {
		return SupportTicketDetail{}, err
	}
	ticket.UnreadCount = 0
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, author_role, body, created_at
		FROM sesame_support_messages
		WHERE ticket_id = $1
		ORDER BY created_at
	`, ticketID)
	if err != nil {
		return SupportTicketDetail{}, err
	}
	defer rows.Close()
	ticket.Messages = make([]SupportTicketMessage, 0)
	for rows.Next() {
		var message SupportTicketMessage
		if err := rows.Scan(&message.ID, &message.AuthorRole, &message.Body, &message.CreatedAt); err != nil {
			return SupportTicketDetail{}, err
		}
		ticket.Messages = append(ticket.Messages, message)
	}
	return ticket, rows.Err()
}

func (s *PostgresStore) CloseSupportTicket(ctx context.Context, accountID, ticketID string, now time.Time) (SupportTicketDetail, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SupportTicketDetail{}, err
	}
	defer tx.Rollback()
	result, err := support.Close(ctx, tx, ticketID, "", now, " AND account_id = $4 AND status <> 'closed'", accountID)
	if err != nil {
		return SupportTicketDetail{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return SupportTicketDetail{}, err
	}
	if affected == 0 {
		var status string
		err := tx.QueryRowContext(ctx, `SELECT status FROM sesame_support_requests WHERE id = $1 AND account_id = $2`, ticketID, accountID).Scan(&status)
		if errors.Is(err, sql.ErrNoRows) {
			return SupportTicketDetail{}, ErrNotFound
		}
		if err != nil {
			return SupportTicketDetail{}, err
		}
		return SupportTicketDetail{}, ErrSupportTicketClosed
	}
	if err := tx.Commit(); err != nil {
		return SupportTicketDetail{}, err
	}
	return s.SupportTicketForAccount(ctx, accountID, ticketID)
}

func (s *PostgresStore) ReopenSupportTicket(ctx context.Context, accountID, ticketID string, now time.Time) (SupportTicketDetail, error) {
	result, err := support.SetOpenStatus(ctx, s.db, ticketID, "open", now, " AND account_id = $4 AND status = 'closed' AND account_reopen_until > $3", accountID)
	if err != nil {
		return SupportTicketDetail{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return SupportTicketDetail{}, err
	}
	if affected == 0 {
		var exists bool
		if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sesame_support_requests WHERE id = $1 AND account_id = $2)`, ticketID, accountID).Scan(&exists); err != nil {
			return SupportTicketDetail{}, err
		}
		if !exists {
			return SupportTicketDetail{}, ErrNotFound
		}
		return SupportTicketDetail{}, ErrSupportTicketReopenExpired
	}
	return s.SupportTicketForAccount(ctx, accountID, ticketID)
}

func (s *PostgresStore) ReplyToSupportTicket(ctx context.Context, accountID, ticketID, body string) (SupportTicketDetail, error) {
	messageID, err := newID()
	if err != nil {
		return SupportTicketDetail{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SupportTicketDetail{}, err
	}
	defer tx.Rollback()
	var status string
	err = tx.QueryRowContext(ctx, `
		SELECT status FROM sesame_support_requests
		WHERE id = $1 AND account_id = $2
		FOR UPDATE
	`, ticketID, accountID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return SupportTicketDetail{}, ErrNotFound
	}
	if err != nil {
		return SupportTicketDetail{}, err
	}
	if status == "closed" {
		return SupportTicketDetail{}, ErrSupportTicketClosed
	}
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO sesame_support_messages (id, ticket_id, author_role, body)
		VALUES ($1, $2, 'user', $3)
	`, messageID, ticketID, body); err != nil {
		return SupportTicketDetail{}, err
	}
	if _, err = tx.ExecContext(ctx, `
		UPDATE sesame_support_requests
		SET status = CASE WHEN assigned_admin_id IS NULL THEN 'open' ELSE 'in_progress' END,
			updated_at = NOW()
		WHERE id = $1
	`, ticketID); err != nil {
		return SupportTicketDetail{}, err
	}
	if err = tx.Commit(); err != nil {
		return SupportTicketDetail{}, err
	}
	return s.SupportTicketForAccount(ctx, accountID, ticketID)
}

// resolveSupportAccessLink locks the ticket first and then the link, the same
// order the close and attach transactions take, so a concurrent close, attach,
// or newer link leaves nothing live.
func resolveSupportAccessLink(ctx context.Context, tx *sql.Tx, tokenHash []byte) (string, error) {
	var ticketID string
	err := tx.QueryRowContext(ctx, `SELECT ticket_id FROM sesame_support_access_links WHERE token_hash = $1`, tokenHash).Scan(&ticketID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	var ticketEmail, status string
	var accountID sql.NullString
	err = tx.QueryRowContext(ctx, `
		SELECT email, status, account_id FROM sesame_support_requests WHERE id = $1 FOR UPDATE
	`, ticketID).Scan(&ticketEmail, &status, &accountID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if accountID.Valid || status == "closed" {
		return "", ErrNotFound
	}
	var requesterEmail string
	err = tx.QueryRowContext(ctx, `
		SELECT requester_email FROM sesame_support_access_links
		WHERE token_hash = $1 AND revoked_at IS NULL AND expires_at > NOW()
		FOR UPDATE
	`, tokenHash).Scan(&requesterEmail)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if requesterEmail != ticketEmail {
		return "", ErrNotFound
	}
	return ticketID, nil
}

func (s *PostgresStore) SupportTicketForAccessToken(ctx context.Context, tokenHash []byte) (SupportTicketDetail, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SupportTicketDetail{}, err
	}
	defer tx.Rollback()
	ticketID, err := resolveSupportAccessLink(ctx, tx, tokenHash)
	if err != nil {
		return SupportTicketDetail{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sesame_support_access_links SET last_used_at = NOW() WHERE token_hash = $1`, tokenHash); err != nil {
		return SupportTicketDetail{}, err
	}
	var ticket SupportTicketDetail
	err = tx.QueryRowContext(ctx, `
		SELECT id, subject, status, category, app_version, diagnostic_code, browser_integration, request_id, created_at, updated_at,
			(SELECT COUNT(*) FROM sesame_support_messages message WHERE message.ticket_id = sesame_support_requests.id)
		FROM sesame_support_requests
		WHERE id = $1
	`, ticketID).Scan(&ticket.ID, &ticket.Subject, &ticket.Status, &ticket.Category, &ticket.AppVersion, &ticket.DiagnosticCode, &ticket.BrowserIntegration, &ticket.RequestID, &ticket.CreatedAt, &ticket.UpdatedAt, &ticket.MessageCount)
	if err != nil {
		return SupportTicketDetail{}, err
	}
	ticket.CanClose = false
	ticket.CanReopen = false
	rows, err := tx.QueryContext(ctx, `
		SELECT id, author_role, body, created_at
		FROM sesame_support_messages
		WHERE ticket_id = $1
		ORDER BY created_at
	`, ticketID)
	if err != nil {
		return SupportTicketDetail{}, err
	}
	defer rows.Close()
	ticket.Messages = make([]SupportTicketMessage, 0)
	for rows.Next() {
		var message SupportTicketMessage
		if err := rows.Scan(&message.ID, &message.AuthorRole, &message.Body, &message.CreatedAt); err != nil {
			return SupportTicketDetail{}, err
		}
		ticket.Messages = append(ticket.Messages, message)
	}
	if err := rows.Err(); err != nil {
		return SupportTicketDetail{}, err
	}
	if err := tx.Commit(); err != nil {
		return SupportTicketDetail{}, err
	}
	return ticket, nil
}

func (s *PostgresStore) ReplyToSupportTicketWithAccessToken(ctx context.Context, tokenHash []byte, body string) (SupportTicketDetail, error) {
	messageID, err := newID()
	if err != nil {
		return SupportTicketDetail{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SupportTicketDetail{}, err
	}
	defer tx.Rollback()
	ticketID, err := resolveSupportAccessLink(ctx, tx, tokenHash)
	if err != nil {
		return SupportTicketDetail{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO sesame_support_messages (id, ticket_id, author_role, body)
		VALUES ($1, $2, 'user', $3)
	`, messageID, ticketID, body); err != nil {
		return SupportTicketDetail{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE sesame_support_requests
		SET status = CASE WHEN assigned_admin_id IS NULL THEN 'open' ELSE 'in_progress' END,
			updated_at = NOW()
		WHERE id = $1
	`, ticketID); err != nil {
		return SupportTicketDetail{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sesame_support_access_links SET last_used_at = NOW() WHERE token_hash = $1`, tokenHash); err != nil {
		return SupportTicketDetail{}, err
	}
	if err := tx.Commit(); err != nil {
		return SupportTicketDetail{}, err
	}
	return s.SupportTicketForAccessToken(ctx, tokenHash)
}

func (s *PostgresStore) AttachSupportTicket(ctx context.Context, accountID, ticketID string) (SupportTicketDetail, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SupportTicketDetail{}, err
	}
	defer tx.Rollback()
	var ticketEmail string
	var owner sql.NullString
	err = tx.QueryRowContext(ctx, `
		SELECT LOWER(email), account_id FROM sesame_support_requests WHERE id = $1 FOR UPDATE
	`, ticketID).Scan(&ticketEmail, &owner)
	if errors.Is(err, sql.ErrNoRows) {
		return SupportTicketDetail{}, ErrNotFound
	}
	if err != nil {
		return SupportTicketDetail{}, err
	}
	if owner.Valid {
		return SupportTicketDetail{}, ErrNotFound
	}
	var accountEmail string
	var verified bool
	err = tx.QueryRowContext(ctx, `
		SELECT LOWER(email), email_verified_at IS NOT NULL FROM sesame_accounts WHERE id = $1
	`, accountID).Scan(&accountEmail, &verified)
	if errors.Is(err, sql.ErrNoRows) {
		return SupportTicketDetail{}, ErrNotFound
	}
	if err != nil {
		return SupportTicketDetail{}, err
	}
	if !verified {
		return SupportTicketDetail{}, ErrEmailUnverified
	}
	if ticketEmail != accountEmail {
		return SupportTicketDetail{}, ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sesame_support_requests SET account_id = $2 WHERE id = $1`, ticketID, accountID); err != nil {
		return SupportTicketDetail{}, err
	}
	if _, err := support.RevokeAccessLinks(ctx, tx, ticketID, time.Now().UTC()); err != nil {
		return SupportTicketDetail{}, err
	}
	if err := tx.Commit(); err != nil {
		return SupportTicketDetail{}, err
	}
	return s.SupportTicketForAccount(ctx, accountID, ticketID)
}
