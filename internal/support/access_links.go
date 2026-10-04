package support

import (
	"context"
	"database/sql"
	"time"
)

// IssueAccessLink revokes every live link for one ticket and inserts the new
// secret hash in the same transaction the caller already owns.
func IssueAccessLink(ctx context.Context, exec Executor, ticketID, requesterEmail string, tokenHash []byte, expiresAt, now time.Time) error {
	if _, err := RevokeAccessLinks(ctx, exec, ticketID, now); err != nil {
		return err
	}
	_, err := exec.ExecContext(ctx, `
		INSERT INTO sesame_support_access_links (token_hash, ticket_id, requester_email, expires_at)
		VALUES ($1, $2, $3, $4)
	`, tokenHash, ticketID, requesterEmail, expiresAt.UTC())
	return err
}

// RevokeAccessLinks clears every live link for a ticket. Reopening a ticket
// does not revive them; redemption also requires an open, unattached request.
func RevokeAccessLinks(ctx context.Context, exec Executor, ticketID string, now time.Time) (sql.Result, error) {
	return exec.ExecContext(ctx, `
		UPDATE sesame_support_access_links SET revoked_at = $2
		WHERE ticket_id = $1 AND revoked_at IS NULL
	`, ticketID, now.UTC())
}
