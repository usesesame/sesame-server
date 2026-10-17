package httpapi

import (
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"usesesame.app/backend/internal/syncsvc"
	"usesesame.app/backend/internal/syncsvc/memstore"
)

var syncRouteList = []struct{ method, path string }{
	{http.MethodGet, "/v1/sync/state"},
	{http.MethodPost, "/v1/sync/vault"},
	{http.MethodGet, "/v1/sync/device-list"},
	{http.MethodGet, "/v1/sync/devices"},
	{http.MethodPost, "/v1/sync/pairings"},
	{http.MethodGet, "/v1/sync/pairings/mine"},
	{http.MethodPost, "/v1/sync/pairings/device-123456789012/answer"},
	{http.MethodPost, "/v1/sync/pairings/mine/confirm"},
	{http.MethodDelete, "/v1/sync/pairings/device-123456789012"},
	{http.MethodPost, "/v1/sync/devices"},
	{http.MethodPost, "/v1/sync/devices/remove"},
	{http.MethodGet, "/v1/sync/key-package"},
	{http.MethodPost, "/v1/sync/activate"},
	{http.MethodPost, "/v1/sync/snapshots"},
	{http.MethodGet, "/v1/sync/headers"},
	{http.MethodGet, "/v1/sync/snapshot"},
	{http.MethodPost, "/v1/sync/reset"},
}

func requireSyncClosed(t *testing.T, handler http.Handler, label string) {
	t.Helper()
	for _, route := range syncRouteList {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(route.method, route.path, nil))
		if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "sync_unavailable") {
			t.Fatalf("%s: %s %s = %d %s", label, route.method, route.path, response.Code, response.Body.String())
		}
	}
}

func TestSyncRoutesStayClosedWithoutTheSyncService(t *testing.T) {
	requireSyncClosed(t, New(Config{
		AllowedOrigin: "https://account.example.invalid",
		AdminOrigin:   "https://admin.example.invalid",
	}), "no service")
}

func TestSyncRoutesStayClosedWhileTheFlagIsOff(t *testing.T) {
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := syncsvc.New(memstore.New(), syncsvc.Config{ReceiptKey: key})
	if err != nil {
		t.Fatal(err)
	}
	requireSyncClosed(t, New(Config{
		AllowedOrigin: "https://account.example.invalid",
		AdminOrigin:   "https://admin.example.invalid",
		Sync:          service,
	}), "service without the flag")
}
