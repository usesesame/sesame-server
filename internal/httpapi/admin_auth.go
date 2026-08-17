package httpapi

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"

	"usesesame.app/backend/internal/accounts"
	adminstore "usesesame.app/backend/internal/admin"
)

const (
	adminSessionCookie = "sesame_admin_session"
	adminCSRFCookie    = "sesame_admin_csrf"
	maxAdminBodyBytes  = 32 * 1024
)

type adminLoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Code     string `json:"code"`
}

type adminSetupRequest struct {
	Token    string `json:"token"`
	Password string `json:"password,omitempty"`
	Code     string `json:"code,omitempty"`
}

func (a *api) registerAdminRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/v1/admin/auth/csrf", a.adminCSRF)
	mux.HandleFunc("/v1/admin/auth/login", a.adminLogin)
	mux.HandleFunc("/v1/admin/auth/logout", a.adminLogout)
	mux.HandleFunc("/v1/admin/auth/me", a.adminMe)
	mux.HandleFunc("/v1/admin/auth/setup/begin", a.adminSetupBegin)
	mux.HandleFunc("/v1/admin/auth/setup/complete", a.adminSetupComplete)
	mux.HandleFunc("/v1/admin/overview", a.adminOverview)
	mux.HandleFunc("/v1/admin/users", a.adminUsers)
	mux.HandleFunc("/v1/admin/users/", a.adminUserRoute)
	mux.HandleFunc("/v1/admin/flags", a.adminFlags)
	mux.HandleFunc("/v1/admin/flags/", a.adminFlag)
	mux.HandleFunc("/v1/admin/releases", a.adminReleases)
	mux.HandleFunc("/v1/admin/releases/", a.adminRelease)
	mux.HandleFunc("/v1/admin/plans", a.adminPlans)
	mux.HandleFunc("/v1/admin/plans/", a.adminPlan)
	mux.HandleFunc("/v1/admin/admins", a.adminAccounts)
	mux.HandleFunc("/v1/admin/admins/", a.adminAccountRoute)
	mux.HandleFunc("/v1/admin/audit", a.adminAudit)
	mux.HandleFunc("/v1/admin/audit/me", a.adminAuditMe)
	mux.HandleFunc("/v1/admin/audit/export", a.adminAuditExport)
	mux.HandleFunc("/v1/admin/system/health", a.adminSystemHealth)
	mux.HandleFunc("/v1/admin/system/rate-limits", a.adminRateLimits)
	mux.HandleFunc("/v1/admin/system/config", a.adminSystemConfig)
	mux.HandleFunc("/v1/admin/support", a.adminSupportTickets)
	mux.HandleFunc("/v1/admin/support/assignees", a.adminSupportAssignees)
	mux.HandleFunc("/v1/admin/support/", a.adminSupportTicketRoute)
}

func (a *api) requireAdminStore(response http.ResponseWriter) (*adminstore.Store, bool) {
	if a.config.Admin == nil {
		writeError(response, http.StatusServiceUnavailable, "admin_unavailable", "The admin service is not configured.")
		return nil, false
	}
	return a.config.Admin, true
}

func (a *api) adminCSRF(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodGet) {
		return
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		writeError(response, http.StatusServiceUnavailable, "csrf_unavailable", "The admin security token is temporarily unavailable.")
		return
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	http.SetCookie(response, &http.Cookie{Name: adminCSRFCookie, Value: token, Path: "/v1/admin", Domain: a.config.AdminSessionDomain, MaxAge: 3600, HttpOnly: true, Secure: a.config.AdminSecure, SameSite: http.SameSiteStrictMode})
	writeJSON(response, http.StatusOK, map[string]string{"token": token})
}

func (a *api) validAdminCSRF(request *http.Request) bool {
	header := request.Header.Get("X-Sesame-CSRF")
	cookie, err := request.Cookie(adminCSRFCookie)
	if err != nil || header == "" || cookie.Value == "" || len(header) != len(cookie.Value) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(header), []byte(cookie.Value)) == 1
}

func (a *api) setAdminSessionCookie(response http.ResponseWriter, token string) {
	http.SetCookie(response, &http.Cookie{Name: adminSessionCookie, Value: token, Path: "/v1/admin", Domain: a.config.AdminSessionDomain, MaxAge: int(a.config.AdminSessionTTL.Seconds()), HttpOnly: true, Secure: a.config.AdminSecure, SameSite: http.SameSiteStrictMode})
}

func (a *api) clearAdminSessionCookie(response http.ResponseWriter) {
	http.SetCookie(response, &http.Cookie{Name: adminSessionCookie, Value: "", Path: "/v1/admin", Domain: a.config.AdminSessionDomain, MaxAge: -1, HttpOnly: true, Secure: a.config.AdminSecure, SameSite: http.SameSiteStrictMode})
}

func (a *api) adminIPHash(request *http.Request) string {
	return adminstore.HashIP(a.clientIP(request), a.config.AdminIPPepper)
}

func (a *api) adminLogin(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodPost) {
		return
	}
	store, ok := a.requireAdminStore(response)
	if !ok {
		return
	}
	var input adminLoginRequest
	if !decodeAdminJSON(response, request, &input) {
		return
	}
	email, valid := normalizedEmail(input.Email)
	if !valid {
		accounts.DummyVerifyPassword()
		writeError(response, http.StatusUnauthorized, "invalid_admin_credentials", "Email, password, or MFA code is incorrect.")
		return
	}
	// Bounded by the attempted email and by the peer, so a multi-IP attacker gets no fresh per-account budget.
	if !a.allowKeyed(response, request, "admin-login:"+adminstore.HashIP(email, a.config.AdminIPPepper), 5, time.Minute) {
		return
	}
	if !a.allowRequest(response, request, "admin-login-peer", 20, time.Minute) {
		return
	}
	if len(input.Password) < 12 || len(input.Code) != 6 {
		accounts.DummyVerifyPassword()
		writeError(response, http.StatusUnauthorized, "invalid_admin_credentials", "Email, password, or MFA code is incorrect.")
		return
	}
	admin, passwordHash, secret, lastUsedCounter, err := store.FindByEmail(request.Context(), email)
	if err != nil {
		// The response stays identical so it reveals nothing about whether the email exists.
		if !errors.Is(err, adminstore.ErrNotFound) {
			slog.Warn("admin sign-in could not read the account",
				"reason", "the stored MFA secret did not decrypt, or the admin database is unreachable",
				"error", err)
		}
		accounts.DummyVerifyPassword()
		writeError(response, http.StatusUnauthorized, "invalid_admin_credentials", "Email, password, or MFA code is incorrect.")
		return
	}
	counter, totpOK := adminstore.VerifyTOTP(secret, input.Code, time.Now().UTC())
	if admin.Suspended || !admin.MFAVerified || !accounts.VerifyPassword(passwordHash, input.Password) || !totpOK || counter <= lastUsedCounter {
		writeError(response, http.StatusUnauthorized, "invalid_admin_credentials", "Email, password, or MFA code is incorrect.")
		return
	}
	token, tokenHash, err := adminstore.NewToken()
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "admin_login_unavailable", "Admin sign-in is temporarily unavailable.")
		return
	}
	if err := store.CreateSession(request.Context(), admin, tokenHash, a.adminIPHash(request), request.UserAgent(), time.Now().UTC().Add(a.config.AdminSessionTTL), counter); err != nil {
		if errors.Is(err, adminstore.ErrTOTPReplay) {
			writeError(response, http.StatusUnauthorized, "invalid_admin_credentials", "Email, password, or MFA code is incorrect.")
		} else {
			writeError(response, http.StatusServiceUnavailable, "admin_login_unavailable", "Admin sign-in is temporarily unavailable.")
		}
		return
	}
	a.setAdminSessionCookie(response, token)
	writeJSON(response, http.StatusOK, map[string]any{"admin": admin})
}

func (a *api) adminSetupBegin(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodPost) {
		return
	}
	if !a.allowRequest(response, request, "admin-setup-begin", 10, time.Minute) {
		return
	}
	store, ok := a.requireAdminStore(response)
	if !ok {
		return
	}
	var input adminSetupRequest
	if !decodeAdminJSON(response, request, &input) || len(input.Token) < 32 {
		return
	}
	account, secret, err := store.SetupDetails(request.Context(), adminstore.HashToken(input.Token), time.Now().UTC())
	if err != nil {
		// A fresh link reporting itself expired means the encryption key does not match.
		if errors.Is(err, adminstore.ErrSecretUnreadable) {
			slog.Warn("admin setup link could not be opened",
				"reason", "SESAME_ADMIN_ENCRYPTION_KEY does not match the stored MFA secret")
		}
		writeError(response, http.StatusBadRequest, "admin_setup_expired", "That admin setup link is invalid or expired.")
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"email": account.Email, "role": account.Role, "secret": secret, "uri": adminstore.TOTPURI(account.Email, secret)})
}

func (a *api) adminSetupComplete(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodPost) {
		return
	}
	if !a.allowRequest(response, request, "admin-setup-complete", 5, time.Minute) {
		return
	}
	store, ok := a.requireAdminStore(response)
	if !ok {
		return
	}
	var input adminSetupRequest
	if !decodeAdminJSON(response, request, &input) || len(input.Token) < 32 || !validPassword(input.Password) || len(input.Code) != 6 {
		writeError(response, http.StatusBadRequest, "invalid_admin_setup", "Use a password of 12 to 1024 characters and the current six-digit MFA code.")
		return
	}
	_, secret, err := store.SetupDetails(request.Context(), adminstore.HashToken(input.Token), time.Now().UTC())
	counter, totpOK := adminstore.VerifyTOTP(secret, input.Code, time.Now().UTC())
	if err != nil || !totpOK {
		writeError(response, http.StatusBadRequest, "invalid_admin_setup", "The setup link or MFA code is invalid or expired.")
		return
	}
	passwordHash, err := accounts.HashPassword(input.Password)
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "admin_setup_unavailable", "Admin setup is temporarily unavailable.")
		return
	}
	account, err := store.CompleteSetup(request.Context(), adminstore.HashToken(input.Token), passwordHash, time.Now().UTC(), a.adminIPHash(request), counter)
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "admin_setup_unavailable", "Admin setup is temporarily unavailable.")
		return
	}
	token, tokenHash, err := adminstore.NewToken()
	if err != nil || store.CreateSessionAfterSetup(request.Context(), account, tokenHash, a.adminIPHash(request), request.UserAgent(), time.Now().UTC().Add(a.config.AdminSessionTTL), counter) != nil {
		writeError(response, http.StatusServiceUnavailable, "admin_setup_unavailable", "Admin setup completed, but the session could not be created. Sign in again.")
		return
	}
	a.setAdminSessionCookie(response, token)
	writeJSON(response, http.StatusOK, map[string]any{"admin": account})
}

func (a *api) adminForRequest(response http.ResponseWriter, request *http.Request) (adminstore.Account, bool) {
	store, ok := a.requireAdminStore(response)
	if !ok {
		return adminstore.Account{}, false
	}
	cookie, err := request.Cookie(adminSessionCookie)
	if err != nil || cookie.Value == "" {
		writeError(response, http.StatusUnauthorized, "admin_not_authenticated", "Sign in with an admin account to continue.")
		return adminstore.Account{}, false
	}
	account, err := store.AccountBySession(request.Context(), adminstore.HashToken(cookie.Value))
	if err != nil {
		a.clearAdminSessionCookie(response)
		writeError(response, http.StatusUnauthorized, "admin_not_authenticated", "The admin session is invalid or expired.")
		return adminstore.Account{}, false
	}
	return account, true
}

func (a *api) requireAdminPermission(response http.ResponseWriter, request *http.Request, permission adminstore.Permission) (adminstore.Account, bool) {
	account, ok := a.adminForRequest(response, request)
	if !ok {
		return adminstore.Account{}, false
	}
	if !adminstore.Allowed(account.Role, permission) {
		writeError(response, http.StatusForbidden, "admin_forbidden", "Your admin role cannot perform this action.")
		return adminstore.Account{}, false
	}
	return account, true
}

func (a *api) adminMe(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodGet) {
		return
	}
	account, ok := a.adminForRequest(response, request)
	if ok {
		writeJSON(response, http.StatusOK, map[string]any{"admin": account})
	}
}

func (a *api) adminLogout(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodPost) {
		return
	}
	store, ok := a.requireAdminStore(response)
	if !ok {
		return
	}
	account, authenticated := a.adminForRequest(response, request)
	if !authenticated {
		return
	}
	if cookie, err := request.Cookie(adminSessionCookie); err == nil && cookie.Value != "" {
		if err := store.DeleteSession(request.Context(), account, adminstore.HashToken(cookie.Value), a.adminIPHash(request)); err != nil {
			adminStoreError(response, err)
			return
		}
	}
	a.clearAdminSessionCookie(response)
	response.WriteHeader(http.StatusNoContent)
}

func decodeAdminJSON(response http.ResponseWriter, request *http.Request, target any) bool {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(response, http.StatusUnsupportedMediaType, "admin_json_required", "Admin requests must use application/json.")
		return false
	}
	request.Body = http.MaxBytesReader(response, request.Body, maxAdminBodyBytes)
	body, err := io.ReadAll(request.Body)
	if err != nil || len(body) == 0 {
		writeError(response, http.StatusBadRequest, "invalid_admin_request", "The admin request could not be read.")
		return false
	}
	var raw any
	if json.Unmarshal(body, &raw) != nil {
		writeError(response, http.StatusBadRequest, "invalid_admin_request", "The admin request must be valid JSON.")
		return false
	}
	if containsVaultShapedField(raw) {
		writeError(response, http.StatusBadRequest, "vault_data_not_accepted", "The admin API does not accept vault data or vault credentials.")
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_admin_request", "The admin request contains unsupported fields.")
		return false
	}
	return true
}

func containsVaultShapedField(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, nested := range typed {
			if _, forbidden := adminstore.VaultShapedFields[key]; forbidden {
				return true
			}
			if containsVaultShapedField(nested) {
				return true
			}
		}
	case []any:
		for _, nested := range typed {
			if containsVaultShapedField(nested) {
				return true
			}
		}
	}
	return false
}

func adminPathParts(path, prefix string) []string {
	trimmed := strings.Trim(strings.TrimPrefix(path, prefix), "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

func adminStoreError(response http.ResponseWriter, err error) {
	if errors.Is(err, adminstore.ErrNotFound) {
		writeError(response, http.StatusNotFound, "admin_record_not_found", "That record does not exist.")
		return
	}
	if errors.Is(err, adminstore.ErrNotAllowed) {
		writeError(response, http.StatusConflict, "admin_action_not_allowed", "That admin action is not allowed.")
		return
	}
	writeError(response, http.StatusServiceUnavailable, "admin_action_unavailable", "The admin action could not be completed and no change was committed.")
}
