package accounts

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func seedDesktopTokenAccount(t *testing.T, db *sql.DB, id, passwordHash string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO sesame_accounts (id, email, password_hash)
		VALUES ($1, $2, $3)
	`, id, id+"@example.invalid", passwordHash); err != nil {
		t.Fatalf("create fixture account %s: %v", id, err)
	}
	t.Cleanup(func() {
		if _, err := db.ExecContext(context.Background(), `DELETE FROM sesame_accounts WHERE id = $1`, id); err != nil {
			t.Error(err)
		}
	})
}

func seedDesktopToken(t *testing.T, db *sql.DB, accountID string, tokenHash []byte) {
	t.Helper()
	deviceID, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO sesame_desktop_connections (token_hash, account_id, device_id, device_name, expires_at)
		VALUES ($1, $2, $3, 'Fictional desktop', $4)
	`, tokenHash, accountID, deviceID, time.Now().Add(90*24*time.Hour)); err != nil {
		t.Fatalf("insert desktop connection: %v", err)
	}
}

func TestChangePasswordRevokesDesktopTokens(t *testing.T) {
	store, db := lifecycleTestStore(t)
	ctx := context.Background()
	seedDesktopTokenAccount(t, db, "acct-password-change", "fictional-current-hash")
	seedDesktopTokenAccount(t, db, "acct-password-change-other", "fictional-other-hash")
	revokedToken := bytes.Repeat([]byte{1}, 32)
	seedDesktopToken(t, db, "acct-password-change", revokedToken)
	seedDesktopToken(t, db, "acct-password-change-other", bytes.Repeat([]byte{2}, 32))
	now := time.Now().UTC()
	if err := store.ChangePasswordAndRotateSession(ctx, PasswordRotation{
		AccountID:            "acct-password-change",
		ExpectedPasswordHash: "fictional-current-hash",
		PasswordHash:         "fictional-new-hash",
		SessionTokenHash:     bytes.Repeat([]byte{3}, 32),
		SessionExpiresAt:     now.Add(time.Hour),
		SessionLabel:         "Browser",
		AuthenticatedAt:      now,
	}); err != nil {
		t.Fatalf("change password: %v", err)
	}
	if count := countRows(t, db, `SELECT COUNT(*) FROM sesame_desktop_connections WHERE account_id = $1`, "acct-password-change"); count != 0 {
		t.Fatalf("desktop connections after password change = %d, want 0", count)
	}
	if count := countRows(t, db, `SELECT COUNT(*) FROM sesame_desktop_connections WHERE account_id = $1`, "acct-password-change-other"); count != 1 {
		t.Fatalf("other account desktop connections = %d, want 1", count)
	}
	if _, err := store.DesktopConnectionForToken(ctx, revokedToken); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked desktop token error = %v, want ErrNotFound", err)
	}
	if count := countRows(t, db, `SELECT COUNT(*) FROM sesame_sessions WHERE account_id = $1`, "acct-password-change"); count != 1 {
		t.Fatalf("replacement sessions after password change = %d, want 1", count)
	}
}

func TestPasswordResetRevokesDesktopTokens(t *testing.T) {
	store, db := lifecycleTestStore(t)
	ctx := context.Background()
	seedDesktopTokenAccount(t, db, "acct-password-reset", "fictional-current-hash")
	revokedToken := bytes.Repeat([]byte{4}, 32)
	seedDesktopToken(t, db, "acct-password-reset", revokedToken)
	recoveryHash := bytes.Repeat([]byte{5}, 32)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO sesame_account_tokens (token_hash, account_id, purpose, expires_at)
		VALUES ($1, $2, $3, $4)
	`, recoveryHash, "acct-password-reset", TokenRecoverPassword, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("create recovery token: %v", err)
	}
	now := time.Now().UTC()
	user, err := store.ResetPasswordAndRotateSession(ctx, TokenPasswordRotation{
		TokenHash:        recoveryHash,
		PasswordHash:     "fictional-reset-hash",
		SessionTokenHash: bytes.Repeat([]byte{6}, 32),
		SessionExpiresAt: now.Add(time.Hour),
		SessionLabel:     "Browser",
		AuthenticatedAt:  now,
	})
	if err != nil {
		t.Fatalf("reset password: %v", err)
	}
	if user.ID != "acct-password-reset" {
		t.Fatalf("reset returned account %q, want acct-password-reset", user.ID)
	}
	if count := countRows(t, db, `SELECT COUNT(*) FROM sesame_desktop_connections WHERE account_id = $1`, "acct-password-reset"); count != 0 {
		t.Fatalf("desktop connections after password reset = %d, want 0", count)
	}
	if _, err := store.DesktopConnectionForToken(ctx, revokedToken); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked desktop token error = %v, want ErrNotFound", err)
	}
	if count := countRows(t, db, `SELECT COUNT(*) FROM sesame_sessions WHERE account_id = $1`, "acct-password-reset"); count != 1 {
		t.Fatalf("replacement sessions after password reset = %d, want 1", count)
	}
}

func TestRevokeAllSessionsRevokesDesktopTokens(t *testing.T) {
	store, db := lifecycleTestStore(t)
	ctx := context.Background()
	seedDesktopTokenAccount(t, db, "acct-sign-out", "fictional-current-hash")
	seedDesktopTokenAccount(t, db, "acct-sign-out-other", "fictional-other-hash")
	revokedToken := bytes.Repeat([]byte{7}, 32)
	seedDesktopToken(t, db, "acct-sign-out", revokedToken)
	seedDesktopToken(t, db, "acct-sign-out-other", bytes.Repeat([]byte{8}, 32))
	now := time.Now().UTC()
	for _, token := range [][]byte{bytes.Repeat([]byte{9}, 32), bytes.Repeat([]byte{10}, 32)} {
		if err := store.CreateSession(ctx, "acct-sign-out", token, now.Add(time.Hour)); err != nil {
			t.Fatalf("create session: %v", err)
		}
	}
	if err := store.RevokeAllSessions(ctx, "acct-sign-out"); err != nil {
		t.Fatalf("revoke all sessions: %v", err)
	}
	if count := countRows(t, db, `SELECT COUNT(*) FROM sesame_sessions WHERE account_id = $1`, "acct-sign-out"); count != 0 {
		t.Fatalf("website sessions after sign-out-everywhere = %d, want 0", count)
	}
	if count := countRows(t, db, `SELECT COUNT(*) FROM sesame_desktop_connections WHERE account_id = $1`, "acct-sign-out"); count != 0 {
		t.Fatalf("desktop connections after sign-out-everywhere = %d, want 0", count)
	}
	if count := countRows(t, db, `SELECT COUNT(*) FROM sesame_desktop_connections WHERE account_id = $1`, "acct-sign-out-other"); count != 1 {
		t.Fatalf("other account desktop connections = %d, want 1", count)
	}
	if _, err := store.DesktopConnectionForToken(ctx, revokedToken); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked desktop token error = %v, want ErrNotFound", err)
	}
}
