package accounts

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

func seedAccountWithPassword(t *testing.T, accountID, password string, verified bool) (*PostgresStore, []byte) {
	t.Helper()
	store, db := lifecycleTestStore(t)
	ctx := context.Background()
	clearFixtureAccount(t, db, accountID)
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO sesame_accounts (id, email, password_hash) VALUES ($1, $2, $3)`, accountID, accountID+"@example.invalid", hash); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	if verified {
		if _, err := db.ExecContext(ctx, `UPDATE sesame_accounts SET email_verified_at = NOW() WHERE id = $1`, accountID); err != nil {
			t.Fatalf("verify seed account: %v", err)
		}
	}
	tokenHash := bytes.Repeat([]byte{21}, 32)
	if err := store.CreateEmailVerification(ctx, accountID, tokenHash, time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatalf("create verification token: %v", err)
	}
	return store, tokenHash
}

func rotationFor(tokenHash []byte, confirm string) TokenSessionRotation {
	return TokenSessionRotation{
		TokenHash: tokenHash, SessionTokenHash: bytes.Repeat([]byte{22}, 32),
		SessionExpiresAt: time.Now().UTC().Add(time.Hour), SessionLabel: "Browser", AuthenticatedAt: time.Now().UTC(),
		ConfirmPassword: confirm,
	}
}

func storedHash(t *testing.T, store *PostgresStore, accountID string) (string, bool) {
	t.Helper()
	var hash string
	var verified bool
	if err := store.DB().QueryRowContext(context.Background(), `SELECT password_hash, email_verified_at IS NOT NULL FROM sesame_accounts WHERE id = $1`, accountID).Scan(&hash, &verified); err != nil {
		t.Fatalf("read account: %v", err)
	}
	return hash, verified
}

func TestVerifyEmailKeepsTheRegistrationPasswordOnlyForAVerifierWhoKnowsIt(t *testing.T) {
	accountID := "acct-verify-keeps-password"
	store, tokenHash := seedAccountWithPassword(t, accountID, "fictional registration password", false)
	ctx := context.Background()
	before, _ := storedHash(t, store, accountID)

	if _, err := store.VerifyEmail(ctx, rotationFor(tokenHash, "fictional wrong password")); !errors.Is(err, ErrPasswordIncorrect) {
		t.Fatalf("wrong password = %v, want ErrPasswordIncorrect", err)
	}
	if hash, verified := storedHash(t, store, accountID); verified || hash != before {
		t.Fatalf("a wrong password changed the account: verified=%v hashChanged=%v", verified, hash != before)
	}

	if _, err := store.VerifyEmail(ctx, rotationFor(tokenHash, "fictional registration password")); err != nil {
		t.Fatalf("correct password after a wrong one = %v, want the same token to work", err)
	}
	if hash, verified := storedHash(t, store, accountID); !verified || hash != before {
		t.Fatalf("a proven password was not kept: verified=%v hashKept=%v", verified, hash == before)
	}
	if required, err := store.CredentialSetupRequired(ctx, accountID); err != nil || required {
		t.Fatalf("credential setup required = %v, %v, want false", required, err)
	}
}

func TestVerifyEmailWithoutAPasswordClearsTheRegistrationPassword(t *testing.T) {
	accountID := "acct-verify-clears-password"
	store, tokenHash := seedAccountWithPassword(t, accountID, "fictional registration password", false)
	ctx := context.Background()
	if _, err := store.VerifyEmail(ctx, rotationFor(tokenHash, "")); err != nil {
		t.Fatalf("verify without a password: %v", err)
	}
	if hash, verified := storedHash(t, store, accountID); !verified || hash != "" {
		t.Fatalf("registration password survived an unproven verification: verified=%v hash=%q", verified, hash)
	}
	if required, err := store.CredentialSetupRequired(ctx, accountID); err != nil || !required {
		t.Fatalf("credential setup required = %v, %v, want true", required, err)
	}
}

func TestVerifyEmailRefusesAPasswordForAnAccountThatHasNone(t *testing.T) {
	accountID := "acct-verify-no-password"
	store, tokenHash := seedAccountWithPassword(t, accountID, "fictional registration password", false)
	ctx := context.Background()
	if _, err := store.DB().ExecContext(ctx, `UPDATE sesame_accounts SET password_hash = '' WHERE id = $1`, accountID); err != nil {
		t.Fatalf("clear seed password: %v", err)
	}
	if _, err := store.VerifyEmail(ctx, rotationFor(tokenHash, "fictional anything at all")); !errors.Is(err, ErrPasswordIncorrect) {
		t.Fatalf("a password for a passwordless account = %v, want ErrPasswordIncorrect", err)
	}
	if _, verified := storedHash(t, store, accountID); verified {
		t.Fatal("a passwordless account was verified with a guessed password")
	}
}

func TestVerifyEmailLeavesAnAlreadyVerifiedPasswordAlone(t *testing.T) {
	accountID := "acct-verify-already-verified"
	store, tokenHash := seedAccountWithPassword(t, accountID, "fictional registration password", true)
	ctx := context.Background()
	before, _ := storedHash(t, store, accountID)
	if _, err := store.VerifyEmail(ctx, rotationFor(tokenHash, "fictional an unrelated value")); err != nil {
		t.Fatalf("re-verifying a verified account: %v", err)
	}
	if hash, _ := storedHash(t, store, accountID); hash != before {
		t.Fatal("verifying a verified account changed its password")
	}
}
