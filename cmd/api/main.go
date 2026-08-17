package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"usesesame.app/backend/internal/accounts"
	adminstore "usesesame.app/backend/internal/admin"
	"usesesame.app/backend/internal/httpapi"
	"usesesame.app/backend/internal/notifications"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if databaseURL == "" {
		slog.Error("Sesame API configuration is invalid", "error", "DATABASE_URL is required")
		os.Exit(1)
	}
	webOrigin, configErr := configuredOrigin("SESAME_WEB_ORIGIN")
	if configErr != nil {
		slog.Error("Sesame API configuration is invalid", "error", configErr)
		os.Exit(1)
	}
	capabilityKey, capabilityKeyErr := capabilitySigningKey(os.Getenv("SESAME_CAPABILITY_SIGNING_KEY"))
	if capabilityKeyErr != nil {
		slog.Error("Sesame API configuration is invalid", "error", capabilityKeyErr)
		os.Exit(1)
	}
	releaseCandidateKey, releaseCandidateKeyErr := releaseCandidatePublicKey(os.Getenv("SESAME_RELEASE_CANDIDATE_PUBLIC_KEY"))
	if releaseCandidateKeyErr != nil {
		slog.Error("Sesame API configuration is invalid", "error", releaseCandidateKeyErr)
		os.Exit(1)
	}
	releaseCandidateToken, releaseCandidateTokenErr := releaseCandidateTokenHash(os.Getenv("SESAME_RELEASE_CANDIDATE_TOKEN"))
	if releaseCandidateTokenErr != nil {
		slog.Error("Sesame API configuration is invalid", "error", releaseCandidateTokenErr)
		os.Exit(1)
	}
	artifactDelivery, artifactDeliveryErr := artifactDeliveryFromEnvironment()
	if artifactDeliveryErr != nil {
		slog.Error("Sesame API configuration is invalid", "error", artifactDeliveryErr)
		os.Exit(1)
	}
	sessionSecure := envBool("SESAME_SESSION_SECURE", true)
	if strings.HasPrefix(webOrigin, "https://") && !sessionSecure {
		slog.Error("Sesame API configuration is invalid", "error", "Secure session cookies are required for an HTTPS website origin")
		os.Exit(1)
	}
	trustedProxies, err := parseTrustedProxies(os.Getenv("SESAME_TRUSTED_PROXIES"))
	if err != nil {
		slog.Error("Sesame API configuration is invalid", "error", err)
		os.Exit(1)
	}
	store, err := accounts.OpenWithoutMigrate(ctx, databaseURL)
	if err != nil {
		slog.Error("Sesame API could not open the account database", "error", err)
		os.Exit(1)
	}
	defer store.Close()
	var adminService *adminstore.Store
	adminKeyValue := strings.TrimSpace(os.Getenv("SESAME_ADMIN_ENCRYPTION_KEY"))
	adminOrigin := ""
	adminSecure := envBool("SESAME_ADMIN_SESSION_SECURE", sessionSecure)
	adminIPPepper := strings.TrimSpace(os.Getenv("SESAME_ADMIN_IP_PEPPER"))
	if adminKeyValue != "" {
		var originErr error
		adminOrigin, originErr = configuredOrigin("SESAME_ADMIN_ORIGIN")
		if originErr != nil {
			slog.Error("Sesame admin configuration is invalid", "error", originErr)
			os.Exit(1)
		}
		adminKey, keyErr := adminstore.ParseEncryptionKey(adminKeyValue)
		if keyErr != nil {
			slog.Error("Sesame admin configuration is invalid", "error", keyErr)
			os.Exit(1)
		}
		if strings.HasPrefix(adminOrigin, "https://") && !adminSecure {
			slog.Error("Sesame admin configuration is invalid", "error", "Secure admin cookies are required for an HTTPS admin origin")
			os.Exit(1)
		}
		if adminIPPepper == "" {
			slog.Error("Sesame admin configuration is invalid", "error", "SESAME_ADMIN_IP_PEPPER is required when the admin service is enabled")
			os.Exit(1)
		}
		adminService, err = adminstore.Open(ctx, databaseURL, adminKey)
		if err != nil {
			slog.Error("Sesame API could not open the admin database", "error", err)
			os.Exit(1)
		}
		defer adminService.Close()

		if unreadable, checkErr := adminService.UnreadableSecrets(ctx); checkErr != nil {
			slog.Warn("Sesame API could not verify the admin encryption key", "error", checkErr)
		} else if len(unreadable) > 0 {
			slog.Warn("Sesame admin accounts cannot be read with the configured key",
				"accounts", len(unreadable),
				"reason", "SESAME_ADMIN_ENCRYPTION_KEY differs from the key that wrote these MFA secrets",
				"effect", "sign-in for these accounts will fail as though the password were wrong",
				"fix", "restore the original key, or run `npm run backend:admin:bootstrap -- reset <email>` to issue a new setup link")
		}
	} else {
		slog.Warn("Sesame admin API is disabled", "reason", "SESAME_ADMIN_ENCRYPTION_KEY is not configured")
	}
	emailSender, outbox, worker, err := buildEmailSender(ctx, store.DB())
	if err != nil {
		slog.Error("Sesame API email configuration is invalid", "error", err)
		os.Exit(1)
	}
	if worker != nil {
		go worker.Run(ctx)
	}
	config := httpapi.Config{
		Version:                   env("SESAME_API_VERSION", "0.1.0-dev"),
		AllowedOrigin:             webOrigin,
		PublicSiteOrigin:          strings.TrimSuffix(strings.TrimSpace(os.Getenv("SESAME_PUBLIC_SITE_ORIGIN")), "/"),
		SessionSecure:             sessionSecure,
		SessionDomain:             env("SESAME_SESSION_DOMAIN", ""),
		SessionDuration:           30 * 24 * time.Hour,
		Accounts:                  store,
		RegistrationMode:          env("SESAME_REGISTRATION_MODE", "invite"),
		WebBaseURL:                webOrigin,
		EmailSender:               emailSender,
		RecentAuthDuration:        10 * time.Minute,
		TrustedProxies:            trustedProxies,
		Passkeys:                  buildPasskeys(webOrigin, env("SESAME_RP_ID", ""), env("SESAME_RP_NAME", "Sesame")),
		Admin:                     adminService,
		AdminOrigin:               adminOrigin,
		AdminSecure:               adminSecure,
		AdminSessionDomain:        strings.TrimSpace(os.Getenv("SESAME_ADMIN_SESSION_DOMAIN")),
		AdminSessionTTL:           8 * time.Hour,
		AdminIPPepper:             adminIPPepper,
		CapabilitySigningKey:      capabilityKey,
		CapabilityKeyID:           env("SESAME_CAPABILITY_KEY_ID", "capability-v1"),
		MinimumDesktopVersion:     env("SESAME_MINIMUM_DESKTOP_VERSION", "0.1.0"),
		LatestDesktopVersion:      env("SESAME_LATEST_DESKTOP_VERSION", "0.1.0"),
		CapabilityTTL:             5 * time.Minute,
		ReleaseCandidatePublicKey: releaseCandidateKey,
		ReleaseCandidateKeyID:     strings.TrimSpace(os.Getenv("SESAME_RELEASE_CANDIDATE_KEY_ID")),
		ReleaseCandidateTokenHash: releaseCandidateToken,
		DesktopUpdateBaseURL:      strings.TrimRight(strings.TrimSpace(os.Getenv("SESAME_DESKTOP_UPDATE_BASE_URL")), "/"),
		ArtifactDelivery:          artifactDelivery,
	}
	if err := store.PurgeExpired(ctx); err != nil {
		slog.Warn("Sesame API could not purge expired security records", "error", err)
	}
	go runMaintenance(ctx, store, outbox)
	server := &http.Server{
		Addr:              env("SESAME_API_ADDR", "127.0.0.1:8787"),
		Handler:           httpapi.New(config),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			slog.Error("API shutdown failed", "error", err)
		}
	}()

	slog.Info("Sesame API listening", "address", server.Addr, "version", config.Version)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("Sesame API stopped", "error", err)
		os.Exit(1)
	}
}

func artifactDeliveryFromEnvironment() (httpapi.ArtifactDelivery, error) {
	baseURL := strings.TrimSpace(os.Getenv("SESAME_ARTIFACT_GATEWAY_URL"))
	rawKey := strings.TrimSpace(os.Getenv("SESAME_ARTIFACT_GATEWAY_SIGNING_KEY"))
	if baseURL == "" && rawKey == "" {
		return nil, nil
	}
	if baseURL == "" || rawKey == "" {
		return nil, errors.New("SESAME_ARTIFACT_GATEWAY_URL and SESAME_ARTIFACT_GATEWAY_SIGNING_KEY must be configured together")
	}
	key, err := base64.RawURLEncoding.DecodeString(rawKey)
	if err != nil || len(key) != 32 {
		return nil, errors.New("SESAME_ARTIFACT_GATEWAY_SIGNING_KEY must contain a base64url 32-byte key")
	}
	return httpapi.NewHMACArtifactDelivery(baseURL, key)
}

func releaseCandidatePublicKey(value string) (ed25519.PublicKey, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil || len(decoded) != ed25519.PublicKeySize {
		return nil, errors.New("SESAME_RELEASE_CANDIDATE_PUBLIC_KEY must contain a base64url 32-byte Ed25519 public key")
	}
	return ed25519.PublicKey(decoded), nil
}

func releaseCandidateTokenHash(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) != 32 {
		return nil, errors.New("SESAME_RELEASE_CANDIDATE_TOKEN must contain a base64url 32-byte CI secret")
	}
	digest := sha256.Sum256(decoded)
	return digest[:], nil
}

func capabilitySigningKey(value string) (ed25519.PrivateKey, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return nil, errors.New("SESAME_CAPABILITY_SIGNING_KEY must be base64url encoded")
	}
	if len(decoded) == ed25519.SeedSize {
		return ed25519.NewKeyFromSeed(decoded), nil
	}
	if len(decoded) == ed25519.PrivateKeySize {
		return ed25519.PrivateKey(decoded), nil
	}
	return nil, errors.New("SESAME_CAPABILITY_SIGNING_KEY must contain a 32-byte seed or 64-byte private key")
}

func buildEmailSender(ctx context.Context, db *sql.DB) (httpapi.EmailSender, notifications.Outbox, *notifications.Worker, error) {
	address := strings.TrimSpace(os.Getenv("SESAME_SMTP_ADDR"))
	if address == "" {
		return nil, nil, nil, nil
	}
	from := strings.TrimSpace(os.Getenv("SESAME_SMTP_FROM"))
	localCapture := envBool("SESAME_SMTP_ALLOW_INSECURE_LOCAL", false)
	if localCapture && env("SESAME_ENV", "") != "development" {
		return nil, nil, nil, errors.New("SESAME_SMTP_ALLOW_INSECURE_LOCAL is allowed only when SESAME_ENV=development")
	}
	if localCapture && (strings.TrimSpace(os.Getenv("SESAME_SMTP_USERNAME")) != "" || os.Getenv("SESAME_SMTP_PASSWORD") != "") {
		return nil, nil, nil, errors.New("local SMTP capture must not use SMTP credentials")
	}
	var sender *notifications.SMTP
	var err error
	if localCapture {
		sender, err = notifications.NewSMTPForLocalDevelopment(address, from)
	} else {
		sender, err = notifications.NewSMTP(address, strings.TrimSpace(os.Getenv("SESAME_SMTP_USERNAME")), os.Getenv("SESAME_SMTP_PASSWORD"), from)
	}
	if err != nil {
		return nil, nil, nil, err
	}
	outbox := notifications.NewPostgresOutbox(db)
	worker := notifications.NewWorker(outbox, sender)
	if err := outbox.Ping(ctx); err != nil {
		return nil, nil, nil, err
	}
	return notifications.NewOutboxEmailSender(outbox), outbox, worker, nil
}

func parseTrustedProxies(value string) ([]netip.Prefix, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	prefixes := make([]netip.Prefix, 0)
	for _, raw := range strings.Split(value, ",") {
		raw = strings.TrimSpace(raw)
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			return nil, errors.New("SESAME_TRUSTED_PROXIES must contain CIDR ranges")
		}
		prefixes = append(prefixes, prefix.Masked())
	}
	return prefixes, nil
}

func runMaintenance(ctx context.Context, store accounts.MaintenanceStore, outbox notifications.Outbox) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			purgeContext, cancel := context.WithTimeout(ctx, 30*time.Second)
			if err := store.PurgeExpired(purgeContext); err != nil {
				slog.Warn("Sesame API could not purge expired security records", "error", err)
			}
			if outbox != nil {
				if n, err := outbox.PurgeDeliveredOlderThan(purgeContext, 7*24*time.Hour); err != nil {
					slog.Warn("Sesame API could not purge delivered email outbox records", "error", err)
				} else if n > 0 {
					slog.Info("purged delivered email outbox records", "count", n)
				}
				if n, err := outbox.PurgeFailedOlderThan(purgeContext, 7*24*time.Hour); err != nil {
					slog.Warn("Sesame API could not purge failed email outbox records", "error", err)
				} else if n > 0 {
					slog.Info("purged failed email outbox records", "count", n)
				}
			}
			cancel()
		}
	}
}

func buildPasskeys(origin, rpID, rpName string) *webauthn.WebAuthn {
	if origin == "" {
		return nil
	}
	if rpID == "" {
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Hostname() == "" {
			slog.Warn("passkey sign-in disabled: could not derive relying-party id", "origin", origin)
			return nil
		}
		rpID = parsed.Hostname()
	}
	wa, err := webauthn.New(&webauthn.Config{
		RPID:          rpID,
		RPDisplayName: rpName,
		RPOrigins:     []string{origin},
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementRequired,
			UserVerification: protocol.VerificationPreferred,
		},
	})
	if err != nil {
		slog.Warn("passkey sign-in disabled", "error", err)
		return nil
	}
	return wa
}

func envBool(name string, fallback bool) bool {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	return value == "1" || value == "true" || value == "TRUE"
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func configuredOrigin(name string) (string, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return "", errors.New(name + " is required")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", errors.New(name + " must be an absolute origin without credentials, a path, query, or fragment")
	}
	loopbackHTTP := parsed.Scheme == "http" && (parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "::1")
	if parsed.Scheme != "https" && !loopbackHTTP {
		return "", errors.New(name + " must use HTTPS (HTTP is allowed only for loopback development)")
	}
	return strings.TrimSuffix(parsed.String(), "/"), nil
}
