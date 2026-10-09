package accounts

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPasswordResetAndEmailChangeConfirmationLockTheAccountFirst(t *testing.T) {
	store, db := lifecycleTestStore(t)
	ctx := context.Background()
	seedSecurityAccount(t, db, "acct-lock-order", true)
	recoveryHash := bytes.Repeat([]byte{0x5a}, 32)
	changeHash := bytes.Repeat([]byte{0x5b}, 32)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO sesame_account_tokens (token_hash, account_id, purpose, payload, expires_at)
		VALUES ($1, 'acct-lock-order', $2, '', NOW() + INTERVAL '1 hour'),
			($3, 'acct-lock-order', $4, 'moved-lock-order@example.invalid', NOW() + INTERVAL '1 hour')
	`, recoveryHash, TokenRecoverPassword, changeHash, TokenChangeEmail); err != nil {
		t.Fatalf("seed tokens: %v", err)
	}

	hold := holdAccountLock(t, db, "acct-lock-order", `SELECT id FROM sesame_accounts WHERE id = $1 FOR UPDATE`)
	confirmed := make(chan error, 1)
	go func() {
		_, err := confirmEmailChange(store, changeHash, 0x5c)
		confirmed <- err
	}()
	waitForBlockedCount(t, db, 1)
	reset := make(chan error, 1)
	go func() {
		now := time.Now().UTC()
		_, err := store.ResetPasswordAndRotateSession(ctx, TokenPasswordRotation{
			TokenHash: recoveryHash, PasswordHash: "fictional-reset-hash",
			SessionTokenHash: bytes.Repeat([]byte{0x5d}, 32), SessionExpiresAt: now.Add(time.Hour), SessionLabel: "Browser", AuthenticatedAt: now,
		})
		reset <- err
	}()
	waitForBlockedCount(t, db, 2)
	if err := hold.Commit(); err != nil {
		t.Fatalf("release the account lock: %v", err)
	}

	confirmErr := awaitResult(t, confirmed)
	resetErr := awaitResult(t, reset)
	for name, err := range map[string]error{"email-change confirmation": confirmErr, "password reset": resetErr} {
		if err != nil && strings.Contains(err.Error(), "deadlock") {
			t.Fatalf("%s was chosen as a deadlock victim: %v", name, err)
		}
	}
	if confirmErr != nil {
		t.Fatalf("email-change confirmation: %v", confirmErr)
	}
	if !errors.Is(resetErr, ErrTokenExpired) {
		t.Fatalf("password reset after the email change consumed the recovery token = %v, want ErrTokenExpired", resetErr)
	}
	if email := accountEmailOf(t, db, "acct-lock-order"); email != "moved-lock-order@example.invalid" {
		t.Fatalf("account email = %q", email)
	}
}
