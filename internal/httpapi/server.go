package httpapi

import (
	"container/list"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"

	"usesesame.app/backend/internal/accounts"
	adminstore "usesesame.app/backend/internal/admin"
	"usesesame.app/backend/internal/product"
	"usesesame.app/backend/internal/syncstore"
)

const (
	sessionCookieName = "sesame_session"
	csrfCookieName    = "sesame_csrf"
	maxAuthBodyBytes  = 16 * 1024
	desktopSessionTTL = 90 * 24 * time.Hour
	desktopLinkTTL    = 10 * time.Minute
	securityUpdated   = "2026-07-13"
	termsVersion      = "2026-07-14"
	privacyVersion    = "2026-07-14"
)

type Config struct {
	Version       string
	AllowedOrigin string
	// The marketing site: it may read published metadata only, never act on a session.
	PublicSiteOrigin string
	SessionSecure    bool
	SessionDomain    string
	SessionDuration  time.Duration
	Accounts         accounts.Store
	Admin            *adminstore.Store
	// Nil disables every /v1/sync route; the cloud_sync_available flag must also be on.
	Sync                      *syncstore.Store
	ReleaseRegistry           ReleaseRegistry
	AdminOrigin               string
	AdminSecure               bool
	AdminSessionDomain        string
	AdminSessionTTL           time.Duration
	AdminIPPepper             string
	CapabilitySigningKey      ed25519.PrivateKey
	CapabilityKeyID           string
	MinimumDesktopVersion     string
	LatestDesktopVersion      string
	CapabilityTTL             time.Duration
	ReleaseCandidatePublicKey ed25519.PublicKey
	ReleaseCandidateKeyID     string
	// Hash only; the plaintext token is never retained.
	ReleaseCandidateTokenHash []byte
	DesktopUpdateBaseURL      string
	ArtifactDelivery          ArtifactDelivery
	RegistrationMode          string
	WebBaseURL                string
	EmailSender               EmailSender
	RecentAuthDuration        time.Duration
	// Nil disables the passkey endpoints. It never touches the local vault.
	Passkeys *webauthn.WebAuthn
	// Only these peers may supply X-Forwarded-For, so a caller cannot choose its limiter key.
	TrustedProxies []netip.Prefix
}

type ReleaseRegistry interface {
	AcceptReleaseCandidate(context.Context, adminstore.Account, adminstore.ReleaseCandidate, string) (adminstore.Release, error)
	IsOwnerReleaseRingMember(context.Context, string) (bool, error)
	LatestPublishedReleaseForChannel(context.Context, string, string, string) (adminstore.Release, error)
	PublishedReleasesForUpdate(context.Context, string, string, bool) ([]adminstore.Release, error)
}

type api struct {
	config    Config
	limits    *authLimiter
	startedAt time.Time
}

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

type desktopLinkRequest struct {
	Code       string `json:"code"`
	DeviceName string `json:"deviceName"`
}

type desktopHeartbeatRequest struct {
	AppVersion            string `json:"appVersion"`
	Platform              string `json:"platform"`
	Architecture          string `json:"architecture"`
	UpdateChannel         string `json:"updateChannel"`
	ProtocolVersion       int    `json:"protocolVersion"`
	BrowserHelperCapable  bool   `json:"browserHelperCapable"`
	BrowserHelperObserved bool   `json:"browserHelperObserved"`
}

type deviceRenameRequest struct {
	DeviceName string `json:"deviceName"`
}

func New(config Config) http.Handler {
	if config.ReleaseRegistry == nil && config.Admin != nil {
		config.ReleaseRegistry = config.Admin
	}
	if config.SessionDuration <= 0 {
		config.SessionDuration = 30 * 24 * time.Hour
	}
	if config.RecentAuthDuration <= 0 {
		config.RecentAuthDuration = 10 * time.Minute
	}
	if config.AdminSessionTTL <= 0 {
		config.AdminSessionTTL = 8 * time.Hour
	}
	if config.CapabilityTTL <= 0 {
		config.CapabilityTTL = 5 * time.Minute
	}
	if config.MinimumDesktopVersion == "" {
		config.MinimumDesktopVersion = "0.0.0"
	}
	if config.LatestDesktopVersion == "" {
		config.LatestDesktopVersion = config.MinimumDesktopVersion
	}
	if config.RegistrationMode != "invite" && config.RegistrationMode != "public" {
		config.RegistrationMode = "closed"
	}
	if strings.TrimSpace(config.WebBaseURL) == "" {
		config.WebBaseURL = strings.TrimSuffix(config.AllowedOrigin, "/")
	}
	service := &api{config: config, limits: &authLimiter{attempts: make(map[string]*limitEntry), recency: list.New()}, startedAt: time.Now().UTC()}
	mux := http.NewServeMux()
	mux.HandleFunc("/livez", service.livez)
	mux.HandleFunc("/readyz", service.readyz)
	mux.HandleFunc("/healthz", service.readyz)
	mux.HandleFunc("/v1/plans", service.plans)
	mux.HandleFunc("/v1/product/status", service.productStatus)
	mux.HandleFunc("/v1/releases/latest", service.latestRelease)
	mux.HandleFunc("/v1/security/boundaries", service.boundaries)
	mux.HandleFunc("/v1/capabilities", service.capabilities)
	mux.HandleFunc("/v1/support", service.support)
	mux.HandleFunc("/v1/support/requests", service.createSupportRequest)
	mux.HandleFunc("/v1/auth/registration", service.registrationStatus)
	mux.HandleFunc("/v1/auth/register", service.register)
	mux.HandleFunc("/v1/auth/csrf", service.csrf)
	mux.HandleFunc("/v1/auth/login", service.login)
	mux.HandleFunc("/v1/auth/logout", service.logout)
	mux.HandleFunc("/v1/auth/me", service.me)
	mux.HandleFunc("/v1/auth/email/verification/request", service.requestEmailVerification)
	mux.HandleFunc("/v1/auth/email/verification/confirm", service.confirmEmailVerification)
	mux.HandleFunc("/v1/auth/password/recovery/request", service.requestPasswordRecovery)
	mux.HandleFunc("/v1/auth/password/recovery/confirm", service.confirmPasswordRecovery)
	mux.HandleFunc("/v1/account/reauthenticate", service.reauthenticate)
	mux.HandleFunc("/v1/account/email/change/request", service.requestEmailChange)
	mux.HandleFunc("/v1/account/email/change/confirm", service.confirmEmailChange)
	mux.HandleFunc("/v1/account/sessions", service.accountSessions)
	mux.HandleFunc("/v1/account/sessions/", service.accountSession)
	mux.HandleFunc("/v1/account/access", service.accountAccess)
	mux.HandleFunc("/v1/account/bootstrap", service.accountBootstrap)
	mux.HandleFunc("/v1/account/activity", service.accountActivity)
	mux.HandleFunc("/v1/account/notifications", service.accountNotificationPreferences)
	mux.HandleFunc("/v1/account/downloads", service.accountDownloads)
	mux.HandleFunc("/v1/account/download-tickets", service.accountDownloadTickets)
	mux.HandleFunc("/v1/downloads/", service.redeemDownloadTicket)
	mux.HandleFunc("/v1/account/support", service.accountSupportTickets)
	mux.HandleFunc("/v1/account/support/", service.accountSupportTicket)
	mux.HandleFunc("/v1/account/desktop-link", service.createDesktopLink)
	mux.HandleFunc("/v1/account/devices", service.accountDevices)
	mux.HandleFunc("/v1/account/devices/", service.accountDevice)
	mux.HandleFunc("/v1/account/password", service.changePassword)
	mux.HandleFunc("/v1/account/delete", service.deleteAccount)
	mux.HandleFunc("/v1/account/passkey/register/begin", service.passkeyRegisterBegin)
	mux.HandleFunc("/v1/account/passkey/register/finish", service.passkeyRegisterFinish)
	mux.HandleFunc("/v1/account/passkeys", service.passkeys)
	mux.HandleFunc("/v1/auth/passkey/login/begin", service.passkeyLoginBegin)
	mux.HandleFunc("/v1/auth/passkey/login/finish", service.passkeyLoginFinish)
	mux.HandleFunc("/v1/desktop/link", service.linkDesktop)
	mux.HandleFunc("/v1/desktop/status", service.desktopStatus)
	mux.HandleFunc("/v1/desktop/heartbeat", service.desktopHeartbeat)
	mux.HandleFunc("/v1/desktop/config", service.desktopConfig)
	mux.HandleFunc("/v1/desktop/updates", service.desktopUpdate)
	mux.HandleFunc("/v1/desktop/update-tickets/", service.redeemDesktopUpdateTicket)
	mux.HandleFunc("/v1/desktop/connection", service.revokeDesktopConnection)
	mux.HandleFunc("/v1/release-candidates", service.releaseCandidateIngest)
	service.registerSyncRoutes(mux)
	service.registerAdminRoutes(mux)
	mux.HandleFunc("/", service.notFound)
	return service.middleware(mux)
}

func (a *api) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("X-Request-ID", newRequestID())
		response.Header().Set("X-Content-Type-Options", "nosniff")
		response.Header().Set("Referrer-Policy", "no-referrer")
		response.Header().Set("Cross-Origin-Resource-Policy", "same-site")
		response.Header().Set("X-Frame-Options", "DENY")
		response.Header().Set("Cache-Control", "no-store")
		response.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		response.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		origin := request.Header.Get("Origin")
		expectedOrigin := a.config.AllowedOrigin
		adminRequest := strings.HasPrefix(request.URL.Path, "/v1/admin/")
		if adminRequest {
			expectedOrigin = a.config.AdminOrigin
		}
		publicSiteRead := !adminRequest &&
			origin != "" &&
			a.config.PublicSiteOrigin != "" &&
			origin == a.config.PublicSiteOrigin &&
			!isUnsafeMethod(request.Method) &&
			isPublicMetadataPath(request.URL.Path)
		if origin != "" && origin == expectedOrigin {
			response.Header().Set("Access-Control-Allow-Origin", origin)
			response.Header().Set("Access-Control-Allow-Credentials", "true")
			response.Header().Set("Access-Control-Expose-Headers", "X-Request-ID, Retry-After")
			response.Header().Set("Vary", "Origin")
		} else if publicSiteRead {
			// Deliberately without Access-Control-Allow-Credentials: public-site reads are anonymous.
			response.Header().Set("Access-Control-Allow-Origin", origin)
			response.Header().Set("Access-Control-Expose-Headers", "X-Request-ID, Retry-After")
			response.Header().Set("Vary", "Origin")
		}
		if request.Method == http.MethodOptions {
			if origin != "" && origin != expectedOrigin {
				writeError(response, http.StatusForbidden, "origin_not_allowed", "This origin is not allowed.")
				return
			}
			response.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, PUT, DELETE, OPTIONS")
			response.Header().Set("Access-Control-Allow-Headers", "Content-Type, Idempotency-Key, X-Sesame-CSRF")
			response.Header().Set("Access-Control-Max-Age", "600")
			response.WriteHeader(http.StatusNoContent)
			return
		}
		if isUnsafeMethod(request.Method) {
			if origin != "" && origin != expectedOrigin {
				writeError(response, http.StatusForbidden, "origin_not_allowed", "This request origin is not allowed.")
				return
			}
			if adminRequest {
				if expectedOrigin == "" || origin != expectedOrigin {
					writeError(response, http.StatusForbidden, "origin_not_allowed", "This request must come from the Sesame admin app.")
					return
				}
				if !a.validAdminCSRF(request) {
					writeError(response, http.StatusForbidden, "invalid_csrf", "Refresh the Sesame admin app and try again.")
					return
				}
			} else if requiresWebsiteOrigin(request.URL.Path) && origin != a.config.AllowedOrigin {
				writeError(response, http.StatusForbidden, "origin_not_allowed", "This request must come from the Sesame website.")
				return
			}
			if strings.HasPrefix(request.URL.Path, "/v1/desktop/") && origin != "" {
				writeError(response, http.StatusForbidden, "origin_not_allowed", "Desktop linking is not available from a browser.")
				return
			}
			if !adminRequest && requiresWebsiteOrigin(request.URL.Path) && !a.validCSRF(request) {
				writeError(response, http.StatusForbidden, "invalid_csrf", "Refresh the Sesame website and try again.")
				return
			}
		} else if adminRequest && (expectedOrigin == "" || origin != expectedOrigin) {
			writeError(response, http.StatusForbidden, "origin_not_allowed", "This request must come from the Sesame admin app.")
			return
		} else if requiresPrivateWebsiteRead(request.URL.Path) && origin != a.config.AllowedOrigin {
			writeError(response, http.StatusForbidden, "origin_not_allowed", "This request must come from the Sesame website.")
			return
		}
		if isPublicMetadataPath(request.URL.Path) && (request.ContentLength > 0 || len(request.TransferEncoding) > 0) {
			request.Body.Close()
			writeError(response, http.StatusUnsupportedMediaType, "request_body_not_supported", "This public metadata API does not accept request bodies.")
			return
		}
		next.ServeHTTP(response, request)
	})
}

func newRequestID() string {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "unavailable"
	}
	return base64.RawURLEncoding.EncodeToString(bytes)
}

func requiresPrivateWebsiteRead(path string) bool {
	return path == "/v1/auth/csrf" || path == "/v1/auth/me" || path == "/v1/account/bootstrap" || path == "/v1/account/activity" || path == "/v1/account/notifications" || path == "/v1/account/devices" ||
		path == "/v1/account/passkeys" || path == "/v1/account/sessions" || path == "/v1/account/access" ||
		path == "/v1/account/downloads" || path == "/v1/account/desktop-link"
}

func requiresWebsiteOrigin(path string) bool {
	return strings.HasPrefix(path, "/v1/auth/") || strings.HasPrefix(path, "/v1/account/") || path == "/v1/support/requests"
}

func isPublicMetadataPath(path string) bool {
	switch path {
	case "/livez", "/readyz", "/healthz", "/v1/plans", "/v1/product/status", "/v1/releases/latest", "/v1/security/boundaries", "/v1/capabilities", "/v1/support", "/v1/auth/registration":
		return true
	default:
		return false
	}
}

func (a *api) csrf(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodGet) {
		return
	}
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

func (a *api) livez(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodGet) {
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"status": "ok", "service": "sesame-api", "version": a.config.Version})
}

func (a *api) readyz(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodGet) {
		return
	}
	accountStore := "unconfigured"
	if a.config.Accounts != nil {
		if err := a.config.Accounts.Ping(request.Context()); err != nil {
			writeJSON(response, http.StatusServiceUnavailable, map[string]any{"status": "not_ready", "service": "sesame-api", "version": a.config.Version, "accounts": "unavailable"})
			return
		}
		accountStore = "ready"
	}
	writeJSON(response, http.StatusOK, map[string]any{"status": "ok", "service": "sesame-api", "version": a.config.Version, "accounts": accountStore})
}

func (a *api) productStatus(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodGet) {
		return
	}
	registrationMode := a.runtimeRegistrationMode(request.Context())
	writeJSON(response, http.StatusOK, map[string]any{
		"phase":                      "private-beta-foundation",
		"platforms":                  []string{"windows"},
		"accountRequired":            false,
		"webSignInAvailable":         a.config.Accounts != nil,
		"desktopConnectionAvailable": hasDesktopStore(a.config.Accounts),
		"registrationMode":           registrationMode,
		"accountPurposes":            []string{"beta access", "signed downloads", "licences", "connected-device management"},
		"cloudSyncAvailable":         a.syncEnabled(request.Context()),
		"publicDownload":             a.runtimeFlagBool(request.Context(), "public_download", false),
		"updated":                    securityUpdated,
	})
}

func (a *api) plans(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodGet) {
		return
	}
	if a.config.Admin != nil {
		if plans, err := a.config.Admin.Plans(request.Context()); err == nil {
			writeJSON(response, http.StatusOK, map[string]any{"plans": plans})
			return
		}
	}
	writeJSON(response, http.StatusOK, map[string]any{"plans": product.Plans()})
}

func (a *api) latestRelease(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodGet) {
		return
	}
	if platform := request.URL.Query().Get("platform"); platform != "" && platform != "windows" {
		writeError(response, http.StatusBadRequest, "unsupported_platform", "Only the Windows release channel exists.")
		return
	}
	if a.config.Admin != nil && a.runtimeFlagBool(request.Context(), "public_download", false) {
		if release, err := a.config.Admin.LatestPublishedRelease(request.Context(), "windows"); err == nil {
			supported := splitPublicList(release.SupportedWindows)
			writeJSON(response, http.StatusOK, map[string]any{
				"channel": release.Channel, "platform": release.Platform, "available": true,
				"version": release.Version, "url": release.URL, "sha256": release.SHA256, "signed": release.Signature != "",
				"message": "This Windows build has a verified Tauri updater signature.", "publishedAt": release.PublishedAt,
				"supportedWindows": supported, "rollbackNotice": release.RollbackNotice,
				"releaseNotesUrl": release.ReleaseNotesURL, "signingKeyId": release.SigningKeyID,
			})
			return
		}
	}
	writeJSON(response, http.StatusOK, product.LatestWindowsRelease())
}

func (a *api) runtimeRegistrationMode(ctx context.Context) string {
	if a.config.Admin != nil {
		if value, err := a.config.Admin.FeatureFlag(ctx, "registration_mode"); err == nil && (value == "closed" || value == "invite" || value == "public") {
			return value
		}
		return "closed"
	}
	return a.config.RegistrationMode
}

func (a *api) syncEnabled(ctx context.Context) bool {
	return a.runtimeFlagBool(ctx, "cloud_sync_available", false) && a.config.Sync != nil
}

func (a *api) runtimeFlagBool(ctx context.Context, key string, fallback bool) bool {
	if a.config.Admin != nil {
		if value, err := a.config.Admin.FeatureFlag(ctx, key); err == nil {
			return value == "true"
		}
		return false
	}
	return fallback
}

// Fails closed when the admin flag store is unavailable; no-admin mode keeps the configured baseline.
func (a *api) capabilityEnabled(ctx context.Context, key string) bool {
	return a.runtimeFlagBool(ctx, key, a.config.Admin == nil)
}

func splitPublicList(value string) []string {
	parts := strings.FieldsFunc(value, func(character rune) bool { return character == ',' || character == '|' })
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func (a *api) boundaries(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodGet) {
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{
		"acceptsVaultData":        false,
		"storesVaultData":         false,
		"acceptsVaultCredentials": false,
		"acceptsAccountPassword":  a.config.Accounts != nil,
		"purposes":                []string{"public product metadata", "beta eligibility", "signed download access", "licence metadata", "website sessions", "connected-device management"},
		"prohibitedData":          []string{"vault credentials", "TOTP secrets", "backup codes", "recovery notes", "credential imports", "vault files", "vault keys", "master passwords"},
		"updated":                 securityUpdated,
	})
}

func hasDesktopStore(store accounts.Store) bool {
	_, ok := store.(accounts.DesktopStore)
	return ok
}

func (a *api) support(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodGet) {
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{
		"status":              "private-beta",
		"url":                 strings.TrimRight(a.config.WebBaseURL, "/") + "/support",
		"intake":              "/v1/support/requests",
		"attachmentsAccepted": false,
		"message":             "Describe what happened without sending vault files, passwords, TOTP seeds, backup codes, recovery notes, or tokens.",
	})
}

func (a *api) register(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodPost) || !a.requireAccounts(response) || !a.allowAuthAttempt(response, request, "register") {
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
	if !allowMethod(response, request, http.MethodPost) || !a.requireAccounts(response) || !a.allowAuthAttempt(response, request, "login") {
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
	if !allowMethod(response, request, http.MethodPost) || !a.requireAccounts(response) || !a.allowRequest(response, request, "logout", 30, time.Minute) {
		return
	}
	if cookie, err := request.Cookie(a.sessionCookieName()); err == nil && cookie.Value != "" {
		_ = a.config.Accounts.DeleteSession(request.Context(), accounts.HashSessionToken(cookie.Value))
	}
	a.clearSessionCookie(response)
	response.WriteHeader(http.StatusNoContent)
}

func (a *api) me(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodGet) || !a.requireAccounts(response) || !a.allowRequest(response, request, "me", 60, time.Minute) {
		return
	}
	user, ok := a.userForRequest(response, request)
	if !ok {
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"user": user})
}

func (a *api) changePassword(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodPost) || !a.requireAccounts(response) || !a.allowAuthAttempt(response, request, "password") {
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
	if !allowMethod(response, request, http.MethodPost) || !a.requireAccounts(response) || !a.allowAuthAttempt(response, request, "delete") {
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

func (a *api) createDesktopLink(response http.ResponseWriter, request *http.Request) {
	if !a.capabilityEnabled(request.Context(), "desktop_linking_enabled") {
		writeError(response, http.StatusServiceUnavailable, "desktop_linking_disabled", "Desktop linking is temporarily unavailable.")
		return
	}
	switch request.Method {
	case http.MethodGet:
		a.desktopLinkStatus(response, request)
	case http.MethodPost:
		a.regenerateDesktopLink(response, request)
	case http.MethodDelete:
		a.cancelDesktopLink(response, request)
	default:
		response.Header().Set("Allow", "GET, POST, DELETE, OPTIONS")
		writeError(response, http.StatusMethodNotAllowed, "method_not_allowed", "This endpoint does not allow that method.")
	}
}

func (a *api) accountDevices(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodGet) || !a.requireAccounts(response) || !a.allowRequest(response, request, "account-devices", 60, time.Minute) {
		return
	}
	user, ok := a.userForRequest(response, request)
	if !ok {
		return
	}
	store, ok := a.config.Accounts.(accounts.DesktopStore)
	if !ok {
		writeError(response, http.StatusServiceUnavailable, "desktop_link_unavailable", "Desktop linking is not configured.")
		return
	}
	connections, err := store.DesktopConnectionsForAccount(request.Context(), user.ID)
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "desktop_devices_unavailable", "Connected desktops are temporarily unavailable.")
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"devices": connections})
}

func (a *api) revokeAccountDevice(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodDelete) || !a.requireAccounts(response) || !a.allowRequest(response, request, "account-device-revoke", 20, time.Minute) {
		return
	}
	user, _, _, ok := a.recentSessionForRequest(response, request)
	if !ok {
		return
	}
	deviceID := strings.TrimPrefix(request.URL.Path, "/v1/account/devices/")
	if len(deviceID) != 32 || strings.Contains(deviceID, "/") {
		writeError(response, http.StatusBadRequest, "invalid_device", "That desktop device id is invalid.")
		return
	}
	for _, character := range deviceID {
		if !strings.ContainsRune("0123456789abcdef", character) {
			writeError(response, http.StatusBadRequest, "invalid_device", "That desktop device id is invalid.")
			return
		}
	}
	store, ok := a.config.Accounts.(accounts.DesktopStore)
	if !ok {
		writeError(response, http.StatusServiceUnavailable, "desktop_link_unavailable", "Desktop linking is not configured.")
		return
	}
	if err := store.RevokeDesktopConnectionForAccount(request.Context(), user.ID, deviceID); errors.Is(err, accounts.ErrNotFound) {
		writeError(response, http.StatusNotFound, "device_not_found", "That desktop is no longer connected.")
		return
	} else if err != nil {
		writeError(response, http.StatusServiceUnavailable, "desktop_unlink_unavailable", "Removing that desktop is temporarily unavailable.")
		return
	}
	a.recordAccountEvent(request.Context(), user.ID, "desktop_revoked", "Sesame desktop", nil)
	a.sendSecurityNotification(request.Context(), user, "security-desktop-revoked", "A Sesame desktop was removed", "A connected Sesame desktop was removed from your website account.")
	response.WriteHeader(http.StatusNoContent)
}

func (a *api) accountDevice(response http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodDelete:
		a.revokeAccountDevice(response, request)
	case http.MethodPatch:
		a.renameAccountDevice(response, request)
	default:
		response.Header().Set("Allow", "PATCH, DELETE, OPTIONS")
		writeError(response, http.StatusMethodNotAllowed, "method_not_allowed", "This endpoint does not allow that method.")
	}
}

func (a *api) renameAccountDevice(response http.ResponseWriter, request *http.Request) {
	if !a.requireAccounts(response) || !a.allowAuthAttempt(response, request, "account-device-rename") {
		return
	}
	user, _, _, ok := a.recentSessionForRequest(response, request)
	if !ok {
		return
	}
	deviceID := strings.TrimPrefix(request.URL.Path, "/v1/account/devices/")
	if !validDeviceID(deviceID) {
		writeError(response, http.StatusBadRequest, "invalid_device", "That desktop device id is invalid.")
		return
	}
	var input deviceRenameRequest
	if !decodeJSONBodyWith(response, request, &input, "invalid_device", "Desktop name details could not be read.") {
		return
	}
	if !validDeviceName(input.DeviceName) {
		writeError(response, http.StatusBadRequest, "invalid_device_name", "Enter a desktop name of up to 64 characters.")
		return
	}
	manager, ok := a.config.Accounts.(accounts.DesktopConnectionManager)
	if !ok {
		writeError(response, http.StatusServiceUnavailable, "desktop_devices_unavailable", "Connected desktops are temporarily unavailable.")
		return
	}
	if err := manager.RenameDesktopConnection(request.Context(), user.ID, deviceID, strings.TrimSpace(input.DeviceName)); errors.Is(err, accounts.ErrNotFound) {
		writeError(response, http.StatusNotFound, "device_not_found", "That desktop is no longer connected.")
		return
	} else if err != nil {
		writeError(response, http.StatusServiceUnavailable, "desktop_devices_unavailable", "Connected desktops are temporarily unavailable.")
		return
	}
	a.recordAccountEvent(request.Context(), user.ID, "desktop_renamed", strings.TrimSpace(input.DeviceName), nil)
	response.WriteHeader(http.StatusNoContent)
}

func (a *api) linkDesktop(response http.ResponseWriter, request *http.Request) {
	if !a.capabilityEnabled(request.Context(), "desktop_linking_enabled") {
		writeError(response, http.StatusServiceUnavailable, "desktop_linking_disabled", "Desktop linking is temporarily unavailable.")
		return
	}
	if !allowMethod(response, request, http.MethodPost) || !a.requireAccounts(response) || !a.allowAuthAttempt(response, request, "desktop-link") {
		return
	}
	store, ok := a.config.Accounts.(accounts.DesktopStore)
	if !ok {
		writeError(response, http.StatusServiceUnavailable, "desktop_link_unavailable", "Desktop linking is not configured.")
		return
	}
	input, ok := decodeDesktopLinkRequest(response, request)
	if !ok {
		return
	}
	if !validDeviceName(input.DeviceName) || len(input.Code) < 32 || len(input.Code) > 128 {
		writeError(response, http.StatusBadRequest, "invalid_desktop_link", "This desktop link code or device name is invalid.")
		return
	}
	token, tokenHash, err := accounts.NewSessionToken()
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "desktop_link_unavailable", "Desktop linking is temporarily unavailable.")
		return
	}
	connection, err := store.RedeemDesktopLink(request.Context(), accounts.HashSessionToken(input.Code), input.DeviceName, tokenHash, time.Now().Add(desktopSessionTTL))
	if errors.Is(err, accounts.ErrNotFound) {
		writeError(response, http.StatusUnauthorized, "invalid_desktop_link", "This desktop link code is invalid or has expired.")
		return
	}
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "desktop_link_unavailable", "Desktop linking is temporarily unavailable.")
		return
	}
	a.recordAccountEvent(request.Context(), connection.AccountID, "desktop_linked", connection.DeviceName, nil)
	if user, _, err := a.config.Accounts.FindByID(request.Context(), connection.AccountID); err == nil {
		a.sendSecurityNotification(request.Context(), user, "security-desktop-linked", "A Sesame desktop was linked", "A desktop named "+connection.DeviceName+" was linked to your Sesame website account.")
	}
	writeJSON(response, http.StatusCreated, map[string]any{
		"accessToken":   token,
		"device":        connection,
		"expiresAt":     time.Now().Add(desktopSessionTTL).UTC().Format(time.RFC3339),
		"syncAvailable": false,
	})
}

func (a *api) desktopStatus(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodGet) || !a.requireAccounts(response) || !a.allowRequest(response, request, "desktop-status", 60, time.Minute) {
		return
	}
	connection, ok := a.desktopConnectionForRequest(response, request)
	if !ok {
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"connected": true, "device": connection, "syncAvailable": false, "browserHelperAvailable": connection.BrowserHelperCapable})
}

func (a *api) desktopHeartbeat(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodPost) || !a.requireAccounts(response) || !a.allowRequest(response, request, "desktop-heartbeat", 120, time.Minute) {
		return
	}
	token, ok := desktopToken(response, request)
	if !ok {
		return
	}
	var input desktopHeartbeatRequest
	if !decodeJSONBodyWith(response, request, &input, "invalid_desktop_heartbeat", "Desktop status details could not be read.") || !validDesktopHeartbeat(input) {
		writeError(response, http.StatusBadRequest, "invalid_desktop_heartbeat", "Desktop status details are invalid.")
		return
	}
	manager, ok := a.config.Accounts.(accounts.DesktopConnectionManager)
	if !ok {
		writeError(response, http.StatusServiceUnavailable, "desktop_devices_unavailable", "Desktop status is temporarily unavailable.")
		return
	}
	connection, err := manager.HeartbeatDesktopConnection(request.Context(), accounts.HashSessionToken(token), accounts.DesktopHeartbeat{
		AppVersion: input.AppVersion, Platform: input.Platform, Architecture: input.Architecture, UpdateChannel: input.UpdateChannel,
		ProtocolVersion: input.ProtocolVersion, BrowserHelperCapable: input.BrowserHelperCapable, BrowserHelperObserved: input.BrowserHelperObserved,
	})
	if errors.Is(err, accounts.ErrNotFound) {
		writeError(response, http.StatusUnauthorized, "not_authenticated", "This desktop is no longer linked.")
		return
	}
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "desktop_heartbeat_unavailable", "Desktop status is temporarily unavailable.")
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"device": connection})
}

func (a *api) desktopConfig(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodGet) || !a.requireAccounts(response) || !a.allowRequest(response, request, "desktop-config", 60, time.Minute) {
		return
	}
	connection, ok := a.desktopConnectionForRequest(response, request)
	if !ok {
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{
		"minimumProtocolVersion": 1,
		"syncAvailable":          false,
		"browserHelper":          map[string]any{"capable": connection.BrowserHelperCapable, "lastObservedAt": connection.BrowserHelperLastObservedAt},
	})
}

func (a *api) revokeDesktopConnection(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodDelete) || !a.requireAccounts(response) || !a.allowRequest(response, request, "desktop-revoke", 20, time.Minute) {
		return
	}
	token, ok := desktopToken(response, request)
	if !ok {
		return
	}
	store, ok := a.config.Accounts.(accounts.DesktopStore)
	if !ok {
		writeError(response, http.StatusServiceUnavailable, "desktop_link_unavailable", "Desktop linking is not configured.")
		return
	}
	if err := store.RevokeDesktopConnection(request.Context(), accounts.HashSessionToken(token)); err != nil {
		writeError(response, http.StatusServiceUnavailable, "desktop_link_unavailable", "Desktop unlinking is temporarily unavailable.")
		return
	}
	response.WriteHeader(http.StatusNoContent)
}
