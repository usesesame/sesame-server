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
		SET status = 'closed', closed_at = $2, closed_by = $3, account_reopen_until = $2 + INTERVAL '` + ReopenWindow + `', updated_at = $2
		WHERE id = $1` + extraWhere
	return exec.ExecContext(ctx, query, args...)
}

// SetOpenStatus moves a ticket to a non-closed status and clears the closed
// bookkeeping a Close call set, including the reopen window. extraWhere, if
// non-empty, is appended to the WHERE clause and its placeholders start at $4.
func SetOpenStatus(ctx context.Context, exec Executor, ticketID, status string, now time.Time, extraWhere string, extraArgs ...any) (sql.Result, error) {
	args := append([]any{ticketID, status, now.UTC()}, extraArgs...)
	query := `
		UPDATE sesame_support_requests
		SET status = $2, closed_at = NULL, closed_by = NULL, account_reopen_until = NULL, updated_at = $3
		WHERE id = $1` + extraWhere
	return exec.ExecContext(ctx, query, args...)
}
