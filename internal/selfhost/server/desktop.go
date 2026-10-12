package server

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"usesesame.app/backend/internal/selfhost"
)

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

type desktopDevice struct {
	DeviceID                    string     `json:"deviceId"`
	DeviceName                  string     `json:"deviceName"`
	ConnectedAt                 time.Time  `json:"connectedAt"`
	ExpiresAt                   time.Time  `json:"expiresAt"`
	AppVersion                  string     `json:"appVersion,omitempty"`
	Platform                    string     `json:"platform,omitempty"`
	Architecture                string     `json:"architecture,omitempty"`
	UpdateChannel               string     `json:"updateChannel,omitempty"`
	LastSeenAt                  time.Time  `json:"lastSeenAt"`
	ProtocolVersion             int        `json:"protocolVersion"`
	BrowserHelperCapable        bool       `json:"browserHelperCapable"`
	BrowserHelperLastObservedAt *time.Time `json:"browserHelperLastObservedAt,omitempty"`
}

func desktopDeviceOf(device selfhost.Device) desktopDevice {
	return desktopDevice{
		DeviceID: device.ID, DeviceName: device.Name,
		ConnectedAt: stamp(device.CreatedAt), ExpiresAt: stamp(device.ExpiresAt),
		AppVersion: device.Meta.AppVersion, Platform: device.Meta.Platform, Architecture: device.Meta.Architecture, UpdateChannel: device.Meta.UpdateChannel,
		LastSeenAt: stamp(device.LastSeenAt), ProtocolVersion: device.Meta.ProtocolVersion,
		BrowserHelperCapable: device.Meta.BrowserHelperCapable, BrowserHelperLastObservedAt: stampPointer(device.BrowserHelperLastObservedAt),
	}
}

func (s *server) desktopRoute(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.hostGuard(w, r) {
			return
		}
		if isUnsafeMethod(r.Method) && r.Header.Get("Origin") != "" {
			writeError(w, http.StatusForbidden, "origin_not_allowed", "Desktop linking is not available from a browser.")
			return
		}
		next(w, r)
	}
}

func desktopToken(w http.ResponseWriter, r *http.Request) (string, bool) {
	value := r.Header.Get("Authorization")
	for _, scheme := range []string{"Sesame ", "Bearer "} {
		if token, found := strings.CutPrefix(value, scheme); found {
			if token != "" && len(token) <= maxTokenLength && !strings.ContainsAny(token, " \t") {
				return token, true
			}
			break
		}
	}
	writeError(w, http.StatusUnauthorized, "not_authenticated", "This desktop is not linked.")
	return "", false
}

func (s *server) authenticateDevice(w http.ResponseWriter, r *http.Request) (selfhost.Device, string, bool) {
	token, ok := desktopToken(w, r)
	if !ok {
		return selfhost.Device{}, "", false
	}
	device, err := s.cfg.Store.AuthenticateDevice(r.Context(), token)
	if err != nil {
		if errors.Is(err, selfhost.ErrDeviceInvalid) || errors.Is(err, selfhost.ErrNotFound) {
			writeError(w, http.StatusUnauthorized, "not_authenticated", "This desktop is no longer linked.")
			return selfhost.Device{}, "", false
		}
		s.storeError(w, r, err)
		return selfhost.Device{}, "", false
	}
	return device, token, true
}

func (s *server) desktopLink(w http.ResponseWriter, r *http.Request) {
	linking, err := s.desktopLinkingEnabled(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "desktop_link_unavailable", "Desktop linking is temporarily unavailable.")
		return
	}
	if !linking {
		writeError(w, http.StatusServiceUnavailable, "desktop_linking_disabled", "Desktop linking is temporarily unavailable.")
		return
	}
	if !s.allowIP(w, r, "desktop-link", 8, time.Minute) {
		return
	}
	var input desktopLinkRequest
	if !s.decode(w, r, &input, "invalid_desktop_link", "Desktop link details could not be read.") {
		return
	}
	deviceName, nameOK := selfhost.NormalizeDeviceName(input.DeviceName)
	if !nameOK || !selfhost.ValidPairingCode(input.Code) {
		writeError(w, http.StatusBadRequest, "invalid_desktop_link", "This desktop link code or device name is invalid.")
		return
	}
	issued, err := s.cfg.Store.RedeemPairing(r.Context(), selfhost.RedeemInput{Code: input.Code, DeviceName: deviceName})
	if err != nil {
		switch {
		case errors.Is(err, selfhost.ErrPairingInvalid), errors.Is(err, selfhost.ErrNotFound):
			writeError(w, http.StatusUnauthorized, "invalid_desktop_link", "This desktop link code is invalid or has expired.")
		case errors.Is(err, selfhost.ErrInvalidInput):
			writeError(w, http.StatusBadRequest, "invalid_desktop_link", "This desktop link code or device name is invalid.")
		default:
			requestLog(r.Context()).Error("Sesame server desktop link failed", "error", err)
			writeError(w, http.StatusServiceUnavailable, "desktop_link_unavailable", "Desktop linking is temporarily unavailable.")
		}
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"accessToken":   issued.Token,
		"device":        desktopDeviceOf(issued.Device),
		"expiresAt":     stamp(issued.Device.ExpiresAt).Format(time.RFC3339),
		"syncAvailable": false,
	})
}

func (s *server) desktopStatus(w http.ResponseWriter, r *http.Request) {
	if !s.allowIP(w, r, "desktop-status", 60, time.Minute) {
		return
	}
	device, _, ok := s.authenticateDevice(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"connected":              true,
		"device":                 desktopDeviceOf(device),
		"syncAvailable":          false,
		"browserHelperAvailable": device.Meta.BrowserHelperCapable,
	})
}

func (s *server) desktopHeartbeat(w http.ResponseWriter, r *http.Request) {
	if !s.allowIP(w, r, "desktop-heartbeat", 120, time.Minute) {
		return
	}
	token, ok := desktopToken(w, r)
	if !ok {
		return
	}
	var input desktopHeartbeatRequest
	if !s.decode(w, r, &input, "invalid_desktop_heartbeat", "Desktop status details could not be read.") {
		return
	}
	meta := selfhost.DeviceMeta{
		AppVersion: input.AppVersion, Platform: input.Platform, Architecture: input.Architecture, UpdateChannel: input.UpdateChannel,
		ProtocolVersion: input.ProtocolVersion, BrowserHelperCapable: input.BrowserHelperCapable, BrowserHelperObserved: input.BrowserHelperObserved,
	}
	if input.ProtocolVersion < 1 || !selfhost.ValidDeviceMeta(meta) {
		writeError(w, http.StatusBadRequest, "invalid_desktop_heartbeat", "Desktop status details are invalid.")
		return
	}
	device, err := s.cfg.Store.Heartbeat(r.Context(), token, meta)
	if err != nil {
		switch {
		case errors.Is(err, selfhost.ErrDeviceInvalid), errors.Is(err, selfhost.ErrNotFound):
			writeError(w, http.StatusUnauthorized, "not_authenticated", "This desktop is no longer linked.")
		case errors.Is(err, selfhost.ErrInvalidInput):
			writeError(w, http.StatusBadRequest, "invalid_desktop_heartbeat", "Desktop status details are invalid.")
		default:
			requestLog(r.Context()).Error("Sesame server desktop heartbeat failed", "error", err)
			writeError(w, http.StatusServiceUnavailable, "desktop_heartbeat_unavailable", "Desktop status is temporarily unavailable.")
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"device": desktopDeviceOf(device)})
}

func (s *server) desktopConfig(w http.ResponseWriter, r *http.Request) {
	if !s.allowIP(w, r, "desktop-config", 60, time.Minute) {
		return
	}
	device, _, ok := s.authenticateDevice(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"minimumProtocolVersion": 1,
		"syncAvailable":          false,
		"browserHelper":          map[string]any{"capable": device.Meta.BrowserHelperCapable, "lastObservedAt": stampPointer(device.BrowserHelperLastObservedAt)},
	})
}

func (s *server) desktopDisconnect(w http.ResponseWriter, r *http.Request) {
	if !s.allowIP(w, r, "desktop-revoke", 20, time.Minute) {
		return
	}
	token, ok := desktopToken(w, r)
	if !ok {
		return
	}
	if err := s.cfg.Store.Disconnect(r.Context(), token); err != nil && !errors.Is(err, selfhost.ErrDeviceInvalid) && !errors.Is(err, selfhost.ErrNotFound) {
		requestLog(r.Context()).Error("Sesame server desktop disconnect failed", "error", err)
		writeError(w, http.StatusServiceUnavailable, "desktop_link_unavailable", "Desktop unlinking is temporarily unavailable.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
