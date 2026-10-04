// Package support owns the support-ticket close/reopen transitions that the
// account side and the admin side both write, so the two callers cannot
// disagree about what "closed" means.
package support

import (
	"context"
	"database/sql"
	"time"
)

// ReopenWindow is how long after closing a ticket its owning account may
// reopen it. It applies the same way regardless of who closed the ticket.
const ReopenWindow = "30 days"

// RetentionAfter is how long a closed ticket and its linked outbox rows are
// kept before scheduled maintenance deletes them.
const RetentionAfter = 90 * 24 * time.Hour

// AutoCloseAfter is how long a waiting ticket may go without activity before
// the system closes it.
const AutoCloseAfter = 14 * 24 * time.Hour

// Executor is satisfied by *sql.DB and *sql.Tx.
type Executor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Close marks a ticket closed and opens its reopen window. closedBy is the
// admin id when an admin closed it, or "" for an account-initiated close.
// extraWhere, if non-empty, is appended to the WHERE clause (e.g. an
// ownership check) and its placeholders start at $4.
func Close(ctx context.Context, exec Executor, ticketID, closedBy string, now time.Time, extraWhere string, extraArgs ...any) (sql.Result, error) {
	var closedByArg any
	if closedBy != "" {
		closedByArg = closedBy
	}
	args := append([]any{ticketID, now.UTC(), closedByArg}, extraArgs...)
	query := `
		UPDATE sesame_support_requests
		SET status = 'closed', closed_at = $2, closed_by = $3, closed_by_system = FALSE, account_reopen_until = $2::timestamptz + INTERVAL '` + ReopenWindow + `', updated_at = $2
		WHERE id = $1` + extraWhere
	result, err := exec.ExecContext(ctx, query, args...)
	if err != nil {
		return result, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return result, err
	}
	if affected == 0 {
		return result, nil
	}
	if _, err := RevokeAccessLinks(ctx, exec, ticketID, now); err != nil {
		return result, err
	}
	return result, nil
}

// CloseAutomatically closes a waiting ticket that has seen no activity since
// staleBefore and attributes the close to the system, not an administrator.
// It runs Close, so the reopen window is identical. Run it in the caller's
// transaction: the system attribution is a second statement.
func CloseAutomatically(ctx context.Context, exec Executor, ticketID string, staleBefore, now time.Time) (sql.Result, error) {
	result, err := Close(ctx, exec, ticketID, "", now, " AND status = 'waiting' AND updated_at <= $4", staleBefore.UTC())
	if err != nil {
		return nil, err
	}
	affected, err := result.RowsAffected()
	if err != nil || affected == 0 {
		return result, err
	}
	if _, err := exec.ExecContext(ctx, `UPDATE sesame_support_requests SET closed_by_system = TRUE WHERE id = $1`, ticketID); err != nil {
		return nil, err
	}
	return result, nil
}

// SetOpenStatus moves a ticket to a non-closed status and clears the closed
// bookkeeping a Close call set, including the reopen window. extraWhere, if
// non-empty, is appended to the WHERE clause and its placeholders start at $4.
func SetOpenStatus(ctx context.Context, exec Executor, ticketID, status string, now time.Time, extraWhere string, extraArgs ...any) (sql.Result, error) {
	args := append([]any{ticketID, status, now.UTC()}, extraArgs...)
	query := `
		UPDATE sesame_support_requests
		SET status = $2, closed_at = NULL, closed_by = NULL, closed_by_system = FALSE, account_reopen_until = NULL, updated_at = $3
		WHERE id = $1` + extraWhere
	return exec.ExecContext(ctx, query, args...)
}
