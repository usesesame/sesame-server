package accounts

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"
)

func securityGenerationOf(t *testing.T, db *sql.DB, id string) int64 {
	t.Helper()
	var generation int64
	if err := db.QueryRowContext(context.Background(), `SELECT security_generation FROM sesame_accounts WHERE id = $1`, id).Scan(&generation); err != nil {
		t.Fatalf("read security generation: %v", err)
	}
	return generation
}

func issueDesktopLinkCode(t *testing.T, store *PostgresStore, db *sql.DB, accountID string, fill byte) []byte {
	t.Helper()
	codeHash := bytes.Repeat([]byte{fill}, 32)
	if _, err := store.CreateOrReplaceDesktopLink(context.Background(), accountID, securityGenerationOf(t, db, accountID), codeHash, time.Now().UTC().Add(10*time.Minute)); err != nil {
		t.Fatalf("issue desktop link code: %v", err)
	}
	return codeHash
}

func redeemDesktopLinkCode(store *PostgresStore, codeHash []byte, fill byte) error {
	_, err := store.RedeemDesktopLink(context.Background(), codeHash, "Fictional desktop", bytes.Repeat([]byte{fill}, 32), time.Now().UTC().Add(time.Hour))
	return err
}

func TestRecoveryActionsCancelPendingDesktopLinkCodesForVerifiedAccounts(t *testing.T) {
	actions := []struct {
		name string
		run  func(t *testing.T, store *PostgresStore, db *sql.DB, accountID string)
	}{
		{"password change", func(t *testing.T, store *PostgresStore, _ *sql.DB, accountID string) {
			now := time.Now().UTC()
			if err := store.ChangePasswordAndRotateSession(context.Background(), PasswordRotation{
				AccountID: accountID, ExpectedPasswordHash: "fictional-current-hash", PasswordHash: "fictional-new-hash",
				SessionTokenHash: bytes.Repeat([]byte{0x31}, 32), SessionExpiresAt: now.Add(time.Hour), SessionLabel: "Browser", AuthenticatedAt: now,
			}); err != nil {
				t.Fatalf("change password: %v", err)
			}
		}},
		{"password reset", func(t *testing.T, store *PostgresStore, db *sql.DB, accountID string) {
			recoveryHash := bytes.Repeat([]byte{0x32}, 32)
			if _, err := db.ExecContext(context.Background(), `
				INSERT INTO sesame_account_tokens (token_hash, account_id, purpose, expires_at) VALUES ($1, $2, $3, $4)
			`, recoveryHash, accountID, TokenRecoverPassword, time.Now().Add(time.Hour)); err != nil {
				t.Fatalf("seed recovery token: %v", err)
			}
			now := time.Now().UTC()
			if _, err := store.ResetPasswordAndRotateSession(context.Background(), TokenPasswordRotation{
				TokenHash: recoveryHash, PasswordHash: "fictional-reset-hash",
				SessionTokenHash: bytes.Repeat([]byte{0x33}, 32), SessionExpiresAt: now.Add(time.Hour), SessionLabel: "Browser", AuthenticatedAt: now,
			}); err != nil {
				t.Fatalf("reset password: %v", err)
			}
		}},
		{"sign out everywhere", func(t *testing.T, store *PostgresStore, _ *sql.DB, accountID string) {
			if err := store.RevokeAllSessions(context.Background(), accountID); err != nil {
				t.Fatalf("revoke all sessions: %v", err)
			}
		}},
	}
	for _, action := range actions {
		t.Run(action.name, func(t *testing.T) {
			store, db := lifecycleTestStore(t)
			seedSecurityAccount(t, db, "acct-recovery-codes", true)
			seedSecurityAccount(t, db, "acct-recovery-codes-other", true)
			pending := issueDesktopLinkCode(t, store, db, "acct-recovery-codes", 0x41)
			untouched := issueDesktopLinkCode(t, store, db, "acct-recovery-codes-other", 0x42)
			before := securityGenerationOf(t, db, "acct-recovery-codes")

			action.run(t, store, db, "acct-recovery-codes")

			if err := redeemDesktopLinkCode(store, pending, 0x51); !errors.Is(err, ErrNotFound) {
				t.Fatalf("redeeming a code issued before the %s = %v, want ErrNotFound", action.name, err)
			}
			if count := countRows(t, db, `SELECT COUNT(*) FROM sesame_desktop_link_codes WHERE account_id = $1 AND used_at IS NULL AND cancelled_at IS NULL`, "acct-recovery-codes"); count != 0 {
				t.Fatalf("pending codes after the %s = %d, want 0", action.name, count)
			}
			if after := securityGenerationOf(t, db, "acct-recovery-codes"); after <= before {
				t.Fatalf("security generation after the %s = %d, want more than %d", action.name, after, before)
			}
			if err := redeemDesktopLinkCode(store, untouched, 0x52); err != nil {
				t.Fatalf("another account's pending code was disturbed: %v", err)
			}
		})
	}
}

func TestRedeemDesktopLinkRejectsMalformedStaleAndReplayedCodes(t *testing.T) {
	store, db := lifecycleTestStore(t)
	seedSecurityAccount(t, db, "acct-redeem-states", true)

	if err := redeemDesktopLinkCode(store, bytes.Repeat([]byte{0x61}, 32), 0x71); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown code = %v, want ErrNotFound", err)
	}
	if err := redeemDesktopLinkCode(store, nil, 0x72); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty code = %v, want ErrNotFound", err)
	}

	expired := bytes.Repeat([]byte{0x62}, 32)
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO sesame_desktop_link_codes (id, code_hash, account_id, expires_at, security_generation)
		VALUES ('link-expired', $1, 'acct-redeem-states', NOW() - INTERVAL '1 minute', 0)
	`, expired); err != nil {
		t.Fatalf("seed expired code: %v", err)
	}
	if err := redeemDesktopLinkCode(store, expired, 0x73); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired code = %v, want ErrNotFound", err)
	}

	stale := bytes.Repeat([]byte{0x63}, 32)
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO sesame_desktop_link_codes (id, code_hash, account_id, expires_at, security_generation)
		VALUES ('link-stale', $1, 'acct-redeem-states', NOW() + INTERVAL '10 minutes', 0)
	`, stale); err != nil {
		t.Fatalf("seed stale code: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `UPDATE sesame_accounts SET security_generation = 5 WHERE id = 'acct-redeem-states'`); err != nil {
		t.Fatalf("advance generation: %v", err)
	}
	if err := redeemDesktopLinkCode(store, stale, 0x74); !errors.Is(err, ErrNotFound) {
		t.Fatalf("code from an earlier generation = %v, want ErrNotFound", err)
	}
	if count := countRows(t, db, `SELECT COUNT(*) FROM sesame_desktop_connections WHERE account_id = $1`, "acct-redeem-states"); count != 0 {
		t.Fatalf("desktop connections after rejected redemptions = %d, want 0", count)
	}

	current := issueDesktopLinkCode(t, store, db, "acct-redeem-states", 0x64)
	if err := redeemDesktopLinkCode(store, current, 0x75); err != nil {
		t.Fatalf("redeeming a current code: %v", err)
	}
	if err := redeemDesktopLinkCode(store, current, 0x76); !errors.Is(err, ErrNotFound) {
		t.Fatalf("replayed code = %v, want ErrNotFound", err)
	}
	if count := countRows(t, db, `SELECT COUNT(*) FROM sesame_desktop_connections WHERE account_id = $1`, "acct-redeem-states"); count != 1 {
		t.Fatalf("desktop connections after one redemption and one replay = %d, want 1", count)
	}
}

func TestDesktopLinkIssuedFromAStaleSessionIsRefused(t *testing.T) {
	store, db := lifecycleTestStore(t)
	seedSecurityAccount(t, db, "acct-stale-issue", true)
	observed := securityGenerationOf(t, db, "acct-stale-issue")

	if err := store.RevokeAllSessions(context.Background(), "acct-stale-issue"); err != nil {
		t.Fatalf("revoke all sessions: %v", err)
	}
	_, err := store.CreateOrReplaceDesktopLink(context.Background(), "acct-stale-issue", observed, bytes.Repeat([]byte{0x81}, 32), time.Now().UTC().Add(10*time.Minute))
	if !errors.Is(err, ErrSecurityStateChanged) {
		t.Fatalf("issuing with the generation seen before sign-out = %v, want ErrSecurityStateChanged", err)
	}
	if count := countRows(t, db, `SELECT COUNT(*) FROM sesame_desktop_link_codes WHERE account_id = $1`, "acct-stale-issue"); count != 0 {
		t.Fatalf("codes after a refused issuance = %d, want 0", count)
	}
	if _, err := store.CreateOrReplaceDesktopLink(context.Background(), "missing-account", 0, bytes.Repeat([]byte{0x82}, 32), time.Now().UTC().Add(time.Minute)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("issuing for a missing account = %v, want ErrNotFound", err)
	}
}

func TestRedemptionThatRacesARecoveryActionIsRejected(t *testing.T) {
	store, db := lifecycleTestStore(t)
	seedSecurityAccount(t, db, "acct-race-redeem", true)
	code := issueDesktopLinkCode(t, store, db, "acct-race-redeem", 0x91)

	recovery := holdAccountLock(t, db, "acct-race-redeem", `UPDATE sesame_accounts SET security_generation = security_generation + 1 WHERE id = $1`)
	result := make(chan error, 1)
	go func() { result <- redeemDesktopLinkCode(store, code, 0xa1) }()
	waitForBlockedStatement(t, db, "FOR SHARE")
	if err := recovery.Commit(); err != nil {
		t.Fatalf("commit the recovery action: %v", err)
	}

	if err := awaitResult(t, result); !errors.Is(err, ErrNotFound) {
		t.Fatalf("redemption that waited for a recovery action = %v, want ErrNotFound", err)
	}
	if count := countRows(t, db, `SELECT COUNT(*) FROM sesame_desktop_connections WHERE account_id = $1`, "acct-race-redeem"); count != 0 {
		t.Fatalf("desktop connections created by the raced redemption = %d, want 0", count)
	}
	if count := countRows(t, db, `SELECT COUNT(*) FROM sesame_desktop_link_codes WHERE account_id = $1 AND used_at IS NOT NULL`, "acct-race-redeem"); count != 0 {
		t.Fatalf("codes marked used by the raced redemption = %d, want 0", count)
	}
}

func TestRecoveryActionWaitsForARedemptionAlreadyInFlight(t *testing.T) {
	store, db := lifecycleTestStore(t)
	seedSecurityAccount(t, db, "acct-race-revoke", true)
	code := issueDesktopLinkCode(t, store, db, "acct-race-revoke", 0x92)

	redemption := holdAccountLock(t, db, "acct-race-revoke", `SELECT security_generation FROM sesame_accounts WHERE id = $1 FOR SHARE`)
	result := make(chan error, 1)
	go func() { result <- store.RevokeAllSessions(context.Background(), "acct-race-revoke") }()
	waitForBlockedStatement(t, db, "security_generation + 1")
	if _, err := redemption.ExecContext(context.Background(), `UPDATE sesame_desktop_link_codes SET used_at = NOW() WHERE code_hash = $1`, code); err != nil {
		t.Fatalf("mark the in-flight code used: %v", err)
	}
	if _, err := redemption.ExecContext(context.Background(), `
		INSERT INTO sesame_desktop_connections (token_hash, account_id, device_id, device_name, expires_at)
		VALUES ($1, 'acct-race-revoke', 'in-flight-device', 'Fictional desktop', NOW() + INTERVAL '1 day')
	`, bytes.Repeat([]byte{0xa2}, 32)); err != nil {
		t.Fatalf("insert the in-flight connection: %v", err)
	}
	if err := redemption.Commit(); err != nil {
		t.Fatalf("commit the in-flight redemption: %v", err)
	}

	if err := awaitResult(t, result); err != nil {
		t.Fatalf("sign out everywhere: %v", err)
	}
	if count := countRows(t, db, `SELECT COUNT(*) FROM sesame_desktop_connections WHERE account_id = $1`, "acct-race-revoke"); count != 0 {
		t.Fatalf("desktop connections that survived sign out everywhere = %d, want 0", count)
	}
}

func TestDesktopLinkIssuanceThatRacesARecoveryActionIsRefused(t *testing.T) {
	store, db := lifecycleTestStore(t)
	seedSecurityAccount(t, db, "acct-race-issue", true)
	observed := securityGenerationOf(t, db, "acct-race-issue")

	recovery := holdAccountLock(t, db, "acct-race-issue", `UPDATE sesame_accounts SET security_generation = security_generation + 1 WHERE id = $1`)
	result := make(chan error, 1)
	go func() {
		_, err := store.CreateOrReplaceDesktopLink(context.Background(), "acct-race-issue", observed, bytes.Repeat([]byte{0xb1}, 32), time.Now().UTC().Add(10*time.Minute))
		result <- err
	}()
	waitForBlockedStatement(t, db, "FOR NO KEY UPDATE")
	if err := recovery.Commit(); err != nil {
		t.Fatalf("commit the recovery action: %v", err)
	}

	if err := awaitResult(t, result); !errors.Is(err, ErrSecurityStateChanged) {
		t.Fatalf("issuance that waited for a recovery action = %v, want ErrSecurityStateChanged", err)
	}
	if count := countRows(t, db, `SELECT COUNT(*) FROM sesame_desktop_link_codes WHERE account_id = $1`, "acct-race-issue"); count != 0 {
		t.Fatalf("codes left by the raced issuance = %d, want 0", count)
	}
}

func TestCompetingIssuancesLeaveOnePendingDesktopLinkCode(t *testing.T) {
	store, db := lifecycleTestStore(t)
	seedSecurityAccount(t, db, "acct-double-issue", true)
	observed := securityGenerationOf(t, db, "acct-double-issue")

	var group sync.WaitGroup
	for i := 0; i < 6; i++ {
		group.Add(1)
		go func(fill byte) {
			defer group.Done()
			if _, err := store.CreateOrReplaceDesktopLink(context.Background(), "acct-double-issue", observed, bytes.Repeat([]byte{fill}, 32), time.Now().UTC().Add(10*time.Minute)); err != nil {
				t.Errorf("issue desktop link code: %v", err)
			}
		}(byte(0xc0 + i))
	}
	group.Wait()

	if count := countRows(t, db, `SELECT COUNT(*) FROM sesame_desktop_link_codes WHERE account_id = $1 AND used_at IS NULL AND cancelled_at IS NULL`, "acct-double-issue"); count != 1 {
		t.Fatalf("pending codes after competing issuances = %d, want 1", count)
	}
}

func TestEmailChangeRequiresAVerifiedEmail(t *testing.T) {
	store, db := lifecycleTestStore(t)
	seedSecurityAccount(t, db, "acct-change-unverified", false)

	err := store.CreateEmailChange(context.Background(), "acct-change-unverified", securityGenerationOf(t, db, "acct-change-unverified"), "new-owner@example.invalid", bytes.Repeat([]byte{0xd1}, 32), time.Now().UTC().Add(time.Hour))
	if !errors.Is(err, ErrEmailUnverified) {
		t.Fatalf("email change for an unverified account = %v, want ErrEmailUnverified", err)
	}
	if count := countRows(t, db, `SELECT COUNT(*) FROM sesame_account_tokens WHERE account_id = $1 AND purpose = $2`, "acct-change-unverified", TokenChangeEmail); count != 0 {
		t.Fatalf("email-change tokens for an unverified account = %d, want 0", count)
	}
}

func TestEmailChangeRequestFromAStaleSessionIsRefused(t *testing.T) {
	store, db := lifecycleTestStore(t)
	seedSecurityAccount(t, db, "acct-change-stale", true)
	observed := securityGenerationOf(t, db, "acct-change-stale")
	if err := store.RevokeAllSessions(context.Background(), "acct-change-stale"); err != nil {
		t.Fatalf("revoke all sessions: %v", err)
	}

	err := store.CreateEmailChange(context.Background(), "acct-change-stale", observed, "new-owner@example.invalid", bytes.Repeat([]byte{0xd2}, 32), time.Now().UTC().Add(time.Hour))
	if !errors.Is(err, ErrSecurityStateChanged) {
		t.Fatalf("email change from a session seen before sign-out = %v, want ErrSecurityStateChanged", err)
	}
	if count := countRows(t, db, `SELECT COUNT(*) FROM sesame_account_tokens WHERE account_id = $1 AND purpose = $2`, "acct-change-stale", TokenChangeEmail); count != 0 {
		t.Fatalf("email-change tokens after a refused request = %d, want 0", count)
	}
}

func TestFirstVerificationRevokesPendingEmailChangeTokens(t *testing.T) {
	store, db := lifecycleTestStore(t)
	seedSecurityAccount(t, db, "acct-verify-change", false)
	changeHash := bytes.Repeat([]byte{0xe1}, 32)
	verifyHash := bytes.Repeat([]byte{0xe2}, 32)
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO sesame_account_tokens (token_hash, account_id, purpose, payload, expires_at)
		VALUES ($1, 'acct-verify-change', $2, 'attacker@example.invalid', NOW() + INTERVAL '1 hour'),
			($3, 'acct-verify-change', $4, '', NOW() + INTERVAL '1 hour')
	`, changeHash, TokenChangeEmail, verifyHash, TokenVerifyEmail); err != nil {
		t.Fatalf("seed tokens: %v", err)
	}
	now := time.Now().UTC()
	if _, err := store.VerifyEmail(context.Background(), TokenSessionRotation{
		TokenHash: verifyHash, SessionTokenHash: bytes.Repeat([]byte{0xe3}, 32),
		SessionExpiresAt: now.Add(time.Hour), SessionLabel: "Browser", AuthenticatedAt: now,
	}); err != nil {
		t.Fatalf("verify email: %v", err)
	}

	if _, err := confirmEmailChange(store, changeHash, 0xe4); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("email-change token issued before verification = %v, want ErrTokenExpired", err)
	}
	if email := accountEmailOf(t, db, "acct-verify-change"); email != "acct-verify-change@example.invalid" {
		t.Fatalf("account email after the refused confirmation = %q", email)
	}
	if count := countRows(t, db, `SELECT COUNT(*) FROM sesame_account_tokens WHERE account_id = $1 AND purpose = $2 AND used_at IS NULL`, "acct-verify-change", TokenChangeEmail); count != 0 {
		t.Fatalf("pending email-change tokens after first verification = %d, want 0", count)
	}
	if count := countRows(t, db, `SELECT COUNT(*) FROM sesame_sessions WHERE account_id = $1`, "acct-verify-change"); count != 1 {
		t.Fatalf("sessions after verification and a refused confirmation = %d, want only the verification session", count)
	}
}

func TestEmailChangeConfirmationRejectsMalformedStaleAndReplayedTokens(t *testing.T) {
	store, db := lifecycleTestStore(t)
	seedSecurityAccount(t, db, "acct-change-states", true)
	expiry := time.Now().UTC().Add(time.Hour)

	if _, err := confirmEmailChange(store, bytes.Repeat([]byte{0xf1}, 32), 0x01); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("unknown token = %v, want ErrTokenExpired", err)
	}
	if _, err := confirmEmailChange(store, nil, 0x02); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("empty token = %v, want ErrTokenExpired", err)
	}

	expired := bytes.Repeat([]byte{0xf2}, 32)
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO sesame_account_tokens (token_hash, account_id, purpose, payload, expires_at, security_generation)
		VALUES ($1, 'acct-change-states', $2, 'expired@example.invalid', NOW() - INTERVAL '1 minute', 0)
	`, expired, TokenChangeEmail); err != nil {
		t.Fatalf("seed expired token: %v", err)
	}
	if _, err := confirmEmailChange(store, expired, 0x03); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("expired token = %v, want ErrTokenExpired", err)
	}

	wrongPurpose := bytes.Repeat([]byte{0xf3}, 32)
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO sesame_account_tokens (token_hash, account_id, purpose, payload, expires_at)
		VALUES ($1, 'acct-change-states', $2, 'wrong@example.invalid', NOW() + INTERVAL '1 hour')
	`, wrongPurpose, TokenVerifyEmail); err != nil {
		t.Fatalf("seed wrong-purpose token: %v", err)
	}
	if _, err := confirmEmailChange(store, wrongPurpose, 0x04); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("token of another purpose = %v, want ErrTokenExpired", err)
	}

	stale := bytes.Repeat([]byte{0xf4}, 32)
	if err := store.CreateEmailChange(context.Background(), "acct-change-states", securityGenerationOf(t, db, "acct-change-states"), "stale@example.invalid", stale, expiry); err != nil {
		t.Fatalf("request email change: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `UPDATE sesame_accounts SET security_generation = security_generation + 1 WHERE id = 'acct-change-states'`); err != nil {
		t.Fatalf("advance generation: %v", err)
	}
	if _, err := confirmEmailChange(store, stale, 0x05); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("token from an earlier generation = %v, want ErrTokenExpired", err)
	}
	if email := accountEmailOf(t, db, "acct-change-states"); email != "acct-change-states@example.invalid" {
		t.Fatalf("account email after refused confirmations = %q", email)
	}

	current := bytes.Repeat([]byte{0xf5}, 32)
	if err := store.CreateEmailChange(context.Background(), "acct-change-states", securityGenerationOf(t, db, "acct-change-states"), "current@example.invalid", current, expiry); err != nil {
		t.Fatalf("request current email change: %v", err)
	}
	result, err := confirmEmailChange(store, current, 0x06)
	if err != nil {
		t.Fatalf("confirm current token: %v", err)
	}
	if result.User.Email != "current@example.invalid" || result.PreviousEmail != "acct-change-states@example.invalid" {
		t.Fatalf("confirmed change = %+v", result)
	}
	if _, err := confirmEmailChange(store, current, 0x07); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("replayed token = %v, want ErrTokenExpired", err)
	}
}

func TestEmailChangeConfirmationThatRacesARecoveryActionIsRejected(t *testing.T) {
	store, db := lifecycleTestStore(t)
	seedSecurityAccount(t, db, "acct-change-race", true)
	token := bytes.Repeat([]byte{0xf6}, 32)
	if err := store.CreateEmailChange(context.Background(), "acct-change-race", securityGenerationOf(t, db, "acct-change-race"), "race@example.invalid", token, time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatalf("request email change: %v", err)
	}

	recovery := holdAccountLock(t, db, "acct-change-race", `UPDATE sesame_accounts SET security_generation = security_generation + 1 WHERE id = $1`)
	result := make(chan error, 1)
	go func() {
		_, err := confirmEmailChange(store, token, 0x08)
		result <- err
	}()
	waitForBlockedStatement(t, db, "FOR NO KEY UPDATE")
	if err := recovery.Commit(); err != nil {
		t.Fatalf("commit the recovery action: %v", err)
	}

	if err := awaitResult(t, result); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("confirmation that waited for a recovery action = %v, want ErrTokenExpired", err)
	}
	if email := accountEmailOf(t, db, "acct-change-race"); email != "acct-change-race@example.invalid" {
		t.Fatalf("account email after the raced confirmation = %q", email)
	}
}

func TestEmailChangeRequestThatRacesFirstVerificationIsRefused(t *testing.T) {
	store, db := lifecycleTestStore(t)
	seedSecurityAccount(t, db, "acct-change-verify-race", true)
	observed := securityGenerationOf(t, db, "acct-change-verify-race")

	verification := holdAccountLock(t, db, "acct-change-verify-race", `UPDATE sesame_accounts SET security_generation = security_generation + 1 WHERE id = $1`)
	result := make(chan error, 1)
	go func() {
		result <- store.CreateEmailChange(context.Background(), "acct-change-verify-race", observed, "race@example.invalid", bytes.Repeat([]byte{0xf7}, 32), time.Now().UTC().Add(time.Hour))
	}()
	waitForBlockedStatement(t, db, "FOR NO KEY UPDATE")
	if err := verification.Commit(); err != nil {
		t.Fatalf("commit the verification: %v", err)
	}

	if err := awaitResult(t, result); !errors.Is(err, ErrSecurityStateChanged) {
		t.Fatalf("email-change request that waited for verification = %v, want ErrSecurityStateChanged", err)
	}
	if count := countRows(t, db, `SELECT COUNT(*) FROM sesame_account_tokens WHERE account_id = $1 AND purpose = $2`, "acct-change-verify-race", TokenChangeEmail); count != 0 {
		t.Fatalf("email-change tokens after the raced request = %d, want 0", count)
	}
}

func TestEmailChangeTokenConfirmsOnce(t *testing.T) {
	store, db := lifecycleTestStore(t)
	seedSecurityAccount(t, db, "acct-change-once", true)
	token := bytes.Repeat([]byte{0xf8}, 32)
	if err := store.CreateEmailChange(context.Background(), "acct-change-once", securityGenerationOf(t, db, "acct-change-once"), "once@example.invalid", token, time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatalf("request email change: %v", err)
	}

	var group sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			_, errs[index] = confirmEmailChange(store, token, byte(0x10+index))
		}(i)
	}
	group.Wait()

	succeeded := 0
	for _, err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrTokenExpired):
		default:
			t.Fatalf("concurrent confirmation: %v", err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("concurrent confirmations that succeeded = %d, want 1", succeeded)
	}
	if count := countRows(t, db, `SELECT COUNT(*) FROM sesame_sessions WHERE account_id = $1`, "acct-change-once"); count != 1 {
		t.Fatalf("sessions after concurrent confirmations = %d, want 1", count)
	}
}

func TestEnrollPasswordSetsOnlyTheEmptyPasswordOfAVerifiedAccount(t *testing.T) {
	store, db := lifecycleTestStore(t)
	ctx := context.Background()
	seedSecurityAccount(t, db, "acct-enroll-unverified", false)
	seedSecurityAccount(t, db, "acct-enroll-has-password", true)
	seedSecurityAccount(t, db, "acct-enroll-ready", true)
	for _, id := range []string{"acct-enroll-unverified", "acct-enroll-ready"} {
		if _, err := db.ExecContext(ctx, `UPDATE sesame_accounts SET password_hash = '' WHERE id = $1`, id); err != nil {
			t.Fatalf("clear password: %v", err)
		}
	}

	if err := store.EnrollPassword(ctx, "acct-enroll-unverified", "fictional-new-hash"); !errors.Is(err, ErrEmailUnverified) {
		t.Fatalf("unverified account = %v, want ErrEmailUnverified", err)
	}
	if err := store.EnrollPassword(ctx, "acct-enroll-has-password", "fictional-new-hash"); !errors.Is(err, ErrPasswordAlreadySet) {
		t.Fatalf("account with a password = %v, want ErrPasswordAlreadySet", err)
	}
	if err := store.EnrollPassword(ctx, "acct-enroll-missing", "fictional-new-hash"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing account = %v, want ErrNotFound", err)
	}

	var group sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			errs[index] = store.EnrollPassword(ctx, "acct-enroll-ready", "fictional-new-hash")
		}(i)
	}
	group.Wait()
	succeeded := 0
	for _, err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrPasswordAlreadySet):
		default:
			t.Fatalf("concurrent enrollment: %v", err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("concurrent enrollments that succeeded = %d, want 1", succeeded)
	}
	if _, hash, err := store.FindByID(ctx, "acct-enroll-ready"); err != nil || hash != "fictional-new-hash" {
		t.Fatalf("stored hash = %q, error = %v", hash, err)
	}
	if _, hash, err := store.FindByID(ctx, "acct-enroll-has-password"); err != nil || hash != "fictional-current-hash" {
		t.Fatalf("existing hash = %q, error = %v", hash, err)
	}
}

func TestCredentialSetupIsRequiredAfterFirstVerificationUntilACredentialExists(t *testing.T) {
	store, db := lifecycleTestStore(t)
	ctx := context.Background()
	seedSecurityAccount(t, db, "acct-setup-state", false)

	if required, err := store.CredentialSetupRequired(ctx, "acct-setup-state"); err != nil || required {
		t.Fatalf("unverified account setup required = %v, error = %v, want false", required, err)
	}
	verifyHash := bytes.Repeat([]byte{0x21}, 32)
	if err := store.CreateEmailVerification(ctx, "acct-setup-state", verifyHash, time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatalf("create verification token: %v", err)
	}
	now := time.Now().UTC()
	if _, err := store.VerifyEmail(ctx, TokenSessionRotation{
		TokenHash: verifyHash, SessionTokenHash: bytes.Repeat([]byte{0x22}, 32),
		SessionExpiresAt: now.Add(time.Hour), SessionLabel: "Browser", AuthenticatedAt: now,
	}); err != nil {
		t.Fatalf("verify email: %v", err)
	}
	if _, hash, err := store.FindByID(ctx, "acct-setup-state"); err != nil || hash != "" {
		t.Fatalf("password hash after first verification = %q, error = %v, want empty", hash, err)
	}
	if required, err := store.CredentialSetupRequired(ctx, "acct-setup-state"); err != nil || !required {
		t.Fatalf("verified account without credentials setup required = %v, error = %v, want true", required, err)
	}

	if err := store.AddCredential(ctx, "acct-setup-state", bytes.Repeat([]byte{0x23}, 16), []byte(`{"id":"ZmljdGlvbmFs"}`), "Fictional passkey"); err != nil {
		t.Fatalf("add passkey: %v", err)
	}
	if required, err := store.CredentialSetupRequired(ctx, "acct-setup-state"); err != nil || required {
		t.Fatalf("account with a passkey setup required = %v, error = %v, want false", required, err)
	}
	if removed, err := store.DeleteCredential(ctx, "acct-setup-state", bytes.Repeat([]byte{0x23}, 16)); err != nil || !removed {
		t.Fatalf("delete passkey removed = %v, error = %v", removed, err)
	}
	if err := store.EnrollPassword(ctx, "acct-setup-state", "fictional-new-hash"); err != nil {
		t.Fatalf("enroll password: %v", err)
	}
	if required, err := store.CredentialSetupRequired(ctx, "acct-setup-state"); err != nil || required {
		t.Fatalf("account with a password setup required = %v, error = %v, want false", required, err)
	}
	if _, err := store.CredentialSetupRequired(ctx, "acct-setup-missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing account = %v, want ErrNotFound", err)
	}
}

func TestEmailChangeConfirmationCancelsPendingDesktopLinkCodes(t *testing.T) {
	store, db := lifecycleTestStore(t)
	seedSecurityAccount(t, db, "acct-change-codes", true)
	seedSecurityAccount(t, db, "acct-change-codes-other", true)
	pending := issueDesktopLinkCode(t, store, db, "acct-change-codes", 0x43)
	untouched := issueDesktopLinkCode(t, store, db, "acct-change-codes-other", 0x44)
	token := bytes.Repeat([]byte{0x45}, 32)
	before := securityGenerationOf(t, db, "acct-change-codes")
	if err := store.CreateEmailChange(context.Background(), "acct-change-codes", before, "moved@example.invalid", token, time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatalf("request email change: %v", err)
	}

	if _, err := confirmEmailChange(store, token, 0x46); err != nil {
		t.Fatalf("confirm email change: %v", err)
	}

	if err := redeemDesktopLinkCode(store, pending, 0x53); !errors.Is(err, ErrNotFound) {
		t.Fatalf("redeeming a code issued before the email change = %v, want ErrNotFound", err)
	}
	if count := countRows(t, db, `SELECT COUNT(*) FROM sesame_desktop_link_codes WHERE account_id = $1 AND used_at IS NULL AND cancelled_at IS NULL`, "acct-change-codes"); count != 0 {
		t.Fatalf("pending codes after the email change = %d, want 0", count)
	}
	if after := securityGenerationOf(t, db, "acct-change-codes"); after <= before {
		t.Fatalf("security generation after the email change = %d, want more than %d", after, before)
	}
	if err := redeemDesktopLinkCode(store, untouched, 0x54); err != nil {
		t.Fatalf("another account's pending code was disturbed: %v", err)
	}
}

func TestDesktopLinkIssuedFromASessionSeenBeforeAnEmailChangeIsRefused(t *testing.T) {
	store, db := lifecycleTestStore(t)
	seedSecurityAccount(t, db, "acct-change-stale-link", true)
	observed := securityGenerationOf(t, db, "acct-change-stale-link")
	token := bytes.Repeat([]byte{0x47}, 32)
	if err := store.CreateEmailChange(context.Background(), "acct-change-stale-link", observed, "moved-stale@example.invalid", token, time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatalf("request email change: %v", err)
	}
	if _, err := confirmEmailChange(store, token, 0x48); err != nil {
		t.Fatalf("confirm email change: %v", err)
	}

	_, err := store.CreateOrReplaceDesktopLink(context.Background(), "acct-change-stale-link", observed, bytes.Repeat([]byte{0x49}, 32), time.Now().UTC().Add(10*time.Minute))
	if !errors.Is(err, ErrSecurityStateChanged) {
		t.Fatalf("issuing with the generation seen before the email change = %v, want ErrSecurityStateChanged", err)
	}
}
