package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

const registrationPassword = "fictional-attacker-password"

func credentialSetupRequiredIn(t *testing.T, body string) bool {
	t.Helper()
	var decoded struct {
		CredentialSetupRequired bool `json:"credentialSetupRequired"`
	}
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("decode verification response: %v: %s", err, body)
	}
	return decoded.CredentialSetupRequired
}

func accountVerified(t *testing.T, env *preHijackEnv, accountID string) bool {
	t.Helper()
	return env.countRows(t, `SELECT COUNT(*) FROM sesame_accounts WHERE id = $1 AND email_verified_at IS NOT NULL`, accountID) == 1
}

func TestVerifyingWithTheRegistrationPasswordKeepsIt(t *testing.T) {
	env := newPreHijackEnv(t)
	email := env.uniqueEmail("keep")
	registrant := newPreHijackClient(t, env)
	accountID, _ := env.registerAccount(t, registrant, email)

	verified := registrant.request(http.MethodPost, "/v1/auth/email/verification/confirm", map[string]any{
		"token": env.emails.token(t, "verify-email", email), "password": registrationPassword,
	})
	if verified.Code != http.StatusOK {
		t.Fatalf("verification status = %d: %s", verified.Code, verified.Body.String())
	}
	if credentialSetupRequiredIn(t, verified.Body.String()) {
		t.Fatal("verification asked a registrant who proved the password to choose another one")
	}
	if !accountVerified(t, env, accountID) {
		t.Fatal("account is not verified")
	}
	later := newPreHijackClient(t, env)
	if signIn := later.request(http.MethodPost, "/v1/auth/login", map[string]any{"email": email, "password": registrationPassword}); signIn.Code != http.StatusOK {
		t.Fatalf("sign in with the kept password status = %d: %s", signIn.Code, signIn.Body.String())
	}
}

func TestAWrongPasswordRefusesVerificationAndKeepsTheLinkUsable(t *testing.T) {
	env := newPreHijackEnv(t)
	email := env.uniqueEmail("wrong")
	registrant := newPreHijackClient(t, env)
	accountID, attackerSession := env.registerAccount(t, registrant, email)
	token := env.emails.token(t, "verify-email", email)

	wrong := registrant.request(http.MethodPost, "/v1/auth/email/verification/confirm", map[string]any{"token": token, "password": "fictional-some-other-password"})
	if wrong.Code != http.StatusUnauthorized || !strings.Contains(wrong.Body.String(), "incorrect_password") {
		t.Fatalf("wrong password status = %d: %s", wrong.Code, wrong.Body.String())
	}
	if accountVerified(t, env, accountID) {
		t.Fatal("a wrong password verified the account")
	}
	stillIn := newPreHijackClient(t, env)
	stillIn.cookies[sessionCookieName] = &http.Cookie{Name: sessionCookieName, Value: attackerSession}
	if me := stillIn.request(http.MethodGet, "/v1/auth/me", nil); me.Code != http.StatusOK {
		t.Fatalf("a refused verification revoked the session: status %d", me.Code)
	}

	chosenNew := registrant.request(http.MethodPost, "/v1/auth/email/verification/confirm", map[string]any{"token": token})
	if chosenNew.Code != http.StatusOK {
		t.Fatalf("the link did not survive a wrong password: status %d: %s", chosenNew.Code, chosenNew.Body.String())
	}
	if !credentialSetupRequiredIn(t, chosenNew.Body.String()) {
		t.Fatal("verifying without the password did not ask for a new one")
	}
}

func TestAPrehijackerPasswordDoesNotSurviveTheOwnersVerification(t *testing.T) {
	env := newPreHijackEnv(t)
	email := env.uniqueEmail("prehijack")
	attacker := newPreHijackClient(t, env)
	env.registerAccount(t, attacker, email)
	token := env.emails.token(t, "verify-email", email)

	owner := newPreHijackClient(t, env)
	guess := owner.request(http.MethodPost, "/v1/auth/email/verification/confirm", map[string]any{"token": token, "password": "fictional-owners-own-password"})
	if guess.Code != http.StatusUnauthorized {
		t.Fatalf("the owner's own password was accepted for an account the attacker registered: status %d", guess.Code)
	}
	verified := owner.request(http.MethodPost, "/v1/auth/email/verification/confirm", map[string]any{"token": token})
	if verified.Code != http.StatusOK {
		t.Fatalf("owner verification status = %d: %s", verified.Code, verified.Body.String())
	}
	if !credentialSetupRequiredIn(t, verified.Body.String()) {
		t.Fatal("the owner was not asked to choose a password")
	}
	late := newPreHijackClient(t, env)
	if signIn := late.request(http.MethodPost, "/v1/auth/login", map[string]any{"email": email, "password": registrationPassword}); signIn.Code != http.StatusUnauthorized {
		t.Fatalf("the attacker's password still signs in: status %d", signIn.Code)
	}
}

func TestAVerificationLinkCannotBeReplayedWithThePassword(t *testing.T) {
	env := newPreHijackEnv(t)
	email := env.uniqueEmail("replay")
	registrant := newPreHijackClient(t, env)
	env.registerAccount(t, registrant, email)
	token := env.emails.token(t, "verify-email", email)
	body := map[string]any{"token": token, "password": registrationPassword}
	if first := registrant.request(http.MethodPost, "/v1/auth/email/verification/confirm", body); first.Code != http.StatusOK {
		t.Fatalf("first verification status = %d: %s", first.Code, first.Body.String())
	}
	replay := newPreHijackClient(t, env).request(http.MethodPost, "/v1/auth/email/verification/confirm", body)
	if replay.Code != http.StatusBadRequest {
		t.Fatalf("replayed link status = %d: %s", replay.Code, replay.Body.String())
	}
}

func TestAnOversizedVerificationPasswordIsRefusedBeforeItIsHashed(t *testing.T) {
	env := newPreHijackEnv(t)
	email := env.uniqueEmail("oversized")
	registrant := newPreHijackClient(t, env)
	env.registerAccount(t, registrant, email)
	response := registrant.request(http.MethodPost, "/v1/auth/email/verification/confirm", map[string]any{
		"token": env.emails.token(t, "verify-email", email), "password": strings.Repeat("a", 1025),
	})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("oversized password status = %d: %s", response.Code, response.Body.String())
	}
}
