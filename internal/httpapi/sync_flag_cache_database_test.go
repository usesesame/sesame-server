package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	adminstore "usesesame.app/backend/internal/admin"
)

func TestTurningSyncOffThroughTheAdminRouteClosesSyncAtOnce(t *testing.T) {
	env := newSyncHostEnv(t)
	alpha := env.newAccount("sync-flag-admin", true)
	if state := env.read(alpha, "/v1/sync/state"); state.Code == http.StatusForbidden {
		t.Fatalf("Sync was closed while the flag was on: %s", state.Body.String())
	}
	if _, err := env.db.ExecContext(context.Background(), `
		INSERT INTO sesame_admin_accounts (id, email, password_hash, role, totp_verified)
		VALUES ('super-sync-flag', 'sync-flag-super@example.invalid', 'test', 'super', TRUE)
		ON CONFLICT (id) DO NOTHING`); err != nil {
		t.Fatalf("create super administrator: %v", err)
	}
	actor := adminstore.Account{ID: "super-sync-flag", Email: "sync-flag-super@example.invalid", Role: adminstore.RoleSuper}
	if response := adminProfileRequest(t, env.handler, env.api.config.Admin, actor, http.MethodPatch, "/v1/admin/flags/cloud_sync_available", `{"value":"false"}`); response.Code != http.StatusNoContent {
		t.Fatalf("turn Sync off = %d %s", response.Code, response.Body.String())
	}
	if state := env.read(alpha, "/v1/sync/state"); state.Code != http.StatusForbidden || !strings.Contains(state.Body.String(), "sync_unavailable") {
		t.Fatalf("Sync after the admin turned it off = %d %s", state.Code, state.Body.String())
	}
}

func TestTheSyncFlagIsReadAgainAfterItsCacheWindow(t *testing.T) {
	shortenSyncFlagCache(t, 300*time.Millisecond)
	env := newSyncHostEnv(t)
	alpha := env.newAccount("sync-flag-window", true)
	if state := env.read(alpha, "/v1/sync/state"); state.Code == http.StatusForbidden {
		t.Fatalf("Sync was closed while the flag was on: %s", state.Body.String())
	}
	env.setSyncFlag("false")
	if state := env.read(alpha, "/v1/sync/state"); state.Code == http.StatusForbidden {
		t.Fatalf("the flag was read again inside the cache window: %s", state.Body.String())
	}
	time.Sleep(350 * time.Millisecond)
	if state := env.read(alpha, "/v1/sync/state"); state.Code != http.StatusForbidden || !strings.Contains(state.Body.String(), "sync_unavailable") {
		t.Fatalf("Sync after the cache window = %d %s", state.Code, state.Body.String())
	}
	env.setSyncFlag("true")
	if state := env.read(alpha, "/v1/sync/state"); state.Code != http.StatusForbidden {
		t.Fatalf("a closed flag opened before its cache window ended: %d %s", state.Code, state.Body.String())
	}
}
