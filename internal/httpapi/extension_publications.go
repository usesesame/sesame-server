package httpapi

import (
	"errors"
	"net/http"

	adminstore "usesesame.app/backend/internal/admin"
)

func (a *api) adminExtensionPublications(response http.ResponseWriter, request *http.Request) {
	account, ok := a.adminForRequest(response, request)
	if !ok || !(adminstore.Allowed(account.Role, adminstore.PermissionReleaseWrite) || account.Role == adminstore.RoleReadonly) {
		if ok {
			writeError(response, http.StatusForbidden, "admin_forbidden", "Your admin role cannot view extension publications.")
		}
		return
	}
	publications, err := a.config.Admin.ExtensionPublications(request.Context())
	if err != nil {
		adminStoreError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"publications": publications})
}

func (a *api) adminExtensionPublicationAccept(response http.ResponseWriter, request *http.Request) {
	actor, ok := a.requireAdminPermission(response, request, adminstore.PermissionReleaseWrite)
	if !ok {
		return
	}
	var candidate adminstore.ExtensionPublicationCandidate
	if !decodeAdminJSON(response, request, &candidate) {
		return
	}
	publication, err := a.config.Admin.AcceptExtensionPublication(request.Context(), actor, candidate, a.adminIPHash(request))
	if err != nil {
		adminExtensionPublicationError(response, err)
		return
	}
	writeJSON(response, http.StatusCreated, publication)
}

func (a *api) adminExtensionPublicationTransition(response http.ResponseWriter, request *http.Request) {
	actor, ok := a.requireAdminPermission(response, request, adminstore.PermissionReleaseWrite)
	if !ok {
		return
	}
	publicationID := request.PathValue("publicationID")
	var input adminstore.ExtensionTransitionInput
	if !decodeAdminJSON(response, request, &input) {
		return
	}
	publication, err := a.config.Admin.TransitionExtensionPublication(request.Context(), actor, publicationID, input, a.adminIPHash(request))
	if err != nil {
		adminExtensionPublicationError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, publication)
}

func adminExtensionPublicationError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, adminstore.ErrExtensionPublicationConflict):
		writeError(response, http.StatusConflict, "extension_publication_conflict", "This publication already exists with different evidence. The reviewed package digest is immutable.")
	case errors.Is(err, adminstore.ErrManifestRevisionConflict):
		writeError(response, http.StatusConflict, "extension_publication_state_conflict", "This publication changed. Reload it before trying again.")
	default:
		adminStoreError(response, err)
	}
}
