package httpapi

import (
	"container/list"
	"context"
	"crypto/ed25519"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"

	"usesesame.app/backend/internal/accounts"
	adminstore "usesesame.app/backend/internal/admin"
	"usesesame.app/backend/internal/syncstore"
)

const (
	sessionCookieName = "sesame_session"
	csrfCookieName    = "sesame_csrf"
	maxAuthBodyBytes  = 16 * 1024
	desktopSessionTTL = 90 * 24 * time.Hour
	desktopLinkTTL    = 10 * time.Minute
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
	routes    *routeRegistry
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
	service := &api{
		config:    config,
		limits:    &authLimiter{attempts: make(map[string]*limitEntry), recency: list.New()},
		startedAt: time.Now().UTC(),
		routes:    newRouteRegistry(),
	}

	mux := http.NewServeMux()
	web := routePolicy{audience: audienceWebsiteSession}
	privateWebRead := routePolicy{audience: audienceWebsiteSession, requireWebOrigin: true}
	metadata := routePolicy{audience: audiencePublicMetadata}

	service.route(mux, metadata, "GET /livez", service.livez)
	service.route(mux, metadata, "GET /readyz", service.readyz)
	service.route(mux, metadata, "GET /healthz", service.readyz)
	service.route(mux, metadata, "GET /v1/plans", service.plans)
	service.route(mux, metadata, "GET /v1/product/status", service.productStatus)
	service.route(mux, metadata, "GET /v1/releases/latest", service.latestRelease)
	service.route(mux, metadata, "GET /v1/security/boundaries", service.boundaries)
	service.route(mux, metadata, "GET /v1/capabilities", service.capabilities)
	service.route(mux, metadata, "GET /v1/support", service.support)
	service.route(mux, metadata, "GET /v1/auth/registration", service.registrationStatus)

	service.route(mux, web, "POST /v1/support/requests", service.createSupportRequest)
	service.route(mux, web, "POST /v1/auth/register", service.register)
	service.route(mux, privateWebRead, "GET /v1/auth/csrf", service.csrf)
	service.route(mux, web, "POST /v1/auth/login", service.login)
	service.route(mux, web, "POST /v1/auth/logout", service.logout)
	service.route(mux, privateWebRead, "GET /v1/auth/me", service.me)
	service.route(mux, web, "POST /v1/auth/email/verification/request", service.requestEmailVerification)
	service.route(mux, web, "POST /v1/auth/email/verification/confirm", service.confirmEmailVerification)
	service.route(mux, web, "POST /v1/auth/password/recovery/request", service.requestPasswordRecovery)
	service.route(mux, web, "POST /v1/auth/password/recovery/confirm", service.confirmPasswordRecovery)
	service.route(mux, web, "POST /v1/account/reauthenticate", service.reauthenticate)
	service.route(mux, web, "POST /v1/account/email/change/request", service.requestEmailChange)
	service.route(mux, web, "POST /v1/account/email/change/confirm", service.confirmEmailChange)
	service.route(mux, privateWebRead, "GET /v1/account/sessions", service.listAccountSessions)
	service.route(mux, web, "DELETE /v1/account/sessions", service.revokeAllAccountSessions)
	service.route(mux, web, "DELETE /v1/account/sessions/{sessionID}", service.accountSession)
	service.route(mux, privateWebRead, "GET /v1/account/access", service.accountAccess)
	service.route(mux, privateWebRead, "GET /v1/account/bootstrap", service.accountBootstrap)
	service.route(mux, privateWebRead, "GET /v1/account/activity", service.accountActivity)
	service.route(mux, privateWebRead, "GET /v1/account/notifications", service.accountNotificationPreferences)
	service.route(mux, web, "PATCH /v1/account/notifications", service.updateAccountNotificationPreferences)
	service.route(mux, privateWebRead, "GET /v1/account/downloads", service.accountDownloads)
	service.route(mux, web, "POST /v1/account/download-tickets", service.accountDownloadTickets)
	service.route(mux, web, "GET /v1/account/support", service.accountSupportTickets)
	service.route(mux, web, "GET /v1/account/support/{ticketID}", service.accountSupportTicket)
	service.route(mux, web, "POST /v1/account/support/{ticketID}/reply", service.accountSupportTicketAction("reply"))
	service.route(mux, web, "POST /v1/account/support/{ticketID}/close", service.accountSupportTicketAction("close"))
	service.route(mux, web, "POST /v1/account/support/{ticketID}/reopen", service.accountSupportTicketAction("reopen"))
	service.route(mux, privateWebRead, "GET /v1/account/desktop-link", service.desktopLinkStatus)
	service.route(mux, web, "POST /v1/account/desktop-link", service.regenerateDesktopLink)
	service.route(mux, web, "DELETE /v1/account/desktop-link", service.cancelDesktopLink)
	service.route(mux, privateWebRead, "GET /v1/account/devices", service.accountDevices)
	service.route(mux, web, "DELETE /v1/account/devices/{deviceID}", service.revokeAccountDevice)
	service.route(mux, web, "PATCH /v1/account/devices/{deviceID}", service.renameAccountDevice)
	service.route(mux, web, "POST /v1/account/password", service.changePassword)
	service.route(mux, web, "POST /v1/account/delete", service.deleteAccount)
	service.route(mux, web, "POST /v1/account/passkey/register/begin", service.passkeyRegisterBegin)
	service.route(mux, web, "POST /v1/account/passkey/register/finish", service.passkeyRegisterFinish)
	service.route(mux, privateWebRead, "GET /v1/account/passkeys", service.listPasskeys)
	service.route(mux, web, "DELETE /v1/account/passkeys", service.deletePasskey)
	service.route(mux, web, "POST /v1/auth/passkey/login/begin", service.passkeyLoginBegin)
	service.route(mux, web, "POST /v1/auth/passkey/login/finish", service.passkeyLoginFinish)

	desktop := routePolicy{audience: audienceDesktopClient}
	service.route(mux, desktop, "GET /v1/downloads/{ticket}", service.redeemDownloadTicket)
	service.route(mux, desktop, "POST /v1/desktop/link", service.linkDesktop)
	service.route(mux, desktop, "GET /v1/desktop/status", service.desktopStatus)
	service.route(mux, desktop, "POST /v1/desktop/heartbeat", service.desktopHeartbeat)
	service.route(mux, desktop, "GET /v1/desktop/config", service.desktopConfig)
	service.route(mux, desktop, "GET /v1/desktop/updates", service.desktopUpdate)
	service.route(mux, desktop, "GET /v1/desktop/update-tickets/{ticket}", service.redeemDesktopUpdateTicket)
	service.route(mux, desktop, "DELETE /v1/desktop/connection", service.revokeDesktopConnection)

	pipeline := routePolicy{audience: audienceReleasePipeline}
	service.route(mux, pipeline, "POST /v1/release-candidates", service.releaseCandidateIngest)

	service.registerSyncRoutes(mux)
	service.registerAdminRoutes(mux)

	service.finishRoutes(mux)
	mux.HandleFunc("/", service.notFound)
	return service.secureMux(mux)
}
