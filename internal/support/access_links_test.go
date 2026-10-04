package support_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"usesesame.app/backend/internal/accounts"
	"usesesame.app/backend/internal/support"
)

type accessLinkState struct {
	requesterEmail string
	revokedAt      sql.NullTime
	lastUsedAt     sql.NullTime
	expiresAt      time.Time
}

func readAccessLinkState(t *testing.T, db *sql.DB, tokenHash []byte) accessLinkState {
	t.Helper()
	var state accessLinkState
	if err := db.QueryRowContext(context.Background(), `
		SELECT requester_email, revoked_at, last_used_at, expires_at
		FROM sesame_support_access_links WHERE token_hash = $1
	`, tokenHash).Scan(&state.requesterEmail, &state.revokedAt, &state.lastUsedAt, &state.expiresAt); err != nil {
		t.Fatalf("read access link: %v", err)
	}
	return state
}

func newAccessLinkHash(t *testing.T) []byte {
	t.Helper()
	_, tokenHash, err := accounts.NewSessionToken()
	if err != nil {
		t.Fatalf("generate access link token: %v", err)
	}
	return tokenHash
}

func TestIssueAccessLinkReplacesLiveLinksAndCloseRevokes(t *testing.T) {
	db := lifecycleTestDatabase(t)
	ctx := context.Background()
	const ticketID = "support-lifecycle-links"
	seedLifecycleTicket(t, db, ticketID, "", "links@example.invalid")
	now := time.Now().UTC().Truncate(time.Millisecond)
	expiresAt := now.Add(7 * 24 * time.Hour)

	hashA := newAccessLinkHash(t)
	if err := support.IssueAccessLink(ctx, db, ticketID, "links@example.invalid", hashA, expiresAt, now); err != nil {
		t.Fatalf("issue first link: %v", err)
	}
	hashB := newAccessLinkHash(t)
	if err := support.IssueAccessLink(ctx, db, ticketID, "links@example.invalid", hashB, expiresAt, now); err != nil {
		t.Fatalf("issue second link: %v", err)
	}
	if state := readAccessLinkState(t, db, hashA); !state.revokedAt.Valid {
		t.Fatal("a newer link must revoke the older one")
	}
	live := readAccessLinkState(t, db, hashB)
	if live.revokedAt.Valid || live.lastUsedAt.Valid || live.requesterEmail != "links@example.invalid" {
		t.Fatalf("newest link state = %+v, want live and unused", live)
	}
	if diff := live.expiresAt.Sub(expiresAt); diff < -time.Second || diff > time.Second {
		t.Fatalf("link expiry = %v, want %v", live.expiresAt, expiresAt)
	}

	result, err := support.Close(ctx, db, ticketID, "", now, "")
	if err != nil {
		t.Fatalf("close ticket: %v", err)
	}
	if affectedRows(t, result) != 1 {
		t.Fatal("the close did not change the ticket")
	}
	if state := readAccessLinkState(t, db, hashB); !state.revokedAt.Valid {
		t.Fatal("closing a ticket must revoke its live links")
	}

	result, err = support.SetOpenStatus(ctx, db, ticketID, "open", now, "")
	if err != nil {
		t.Fatalf("reopen ticket: %v", err)
	}
	if affectedRows(t, result) != 1 {
		t.Fatal("the reopen did not change the ticket")
	}
	if state := readAccessLinkState(t, db, hashB); !state.revokedAt.Valid {
		t.Fatal("reopening a ticket must not revive its revoked links")
	}

	result, err = support.RevokeAccessLinks(ctx, db, ticketID, now)
	if err != nil {
		t.Fatalf("revoke links on a ticket with no live link: %v", err)
	}
	if affectedRows(t, result) != 0 {
		t.Fatal("revoking links with none live reported a change")
	}
}

func TestCloseWithoutAMatchingTicketLeavesLinksAlone(t *testing.T) {
	db := lifecycleTestDatabase(t)
	ctx := context.Background()
	const ticketID = "support-lifecycle-guard"
	seedLifecycleTicket(t, db, ticketID, "", "guard@example.invalid")
	now := time.Now().UTC().Truncate(time.Millisecond)
	hash := newAccessLinkHash(t)
	if err := support.IssueAccessLink(ctx, db, ticketID, "guard@example.invalid", hash, now.Add(7*24*time.Hour), now); err != nil {
		t.Fatalf("issue link: %v", err)
	}
	result, err := support.Close(ctx, db, ticketID, "", now, " AND account_id = $4", "someone-else")
	if err != nil {
		t.Fatalf("close with a foreign owner: %v", err)
	}
	if affectedRows(t, result) != 0 {
		t.Fatal("a guarded close changed the ticket")
	}
	if state := readAccessLinkState(t, db, hash); state.revokedAt.Valid {
		t.Fatal("a guarded close that matched no row must not revoke links")
	}
}
