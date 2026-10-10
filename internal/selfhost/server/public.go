package server

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"time"

	"usesesame.app/backend/internal/selfhost"
)

const serviceName = "sesame-server"

type capabilityDocument struct {
	SchemaVersion         int             `json:"schemaVersion"`
	MinimumDesktopVersion string          `json:"minimumDesktopVersion"`
	LatestDesktopVersion  string          `json:"latestDesktopVersion"`
	Features              map[string]bool `json:"features"`
	ServiceStatus         map[string]bool `json:"serviceStatus"`
	ExpiresAt             time.Time       `json:"expiresAt"`
}

func (s *server) livez(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": serviceName, "version": s.cfg.Version, "commit": s.cfg.Commit})
}

func (s *server) readyz(w http.ResponseWriter, r *http.Request) {
	if err := s.cfg.Store.Ping(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "not_ready", "service": serviceName, "version": s.cfg.Version, "commit": s.cfg.Commit, "database": "unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": serviceName, "version": s.cfg.Version, "commit": s.cfg.Commit, "database": "ready"})
}

func (s *server) consoleConfig(w http.ResponseWriter, r *http.Request) {
	required, err := s.cfg.Store.SetupRequired(r.Context())
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": s.cfg.Version, "setupRequired": required, "apiBase": ""})
}

func (s *server) instance(w http.ResponseWriter, r *http.Request) {
	instance, err := s.cfg.Store.Instance(r.Context())
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	required, err := s.cfg.Store.SetupRequired(r.Context())
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	body := map[string]any{
		"instanceId":           instance.ID,
		"name":                 instance.Name,
		"version":              s.cfg.Version,
		"commit":               s.cfg.Commit,
		"apiVersion":           1,
		"minimumClientVersion": s.cfg.MinimumClientVersion,
		"profile":              selfhost.Profile,
		"modules":              []string{},
		"capabilityKeyId":      s.keyID,
		"capabilityPublicKey":  s.publicKey,
		"fingerprint":          s.fingerprint,
		"setupRequired":        required,
	}
	if s.cfg.MaximumClientVersion != "" {
		body["maximumClientVersion"] = s.cfg.MaximumClientVersion
	}
	writeJSON(w, http.StatusOK, body)
}

func (s *server) desktopLinkingEnabled(r *http.Request) (bool, error) {
	flags, err := s.cfg.Store.Flags(r.Context())
	if err != nil {
		return false, err
	}
	for _, flag := range flags {
		if flag.Key == FlagDesktopLinking {
			return flag.Enabled, nil
		}
	}
	return true, nil
}

func (s *server) capabilities(w http.ResponseWriter, r *http.Request) {
	linking, err := s.desktopLinkingEnabled(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "capabilities_unavailable", "Capability configuration is temporarily unavailable.")
		return
	}
	document := capabilityDocument{
		SchemaVersion:         1,
		MinimumDesktopVersion: s.cfg.MinimumClientVersion,
		LatestDesktopVersion:  s.cfg.LatestDesktopVersion,
		Features:              map[string]bool{"desktopLinking": linking, "downloads": false, "updater": false, "sync": false},
		ServiceStatus:         map[string]bool{"accounts": false, "downloads": false, "desktop": linking, "sync": false},
		ExpiresAt:             s.now().UTC().Add(s.cfg.CapabilityTTL).Truncate(time.Minute),
	}
	payload, err := json.Marshal(document)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "capabilities_unavailable", "Capability configuration is temporarily unavailable.")
		return
	}
	digest := sha256.Sum256(payload)
	etag := `"` + base64.RawURLEncoding.EncodeToString(digest[:]) + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "public, max-age=60")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	signature := ed25519.Sign(s.cfg.SigningKey, payload)
	writeJSON(w, http.StatusOK, map[string]any{
		"payload":   base64.RawURLEncoding.EncodeToString(payload),
		"signature": base64.RawURLEncoding.EncodeToString(signature),
		"keyId":     s.keyID,
	})
}
