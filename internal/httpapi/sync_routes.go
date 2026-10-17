package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"usesesame.app/backend/internal/accounts"
	"usesesame.app/backend/internal/syncsvc"
	"usesesame.app/backend/internal/syncv3http"
)

const syncRateWindow = time.Minute

func (a *api) registerSyncRoutes(mux *http.ServeMux) {
	sync := routePolicy{audience: audienceDesktopClient}
	a.route(mux, sync, "GET /v1/sync/state", a.syncState)
	a.route(mux, sync, "POST /v1/sync/vault", a.syncCreateVault)
	a.route(mux, sync, "GET /v1/sync/device-list", a.syncDeviceList)
	a.route(mux, sync, "GET /v1/sync/devices", a.syncDevices)
	a.route(mux, sync, "POST /v1/sync/pairings", a.syncBeginPairing)
	a.route(mux, sync, "GET /v1/sync/pairings/mine", a.syncPairingStatus)
	a.route(mux, sync, "POST /v1/sync/pairings/{deviceId}/answer", a.syncAnswerPairing)
	a.route(mux, sync, "POST /v1/sync/pairings/mine/confirm", a.syncConfirmPairing)
	a.route(mux, sync, "DELETE /v1/sync/pairings/{deviceId}", a.syncCancelPairing)
	a.route(mux, sync, "POST /v1/sync/devices", a.syncApprove)
	a.route(mux, sync, "POST /v1/sync/devices/remove", a.syncRemove)
	a.route(mux, sync, "GET /v1/sync/key-package", a.syncKeyPackage)
	a.route(mux, sync, "POST /v1/sync/activate", a.syncActivate)
	a.route(mux, sync, "POST /v1/sync/snapshots", a.syncUpload)
	a.route(mux, sync, "GET /v1/sync/headers", a.syncHeaders)
	a.route(mux, sync, "GET /v1/sync/snapshot", a.syncDownload)
	a.route(mux, sync, "POST /v1/sync/checkpoints", a.syncPutCheckpoint)
	a.route(mux, sync, "GET /v1/sync/checkpoints", a.syncCheckpoints)
	a.route(mux, sync, "POST /v1/sync/reset", a.syncReset)
}

func (a *api) syncHandler() *syncv3http.Handler {
	if a.config.Sync == nil {
		return nil
	}
	return syncv3http.New(a.config.Sync, hostedSyncHost{api: a})
}

func (a *api) withSync(response http.ResponseWriter, request *http.Request, serve func(*syncv3http.Handler) http.HandlerFunc) {
	handler := a.syncHandler()
	if handler == nil || !a.syncEnabled(request.Context()) {
		writeError(response, http.StatusForbidden, "sync_unavailable", "Sesame Sync is not available.")
		return
	}
	serve(handler)(response, request)
}

func (a *api) syncState(response http.ResponseWriter, request *http.Request) {
	a.withSync(response, request, func(h *syncv3http.Handler) http.HandlerFunc { return h.State })
}

func (a *api) syncCreateVault(response http.ResponseWriter, request *http.Request) {
	a.withSync(response, request, func(h *syncv3http.Handler) http.HandlerFunc { return h.CreateVault })
}

func (a *api) syncDeviceList(response http.ResponseWriter, request *http.Request) {
	a.withSync(response, request, func(h *syncv3http.Handler) http.HandlerFunc { return h.DeviceList })
}

func (a *api) syncDevices(response http.ResponseWriter, request *http.Request) {
	a.withSync(response, request, func(h *syncv3http.Handler) http.HandlerFunc { return h.Devices })
}

func (a *api) syncBeginPairing(response http.ResponseWriter, request *http.Request) {
	a.withSync(response, request, func(h *syncv3http.Handler) http.HandlerFunc { return h.BeginPairing })
}

func (a *api) syncPairingStatus(response http.ResponseWriter, request *http.Request) {
	a.withSync(response, request, func(h *syncv3http.Handler) http.HandlerFunc { return h.PairingStatus })
}

func (a *api) syncAnswerPairing(response http.ResponseWriter, request *http.Request) {
	a.withSync(response, request, func(h *syncv3http.Handler) http.HandlerFunc { return h.AnswerPairing })
}

func (a *api) syncConfirmPairing(response http.ResponseWriter, request *http.Request) {
	a.withSync(response, request, func(h *syncv3http.Handler) http.HandlerFunc { return h.ConfirmPairing })
}

func (a *api) syncCancelPairing(response http.ResponseWriter, request *http.Request) {
	a.withSync(response, request, func(h *syncv3http.Handler) http.HandlerFunc { return h.CancelPairing })
}

func (a *api) syncApprove(response http.ResponseWriter, request *http.Request) {
	a.withSync(response, request, func(h *syncv3http.Handler) http.HandlerFunc { return h.Approve })
}

func (a *api) syncRemove(response http.ResponseWriter, request *http.Request) {
	a.withSync(response, request, func(h *syncv3http.Handler) http.HandlerFunc { return h.Remove })
}

func (a *api) syncKeyPackage(response http.ResponseWriter, request *http.Request) {
	a.withSync(response, request, func(h *syncv3http.Handler) http.HandlerFunc { return h.KeyPackage })
}

func (a *api) syncActivate(response http.ResponseWriter, request *http.Request) {
	a.withSync(response, request, func(h *syncv3http.Handler) http.HandlerFunc { return h.Activate })
}

func (a *api) syncUpload(response http.ResponseWriter, request *http.Request) {
	a.withSync(response, request, func(h *syncv3http.Handler) http.HandlerFunc { return h.Upload })
}

func (a *api) syncHeaders(response http.ResponseWriter, request *http.Request) {
	a.withSync(response, request, func(h *syncv3http.Handler) http.HandlerFunc { return h.Headers })
}

func (a *api) syncDownload(response http.ResponseWriter, request *http.Request) {
	a.withSync(response, request, func(h *syncv3http.Handler) http.HandlerFunc { return h.Download })
}

func (a *api) syncPutCheckpoint(response http.ResponseWriter, request *http.Request) {
	a.withSync(response, request, func(h *syncv3http.Handler) http.HandlerFunc { return h.PutCheckpoint })
}

func (a *api) syncCheckpoints(response http.ResponseWriter, request *http.Request) {
	a.withSync(response, request, func(h *syncv3http.Handler) http.HandlerFunc { return h.Checkpoints })
}

func (a *api) syncReset(response http.ResponseWriter, request *http.Request) {
	a.withSync(response, request, func(h *syncv3http.Handler) http.HandlerFunc { return h.Reset })
}

type syncLimit struct {
	perIP, perAccount, perDevice int
	needsEntitlement             bool
}

var syncLimits = map[string]syncLimit{
	"state":           {200, 60, 30, false},
	"vault":           {30, 10, 5, true},
	"device-list":     {200, 60, 30, false},
	"devices":         {200, 60, 30, false},
	"pairing-begin":   {30, 10, 5, true},
	"pairing-mine":    {200, 60, 30, false},
	"pairing-answer":  {60, 20, 10, false},
	"pairing-confirm": {60, 20, 10, false},
	"pairing-cancel":  {60, 20, 10, false},
	"approve":         {60, 20, 10, true},
	"remove":          {60, 20, 10, false},
	"key-package":     {90, 30, 15, false},
	"activate":        {60, 20, 10, false},
	"upload":          {180, 60, 30, true},
	"headers":         {300, 120, 60, false},
	"download":        {300, 120, 60, false},
	"checkpoint-put":  {60, 20, 10, false},
	"checkpoints":     {200, 60, 30, false},
	"reset":           {10, 5, 3, false},
}

type hostedSyncHost struct {
	api *api
}

func (host hostedSyncHost) Authenticate(response http.ResponseWriter, request *http.Request, operation string) (syncsvc.Caller, bool) {
	a := host.api
	limit, known := syncLimits[operation]
	if !known {
		writeError(response, http.StatusServiceUnavailable, "sync_unavailable", "Sesame Sync is temporarily unavailable.")
		return syncsvc.Caller{}, false
	}
	if !a.requireAccounts(response) {
		return syncsvc.Caller{}, false
	}
	if !a.allowRequest(response, request, "sync-"+operation, limit.perIP, syncRateWindow) {
		return syncsvc.Caller{}, false
	}
	connection, ok := a.desktopConnectionForRequest(response, request)
	if !ok {
		return syncsvc.Caller{}, false
	}
	if !a.allowKeyed(response, request, "sync-"+operation+":account:"+connection.AccountID, limit.perAccount, syncRateWindow) {
		return syncsvc.Caller{}, false
	}
	if !a.allowKeyed(response, request, "sync-"+operation+":device:"+connection.DeviceID, limit.perDevice, syncRateWindow) {
		return syncsvc.Caller{}, false
	}
	if limit.needsEntitlement && !a.syncEntitled(response, request, connection.AccountID) {
		return syncsvc.Caller{}, false
	}
	token := strings.TrimPrefix(request.Header.Get("Authorization"), "Sesame ")
	return syncsvc.Caller{OwnerKey: connection.AccountID, TokenDeviceID: connection.DeviceID, Token: token}, true
}

type syncEntitlementStore interface {
	AccountAccess(context.Context, string) (accounts.Access, error)
}

func (a *api) syncEntitled(response http.ResponseWriter, request *http.Request, accountID string) bool {
	store, ok := a.config.Accounts.(syncEntitlementStore)
	if !ok {
		writeError(response, http.StatusServiceUnavailable, "sync_unavailable", "Sesame Sync is temporarily unavailable.")
		return false
	}
	access, err := store.AccountAccess(request.Context(), accountID)
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "sync_unavailable", "Sesame Sync is temporarily unavailable.")
		return false
	}
	if !access.DownloadsAllowed {
		writeError(response, http.StatusPaymentRequired, "sync_not_entitled",
			"This account's Sesame subscription is not active. Your synced vault stays available to download and to remove devices from.")
		return false
	}
	return true
}
