package httpapi

import (
	"net/http"
	"time"
)

const (
	accountSyncVaultReadLimit           = 60
	accountSyncVaultDeleteAddressLimit  = 5
	accountSyncVaultDeleteAccountLimit  = 5
	accountSyncVaultDeleteAccountWindow = time.Hour
)

type accountSyncVaultStatus struct {
	Available       bool       `json:"available"`
	HasVault        bool       `json:"hasVault"`
	ApprovedDevices int        `json:"approvedDevices"`
	UpdatedAt       *time.Time `json:"updatedAt,omitempty"`
}

func (a *api) accountSyncConfigured() bool {
	return a.config.Sync != nil && syncOwnerPepperUsable(a.config.SyncOwnerPepper)
}

func (a *api) accountSyncVault(response http.ResponseWriter, request *http.Request) {
	if !a.requireAccounts(response) || !a.allowRequest(response, request, "account-sync-vault", accountSyncVaultReadLimit, time.Minute) {
		return
	}
	user, ok := a.userForRequest(response, request)
	if !ok {
		return
	}
	if !a.accountSyncConfigured() {
		writeJSON(response, http.StatusOK, accountSyncVaultStatus{})
		return
	}
	overview, err := a.config.Sync.Overview(request.Context(), syncOwnerKey(a.config.SyncOwnerPepper, user.ID))
	if err != nil {
		writeTemporarilyUnavailable(response, "sync_vault_unavailable", "Your synced vault could not be checked. Try again shortly.")
		return
	}
	status := accountSyncVaultStatus{Available: true, HasVault: overview.HasVault}
	if overview.HasVault {
		updatedAt := overview.UpdatedAt.UTC()
		status.ApprovedDevices = overview.Devices
		status.UpdatedAt = &updatedAt
	}
	writeJSON(response, http.StatusOK, status)
}

func (a *api) removeAccountSyncVault(response http.ResponseWriter, request *http.Request) {
	if !a.requireAccounts(response) || !a.allowRequest(response, request, "account-sync-vault-delete", accountSyncVaultDeleteAddressLimit, time.Minute) {
		return
	}
	if request.ContentLength > 0 || len(request.TransferEncoding) > 0 {
		writeError(response, http.StatusUnsupportedMediaType, "request_body_not_supported", "Deleting the synced vault does not accept a request body.")
		return
	}
	user, _, _, ok := a.recentSessionForRequest(response, request)
	if !ok {
		return
	}
	if !a.allowKeyed(response, request, "account-sync-vault-delete:account:"+user.ID, accountSyncVaultDeleteAccountLimit, accountSyncVaultDeleteAccountWindow) {
		return
	}
	if !a.accountSyncConfigured() {
		response.WriteHeader(http.StatusNoContent)
		return
	}
	overview, err := a.config.Sync.Overview(request.Context(), syncOwnerKey(a.config.SyncOwnerPepper, user.ID))
	if err != nil {
		writeTemporarilyUnavailable(response, "sync_vault_unavailable", "Your synced vault could not be deleted. Try again shortly.")
		return
	}
	if !overview.HasVault {
		response.WriteHeader(http.StatusNoContent)
		return
	}
	if err := a.deleteAccountSyncVault(request.Context(), user.ID); err != nil {
		writeTemporarilyUnavailable(response, "sync_vault_unavailable", "Your synced vault could not be deleted. Try again shortly.")
		return
	}
	a.recordAccountEvent(request.Context(), user.ID, "sync_vault_deleted", "Sesame Sync", nil)
	a.sendSecurityNotification(request.Context(), user, "security-sync-vault-deleted", "Your synced vault was deleted",
		"The encrypted copy of your vault kept by Sesame Sync was deleted from your website account. The vault on each of your devices was not changed.")
	response.WriteHeader(http.StatusNoContent)
}
