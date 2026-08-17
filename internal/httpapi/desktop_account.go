package httpapi

import (
	"net/http"
	"time"

	"usesesame.app/backend/internal/accounts"
)

func (a *api) desktopLinkStatus(response http.ResponseWriter, request *http.Request) {
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
