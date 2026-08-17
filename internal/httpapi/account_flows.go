package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"usesesame.app/backend/internal/accounts"
)

const (
	emailVerificationTTL = 24 * time.Hour
	passwordRecoveryTTL  = 30 * time.Minute
	emailChangeTTL       = 30 * time.Minute
	downloadTicketTTL    = 5 * time.Minute
)

type EmailSender interface {
	SendAccountEmail(context.Context, AccountEmail) error
}

type AccountEmail struct {
	Kind             string
	To               string
	ActionURL        string
	ExpiresAt        time.Time
	Subject          string
	Body             string
	SupportMessageID string
}

type tokenRequest struct {
	Token string `json:"token"`
}

type recoveryRequest struct {
	Email string `json:"email"`
}

type recoveryConfirmRequest struct {
	Token       string `json:"token"`
	NewPassword string `json:"newPassword"`
}

type emailChangeRequest struct {
	NewEmail string `json:"newEmail"`
}

type reauthenticateRequest struct {
	Password string `json:"password"`
}

type notificationPreferencesRequest struct {
	BetaReleases         bool `json:"betaReleases"`
	SupportReplies       bool `json:"supportReplies"`
	ProductAnnouncements bool `json:"productAnnouncements"`
}

type downloadTicketRequest struct {
	ReleaseID string `json:"releaseId"`
	Platform  string `json:"platform"`
}

func (a *api) registrationStatus(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodGet) {
		return
	}
	mode := a.runtimeRegistrationMode(request.Context())
	_, securityReady := a.config.Accounts.(accounts.AccountSecurityStore)
	writeJSON(response, http.StatusOK, map[string]any{
		"mode":                   mode,
		"enabled":                mode != "closed" && securityReady,
		"requiresInvite":         mode == "invite",
		"emailDeliveryAvailable": a.config.EmailSender != nil,
	})
}

func (a *api) requestEmailVerification(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodPost) || !a.requireAccounts(response) || !a.allowAuthAttempt(response, request, "email-verification") {
		return
	}
	if request.ContentLength > 0 || len(request.TransferEncoding) > 0 {
		writeError(response, http.StatusUnsupportedMediaType, "request_body_not_supported", "This request does not accept a body.")
		return
	}
	store, ok := a.accountSecurity(response)
	if !ok {
		return
	}
	if a.config.EmailSender == nil {
		writeError(response, http.StatusServiceUnavailable, "email_delivery_unavailable", "Account email is temporarily unavailable.")
		return
	}
	user, ok := a.userForRequest(response, request)
	if !ok {
		return
	}
	if user.EmailVerified {
		response.WriteHeader(http.StatusAccepted)
		return
	}
	if !a.allowIdentity(response, request, "email-verification", user.ID, identityMailLimit, identityMailWindow) {
		return
	}
	token, tokenHash, err := accounts.NewSessionToken()
	expiresAt := time.Now().Add(emailVerificationTTL)
	if err != nil || store.CreateEmailVerification(request.Context(), user.ID, tokenHash, expiresAt) != nil {
		writeError(response, http.StatusServiceUnavailable, "email_verification_unavailable", "Email verification is temporarily unavailable.")
		return
	}
	if err := a.sendAccountEmail(request.Context(), "verify-email", user.Email, token, expiresAt); err != nil {
		writeError(response, http.StatusServiceUnavailable, "email_delivery_unavailable", "Account email is temporarily unavailable.")
		return
	}
	response.WriteHeader(http.StatusAccepted)
}

func (a *api) confirmEmailVerification(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodPost) || !a.requireAccounts(response) || !a.allowAuthAttempt(response, request, "email-verification-confirm") {
		return
	}
	store, ok := a.accountSecurity(response)
	if !ok {
		return
	}
	var input tokenRequest
	if !decodeJSONBodyWith(response, request, &input, "invalid_verification", "That verification request could not be read.") {
		return
	}
	if !validActionToken(input.Token) {
		writeError(response, http.StatusBadRequest, "invalid_verification", "That verification link is invalid or expired.")
		return
	}
	user, err := store.VerifyEmail(request.Context(), accounts.HashSessionToken(input.Token), time.Now().UTC())
	if errors.Is(err, accounts.ErrTokenExpired) {
		writeError(response, http.StatusBadRequest, "verification_expired", "That verification link is invalid or expired.")
		return
	}
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "email_verification_unavailable", "Email verification is temporarily unavailable.")
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"user": user})
}

func (a *api) requestPasswordRecovery(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodPost) || !a.requireAccounts(response) || !a.allowAuthAttempt(response, request, "password-recovery") {
		return
	}
	store, ok := a.accountSecurity(response)
	if !ok {
		return
	}
	var input recoveryRequest
	if !decodeJSONBodyWith(response, request, &input, "invalid_request", "Recovery details could not be read.") {
		return
	}
	email, valid := normalizedEmail(input.Email)
	if !valid {
		// Keep the response identical to a missing account.
		response.WriteHeader(http.StatusAccepted)
		return
	}
	if a.config.EmailSender == nil {
		response.WriteHeader(http.StatusAccepted)
		return
	}
	// Keyed by the target address; a 429 here would disclose recent activity for that address.
	if !a.identityWithinBudget(request, "password-recovery", email, identityMailLimit, identityMailWindow) {
		response.WriteHeader(http.StatusAccepted)
		return
	}
	if !a.identityWithinBudget(request, "password-recovery-peer", email+"\x00"+a.clientIP(request), recoveryPeerLimit, identityMailWindow) {
		response.WriteHeader(http.StatusAccepted)
		return
	}
	token, tokenHash, err := accounts.NewSessionToken()
	expiresAt := time.Now().Add(passwordRecoveryTTL)
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "password_recovery_unavailable", "Password recovery is temporarily unavailable.")
		return
	}
	user, found, err := store.CreatePasswordRecovery(request.Context(), email, tokenHash, expiresAt)
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "password_recovery_unavailable", "Password recovery is temporarily unavailable.")
		return
	}
	if found {
		// Delivery failures are deliberately not reflected: the response must not reveal whether the address has an account.
		_ = a.sendAccountEmail(request.Context(), "recover-password", user.Email, token, expiresAt)
	}
	response.WriteHeader(http.StatusAccepted)
}

func (a *api) confirmPasswordRecovery(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodPost) || !a.requireAccounts(response) || !a.allowAuthAttempt(response, request, "password-recovery-confirm") {
		return
	}
	store, ok := a.accountSecurity(response)
	if !ok {
		return
	}
	var input recoveryConfirmRequest
	if !decodeJSONBodyWith(response, request, &input, "invalid_recovery", "Recovery details could not be read.") {
		return
	}
	if !validActionToken(input.Token) || !validPassword(input.NewPassword) {
		writeError(response, http.StatusBadRequest, "invalid_recovery", "That recovery link is invalid, expired, or the new password is too short.")
		return
	}
	passwordHash, err := accounts.HashPassword(input.NewPassword)
	token, sessionHash, tokenErr := accounts.NewSessionToken()
	now := time.Now().UTC()
	if err != nil || tokenErr != nil {
		writeError(response, http.StatusServiceUnavailable, "password_recovery_unavailable", "Password recovery is temporarily unavailable.")
		return
	}
	user, err := store.ResetPasswordAndRotateSession(request.Context(), accounts.TokenPasswordRotation{
		TokenHash: accounts.HashSessionToken(input.Token), PasswordHash: passwordHash,
		SessionTokenHash: sessionHash, SessionExpiresAt: now.Add(a.config.SessionDuration),
		SessionLabel: browserLabel(request), AuthenticatedAt: now,
	})
	if errors.Is(err, accounts.ErrTokenExpired) {
		writeError(response, http.StatusBadRequest, "recovery_expired", "That recovery link is invalid or expired.")
		return
	}
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "password_recovery_unavailable", "Password recovery is temporarily unavailable.")
		return
	}
	a.setSessionCookie(response, token)
	a.recordAccountEvent(request.Context(), user.ID, "password_recovered", browserLabel(request), nil)
	a.sendSecurityNotification(request.Context(), user, "security-password-changed", "Your Sesame account password changed", "Your Sesame website-account password was reset. Other website sessions were revoked.")
	writeJSON(response, http.StatusOK, map[string]any{"user": user, "otherSessionsRevoked": true})
}

func (a *api) reauthenticate(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodPost) || !a.requireAccounts(response) || !a.allowAuthAttempt(response, request, "reauthenticate") {
		return
	}
	store, ok := a.accountSecurity(response)
	if !ok {
		return
	}
	user, _, tokenHash, ok := a.sessionForRequest(response, request)
	if !ok {
		return
	}
	// The cheapest password oracle in the API for anyone holding a stolen session cookie.
	if !a.allowIdentity(response, request, "reauthenticate", user.ID, identityGuessLimit, identityGuessWindow) {
		return
	}
	var input reauthenticateRequest
	if !decodeJSONBodyWith(response, request, &input, "invalid_request", "Authentication details could not be read.") {
		return
	}
	_, passwordHash, err := a.config.Accounts.FindByID(request.Context(), user.ID)
	if err != nil || !accounts.VerifyPassword(passwordHash, input.Password) {
		writeError(response, http.StatusUnauthorized, "invalid_credentials", "Your account password is incorrect.")
		return
	}
	if err := store.MarkSessionAuthenticated(request.Context(), tokenHash, time.Now().UTC()); err != nil {
		writeError(response, http.StatusServiceUnavailable, "reauthentication_unavailable", "Authentication could not be refreshed.")
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (a *api) requestEmailChange(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodPost) || !a.requireAccounts(response) || !a.allowAuthAttempt(response, request, "email-change") {
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
	if a.config.EmailSender == nil {
		writeError(response, http.StatusServiceUnavailable, "email_delivery_unavailable", "Account email is temporarily unavailable.")
		return
	}
	var input emailChangeRequest
	if !decodeJSONBodyWith(response, request, &input, "invalid_request", "Email-change details could not be read.") {
		return
	}
	newEmail, valid := normalizedEmail(input.NewEmail)
	if !valid || newEmail == user.Email {
		writeError(response, http.StatusBadRequest, "invalid_email", "Enter a different valid email address.")
		return
	}
	// Bounded again by the destination so this route cannot mail people with no Sesame account.
	if !a.allowIdentity(response, request, "email-change", user.ID, identityMailLimit, identityMailWindow) ||
		!a.allowIdentity(response, request, "email-change-target", newEmail, identityMailLimit, identityMailWindow) {
		return
	}
	token, tokenHash, err := accounts.NewSessionToken()
	expiresAt := time.Now().Add(emailChangeTTL)
	if err == nil {
		err = store.CreateEmailChange(request.Context(), user.ID, newEmail, tokenHash, expiresAt)
	}
	if errors.Is(err, accounts.ErrEmailTaken) {
		writeError(response, http.StatusConflict, "email_unavailable", "That email address cannot be used.")
		return
	}
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "email_change_unavailable", "Changing your email is temporarily unavailable.")
		return
	}
	if err := a.sendAccountEmail(request.Context(), "change-email", newEmail, token, expiresAt); err != nil {
		writeError(response, http.StatusServiceUnavailable, "email_delivery_unavailable", "Account email is temporarily unavailable.")
		return
	}
	response.WriteHeader(http.StatusAccepted)
}

func (a *api) confirmEmailChange(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodPost) || !a.requireAccounts(response) || !a.allowAuthAttempt(response, request, "email-change-confirm") {
		return
	}
	store, ok := a.accountSecurity(response)
	if !ok {
		return
	}
	var input tokenRequest
	if !decodeJSONBodyWith(response, request, &input, "invalid_email_change", "That email-change request could not be read.") {
		return
	}
	if !validActionToken(input.Token) {
		writeError(response, http.StatusBadRequest, "invalid_email_change", "That email-change link is invalid or expired.")
		return
	}
	token, sessionHash, err := accounts.NewSessionToken()
	now := time.Now().UTC()
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "email_change_unavailable", "Changing your email is temporarily unavailable.")
		return
	}
	user, err := store.ConfirmEmailChangeAndRotateSession(request.Context(), accounts.TokenSessionRotation{
		TokenHash: accounts.HashSessionToken(input.Token), SessionTokenHash: sessionHash,
		SessionExpiresAt: now.Add(a.config.SessionDuration), SessionLabel: browserLabel(request), AuthenticatedAt: now,
	})
	if errors.Is(err, accounts.ErrTokenExpired) {
		writeError(response, http.StatusBadRequest, "email_change_expired", "That email-change link is invalid or expired.")
		return
	}
	if errors.Is(err, accounts.ErrEmailTaken) {
		writeError(response, http.StatusConflict, "email_unavailable", "That email address cannot be used.")
		return
	}
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "email_change_unavailable", "Changing your email is temporarily unavailable.")
		return
	}
	a.setSessionCookie(response, token)
	a.recordAccountEvent(request.Context(), user.ID, "email_changed", "Sesame account", nil)
	a.sendSecurityNotification(request.Context(), user, "security-email-changed", "Your Sesame account email changed", "The email address for your Sesame website account was changed. Other website sessions were revoked.")
	writeJSON(response, http.StatusOK, map[string]any{"user": user, "otherSessionsRevoked": true})
}

func (a *api) accountSessions(response http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		a.listAccountSessions(response, request)
	case http.MethodDelete:
		a.revokeAllAccountSessions(response, request)
	default:
		response.Header().Set("Allow", "GET, DELETE, OPTIONS")
		writeError(response, http.StatusMethodNotAllowed, "method_not_allowed", "This endpoint does not allow that method.")
	}
}

func (a *api) listAccountSessions(response http.ResponseWriter, request *http.Request) {
	if !a.requireAccounts(response) || !a.allowRequest(response, request, "account-sessions", 60, time.Minute) {
		return
	}
	store, ok := a.accountSecurity(response)
	if !ok {
		return
	}
	user, current, _, ok := a.sessionForRequest(response, request)
	if !ok {
		return
	}
	sessions, err := store.SessionsForAccount(request.Context(), user.ID)
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "sessions_unavailable", "Website sessions are temporarily unavailable.")
		return
	}
	for i := range sessions {
		sessions[i].Current = sessions[i].ID == current.ID
	}
	writeJSON(response, http.StatusOK, map[string]any{"sessions": sessions})
}

func (a *api) revokeAllAccountSessions(response http.ResponseWriter, request *http.Request) {
	if !a.requireAccounts(response) || !a.allowAuthAttempt(response, request, "sessions-revoke-all") {
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
	if err := store.RevokeAllSessions(request.Context(), user.ID); err != nil {
		writeError(response, http.StatusServiceUnavailable, "session_revoke_unavailable", "Website sessions could not be revoked.")
		return
	}
	a.clearSessionCookie(response)
	response.WriteHeader(http.StatusNoContent)
}

func (a *api) accountSession(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodDelete) || !a.requireAccounts(response) || !a.allowAuthAttempt(response, request, "session-revoke") {
		return
	}
	store, ok := a.accountSecurity(response)
	if !ok {
		return
	}
	user, current, _, ok := a.recentSessionForRequest(response, request)
	if !ok {
		return
	}
	id := strings.TrimPrefix(request.URL.Path, "/v1/account/sessions/")
	if !validOpaqueID(id) {
		writeError(response, http.StatusBadRequest, "invalid_session", "That website session id is invalid.")
		return
	}
	if err := store.DeleteSessionForAccount(request.Context(), user.ID, id); errors.Is(err, accounts.ErrNotFound) {
		writeError(response, http.StatusNotFound, "session_not_found", "That website session no longer exists.")
		return
	} else if err != nil {
		writeError(response, http.StatusServiceUnavailable, "session_revoke_unavailable", "That website session could not be revoked.")
		return
	}
	if id == current.ID {
		a.clearSessionCookie(response)
	}
	response.WriteHeader(http.StatusNoContent)
}

func (a *api) accountAccess(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodGet) || !a.requireAccounts(response) || !a.allowRequest(response, request, "account-access", 60, time.Minute) {
		return
	}
	store, ok := a.accountSecurity(response)
	if !ok {
		return
	}
	user, ok := a.userForRequest(response, request)
	if !ok {
		return
	}
	access, err := store.AccountAccess(request.Context(), user.ID)
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "account_access_unavailable", "Account access is temporarily unavailable.")
		return
	}
	writeJSON(response, http.StatusOK, access)
}

func (a *api) accountActivity(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodGet) || !a.requireAccounts(response) || !a.allowRequest(response, request, "account-activity", 60, time.Minute) {
		return
	}
	user, ok := a.userForRequest(response, request)
	if !ok {
		return
	}
	store, ok := a.config.Accounts.(accounts.AccountActivityStore)
	if !ok {
		writeError(response, http.StatusServiceUnavailable, "account_activity_unavailable", "Security activity is temporarily unavailable.")
		return
	}
	events, err := store.AccountEvents(request.Context(), user.ID, 50)
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "account_activity_unavailable", "Security activity is temporarily unavailable.")
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"events": events})
}

func (a *api) accountNotificationPreferences(response http.ResponseWriter, request *http.Request) {
	if !a.requireAccounts(response) || !a.allowRequest(response, request, "account-notifications", 60, time.Minute) {
		return
	}
	user, ok := a.userForRequest(response, request)
	if !ok {
		return
	}
	store, ok := a.config.Accounts.(accounts.NotificationPreferencesStore)
	if !ok {
		writeError(response, http.StatusServiceUnavailable, "notifications_unavailable", "Notification preferences are temporarily unavailable.")
		return
	}
	switch request.Method {
	case http.MethodGet:
		preferences, err := store.NotificationPreferences(request.Context(), user.ID)
		if err != nil {
			writeError(response, http.StatusServiceUnavailable, "notifications_unavailable", "Notification preferences are temporarily unavailable.")
			return
		}
		writeJSON(response, http.StatusOK, map[string]any{"securityMandatory": true, "preferences": preferences})
	case http.MethodPatch:
		var input notificationPreferencesRequest
		if !decodeJSONBodyWith(response, request, &input, "invalid_notification_preferences", "Notification preferences could not be read.") {
			return
		}
		if err := store.UpdateNotificationPreferences(request.Context(), user.ID, accounts.NotificationPreferences{BetaReleases: input.BetaReleases, SupportReplies: input.SupportReplies, ProductAnnouncements: input.ProductAnnouncements}); err != nil {
			writeError(response, http.StatusServiceUnavailable, "notifications_unavailable", "Notification preferences are temporarily unavailable.")
			return
		}
		response.WriteHeader(http.StatusNoContent)
	default:
		response.Header().Set("Allow", "GET, PATCH, OPTIONS")
		writeError(response, http.StatusMethodNotAllowed, "method_not_allowed", "This endpoint does not allow that method.")
	}
}

// Never includes vault data, tokens, IP addresses, or browser-helper installation claims.
func (a *api) accountBootstrap(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodGet) || !a.requireAccounts(response) || !a.allowRequest(response, request, "account-bootstrap", 60, time.Minute) {
		return
	}
	store, ok := a.accountSecurity(response)
	if !ok {
		return
	}
	user, session, _, ok := a.sessionForRequest(response, request)
	if !ok {
		return
	}
	access, err := store.AccountAccess(request.Context(), user.ID)
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "account_bootstrap_unavailable", "Account details are temporarily unavailable.")
		return
	}
	sessions, err := store.SessionsForAccount(request.Context(), user.ID)
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "account_bootstrap_unavailable", "Account details are temporarily unavailable.")
		return
	}
	for i := range sessions {
		sessions[i].Current = sessions[i].ID == session.ID
	}
	devices := []accounts.DesktopConnection{}
	supportUnread := 0
	desktopLinking := false
	notificationsAvailable := false
	if _, configured := a.config.Accounts.(accounts.NotificationPreferencesStore); configured && a.config.EmailSender != nil {
		notificationsAvailable = true
	}
	if desktop, configured := a.config.Accounts.(accounts.DesktopStore); configured {
		desktopLinking = true
		devices, err = desktop.DesktopConnectionsForAccount(request.Context(), user.ID)
		if err != nil {
			writeError(response, http.StatusServiceUnavailable, "account_bootstrap_unavailable", "Account details are temporarily unavailable.")
			return
		}
	}
	if tickets, err := store.SupportTicketsForAccount(request.Context(), user.ID); err == nil {
		for _, ticket := range tickets {
			supportUnread += ticket.UnreadCount
		}
	}
	payload := map[string]any{
		"account":  user,
		"access":   access,
		"licences": access.Licences,
		"capabilities": map[string]bool{
			"desktopLinking": desktopLinking,
			"passkeys":       a.config.Passkeys != nil,
			"browserHelper":  false,
			"notifications":  notificationsAvailable,
		},
		"notificationCounts": map[string]int{"security": 0, "support": supportUnread, "product": 0},
		"security": map[string]any{
			"activeSessions":         len(sessions),
			"connectedDesktops":      len(devices),
			"recentAuthenticationAt": session.AuthenticatedAt,
		},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "account_bootstrap_unavailable", "Account details are temporarily unavailable.")
		return
	}
	digest := sha256.Sum256(encoded)
	etag := `"` + base64.RawURLEncoding.EncodeToString(digest[:]) + `"`
	response.Header().Set("ETag", etag)
	response.Header().Set("Vary", "Origin, Cookie")
	if request.Header.Get("If-None-Match") == etag {
		response.WriteHeader(http.StatusNotModified)
		return
	}
	writeJSON(response, http.StatusOK, payload)
}

func (a *api) accountDownloads(response http.ResponseWriter, request *http.Request) {
	if !a.capabilityEnabled(request.Context(), "downloads_enabled") {
		writeError(response, http.StatusServiceUnavailable, "downloads_disabled", "Verified private-beta downloads are temporarily unavailable.")
		return
	}
	if !allowMethod(response, request, http.MethodGet) || !a.requireAccounts(response) || !a.allowRequest(response, request, "account-downloads", 60, time.Minute) {
		return
	}
	store, ok := a.accountSecurity(response)
	if !ok {
		return
	}
	user, ok := a.userForRequest(response, request)
	if !ok {
		return
	}
	releases, err := store.SignedDownloads(request.Context(), user.ID)
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "downloads_unavailable", "Verified private-beta downloads are temporarily unavailable.")
		return
	}
	releases = distributableWindowsReleases(releases)
	writeJSON(response, http.StatusOK, map[string]any{"releases": releases})
}

func (a *api) accountDownloadTickets(response http.ResponseWriter, request *http.Request) {
	if !a.capabilityEnabled(request.Context(), "downloads_enabled") {
		writeError(response, http.StatusServiceUnavailable, "downloads_disabled", "Verified private-beta downloads are temporarily unavailable.")
		return
	}
	if !allowMethod(response, request, http.MethodPost) || !a.requireAccounts(response) || !a.allowRequest(response, request, "download-ticket", 12, time.Minute) {
		return
	}
	store, ok := a.accountSecurity(response)
	if !ok {
		return
	}
	user, ok := a.userForRequest(response, request)
	if !ok {
		return
	}
	var input downloadTicketRequest
	if !decodeJSONBodyWith(response, request, &input, "invalid_download_ticket", "Download details could not be read.") {
		return
	}
	input.ReleaseID = strings.TrimSpace(input.ReleaseID)
	input.Platform = strings.TrimSpace(strings.ToLower(input.Platform))
	if input.ReleaseID == "" || input.Platform == "" {
		writeError(response, http.StatusBadRequest, "invalid_download_ticket", "Choose an eligible release and platform.")
		return
	}
	idempotencyKey := strings.TrimSpace(request.Header.Get("Idempotency-Key"))
	if !validIdempotencyKey(idempotencyKey) {
		writeError(response, http.StatusBadRequest, "idempotency_key_required", "Provide a random Idempotency-Key of at least 24 characters.")
		return
	}
	releases, err := store.SignedDownloads(request.Context(), user.ID)
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "downloads_unavailable", "Verified private-beta downloads are temporarily unavailable.")
		return
	}
	var release *accounts.DownloadRelease
	for index := range releases {
		candidate := &releases[index]
		if candidate.ID == input.ReleaseID && candidate.Platform == input.Platform && candidate.Signed && distributableWindowsRelease(*candidate) && validArtifactObjectKey(candidate.ArtifactObjectKey) {
			release = candidate
			break
		}
	}
	if release == nil {
		writeError(response, http.StatusNotFound, "download_not_available", "That verified private-beta download is not available to this account.")
		return
	}
	token, tokenHash, err := accounts.NewSessionToken()
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "download_ticket_unavailable", "A download ticket could not be created.")
		return
	}
	requestFingerprint := sha256.Sum256([]byte(input.ReleaseID + "\x00" + input.Platform))
	ticket, err := store.CreateOrRefreshDownloadTicket(request.Context(), accounts.DownloadTicketRequest{
		AccountID: user.ID, ReleaseID: release.ID, Platform: release.Platform, ArtifactObjectKey: release.ArtifactObjectKey, ArtifactSHA256: release.SHA256,
		TokenHash: tokenHash, IdempotencyKeyHash: accounts.HashSessionToken(idempotencyKey), RequestHash: requestFingerprint[:], ExpiresAt: time.Now().UTC().Add(downloadTicketTTL),
	})
	if errors.Is(err, accounts.ErrIdempotencyConflict) {
		writeError(response, http.StatusConflict, "idempotency_key_conflict", "That Idempotency-Key was used for a different download request.")
		return
	}
	if errors.Is(err, accounts.ErrDownloadTicketUsed) {
		writeError(response, http.StatusConflict, "download_ticket_completed", "That Idempotency-Key has already completed its download. Start a new download to receive another ticket.")
		return
	}
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "download_ticket_unavailable", "A download ticket could not be created.")
		return
	}
	if ticket.Created {
		a.recordAccountEvent(request.Context(), user.ID, "download_ticket_issued", "Verified private-beta download", map[string]string{"release": ticket.ReleaseID, "platform": ticket.Platform})
	}
	status := http.StatusOK
	if ticket.Created {
		status = http.StatusCreated
	}
	writeJSON(response, status, map[string]any{
		"downloadUrl": "/v1/downloads/" + token,
		"expiresAt":   ticket.ExpiresAt,
		"releaseId":   ticket.ReleaseID,
		"platform":    ticket.Platform,
	})
}

// Enforced at the HTTP boundary so an alternate store cannot expose an unverified build.
func distributableWindowsReleases(releases []accounts.DownloadRelease) []accounts.DownloadRelease {
	available := make([]accounts.DownloadRelease, 0, len(releases))
	for _, release := range releases {
		if distributableWindowsRelease(release) {
			available = append(available, release)
		}
	}
	return available
}

func distributableWindowsRelease(release accounts.DownloadRelease) bool {
	if release.Platform != "windows" || !release.SigstoreVerified {
		return false
	}
	if release.DistributionClass == "early_access" {
		return !release.AuthenticodeVerified
	}
	return release.DistributionClass == "production" && release.AuthenticodeVerified
}

func (a *api) redeemDownloadTicket(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodGet) || !a.requireAccounts(response) {
		return
	}
	token := strings.TrimPrefix(request.URL.Path, "/v1/downloads/")
	if token == "" || strings.Contains(token, "/") || len(token) < 32 || len(token) > 128 {
		writeError(response, http.StatusNotFound, "download_ticket_not_found", "That download ticket is invalid or expired.")
		return
	}
	store, ok := a.accountSecurity(response)
	if !ok {
		return
	}
	user, ok := a.userForRequest(response, request)
	if !ok {
		return
	}
	tokenHash := accounts.HashSessionToken(token)
	now := time.Now().UTC()

	ticket, err := store.PeekDownloadTicket(request.Context(), user.ID, tokenHash, now)
	if errors.Is(err, accounts.ErrTokenExpired) {
		writeError(response, http.StatusGone, "download_ticket_expired", "That download ticket has expired or was already used.")
		return
	}
	if err != nil || !validArtifactObjectKey(ticket.ArtifactObjectKey) || a.config.ArtifactDelivery == nil {
		writeError(response, http.StatusServiceUnavailable, "download_unavailable", "This download is temporarily unavailable.")
		return
	}
	artifactURL, err := a.config.ArtifactDelivery.SignedURL(request.Context(), ticket.ArtifactObjectKey, minArtifactURLExpiry(ticket.ExpiresAt))
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "download_unavailable", "This download is temporarily unavailable.")
		return
	}
	if err := store.MarkDownloadTicketRedeemed(request.Context(), user.ID, tokenHash, now); err != nil {
		if errors.Is(err, accounts.ErrTokenExpired) {
			writeError(response, http.StatusGone, "download_ticket_expired", "That download ticket has expired or was already used.")
			return
		}
		writeError(response, http.StatusServiceUnavailable, "download_unavailable", "This download is temporarily unavailable.")
		return
	}
	a.recordAccountEvent(request.Context(), user.ID, "download_redeemed", "Verified private-beta download", map[string]string{"release": ticket.ReleaseID, "platform": ticket.Platform})
	response.Header().Set("Referrer-Policy", "no-referrer")
	response.Header().Set("Cache-Control", "no-store, max-age=0")
	http.Redirect(response, request, artifactURL, http.StatusSeeOther)
}

func validIdempotencyKey(value string) bool {
	if len(value) < 24 || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '-' && character != '_' {
			return false
		}
	}
	return true
}

func minArtifactURLExpiry(ticketExpiry time.Time) time.Time {
	shortLived := time.Now().UTC().Add(5 * time.Minute)
	if ticketExpiry.Before(shortLived) {
		return ticketExpiry
	}
	return shortLived
}

func (a *api) accountSecurity(response http.ResponseWriter) (accounts.AccountSecurityStore, bool) {
	store, ok := a.config.Accounts.(accounts.AccountSecurityStore)
	if !ok {
		writeError(response, http.StatusServiceUnavailable, "account_security_unavailable", "Account security is not configured.")
		return nil, false
	}
	return store, true
}

func (a *api) sessionForRequest(response http.ResponseWriter, request *http.Request) (accounts.User, accounts.SessionInfo, []byte, bool) {
	store, ok := a.accountSecurity(response)
	if !ok {
		return accounts.User{}, accounts.SessionInfo{}, nil, false
	}
	cookie, err := request.Cookie(a.sessionCookieName())
	if err != nil || cookie.Value == "" {
		writeError(response, http.StatusUnauthorized, "not_authenticated", "Sign in to continue.")
		return accounts.User{}, accounts.SessionInfo{}, nil, false
	}
	tokenHash := accounts.HashSessionToken(cookie.Value)
	user, session, err := store.SessionForToken(request.Context(), tokenHash)
	if err != nil {
		a.clearSessionCookie(response)
		writeError(response, http.StatusUnauthorized, "session_expired", "Your session has expired. Sign in to continue.")
		return accounts.User{}, accounts.SessionInfo{}, nil, false
	}
	return user, session, tokenHash, true
}

func (a *api) recentSessionForRequest(response http.ResponseWriter, request *http.Request) (accounts.User, accounts.SessionInfo, []byte, bool) {
	user, session, tokenHash, ok := a.sessionForRequest(response, request)
	if !ok {
		return accounts.User{}, accounts.SessionInfo{}, nil, false
	}
	if time.Since(session.AuthenticatedAt) > a.config.RecentAuthDuration {
		writeError(response, http.StatusForbidden, "recent_auth_required", "Confirm your account password before making this change.")
		return accounts.User{}, accounts.SessionInfo{}, nil, false
	}
	return user, session, tokenHash, true
}

func (a *api) sendAccountEmail(ctx context.Context, kind, email, token string, expiresAt time.Time) error {
	if a.config.EmailSender == nil {
		return errors.New("account email is not configured")
	}
	path := map[string]string{
		"verify-email":     "/verify-email",
		"recover-password": "/reset-password",
		"change-email":     "/confirm-email-change",
	}[kind]
	// Token in the URL fragment: never sent in the request line, access logs, or Referer.
	actionURL := strings.TrimSuffix(a.config.WebBaseURL, "/") + path + "#token=" + url.QueryEscape(token)
	return a.config.EmailSender.SendAccountEmail(ctx, AccountEmail{Kind: kind, To: email, ActionURL: actionURL, ExpiresAt: expiresAt.UTC()})
}

func validActionToken(token string) bool {
	return len(token) >= 32 && len(token) <= 128 && !strings.ContainsAny(token, " \t\r\n")
}

func validOpaqueID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func browserLabel(request *http.Request) string {
	agent := strings.ToLower(request.UserAgent())
	browser := "Browser"
	switch {
	case strings.Contains(agent, "edg/"):
		browser = "Edge"
	case strings.Contains(agent, "firefox/"):
		browser = "Firefox"
	case strings.Contains(agent, "chrome/"):
		browser = "Chrome"
	case strings.Contains(agent, "safari/"):
		browser = "Safari"
	}
	if strings.Contains(agent, "windows") {
		return browser + " on Windows"
	}
	if strings.Contains(agent, "mac os") {
		return browser + " on macOS"
	}
	if strings.Contains(agent, "linux") {
		return browser + " on Linux"
	}
	return browser
}
