package httpapi

import (
	"context"
	"database/sql"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"usesesame.app/backend/internal/accounts"
)

const (
	accountTokenTestPassword = "fictional-current-password"
	accountTokenNewPassword  = "fictional-replacement-password"
)

func accountTokenFixture(t *testing.T) (accountID, oldEmail, newEmail string) {
	t.Helper()
	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	return "account-token-" + suffix, "token-order-" + suffix + "@example.invalid", "token-order-new-" + suffix + "@example.invalid"
}

func setAccountPassword(t *testing.T, env *supportTestEnv, accountID, password string) {
	t.Helper()
	hash, err := accounts.HashPassword(password)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if _, err := env.db.ExecContext(context.Background(), `UPDATE sesame_accounts SET password_hash = $2 WHERE id = $1`, accountID, hash); err != nil {
		t.Fatalf("set account password: %v", err)
	}
}

func outboxToken(t *testing.T, db *sql.DB, kind, to string) string {
	t.Helper()
	var actionURL string
	err := db.QueryRowContext(context.Background(), `
		SELECT action_url FROM sesame_email_outbox
		WHERE kind = $1 AND to_email = $2
		ORDER BY created_at DESC, id DESC
		LIMIT 1
	`, kind, to).Scan(&actionURL)
	if err != nil {
		t.Fatalf("read %s outbox message for %s: %v", kind, to, err)
	}
	_, token, found := strings.Cut(actionURL, "#token=")
	if !found || token == "" {
		t.Fatalf("outbox %s message for %s has no token: %q", kind, to, actionURL)
	}
	return token
}

func assertAccountEmail(t *testing.T, db *sql.DB, accountID, want string) {
	t.Helper()
	var email string
	if err := db.QueryRowContext(context.Background(), `SELECT email FROM sesame_accounts WHERE id = $1`, accountID).Scan(&email); err != nil {
		t.Fatalf("read account email: %v", err)
	}
	if email != want {
		t.Fatalf("account email = %q, want %q", email, want)
	}
}

func requestEmailChangeToken(t *testing.T, handler http.Handler, env *supportTestEnv, session, newEmail string) string {
	t.Helper()
	response := requestOn(t, handler, env, http.MethodPost, "/v1/account/email/change/request",
		map[string]any{"newEmail": newEmail}, supportSession(session), supportWebMutation())
	if response.Code != http.StatusAccepted {
		t.Fatalf("email change request = %d: %s", response.Code, response.Body.String())
	}
	return outboxToken(t, env.db, "change-email", newEmail)
}

func TestPasswordChangeInvalidatesPendingEmailChangeToken(t *testing.T) {
	env := newSupportTestEnv(t)
	handler := env.emailHandler(supportQueuedEmail{db: env.db})
	accountID, oldEmail, newEmail := accountTokenFixture(t)
	session := env.seedAccount(t, accountID, oldEmail)
	setAccountPassword(t, env, accountID, accountTokenTestPassword)
	changeToken := requestEmailChangeToken(t, handler, env, session, newEmail)

	response := requestOn(t, handler, env, http.MethodPost, "/v1/account/password",
		map[string]any{"currentPassword": accountTokenTestPassword, "newPassword": accountTokenNewPassword},
		supportSession(session), supportWebMutation())
	if response.Code != http.StatusOK {
		t.Fatalf("password change = %d: %s", response.Code, response.Body.String())
	}

	response = requestOn(t, handler, env, http.MethodPost, "/v1/account/email/change/confirm",
		map[string]any{"token": changeToken}, supportWebMutation())
	if response.Code != http.StatusBadRequest {
		t.Fatalf("stale email-change confirm = %d, want 400: %s", response.Code, response.Body.String())
	}
	if code := errorCode(t, response); code != "email_change_expired" {
		t.Fatalf("stale email-change confirm code = %q, want email_change_expired", code)
	}
	assertAccountEmail(t, env.db, accountID, oldEmail)
}

func TestPasswordResetInvalidatesPendingEmailChangeToken(t *testing.T) {
	env := newSupportTestEnv(t)
	handler := env.emailHandler(supportQueuedEmail{db: env.db})
	accountID, oldEmail, newEmail := accountTokenFixture(t)
	session := env.seedAccount(t, accountID, oldEmail)
	setAccountPassword(t, env, accountID, accountTokenTestPassword)
	changeToken := requestEmailChangeToken(t, handler, env, session, newEmail)

	response := requestOn(t, handler, env, http.MethodPost, "/v1/auth/password/recovery/request",
		map[string]any{"email": oldEmail}, supportWebMutation())
	if response.Code != http.StatusAccepted {
		t.Fatalf("recovery request = %d: %s", response.Code, response.Body.String())
	}
	recoveryToken := outboxToken(t, env.db, "recover-password", oldEmail)
	response = requestOn(t, handler, env, http.MethodPost, "/v1/auth/password/recovery/confirm",
		map[string]any{"token": recoveryToken, "newPassword": accountTokenNewPassword}, supportWebMutation())
	if response.Code != http.StatusOK {
		t.Fatalf("recovery confirm = %d: %s", response.Code, response.Body.String())
	}

	response = requestOn(t, handler, env, http.MethodPost, "/v1/account/email/change/confirm",
		map[string]any{"token": changeToken}, supportWebMutation())
	if response.Code != http.StatusBadRequest {
		t.Fatalf("stale email-change confirm = %d, want 400: %s", response.Code, response.Body.String())
	}
	if code := errorCode(t, response); code != "email_change_expired" {
		t.Fatalf("stale email-change confirm code = %q, want email_change_expired", code)
	}
	assertAccountEmail(t, env.db, accountID, oldEmail)
}

func TestEmailChangeInvalidatesPendingRecoveryToken(t *testing.T) {
	env := newSupportTestEnv(t)
	handler := env.emailHandler(supportQueuedEmail{db: env.db})
	accountID, oldEmail, newEmail := accountTokenFixture(t)
	session := env.seedAccount(t, accountID, oldEmail)
	setAccountPassword(t, env, accountID, accountTokenTestPassword)

	response := requestOn(t, handler, env, http.MethodPost, "/v1/auth/password/recovery/request",
		map[string]any{"email": oldEmail}, supportWebMutation())
	if response.Code != http.StatusAccepted {
		t.Fatalf("recovery request = %d: %s", response.Code, response.Body.String())
	}
	recoveryToken := outboxToken(t, env.db, "recover-password", oldEmail)
	changeToken := requestEmailChangeToken(t, handler, env, session, newEmail)

	response = requestOn(t, handler, env, http.MethodPost, "/v1/account/email/change/confirm",
		map[string]any{"token": changeToken}, supportWebMutation())
	if response.Code != http.StatusOK {
		t.Fatalf("email change confirm = %d: %s", response.Code, response.Body.String())
	}
	assertAccountEmail(t, env.db, accountID, newEmail)

	response = requestOn(t, handler, env, http.MethodPost, "/v1/auth/password/recovery/confirm",
		map[string]any{"token": recoveryToken, "newPassword": accountTokenNewPassword}, supportWebMutation())
	if response.Code != http.StatusBadRequest {
		t.Fatalf("stale recovery confirm = %d, want 400: %s", response.Code, response.Body.String())
	}
	if code := errorCode(t, response); code != "recovery_expired" {
		t.Fatalf("stale recovery confirm code = %q, want recovery_expired", code)
	}
	if _, passwordHash, err := env.accountStore.FindByID(context.Background(), accountID); err != nil {
		t.Fatalf("read account password: %v", err)
	} else if !accounts.VerifyPassword(passwordHash, accountTokenTestPassword) {
		t.Fatal("a stale recovery token changed the password")
	}

	type notice struct {
		kind      string
		actionURL string
		subject   string
		body      string
	}
	rows, err := env.db.QueryContext(context.Background(), `
		SELECT kind, action_url, subject, body FROM sesame_email_outbox
		WHERE kind = 'security-email-changed' AND to_email = $1
	`, oldEmail)
	if err != nil {
		t.Fatalf("read old-address notices: %v", err)
	}
	defer rows.Close()
	var notices []notice
	for rows.Next() {
		var item notice
		if err := rows.Scan(&item.kind, &item.actionURL, &item.subject, &item.body); err != nil {
			t.Fatalf("scan old-address notice: %v", err)
		}
		notices = append(notices, item)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate old-address notices: %v", err)
	}
	if len(notices) != 1 {
		t.Fatalf("old address received %d security notices, want 1", len(notices))
	}
	if notices[0].actionURL != "" {
		t.Fatalf("old-address notice action url = %q, want empty", notices[0].actionURL)
	}
	for _, secret := range []string{changeToken, recoveryToken} {
		if strings.Contains(notices[0].subject, secret) || strings.Contains(notices[0].body, secret) {
			t.Fatal("old-address notice contains a token")
		}
	}
}
