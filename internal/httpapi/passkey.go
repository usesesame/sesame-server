package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"

	"usesesame.app/backend/internal/accounts"
)

const (
	passkeyCeremonyCookie = "sesame_wan"
	passkeyCeremonyTTL    = 10 * time.Minute
	maxPasskeyBodyBytes   = 64 * 1024
)

// A non-nil return must be treated as a login failure: cloned-authenticator detection depends on this write.
func persistPasskeyCredentialState(ctx context.Context, store accounts.PasskeyStore, credential *webauthn.Credential) error {
	encoded, err := json.Marshal(credential)
	if err != nil {
		return fmt.Errorf("passkey credential could not be serialized: %w", err)
	}
	if err := store.UpdateCredential(ctx, credential.ID, encoded); err != nil {
		return fmt.Errorf("passkey credential state could not be persisted: %w", err)
	}
	return nil
}

func (a *api) passkeyContext(response http.ResponseWriter) (*webauthn.WebAuthn, accounts.PasskeyStore, bool) {
	store, ok := a.config.Accounts.(accounts.PasskeyStore)
	if a.config.Passkeys == nil || !ok {
		writeError(response, http.StatusServiceUnavailable, "passkeys_unavailable", "Passkey sign-in is not configured.")
		return nil, nil, false
	}
	return a.config.Passkeys, store, true
}

func (a *api) passkeyRegisterBegin(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodPost) || !a.requireAccounts(response) || !a.allowRequest(response, request, "passkey-register-begin", 8, time.Minute) {
		return
	}
	wa, store, ok := a.passkeyContext(response)
	if !ok {
		return
	}
	user, _, _, ok := a.recentSessionForRequest(response, request)
	if !ok {
		return
	}
	waUser, ok := a.webAuthnUser(response, store, request, user)
	if !ok {
		return
	}
	creation, session, err := wa.BeginRegistration(waUser)
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "passkey_unavailable", "Sesame could not start passkey registration.")
		return
	}
	if !a.storeCeremony(response, request, user.ID, session) {
		return
	}
	writeJSON(response, http.StatusOK, creation)
}

func (a *api) passkeyRegisterFinish(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodPost) || !a.requireAccounts(response) || !a.allowRequest(response, request, "passkey-register-finish", 8, time.Minute) {
		return
	}
	wa, store, ok := a.passkeyContext(response)
	if !ok {
		return
	}
	user, _, _, ok := a.recentSessionForRequest(response, request)
	if !ok {
		return
	}
	accountID, session, ok := a.takeCeremony(response, request)
	if !ok {
		return
	}
	if accountID != user.ID {
		writeError(response, http.StatusBadRequest, "passkey_ceremony_mismatch", "That passkey registration did not match your account.")
		return
	}
	waUser, ok := a.webAuthnUser(response, store, request, user)
	if !ok {
		return
	}
	request.Body = http.MaxBytesReader(response, request.Body, maxPasskeyBodyBytes)
	credential, err := wa.FinishRegistration(waUser, session, request)
	if err != nil {
		writeError(response, http.StatusBadRequest, "passkey_registration_failed", "Sesame could not verify that passkey.")
		return
	}
	encoded, err := json.Marshal(credential)
	if err != nil || store.AddCredential(request.Context(), user.ID, credential.ID, encoded, passkeyName(request)) != nil {
		writeError(response, http.StatusServiceUnavailable, "passkey_unavailable", "Sesame could not save that passkey.")
		return
	}
	a.recordAccountEvent(request.Context(), user.ID, "passkey_added", passkeyName(request), nil)
	a.sendSecurityNotification(request.Context(), user, "security-passkey-added", "A passkey was added to your Sesame account", "A new passkey was added to your Sesame website account.")
	writeJSON(response, http.StatusCreated, map[string]any{"registered": true})
}

func (a *api) passkeyLoginBegin(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodPost) || !a.requireAccounts(response) || !a.allowAuthAttempt(response, request, "passkey-login") {
		return
	}
	wa, _, ok := a.passkeyContext(response)
	if !ok {
		return
	}
	assertion, session, err := wa.BeginDiscoverableLogin()
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "passkey_unavailable", "Sesame could not start passkey sign-in.")
		return
	}
	if !a.storeCeremony(response, request, "", session) {
		return
	}
	writeJSON(response, http.StatusOK, assertion)
}

func (a *api) passkeyLoginFinish(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodPost) || !a.requireAccounts(response) || !a.allowAuthAttempt(response, request, "passkey-login") {
		return
	}
	wa, store, ok := a.passkeyContext(response)
	if !ok {
		return
	}
	_, session, ok := a.takeCeremony(response, request)
	if !ok {
		return
	}

	var matchedAccountID string
	handler := func(_, userHandle []byte) (webauthn.User, error) {
		accountID := accounts.AccountIDForHandle(userHandle)
		user, _, err := a.config.Accounts.FindByID(request.Context(), accountID)
		if err != nil {
			return nil, err
		}
		credentials, err := store.CredentialsForAccount(request.Context(), accountID)
		if err != nil {
			return nil, err
		}
		waUser, err := accounts.NewWebAuthnUser(user, credentials)
		if err != nil {
			return nil, err
		}
		matchedAccountID = accountID
		return waUser, nil
	}

	request.Body = http.MaxBytesReader(response, request.Body, maxPasskeyBodyBytes)
	credential, err := wa.FinishDiscoverableLogin(handler, session, request)
	if err != nil || matchedAccountID == "" {
		writeError(response, http.StatusUnauthorized, "passkey_login_failed", "That passkey could not be verified.")
		return
	}
	// Issuing a session while this write is unconfirmed would let a session
	// through on state the server never actually saved.
	if persistErr := persistPasskeyCredentialState(request.Context(), store, credential); persistErr != nil {
		slog.Error("passkey credential state could not be persisted after a successful assertion",
			"error", persistErr)
		writeError(response, http.StatusServiceUnavailable, "login_unavailable", "Sign in is temporarily unavailable.")
		return
	}

	user, _, err := a.config.Accounts.FindByID(request.Context(), matchedAccountID)
	if err != nil {
		writeError(response, http.StatusUnauthorized, "passkey_login_failed", "That passkey could not be verified.")
		return
	}
	if user.Suspended {
		writeError(response, http.StatusLocked, "account_suspended", "This Sesame account is suspended. Contact support if you think this is a mistake.")
		return
	}
	token, tokenHash, err := accounts.NewSessionToken()
	if err != nil || a.config.Accounts.CreateSession(request.Context(), user.ID, tokenHash, time.Now().Add(a.config.SessionDuration)) != nil {
		writeError(response, http.StatusServiceUnavailable, "login_unavailable", "Sign in is temporarily unavailable.")
		return
	}
	a.setSessionCookie(response, token)
	a.recordAccountEvent(request.Context(), user.ID, "sign_in", "Passkey", map[string]string{"method": "passkey"})
	a.sendSecurityNotification(request.Context(), user, "security-sign-in", "New Sesame account sign-in", "A new passkey sign-in to your Sesame website account was completed.")
	writeJSON(response, http.StatusOK, map[string]any{"user": user})
}

func (a *api) passkeys(response http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		a.listPasskeys(response, request)
	case http.MethodDelete:
		a.deletePasskey(response, request)
	default:
		response.Header().Set("Allow", "GET, DELETE, OPTIONS")
		writeError(response, http.StatusMethodNotAllowed, "method_not_allowed", "This endpoint does not allow that method.")
	}
}

func (a *api) listPasskeys(response http.ResponseWriter, request *http.Request) {
	if !a.requireAccounts(response) || !a.allowRequest(response, request, "passkeys-list", 60, time.Minute) {
		return
	}
	_, store, ok := a.passkeyContext(response)
	if !ok {
		return
	}
	user, ok := a.userForRequest(response, request)
	if !ok {
		return
	}
	list, err := store.ListCredentials(request.Context(), user.ID)
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "passkey_unavailable", "Sesame could not list your passkeys.")
		return
	}
	if list == nil {
		list = []accounts.CredentialInfo{}
	}
	writeJSON(response, http.StatusOK, map[string]any{"passkeys": list})
}

func (a *api) deletePasskey(response http.ResponseWriter, request *http.Request) {
	if !a.requireAccounts(response) || !a.allowRequest(response, request, "passkey-delete", 20, time.Minute) {
		return
	}
	_, store, ok := a.passkeyContext(response)
	if !ok {
		return
	}
	user, _, _, ok := a.recentSessionForRequest(response, request)
	if !ok {
		return
	}
	credentialID, err := hex.DecodeString(request.URL.Query().Get("id"))
	if err != nil || len(credentialID) == 0 {
		writeError(response, http.StatusBadRequest, "invalid_passkey", "That passkey id is invalid.")
		return
	}
	deleted, err := store.DeleteCredential(request.Context(), user.ID, credentialID)
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "passkey_unavailable", "Sesame could not remove that passkey.")
		return
	}
	if !deleted {
		writeError(response, http.StatusNotFound, "passkey_not_found", "That passkey does not exist.")
		return
	}
	a.recordAccountEvent(request.Context(), user.ID, "passkey_removed", "Passkey", nil)
	a.sendSecurityNotification(request.Context(), user, "security-passkey-removed", "A passkey was removed from your Sesame account", "A passkey was removed from your Sesame website account.")
	response.WriteHeader(http.StatusNoContent)
}

func (a *api) webAuthnUser(response http.ResponseWriter, store accounts.PasskeyStore, request *http.Request, user accounts.User) (*accounts.WebAuthnUser, bool) {
	credentials, err := store.CredentialsForAccount(request.Context(), user.ID)
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "passkey_unavailable", "Sesame could not load your passkeys.")
		return nil, false
	}
	waUser, err := accounts.NewWebAuthnUser(user, credentials)
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "passkey_unavailable", "Sesame could not prepare passkey sign-in.")
		return nil, false
	}
	return waUser, true
}

func (a *api) storeCeremony(response http.ResponseWriter, request *http.Request, accountID string, session *webauthn.SessionData) bool {
	data, err := json.Marshal(session)
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "passkey_unavailable", "Sesame could not prepare passkey sign-in.")
		return false
	}
	id := make([]byte, 32)
	if _, err := rand.Read(id); err != nil {
		writeError(response, http.StatusServiceUnavailable, "passkey_unavailable", "Sesame could not prepare passkey sign-in.")
		return false
	}
	store := a.config.Accounts.(accounts.PasskeyStore)
	if store.SaveWebAuthnSession(request.Context(), id, accountID, data, time.Now().Add(passkeyCeremonyTTL)) != nil {
		writeError(response, http.StatusServiceUnavailable, "passkey_unavailable", "Sesame could not prepare passkey sign-in.")
		return false
	}
	http.SetCookie(response, &http.Cookie{
		Name:     a.passkeyCeremonyCookieName(),
		Value:    base64.RawURLEncoding.EncodeToString(id),
		Path:     "/",
		Domain:   a.config.SessionDomain,
		MaxAge:   int(passkeyCeremonyTTL.Seconds()),
		HttpOnly: true,
		Secure:   a.config.SessionSecure,
		SameSite: http.SameSiteLaxMode,
	})
	return true
}

func (a *api) takeCeremony(response http.ResponseWriter, request *http.Request) (string, webauthn.SessionData, bool) {
	cookie, err := request.Cookie(a.passkeyCeremonyCookieName())
	if err != nil || cookie.Value == "" {
		writeError(response, http.StatusBadRequest, "passkey_ceremony_expired", "That passkey request expired. Try again.")
		return "", webauthn.SessionData{}, false
	}
	a.clearCeremonyCookie(response)
	id, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil {
		writeError(response, http.StatusBadRequest, "passkey_ceremony_expired", "That passkey request expired. Try again.")
		return "", webauthn.SessionData{}, false
	}
	store := a.config.Accounts.(accounts.PasskeyStore)
	accountID, data, err := store.TakeWebAuthnSession(request.Context(), id)
	if err != nil {
		writeError(response, http.StatusBadRequest, "passkey_ceremony_expired", "That passkey request expired. Try again.")
		return "", webauthn.SessionData{}, false
	}
	var session webauthn.SessionData
	if json.Unmarshal(data, &session) != nil {
		writeError(response, http.StatusBadRequest, "passkey_ceremony_expired", "That passkey request expired. Try again.")
		return "", webauthn.SessionData{}, false
	}
	return accountID, session, true
}

func (a *api) clearCeremonyCookie(response http.ResponseWriter) {
	http.SetCookie(response, &http.Cookie{Name: a.passkeyCeremonyCookieName(), Value: "", Path: "/", Domain: a.config.SessionDomain, MaxAge: -1, HttpOnly: true, Secure: a.config.SessionSecure, SameSite: http.SameSiteLaxMode})
}

func passkeyName(request *http.Request) string {
	if validDeviceName(request.URL.Query().Get("name")) {
		return request.URL.Query().Get("name")
	}
	return "Passkey"
}
