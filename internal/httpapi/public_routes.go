package httpapi

import (
	"context"
	"net/http"
	"strings"

	"usesesame.app/backend/internal/accounts"
	"usesesame.app/backend/internal/product"
)

const securityUpdated = "2026-07-13"

func (a *api) livez(response http.ResponseWriter, request *http.Request) {
	writeJSON(response, http.StatusOK, map[string]any{"status": "ok", "service": "sesame-api", "version": a.config.Version})
}

func (a *api) readyz(response http.ResponseWriter, request *http.Request) {
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
	registrationMode := a.runtimeRegistrationMode(request.Context())
	publicDownload := a.runtimeFlagBool(request.Context(), "public_download", false)
	// Every other field here is read from runtime state, so the phase is too.
	// A literal would keep announcing an invite-only beta to the website long
	// after the download gate was opened.
	phase := "private-beta-foundation"
	accountPurposes := []string{"beta access", "signed downloads", "licences", "connected-device management"}
	if publicDownload {
		phase = "public-beta"
		accountPurposes = []string{"signed downloads", "licences", "connected-device management"}
	}
	writeJSON(response, http.StatusOK, map[string]any{
		"phase":                      phase,
		"platforms":                  []string{"windows"},
		"accountRequired":            false,
		"webSignInAvailable":         a.config.Accounts != nil,
		"desktopConnectionAvailable": hasDesktopStore(a.config.Accounts),
		"registrationMode":           registrationMode,
		"accountPurposes":            accountPurposes,
		"cloudSyncAvailable":         a.syncEnabled(request.Context()),
		"publicDownload":             publicDownload,
		"updated":                    securityUpdated,
	})
}

func (a *api) plans(response http.ResponseWriter, request *http.Request) {
	if a.config.Admin != nil {
		if plans, err := a.config.Admin.Plans(request.Context()); err == nil {
			writeJSON(response, http.StatusOK, map[string]any{"plans": plans})
			return
		}
	}
	writeJSON(response, http.StatusOK, map[string]any{"plans": product.Plans()})
}

func (a *api) latestRelease(response http.ResponseWriter, request *http.Request) {
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

func (a *api) boundaries(response http.ResponseWriter, request *http.Request) {
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

func (a *api) support(response http.ResponseWriter, request *http.Request) {
	writeJSON(response, http.StatusOK, map[string]any{
		"status":              "private-beta",
		"url":                 strings.TrimRight(a.config.WebBaseURL, "/") + "/support",
		"intake":              "/v1/support/requests",
		"attachmentsAccepted": false,
		"message":             "Describe what happened without sending vault files, passwords, TOTP seeds, backup codes, recovery notes, or tokens.",
	})
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

func hasDesktopStore(store accounts.Store) bool {
	_, ok := store.(accounts.DesktopStore)
	return ok
}
