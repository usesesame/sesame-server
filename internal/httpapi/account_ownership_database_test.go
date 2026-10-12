package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"usesesame.app/backend/internal/accounts"
)

const (
	ownerPassword        = "fictional-owner-password"
	ownerReplacementWord = "fictional-replacement-password"
)

func decodeBody(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(response.Body.Bytes(), target); err != nil {
		t.Fatalf("decode %q: %v", response.Body.String(), err)
	}
}

func (e *preHijackEnv) verifyOwner(t *testing.T, client *preHijackClient, email string) *httptest.ResponseRecorder {
	t.Helper()
	verified := client.request(http.MethodPost, "/v1/auth/email/verification/confirm", map[string]any{"token": e.emails.token(t, "verify-email", email)})
	if verified.Code != http.StatusOK {
		t.Fatalf("verification status = %d: %s", verified.Code, verified.Body.String())
	}
	return verified
}

func (e *preHijackEnv) recoverPassword(t *testing.T, client *preHijackClient, email, password string) {
	t.Helper()
	requested := client.request(http.MethodPost, "/v1/auth/password/recovery/request", map[string]any{"email": email})
	if requested.Code != http.StatusAccepted {
		t.Fatalf("recovery request status = %d: %s", requested.Code, requested.Body.String())
	}
	confirmed := client.request(http.MethodPost, "/v1/auth/password/recovery/confirm", map[string]any{
		"token": e.emails.token(t, "recover-password", email), "newPassword": password,
	})
	if confirmed.Code != http.StatusOK {
		t.Fatalf("recovery status = %d: %s", confirmed.Code, confirmed.Body.String())
	}
}

func (e *preHijackEnv) verifiedOwner(t *testing.T, label string) (*preHijackClient, string, string) {
	t.Helper()
	email := e.uniqueEmail(label)
	client := newPreHijackClient(t, e)
	accountID, _ := e.registerAccount(t, client, email)
	e.verifyOwner(t, client, email)
	e.recoverPassword(t, client, email, ownerPassword)
	return client, accountID, email
}

func (c *preHijackClient) issueDesktopCode(t *testing.T) string {
	t.Helper()
	created := c.request(http.MethodPost, "/v1/account/desktop-link", nil)
	if created.Code != http.StatusCreated {
		t.Fatalf("desktop link status = %d: %s", created.Code, created.Body.String())
	}
	var payload struct {
		Code string `json:"code"`
	}
	decodeBody(t, created, &payload)
	if payload.Code == "" {
		t.Fatal("desktop link response has no code")
	}
	return payload.Code
}

func (c *preHijackClient) redeemDesktopCode(code string) *httptest.ResponseRecorder {
	c.t.Helper()
	return c.desktopRequest(http.MethodPost, "/v1/desktop/link", "", map[string]any{"code": code, "deviceName": "Fictional desktop"})
}

func TestPendingDesktopLinkCodeDoesNotSurviveARecoveryAction(t *testing.T) {
	env := newPreHijackEnv(t)
	actions := []struct {
		name string
		run  func(t *testing.T, client *preHijackClient, email string)
	}{
		{"password change", func(t *testing.T, client *preHijackClient, _ string) {
			changed := client.request(http.MethodPost, "/v1/account/password", map[string]any{"currentPassword": ownerPassword, "newPassword": ownerReplacementWord})
			if changed.Code != http.StatusOK {
				t.Fatalf("password change status = %d: %s", changed.Code, changed.Body.String())
			}
		}},
		{"password reset", func(t *testing.T, client *preHijackClient, email string) {
			env.recoverPassword(t, client, email, ownerReplacementWord)
		}},
		{"sign out everywhere", func(t *testing.T, client *preHijackClient, _ string) {
			revoked := client.request(http.MethodDelete, "/v1/account/sessions", nil)
			if revoked.Code != http.StatusNoContent {
				t.Fatalf("sign out everywhere status = %d: %s", revoked.Code, revoked.Body.String())
			}
		}},
	}
	for _, action := range actions {
		t.Run(action.name, func(t *testing.T) {
			client, accountID, email := env.verifiedOwner(t, "codes")
			pending := client.issueDesktopCode(t)
			stranger := newPreHijackClient(t, env)

			action.run(t, client, email)

			late := stranger.redeemDesktopCode(pending)
			if late.Code != http.StatusUnauthorized {
				t.Fatalf("code issued before the %s status = %d: %s", action.name, late.Code, late.Body.String())
			}
			if count := env.countRows(t, `SELECT COUNT(*) FROM sesame_desktop_connections WHERE account_id = $1`, accountID); count != 0 {
				t.Fatalf("desktop connections after the rejected code = %d, want 0", count)
			}
		})
	}
}

func TestFreshDesktopLinkCodeStillWorksAfterARecoveryAction(t *testing.T) {
	env := newPreHijackEnv(t)
	client, accountID, _ := env.verifiedOwner(t, "fresh-code")
	if changed := client.request(http.MethodPost, "/v1/account/password", map[string]any{"currentPassword": ownerPassword, "newPassword": ownerReplacementWord}); changed.Code != http.StatusOK {
		t.Fatalf("password change status = %d: %s", changed.Code, changed.Body.String())
	}
	fresh := client.issueDesktopCode(t)
	linked := newPreHijackClient(t, env).redeemDesktopCode(fresh)
	if linked.Code != http.StatusCreated && linked.Code != http.StatusOK {
		t.Fatalf("code issued after the password change status = %d: %s", linked.Code, linked.Body.String())
	}
	if count := env.countRows(t, `SELECT COUNT(*) FROM sesame_desktop_connections WHERE account_id = $1`, accountID); count != 1 {
		t.Fatalf("desktop connections after one redemption = %d, want 1", count)
	}
	replay := newPreHijackClient(t, env).redeemDesktopCode(fresh)
	if replay.Code != http.StatusUnauthorized {
		t.Fatalf("replayed code status = %d: %s", replay.Code, replay.Body.String())
	}
}

func TestEmailChangeTokenIssuedBeforeOwnershipProofDoesNotSurviveVerification(t *testing.T) {
	env := newPreHijackEnv(t)
	victimAddress := env.uniqueEmail("pre-change")
	attackerAddress := env.uniqueEmail("attacker-mailbox")
	attacker := newPreHijackClient(t, env)
	accountID, _ := env.registerAccount(t, attacker, victimAddress)

	changeToken, changeHash, err := accounts.NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.db.ExecContext(context.Background(), `
		INSERT INTO sesame_account_tokens (token_hash, account_id, purpose, payload, expires_at)
		VALUES ($1, $2, 'change_email', $3, $4)
	`, changeHash, accountID, attackerAddress, time.Now().UTC().Add(30*time.Minute)); err != nil {
		t.Fatalf("seed the pre-verification email-change token: %v", err)
	}

	owner := newPreHijackClient(t, env)
	env.verifyOwner(t, owner, victimAddress)

	replay := newPreHijackClient(t, env)
	confirmed := replay.request(http.MethodPost, "/v1/account/email/change/confirm", map[string]any{"token": changeToken})
	if confirmed.Code != http.StatusBadRequest || errorCode(t, confirmed) != "email_change_expired" {
		t.Fatalf("pre-verification email-change token status = %d: %s", confirmed.Code, confirmed.Body.String())
	}
	if replay.cookies[sessionCookieName] != nil {
		t.Fatal("a refused email-change confirmation issued a browser session")
	}
	var email string
	if err := env.db.QueryRowContext(context.Background(), `SELECT email FROM sesame_accounts WHERE id = $1`, accountID).Scan(&email); err != nil {
		t.Fatalf("read account email: %v", err)
	}
	if email != victimAddress {
		t.Fatalf("account email = %q, want the verified address %q", email, victimAddress)
	}
}

func TestUnverifiedAccountCannotRequestAnEmailChange(t *testing.T) {
	env := newPreHijackEnv(t)
	address := env.uniqueEmail("unverified-change")
	target := env.uniqueEmail("unverified-target")
	client := newPreHijackClient(t, env)
	accountID, _ := env.registerAccount(t, client, address)

	requested := client.request(http.MethodPost, "/v1/account/email/change/request", map[string]any{"newEmail": target})
	if requested.Code != http.StatusForbidden || errorCode(t, requested) != "email_verification_required" {
		t.Fatalf("email change for an unverified account status = %d: %s", requested.Code, requested.Body.String())
	}
	if count := env.countRows(t, `SELECT COUNT(*) FROM sesame_account_tokens WHERE account_id = $1 AND purpose = 'change_email'`, accountID); count != 0 {
		t.Fatalf("email-change tokens for an unverified account = %d, want 0", count)
	}
}

func TestVerifiedOwnerSetsAPasswordLinksADesktopAndSignsInAgain(t *testing.T) {
	env := newPreHijackEnv(t)
	email := env.uniqueEmail("journey")
	owner := newPreHijackClient(t, env)
	accountID, _ := env.registerAccount(t, owner, email)

	verified := env.verifyOwner(t, owner, email)
	var verification struct {
		CredentialSetupRequired bool `json:"credentialSetupRequired"`
	}
	decodeBody(t, verified, &verification)
	if !verification.CredentialSetupRequired {
		t.Fatalf("verification response does not ask for credential setup: %s", verified.Body.String())
	}
	if signIn := newPreHijackClient(t, env).request(http.MethodPost, "/v1/auth/login", map[string]any{"email": email, "password": "fictional-attacker-password"}); signIn.Code != http.StatusUnauthorized {
		t.Fatalf("registration password after verification status = %d: %s", signIn.Code, signIn.Body.String())
	}
	bootstrap := owner.request(http.MethodGet, "/v1/account/bootstrap", nil)
	var before struct {
		Security struct {
			CredentialSetupRequired bool `json:"credentialSetupRequired"`
		} `json:"security"`
	}
	decodeBody(t, bootstrap, &before)
	if !before.Security.CredentialSetupRequired {
		t.Fatalf("bootstrap does not report that credential setup is required: %s", bootstrap.Body.String())
	}

	if set := owner.request(http.MethodPost, "/v1/account/password/setup", map[string]any{"newPassword": ownerPassword}); set.Code != http.StatusNoContent {
		t.Fatalf("password setup status = %d: %s", set.Code, set.Body.String())
	}
	bootstrap = owner.request(http.MethodGet, "/v1/account/bootstrap", nil)
	var after struct {
		Security struct {
			CredentialSetupRequired bool `json:"credentialSetupRequired"`
		} `json:"security"`
	}
	decodeBody(t, bootstrap, &after)
	if after.Security.CredentialSetupRequired {
		t.Fatalf("bootstrap still asks for credential setup: %s", bootstrap.Body.String())
	}

	code := owner.issueDesktopCode(t)
	if linked := newPreHijackClient(t, env).redeemDesktopCode(code); linked.Code != http.StatusCreated && linked.Code != http.StatusOK {
		t.Fatalf("desktop link status = %d: %s", linked.Code, linked.Body.String())
	}
	if count := env.countRows(t, `SELECT COUNT(*) FROM sesame_desktop_connections WHERE account_id = $1`, accountID); count != 1 {
		t.Fatalf("desktop connections = %d, want 1", count)
	}

	if out := owner.request(http.MethodPost, "/v1/auth/logout", nil); out.Code != http.StatusNoContent {
		t.Fatalf("sign out status = %d: %s", out.Code, out.Body.String())
	}
	again := newPreHijackClient(t, env)
	signedIn := again.request(http.MethodPost, "/v1/auth/login", map[string]any{"email": email, "password": ownerPassword})
	if signedIn.Code != http.StatusOK {
		t.Fatalf("sign in with the new password status = %d: %s", signedIn.Code, signedIn.Body.String())
	}
	if user := decodeResponseUser(t, signedIn); user.ID != accountID || !user.EmailVerified {
		t.Fatalf("signed-in user = %+v", user)
	}
}

func TestPasswordSetupRefusesMalformedStaleUnauthorizedAndRepeatedRequests(t *testing.T) {
	env := newPreHijackEnv(t)
	email := env.uniqueEmail("setup-refusals")
	owner := newPreHijackClient(t, env)
	accountID, _ := env.registerAccount(t, owner, email)
	setup := func(client *preHijackClient, body any) *httptest.ResponseRecorder {
		return client.request(http.MethodPost, "/v1/account/password/setup", body)
	}

	if refused := setup(owner, map[string]any{"newPassword": ownerPassword}); refused.Code != http.StatusForbidden || errorCode(t, refused) != "email_verification_required" {
		t.Fatalf("setup before verification status = %d: %s", refused.Code, refused.Body.String())
	}
	env.verifyOwner(t, owner, email)

	if refused := setup(newPreHijackClient(t, env), map[string]any{"newPassword": ownerPassword}); refused.Code != http.StatusUnauthorized {
		t.Fatalf("setup without a session status = %d: %s", refused.Code, refused.Body.String())
	}
	if refused := setup(owner, map[string]any{"newPassword": "short"}); refused.Code != http.StatusBadRequest || errorCode(t, refused) != "invalid_password" {
		t.Fatalf("setup with a short password status = %d: %s", refused.Code, refused.Body.String())
	}
	if refused := setup(owner, map[string]any{"newPassword": ownerPassword, "currentPassword": "unexpected"}); refused.Code != http.StatusBadRequest {
		t.Fatalf("setup with an unknown field status = %d: %s", refused.Code, refused.Body.String())
	}
	if refused := setup(owner, nil); refused.Code != http.StatusUnsupportedMediaType && refused.Code != http.StatusBadRequest {
		t.Fatalf("setup without a body status = %d: %s", refused.Code, refused.Body.String())
	}

	if _, err := env.db.ExecContext(context.Background(), `UPDATE sesame_sessions SET authenticated_at = NOW() - INTERVAL '1 hour' WHERE account_id = $1`, accountID); err != nil {
		t.Fatalf("age the session: %v", err)
	}
	if refused := setup(owner, map[string]any{"newPassword": ownerPassword}); refused.Code != http.StatusForbidden || errorCode(t, refused) != "recent_auth_required" {
		t.Fatalf("setup from a stale session status = %d: %s", refused.Code, refused.Body.String())
	}
	if _, err := env.db.ExecContext(context.Background(), `UPDATE sesame_sessions SET authenticated_at = NOW() WHERE account_id = $1`, accountID); err != nil {
		t.Fatalf("refresh the session: %v", err)
	}

	if set := setup(owner, map[string]any{"newPassword": ownerPassword}); set.Code != http.StatusNoContent {
		t.Fatalf("password setup status = %d: %s", set.Code, set.Body.String())
	}
	if repeated := setup(owner, map[string]any{"newPassword": ownerReplacementWord}); repeated.Code != http.StatusConflict || errorCode(t, repeated) != "password_already_set" {
		t.Fatalf("repeated setup status = %d: %s", repeated.Code, repeated.Body.String())
	}
	if signIn := newPreHijackClient(t, env).request(http.MethodPost, "/v1/auth/login", map[string]any{"email": email, "password": ownerReplacementWord}); signIn.Code != http.StatusUnauthorized {
		t.Fatalf("password from the refused repeat status = %d: %s", signIn.Code, signIn.Body.String())
	}
}

func TestPendingDesktopLinkCodeDoesNotSurviveAnEmailChange(t *testing.T) {
	env := newPreHijackEnv(t)
	client, accountID, _ := env.verifiedOwner(t, "change-codes")
	pending := client.issueDesktopCode(t)
	movedAddress := env.uniqueEmail("moved")

	requested := client.request(http.MethodPost, "/v1/account/email/change/request", map[string]any{"newEmail": movedAddress})
	if requested.Code != http.StatusAccepted {
		t.Fatalf("email change request status = %d: %s", requested.Code, requested.Body.String())
	}
	confirmed := client.request(http.MethodPost, "/v1/account/email/change/confirm", map[string]any{"token": env.emails.token(t, "change-email", movedAddress)})
	if confirmed.Code != http.StatusOK {
		t.Fatalf("email change status = %d: %s", confirmed.Code, confirmed.Body.String())
	}

	late := newPreHijackClient(t, env).redeemDesktopCode(pending)
	if late.Code != http.StatusUnauthorized {
		t.Fatalf("code issued before the email change status = %d: %s", late.Code, late.Body.String())
	}
	if count := env.countRows(t, `SELECT COUNT(*) FROM sesame_desktop_connections WHERE account_id = $1`, accountID); count != 0 {
		t.Fatalf("desktop connections after the rejected code = %d, want 0", count)
	}
	fresh := client.issueDesktopCode(t)
	if linked := newPreHijackClient(t, env).redeemDesktopCode(fresh); linked.Code != http.StatusCreated && linked.Code != http.StatusOK {
		t.Fatalf("code issued after the email change status = %d: %s", linked.Code, linked.Body.String())
	}
}
