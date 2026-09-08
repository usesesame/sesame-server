package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSyncRoutesStayClosedWithoutTheSyncStore(t *testing.T) {
	handler := New(Config{
		AllowedOrigin: "https://account.example.invalid",
		AdminOrigin:   "https://admin.example.invalid",
	})
	routes := []struct{ method, path string }{
		{http.MethodPost, "/v1/sync/enroll/begin"},
		{http.MethodPost, "/v1/sync/enroll/finish"},
		{http.MethodGet, "/v1/sync/devices"},
		{http.MethodDelete, "/v1/sync/devices/device-123456789012"},
		{http.MethodPost, "/v1/sync/devices/device-123456789012/approve"},
		{http.MethodPost, "/v1/sync/devices/device-123456789012/deny"},
		{http.MethodPost, "/v1/sync/devices/device-123456789012/rekey"},
		{http.MethodGet, "/v1/sync/key-package"},
		{http.MethodPost, "/v1/sync/activate"},
		{http.MethodPost, "/v1/sync/reset"},
		{http.MethodGet, "/v1/sync/envelope"},
		{http.MethodPost, "/v1/sync/envelope"},
	}
	for _, route := range routes {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(route.method, route.path, nil))
		if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "sync_unavailable") {
			t.Fatalf("%s %s without a sync store = %d %s", route.method, route.path, response.Code, response.Body.String())
		}
	}
}
