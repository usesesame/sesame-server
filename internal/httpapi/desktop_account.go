package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"usesesame.app/backend/internal/accounts"
)

func (a *api) desktopLinkStatus(response http.ResponseWriter, request *http.Request) {
	if !a.capabilityEnabled(request.Context(), "desktop_linking_enabled") {
		writeError(response, http.StatusServiceUnavailable, "desktop_linking_disabled", "Desktop linking is temporarily unavailable.")
		return
	}
	if !a.requireAccounts(response) || !a.allowRequest(response, request, "desktop-link-status", 60, time.Minute) {
		return
	}
	user, ok := a.userForRequest(response, request)
	if !ok {
		return
	}
	manager, ok := a.config.Accounts.(accounts.DesktopLinkManager)
	if !ok {
		writeError(response, http.StatusServiceUnavailable, "desktop_link_unavailable", "Desktop linking is not configured.")
		return
	}
	link, err := manager.DesktopLinkStatus(request.Context(), user.ID, time.Now().UTC())
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "desktop_link_unavailable", "Desktop link status is temporarily unavailable.")
		return
	}
	writeJSON(response, http.StatusOK, link)
}

func (a *api) regenerateDesktopLink(response http.ResponseWriter, request *http.Request) {
	if !a.capabilityEnabled(request.Context(), "desktop_linking_enabled") {
		writeError(response, http.StatusServiceUnavailable, "desktop_linking_disabled", "Desktop linking is temporarily unavailable.")
		return
	}
	if !a.requireAccounts(response) || !a.allowRequest(response, request, "desktop-link-create", 8, time.Minute) {
		return
	}
	if request.ContentLength > 0 || len(request.TransferEncoding) > 0 {
		writeError(response, http.StatusUnsupportedMediaType, "request_body_not_supported", "Creating a desktop link does not accept a request body.")
		return
	}
	user, _, _, ok := a.recentSessionForRequest(response, request)
	if !ok {
		return
	}
	if !user.BetaAccess || !user.EmailVerified {
		writeError(response, http.StatusForbidden, "account_access_required", "Verify an eligible beta account before connecting a desktop.")
		return
	}
	manager, ok := a.config.Accounts.(accounts.DesktopLinkManager)
	if !ok {
		writeError(response, http.StatusServiceUnavailable, "desktop_link_unavailable", "Desktop linking is not configured.")
		return
	}
	code, codeHash, err := accounts.NewSessionToken()
	expiresAt := time.Now().Add(desktopLinkTTL)
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "desktop_link_unavailable", "Desktop linking is temporarily unavailable.")
		return
	}
	link, err := manager.CreateOrReplaceDesktopLink(request.Context(), user.ID, codeHash, expiresAt)
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "desktop_link_unavailable", "Desktop linking is temporarily unavailable.")
		return
	}
	writeJSON(response, http.StatusCreated, map[string]any{
		"state": link.State, "linkId": link.LinkID, "code": code,
		"createdAt": link.CreatedAt, "expiresAt": link.ExpiresAt,
	})
}

func (a *api) cancelDesktopLink(response http.ResponseWriter, request *http.Request) {
	if !a.capabilityEnabled(request.Context(), "desktop_linking_enabled") {
		writeError(response, http.StatusServiceUnavailable, "desktop_linking_disabled", "Desktop linking is temporarily unavailable.")
		return
	}
	if !a.requireAccounts(response) || !a.allowRequest(response, request, "desktop-link-cancel", 20, time.Minute) {
		return
	}
	user, _, _, ok := a.recentSessionForRequest(response, request)
	if !ok {
		return
	}
	manager, ok := a.config.Accounts.(accounts.DesktopLinkManager)
	if !ok {
		writeError(response, http.StatusServiceUnavailable, "desktop_link_unavailable", "Desktop linking is not configured.")
		return
	}
	if err := manager.CancelDesktopLink(request.Context(), user.ID); err != nil {
		writeError(response, http.StatusServiceUnavailable, "desktop_link_unavailable", "Desktop linking is temporarily unavailable.")
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

type deviceRenameRequest struct {
	DeviceName string `json:"deviceName"`
}

func (a *api) accountDevices(response http.ResponseWriter, request *http.Request) {
	if !a.requireAccounts(response) || !a.allowRequest(response, request, "account-devices", 60, time.Minute) {
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
	if !a.requireAccounts(response) || !a.allowRequest(response, request, "account-device-revoke", 20, time.Minute) {
		return
	}
	user, _, _, ok := a.recentSessionForRequest(response, request)
	if !ok {
		return
	}
	deviceID := request.PathValue("deviceID")
	if len(deviceID) != 32 {
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

func (a *api) renameAccountDevice(response http.ResponseWriter, request *http.Request) {
	if !a.requireAccounts(response) || !a.allowAuthAttempt(response, request, "account-device-rename") {
		return
	}
	user, _, _, ok := a.recentSessionForRequest(response, request)
	if !ok {
		return
	}
	deviceID := request.PathValue("deviceID")
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
