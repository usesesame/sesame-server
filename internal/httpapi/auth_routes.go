package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"time"

	"usesesame.app/backend/internal/accounts"
)

const (
	termsVersion  = "2026-08-18"
	privacyVersion = "2026-08-18"
)

type authRequest struct {
	Email               string `json:"email"`
	Password            string `json:"password"`
	InviteCode          string `json:"inviteCode,omitempty"`
	TermsAccepted       bool   `json:"termsAccepted,omitempty"`
	TermsVersion        string `json:"termsVersion,omitempty"`
	PrivacyAcknowledged bool   `json:"privacyAcknowledged,omitempty"`
	PrivacyVersion      string `json:"privacyVersion,omitempty"`
}

type passwordChangeRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

type accountDeleteRequest struct {
	Password string `json:"password"`
}

func (a *api) csrf(response http.ResponseWriter, request *http.Request) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		writeError(response, http.StatusServiceUnavailable, "csrf_unavailable", "The website security token is temporarily unavailable.")
		return
	}
	token := base64.RawURLEncoding.EncodeToString(bytes)
	http.SetCookie(response, &http.Cookie{
		Name: a.csrfCookieName(), Value: token, Path: "/", Domain: a.config.SessionDomain,
		MaxAge: 3600, HttpOnly: true, Secure: a.config.SessionSecure, SameSite: http.SameSiteStrictMode,
	})
	writeJSON(response, http.StatusOK, map[string]string{"token": token})
}

func (a *api) validCSRF(request *http.Request) bool {
	header := request.Header.Get("X-Sesame-CSRF")
	cookie, err := request.Cookie(a.csrfCookieName())
	if err != nil || header == "" || cookie.Value == "" || len(header) != len(cookie.Value) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(header), []byte(cookie.Value)) == 1
}

func (a *api) register(response http.ResponseWriter, request *http.Request) {
	if !a.requireAccounts(response) || !a.allowAuthAttempt(response, request, "register") {
		return
	}
	registrationMode := a.runtimeRegistrationMode(request.Context())
	if registrationMode == "closed" {
		writeError(response, http.StatusForbidden, "registration_closed", "Sesame beta registration is not open.")
		return
	}
	store, ok := a.accountSecurity(response)
	if !ok {
		return
	}
	input, ok := decodeAuthRequest(response, request)
	if !ok {
		return
	}
	email, valid := normalizedEmail(input.Email)
	if !valid || !validPassword(input.Password) || len(input.InviteCode) > 256 {
		writeError(response, http.StatusBadRequest, "invalid_registration", "Use a valid email address and a password of 12 to 1024 characters.")
		return
	}
	if !a.allowIdentity(response, request, "register", email, identityMailLimit, identityMailWindow) {
		return
	}
	if !input.TermsAccepted || !input.PrivacyAcknowledged || input.TermsVersion != termsVersion || input.PrivacyVersion != privacyVersion {
		writeError(response, http.StatusBadRequest, "legal_documents_not_accepted", "Read and accept the current Terms of Use and acknowledge the Privacy Policy to create an account.")
		return
	}
	passwordHash, err := accounts.HashPassword(input.Password)
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "registration_unavailable", "Account registration is temporarily unavailable.")
		return
	}
	token, tokenHash, err := accounts.NewSessionToken()
	verificationToken, verificationHash, verificationErr := accounts.NewSessionToken()
	if err != nil || verificationErr != nil {
		writeError(response, http.StatusServiceUnavailable, "registration_unavailable", "Account registration is temporarily unavailable.")
		return
	}
	var inviteHash []byte
	if input.InviteCode != "" {
		inviteHash = accounts.HashSessionToken(input.InviteCode)
	}
	now := time.Now().UTC()
	user, err := store.RegisterEligible(request.Context(), accounts.Registration{
		Email: email, PasswordHash: passwordHash, SessionTokenHash: tokenHash,
		SessionExpiresAt: now.Add(a.config.SessionDuration), SessionLabel: browserLabel(request),
		VerificationTokenHash: verificationHash, VerificationExpiresAt: now.Add(emailVerificationTTL),
		InviteHash: inviteHash, AllowPublic: registrationMode == "public",
		TermsAcceptedAt: now, TermsVersion: termsVersion,
		PrivacyAcknowledgedAt: now, PrivacyVersion: privacyVersion,
	})
	if errors.Is(err, accounts.ErrNotEligible) {
		writeError(response, http.StatusForbidden, "registration_not_eligible", "This beta invitation is unavailable or does not match that email address.")
		return
	}
	if errors.Is(err, accounts.ErrEmailTaken) {
		writeError(response, http.StatusConflict, "account_unavailable", "That email address cannot be registered.")
		return
	}
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "registration_unavailable", "Account registration is temporarily unavailable.")
		return
	}
	a.setSessionCookie(response, token)
	verificationQueued := false
	if a.config.EmailSender != nil {
		verificationQueued = a.sendAccountEmail(request.Context(), "verify-email", user.Email, verificationToken, now.Add(emailVerificationTTL)) == nil
	}
	writeJSON(response, http.StatusCreated, map[string]any{"user": user, "verificationQueued": verificationQueued})
}

func (a *api) login(response http.ResponseWriter, request *http.Request) {
	if !a.requireAccounts(response) || !a.allowAuthAttempt(response, request, "login") {
		return
	}
	input, ok := decodeAuthRequest(response, request)
	if !ok {
		return
	}
	email, valid := normalizedEmail(input.Email)
	if !valid || len(input.Password) == 0 {
		writeError(response, http.StatusUnauthorized, "invalid_credentials", "Email or password is incorrect.")
		return
	}
	// The budget is spent for an unregistered address too, so a refusal says nothing about whether the account exists.
	if !a.allowIdentity(response, request, "login", email, identityGuessLimit, identityGuessWindow) {
		return
	}
	user, passwordHash, err := a.config.Accounts.FindByEmail(request.Context(), email)
	if err != nil {
		accounts.DummyVerifyPassword()
		writeError(response, http.StatusUnauthorized, "invalid_credentials", "Email or password is incorrect.")
		return
	}
	if !accounts.VerifyPassword(passwordHash, input.Password) {
		writeError(response, http.StatusUnauthorized, "invalid_credentials", "Email or password is incorrect.")
		return
	}
	if user.Suspended {
		writeError(response, http.StatusLocked, "account_suspended", "This Sesame account is suspended. Contact support if you think this is a mistake.")
		return
	}
	if accounts.NeedsRehash(passwordHash) {
		if newHash, hashErr := accounts.HashPassword(input.Password); hashErr == nil {
			_ = a.config.Accounts.UpdatePassword(request.Context(), user.ID, newHash)
		}
	}
	token, tokenHash, err := accounts.NewSessionToken()
	if err != nil || a.config.Accounts.CreateSession(request.Context(), user.ID, tokenHash, time.Now().Add(a.config.SessionDuration)) != nil {
		writeError(response, http.StatusServiceUnavailable, "login_unavailable", "Sign in is temporarily unavailable.")
		return
	}
	a.setSessionCookie(response, token)
	a.recordAccountEvent(request.Context(), user.ID, "sign_in", browserLabel(request), nil)
	a.sendSecurityNotification(request.Context(), user, "security-sign-in", "New Sesame account sign-in", "A new sign-in to your Sesame website account was completed from: "+browserLabel(request)+".")
	writeJSON(response, http.StatusOK, map[string]any{"user": user})
}

func (a *api) logout(response http.ResponseWriter, request *http.Request) {
	if !a.requireAccounts(response) || !a.allowRequest(response, request, "logout", 30, time.Minute) {
		return
	}
	if cookie, err := request.Cookie(a.sessionCookieName()); err == nil && cookie.Value != "" {
		_ = a.config.Accounts.DeleteSession(request.Context(), accounts.HashSessionToken(cookie.Value))
	}
	a.clearSessionCookie(response)
	response.WriteHeader(http.StatusNoContent)
}

func (a *api) me(response http.ResponseWriter, request *http.Request) {
	if !a.requireAccounts(response) || !a.allowRequest(response, request, "me", 60, time.Minute) {
		return
	}
	user, ok := a.userForRequest(response, request)
	if !ok {
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"user": user})
}

func (a *api) changePassword(response http.ResponseWriter, request *http.Request) {
	if !a.requireAccounts(response) || !a.allowAuthAttempt(response, request, "password") {
		return
	}
	store, ok := a.accountSecurity(response)
	if !ok {
		return
	}
	user, _, _, ok := a.recentSessionForRequest(response, request)
	if !ok {
		return
	}
	// The current-password check is a password oracle bound to one account.
	if !a.allowIdentity(response, request, "password-change", user.ID, identityGuessLimit, identityGuessWindow) {
		return
	}
	input, ok := decodePasswordChange(response, request)
	if !ok {
		return
	}
	_, passwordHash, err := a.config.Accounts.FindByID(request.Context(), user.ID)
	if err != nil || !accounts.VerifyPassword(passwordHash, input.CurrentPassword) {
		writeError(response, http.StatusUnauthorized, "invalid_credentials", "Your current password is incorrect.")
		return
	}
	if !validPassword(input.NewPassword) {
		writeError(response, http.StatusBadRequest, "invalid_password", "Use a new password of 12 to 1024 characters.")
		return
	}
	if accounts.VerifyPassword(passwordHash, input.NewPassword) {
		writeError(response, http.StatusBadRequest, "password_unchanged", "Choose a password different from your current one.")
		return
	}
	newHash, err := accounts.HashPassword(input.NewPassword)
	token, tokenHash, tokenErr := accounts.NewSessionToken()
	if err != nil || tokenErr != nil {
		writeError(response, http.StatusServiceUnavailable, "password_update_unavailable", "Changing your password is temporarily unavailable.")
		return
	}
	now := time.Now().UTC()
	if err := store.ChangePasswordAndRotateSession(request.Context(), accounts.PasswordRotation{
		AccountID: user.ID, ExpectedPasswordHash: passwordHash, PasswordHash: newHash, SessionTokenHash: tokenHash,
		SessionExpiresAt: now.Add(a.config.SessionDuration), SessionLabel: browserLabel(request), AuthenticatedAt: now,
	}); err != nil {
		writeError(response, http.StatusServiceUnavailable, "password_update_unavailable", "Changing your password is temporarily unavailable.")
		return
	}
	a.setSessionCookie(response, token)
	a.recordAccountEvent(request.Context(), user.ID, "password_changed", browserLabel(request), nil)
	a.sendSecurityNotification(request.Context(), user, "security-password-changed", "Your Sesame account password changed", "Your Sesame website-account password was changed. Other website sessions were revoked.")
	writeJSON(response, http.StatusOK, map[string]any{"user": user, "otherSessionsRevoked": true})
}

func (a *api) deleteAccount(response http.ResponseWriter, request *http.Request) {
	if !a.requireAccounts(response) || !a.allowAuthAttempt(response, request, "delete") {
		return
	}
	user, _, _, ok := a.recentSessionForRequest(response, request)
	if !ok {
		return
	}
	if !a.allowIdentity(response, request, "account-delete", user.ID, identityGuessLimit, identityGuessWindow) {
		return
	}
	input, ok := decodeAccountDelete(response, request)
	if !ok {
		return
	}
	_, passwordHash, err := a.config.Accounts.FindByID(request.Context(), user.ID)
	if err != nil || !accounts.VerifyPassword(passwordHash, input.Password) {
		writeError(response, http.StatusUnauthorized, "invalid_credentials", "Your password is incorrect.")
		return
	}
	if err := a.config.Accounts.DeleteAccount(request.Context(), user.ID); err != nil {
		writeError(response, http.StatusServiceUnavailable, "account_delete_unavailable", "Deleting your account is temporarily unavailable.")
		return
	}
	a.clearSessionCookie(response)
	response.WriteHeader(http.StatusNoContent)
}

func (a *api) recordAccountEvent(ctx context.Context, accountID, eventType, label string, metadata map[string]string) {
	store, ok := a.config.Accounts.(accounts.AccountActivityStore)
	if !ok {
		return
	}
	_ = store.RecordAccountEvent(ctx, accounts.AccountEvent{AccountID: accountID, Type: eventType, Label: label, Metadata: metadata})
}

func (a *api) sendSecurityNotification(ctx context.Context, user accounts.User, kind, subject, body string) {
	if a.config.EmailSender == nil || user.Email == "" {
		return
	}
	// Mandatory; contains no action link, token, vault identifier, or raw network address.
	_ = a.config.EmailSender.SendAccountEmail(ctx, AccountEmail{
		Kind: kind, To: user.Email, Subject: subject, Body: body,
		ExpiresAt: time.Now().UTC().Add(7 * 24 * time.Hour),
	})
}
