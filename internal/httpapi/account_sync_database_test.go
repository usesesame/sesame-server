package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"usesesame.app/backend/internal/accounts"
)

const accountSyncDatabaseCSRF = "fictional-account-sync-database-csrf"

func (e *syncHostEnv) webSession(accountID string) string {
	e.t.Helper()
	token, tokenHash, err := accounts.NewSessionToken()
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.accounts.CreateSession(context.Background(), accountID, tokenHash, time.Now().UTC().Add(time.Hour)); err != nil {
		e.t.Fatalf("create session: %v", err)
	}
	return token
}

func (e *syncHostEnv) portal(handler http.Handler, method, origin, csrf, session string) *httptest.ResponseRecorder {
	e.t.Helper()
	request := httptest.NewRequest(method, "/v1/account/sync-vault", nil)
	request.RemoteAddr = e.nextIP()
	request.Header.Set("Origin", origin)
	if csrf != "" {
		request.Header.Set("X-Sesame-CSRF", csrf)
		request.AddCookie(&http.Cookie{Name: e.api.csrfCookieName(), Value: accountSyncDatabaseCSRF})
	}
	request.AddCookie(&http.Cookie{Name: e.api.sessionCookieName(), Value: session})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func (e *syncHostEnv) readSyncVault(session string) *httptest.ResponseRecorder {
	return e.portal(e.handler, http.MethodGet, syncHostTestOrigin, "", session)
}

func (e *syncHostEnv) deleteSyncVault(session string) *httptest.ResponseRecorder {
	return e.portal(e.handler, http.MethodDelete, syncHostTestOrigin, accountSyncDatabaseCSRF, session)
}

func (e *syncHostEnv) syncVaultEvents(accountID string) int {
	e.t.Helper()
	var count int
	if err := e.db.QueryRowContext(context.Background(), `
		SELECT COUNT(*) FROM sesame_account_events WHERE account_id = $1 AND event_type = 'sync_vault_deleted' AND label = 'Sesame Sync'`, accountID).Scan(&count); err != nil {
		e.t.Fatalf("count events: %v", err)
	}
	return count
}

func TestAccountPortalDeletesOnlyTheSignedInAccountsSyncVault(t *testing.T) {
	env := newSyncHostEnv(t)
	alpha := env.newAccount("acct-portal-sync-a", true)
	bravo := env.newAccount("acct-portal-sync-b", true)
	const alphaVault = "vaultPortalAlpha0001"
	if created := env.createVault(alpha, alphaVault); created.Code != http.StatusCreated {
		t.Fatalf("create alpha vault: %d %s", created.Code, created.Body.String())
	}
	if created := env.createVault(bravo, "vaultPortalBravo0001"); created.Code != http.StatusCreated {
		t.Fatalf("create bravo vault: %d %s", created.Code, created.Body.String())
	}
	session := env.webSession(alpha.id)

	read := env.readSyncVault(session)
	status := decodeAccountSyncStatus(t, read)
	if read.Code != http.StatusOK || status["available"] != true || status["hasVault"] != true || status["approvedDevices"] != float64(1) {
		t.Fatalf("GET with a vault = %d %s", read.Code, read.Body.String())
	}
	updatedAt, ok := status["updatedAt"].(string)
	if !ok {
		t.Fatalf("GET has no updatedAt: %s", read.Body.String())
	}
	if _, err := time.Parse(time.RFC3339Nano, updatedAt); err != nil {
		t.Fatalf("updatedAt %q: %v", updatedAt, err)
	}
	for _, secret := range []string{alphaVault, alpha.signingID, alpha.deviceID, syncOwnerKey(syncOwnerTestPepper, alpha.id)} {
		if strings.Contains(read.Body.String(), secret) {
			t.Fatalf("GET exposed %q: %s", secret, read.Body.String())
		}
	}
	if len(status) != 4 {
		t.Fatalf("GET returned unexpected fields: %s", read.Body.String())
	}

	if removed := env.deleteSyncVault(session); removed.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d %s", removed.Code, removed.Body.String())
	}
	if vaultCount(t, env.db, alpha.id) != 0 {
		t.Fatal("the signed-in account's vault survived")
	}
	if vaultCount(t, env.db, bravo.id) != 1 {
		t.Fatal("another account's vault was deleted")
	}
	if env.syncVaultEvents(alpha.id) != 1 || env.syncVaultEvents(bravo.id) != 0 {
		t.Fatalf("activity events alpha=%d bravo=%d", env.syncVaultEvents(alpha.id), env.syncVaultEvents(bravo.id))
	}
	if bravoState := env.read(bravo, "/v1/sync/state"); bravoState.Code != http.StatusOK {
		t.Fatalf("bravo lost its vault: %d %s", bravoState.Code, bravoState.Body.String())
	}

	after := decodeAccountSyncStatus(t, env.readSyncVault(session))
	if after["available"] != true || after["hasVault"] != false || after["updatedAt"] != nil {
		t.Fatalf("GET after delete = %v", after)
	}
	stale := env.read(alpha, "/v1/sync/state")
	if stale.Code != http.StatusNotFound || errorCode(t, stale) != "sync_vault_not_found" {
		t.Fatalf("a former member's state after delete = %d %s", stale.Code, stale.Body.String())
	}

	if again := env.deleteSyncVault(session); again.Code != http.StatusNoContent {
		t.Fatalf("DELETE without a vault = %d %s", again.Code, again.Body.String())
	}
	if env.syncVaultEvents(alpha.id) != 1 {
		t.Fatal("deleting a missing vault recorded another event")
	}

	if created := env.createVault(alpha, "vaultPortalAlpha0002"); created.Code != http.StatusCreated {
		t.Fatalf("a device could not turn Sync on again: %d %s", created.Code, created.Body.String())
	}
	if fresh := decodeAccountSyncStatus(t, env.readSyncVault(session)); fresh["hasVault"] != true {
		t.Fatalf("GET after a new vault = %v", fresh)
	}
}

func TestAccountPortalSyncVaultDeleteRefusesStaleSessionsAndForeignRequests(t *testing.T) {
	env := newSyncHostEnv(t)
	alpha := env.newAccount("acct-portal-sync-guard", true)
	if created := env.createVault(alpha, "vaultPortalGuard0001"); created.Code != http.StatusCreated {
		t.Fatalf("create vault: %d %s", created.Code, created.Body.String())
	}
	session := env.webSession(alpha.id)

	if response := env.portal(env.handler, http.MethodDelete, "https://elsewhere.example.invalid", accountSyncDatabaseCSRF, session); response.Code != http.StatusForbidden || errorCode(t, response) != "origin_not_allowed" {
		t.Fatalf("DELETE from another origin = %d %s", response.Code, response.Body.String())
	}
	if response := env.portal(env.handler, http.MethodDelete, syncHostTestOrigin, "", session); response.Code != http.StatusForbidden || errorCode(t, response) != "invalid_csrf" {
		t.Fatalf("DELETE without CSRF = %d %s", response.Code, response.Body.String())
	}
	if response := env.portal(env.handler, http.MethodGet, "https://elsewhere.example.invalid", "", session); response.Code != http.StatusForbidden || errorCode(t, response) != "origin_not_allowed" {
		t.Fatalf("GET from another origin = %d %s", response.Code, response.Body.String())
	}
	if response := env.deleteSyncVault("fictional-unknown-session-token"); response.Code != http.StatusUnauthorized {
		t.Fatalf("DELETE with an unknown session = %d %s", response.Code, response.Body.String())
	}

	if _, err := env.db.ExecContext(context.Background(), `UPDATE sesame_sessions SET authenticated_at = NOW() - INTERVAL '1 hour' WHERE account_id = $1`, alpha.id); err != nil {
		t.Fatal(err)
	}
	if response := env.deleteSyncVault(session); response.Code != http.StatusForbidden || errorCode(t, response) != "recent_auth_required" {
		t.Fatalf("DELETE with a stale session = %d %s", response.Code, response.Body.String())
	}
	if vaultCount(t, env.db, alpha.id) != 1 || env.syncVaultEvents(alpha.id) != 0 {
		t.Fatal("a refused request deleted the vault or recorded an event")
	}

	if _, err := env.db.ExecContext(context.Background(), `UPDATE sesame_sessions SET authenticated_at = NOW() WHERE account_id = $1`, alpha.id); err != nil {
		t.Fatal(err)
	}
	if response := env.deleteSyncVault(session); response.Code != http.StatusNoContent {
		t.Fatalf("DELETE after reauthentication = %d %s", response.Code, response.Body.String())
	}
	if vaultCount(t, env.db, alpha.id) != 0 || env.syncVaultEvents(alpha.id) != 1 {
		t.Fatal("the vault was not deleted after reauthentication")
	}
}

func TestAccountPortalSyncVaultAnswersRetryableWhenTheSyncStoreFails(t *testing.T) {
	env := newSyncHostEnv(t)
	alpha := env.newAccount("acct-portal-sync-down", true)
	if created := env.createVault(alpha, "vaultPortalDown00001"); created.Code != http.StatusCreated {
		t.Fatalf("create vault: %d %s", created.Code, created.Body.String())
	}
	session := env.webSession(alpha.id)
	failing := New(Config{
		Accounts: env.accounts, Sync: newClosedDatabaseSyncService(t), SyncOwnerPepper: syncOwnerTestPepper,
		AllowedOrigin: syncHostTestOrigin, AdminOrigin: "https://admin.example.invalid", WebBaseURL: syncHostTestOrigin,
	})

	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		response := env.portal(failing, method, syncHostTestOrigin, accountSyncDatabaseCSRF, session)
		if response.Code != http.StatusServiceUnavailable || errorCode(t, response) != "sync_vault_unavailable" || response.Header().Get("Retry-After") == "" {
			t.Fatalf("%s with the Sync store down = %d %s", method, response.Code, response.Body.String())
		}
	}
	if vaultCount(t, env.db, alpha.id) != 1 || env.syncVaultEvents(alpha.id) != 0 {
		t.Fatal("a failed delete removed the vault or recorded an event")
	}
}
