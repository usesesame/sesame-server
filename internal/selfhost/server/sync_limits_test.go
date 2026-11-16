package server_test

import (
	"net/http"
	"testing"
)

func TestSyncLimitsFitABusyDevice(t *testing.T) {
	e := newSQLiteEnv(t, withSync(t))
	issue, err := e.setupStore.StartFirstSetup(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	o := e.completeSetup(issue.Token, "Dana")
	enableSync(o)
	token, tokenID := e.pairedDevice(o)
	laptop := e.newSyncDevice(token, tokenID, "laptop")
	laptop.createVault()

	for request := 1; request <= 60; request++ {
		recorder := e.do(req{path: "/v1/sync/state", header: bearer(token)})
		if recorder.Code == http.StatusTooManyRequests {
			t.Fatalf("state request %d of 60 in a minute was refused", request)
		}
	}
	expectError(t, e.do(req{path: "/v1/sync/state", header: bearer(token)}), http.StatusTooManyRequests, "too_many_attempts")

	for request := 1; request <= 30; request++ {
		if code, body := laptop.call(http.MethodPost, "/v1/sync/checkpoints", map[string]any{}, false); code == http.StatusTooManyRequests {
			t.Fatalf("checkpoint post %d of 30 in a minute was refused: %v", request, body)
		}
	}
	if code, body := laptop.call(http.MethodPost, "/v1/sync/checkpoints", map[string]any{}, false); code != http.StatusTooManyRequests {
		t.Fatalf("checkpoint post 31 = %d %v", code, body)
	}
}
