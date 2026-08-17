package httpapi

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"time"
)

// Vault-blind: feature availability only. Account entitlements are checked by their own endpoints.
type capabilityDocument struct {
	SchemaVersion         int             `json:"schemaVersion"`
	MinimumDesktopVersion string          `json:"minimumDesktopVersion"`
	LatestDesktopVersion  string          `json:"latestDesktopVersion"`
	Features              map[string]bool `json:"features"`
	ServiceStatus         map[string]bool `json:"serviceStatus"`
	ExpiresAt             time.Time       `json:"expiresAt"`
}

func (a *api) capabilities(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodGet) {
		return
	}
	if len(a.config.CapabilitySigningKey) != ed25519.PrivateKeySize {
		writeError(response, http.StatusServiceUnavailable, "capabilities_unavailable", "Capability configuration is temporarily unavailable.")
		return
	}
	document := capabilityDocument{
		SchemaVersion:         1,
		MinimumDesktopVersion: a.config.MinimumDesktopVersion,
		LatestDesktopVersion:  a.config.LatestDesktopVersion,
		Features: map[string]bool{
			"desktopLinking": a.runtimeFlagBool(request.Context(), "desktop_linking_enabled", false),
			"downloads":      a.runtimeFlagBool(request.Context(), "downloads_enabled", false),
			"updater":        a.runtimeFlagBool(request.Context(), "updater_enabled", false),
			"sync":           a.syncEnabled(request.Context()),
		},
		ServiceStatus: map[string]bool{
			"accounts":  a.config.Accounts != nil,
			"downloads": a.runtimeFlagBool(request.Context(), "downloads_enabled", false),
			"desktop":   a.runtimeFlagBool(request.Context(), "desktop_linking_enabled", false),
			"sync":      a.syncEnabled(request.Context()),
		},
		ExpiresAt: time.Now().UTC().Add(a.config.CapabilityTTL).Truncate(time.Minute),
	}
	payload, err := json.Marshal(document)
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "capabilities_unavailable", "Capability configuration is temporarily unavailable.")
		return
	}
	digest := sha256.Sum256(payload)
	etag := `"` + base64.RawURLEncoding.EncodeToString(digest[:]) + `"`
	response.Header().Set("ETag", etag)
	response.Header().Set("Cache-Control", "public, max-age=60")
	if request.Header.Get("If-None-Match") == etag {
		response.WriteHeader(http.StatusNotModified)
		return
	}
	signature := ed25519.Sign(a.config.CapabilitySigningKey, payload)
	writeJSON(response, http.StatusOK, map[string]any{
		"payload":   base64.RawURLEncoding.EncodeToString(payload),
		"signature": base64.RawURLEncoding.EncodeToString(signature),
		"keyId":     a.config.CapabilityKeyID,
	})
}
