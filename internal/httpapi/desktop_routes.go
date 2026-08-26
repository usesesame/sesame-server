package httpapi

import (
	"errors"
	"net/http"
	"time"

	"usesesame.app/backend/internal/accounts"
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

func (a *api) linkDesktop(response http.ResponseWriter, request *http.Request) {
	if !a.capabilityEnabled(request.Context(), "desktop_linking_enabled") {
		writeError(response, http.StatusServiceUnavailable, "desktop_linking_disabled", "Desktop linking is temporarily unavailable.")
		return
	}
	if !a.requireAccounts(response) || !a.allowAuthAttempt(response, request, "desktop-link") {
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
	if !a.requireAccounts(response) || !a.allowRequest(response, request, "desktop-status", 60, time.Minute) {
		return
	}
	connection, ok := a.desktopConnectionForRequest(response, request)
	if !ok {
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"connected": true, "device": connection, "syncAvailable": false, "browserHelperAvailable": connection.BrowserHelperCapable})
}

func (a *api) desktopHeartbeat(response http.ResponseWriter, request *http.Request) {
	if !a.requireAccounts(response) || !a.allowRequest(response, request, "desktop-heartbeat", 120, time.Minute) {
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
	if !a.requireAccounts(response) || !a.allowRequest(response, request, "desktop-config", 60, time.Minute) {
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
	if !a.requireAccounts(response) || !a.allowRequest(response, request, "desktop-revoke", 20, time.Minute) {
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
