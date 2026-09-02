package httpapi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthRoutesReportBuildIdentity(t *testing.T) {
	handler := New(Config{Version: "1.2.3", Commit: "0123456789abcdef0123456789abcdef01234567"})
	for _, path := range []string{"/livez", "/readyz"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d", path, response.Code)
		}
		body := response.Body.Bytes()
		if !bytes.Contains(body, []byte(`"version":"1.2.3"`)) || !bytes.Contains(body, []byte(`"commit":"0123456789abcdef0123456789abcdef01234567"`)) {
			t.Fatalf("%s identity = %s", path, body)
		}
	}
}
