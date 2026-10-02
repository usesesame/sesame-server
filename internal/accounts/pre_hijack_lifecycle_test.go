package accounts

import (
	"bytes"
	"context"
	"testing"
	"time"
)

func TestVerifyEmailRollsBackWhenTheReplacementSessionConflicts(t *testing.T) {
	store, db := lifecycleTestStore(t)
	ctx := context.Background()
	accountID := "acct-pre-hijack-rollback"
	clearFixtureAccount(t, db, accountID)
	if _, err := db.ExecContext(ctx, `INSERT INTO sesame_accounts (id, email, password_hash) VALUES ($1, 'pre-hijack-rollback@example.invalid', 'fictional-test-hash')`, accountID); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	verificationHash := bytes.Repeat([]byte{7}, 32)
	if err := store.CreateEmailVerification(ctx, accountID, verificationHash, time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatalf("create verification token: %v", err)
	}
	credentialID := bytes.Repeat([]byte{8}, 16)
	if err := store.AddCredential(ctx, accountID, credentialID, []byte(`{"id":"ZmVsaWNl"}`), "Fictional passkey"); err != nil {
		t.Fatalf("seed passkey: %v", err)
	}
	otherAccountID := "acct-pre-hijack-rollback-other"
	clearFixtureAccount(t, db, otherAccountID)
	if _, err := db.ExecContext(ctx, `INSERT INTO sesame_accounts (id, email, password_hash) VALUES ($1, 'pre-hijack-rollback-other@example.invalid', 'fictional-test-hash')`, otherAccountID); err != nil {
		t.Fatalf("seed other account: %v", err)
	}
	sessionHash := bytes.Repeat([]byte{9}, 32)
	if err := store.CreateSession(ctx, otherAccountID, sessionHash, time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatalf("seed conflicting session: %v", err)
	}
	if err := store.CreateSession(ctx, accountID, bytes.Repeat([]byte{10}, 32), time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	_, err := store.VerifyEmail(ctx, TokenSessionRotation{
		TokenHash: verificationHash, SessionTokenHash: sessionHash,
		SessionExpiresAt: time.Now().UTC().Add(time.Hour), SessionLabel: "Browser", AuthenticatedAt: time.Now().UTC(),
	})
	if err == nil {
		t.Fatal("verification reused an existing session token")
	}
	var verified bool
	if err := db.QueryRowContext(ctx, `SELECT email_verified_at IS NOT NULL FROM sesame_accounts WHERE id = $1`, accountID).Scan(&verified); err != nil {
		t.Fatalf("read account: %v", err)
	}
	if verified {
		t.Fatal("rollback left the account verified")
	}
	var tokenUsed bool
	if err := db.QueryRowContext(ctx, `SELECT used_at IS NOT NULL FROM sesame_account_tokens WHERE token_hash = $1`, verificationHash).Scan(&tokenUsed); err != nil {
		t.Fatalf("read verification token: %v", err)
	}
	if tokenUsed {
		t.Fatal("rollback consumed the verification token")
	}
	var credentials int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sesame_webauthn_credentials WHERE account_id = $1`, accountID).Scan(&credentials); err != nil {
		t.Fatalf("count passkeys: %v", err)
	}
	if credentials != 1 {
		t.Fatalf("rollback removed the passkey: %d remain", credentials)
	}
	var sessions int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sesame_sessions WHERE account_id = $1`, accountID).Scan(&sessions); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if sessions != 1 {
		t.Fatalf("rollback left %d sessions, want the original one", sessions)
	}
}
