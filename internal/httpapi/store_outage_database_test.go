package httpapi

import (
	"bytes"
	"container/list"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"usesesame.app/backend/internal/accounts"
	adminstore "usesesame.app/backend/internal/admin"
	"usesesame.app/backend/internal/syncsvc"
	"usesesame.app/backend/internal/syncsvc/memstore"
	"usesesame.app/backend/internal/syncsvc/pgstore"
)

const deletionTestPassword = "fictional-deletion-password"

func newMemorySyncService(t *testing.T) *syncsvc.Service {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	service, err := syncsvc.New(memstore.New(), syncsvc.Config{ReceiptKey: key})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func newClosedDatabaseSyncService(t *testing.T) *syncsvc.Service {
	t.Helper()
	store, err := accounts.Open(context.Background(), os.Getenv("SESAME_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatalf("open account store: %v", err)
	}
	closed := store.DB()
	_ = store.Close()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	service, err := syncsvc.New(pgstore.New(closed), syncsvc.Config{ReceiptKey: key})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func newClosedAdminStore(t *testing.T) *adminstore.Store {
	t.Helper()
	store, err := adminstore.Open(context.Background(), os.Getenv("SESAME_TEST_DATABASE_URL"), bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatalf("open admin store: %v", err)
	}
	_ = store.Close()
	return store
}

func syncStateRequest(handler http.Handler, remote string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "/v1/sync/state", nil)
	request.RemoteAddr = remote
	request.Header.Set("Authorization", "Sesame fictional-desktop-token")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func vaultCount(t *testing.T, db *sql.DB, accountID string) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM sesame_sync3_vaults WHERE owner_key = $1`, syncOwnerKey(syncOwnerTestPepper, accountID)).Scan(&count); err != nil {
		t.Fatalf("count vaults: %v", err)
	}
	return count
}

func accountCount(t *testing.T, db *sql.DB, accountID string) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM sesame_accounts WHERE id = $1`, accountID).Scan(&count); err != nil {
		t.Fatalf("count accounts: %v", err)
	}
	return count
}

func seedVaultRow(t *testing.T, db *sql.DB, vaultID, accountID string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO sesame_sync3_vaults (id, owner_key, current_revision, head_index, epoch, created_at, updated_at)
		VALUES ($1, $2, 0, 1, 1, NOW(), NOW())`, vaultID, syncOwnerKey(syncOwnerTestPepper, accountID)); err != nil {
		t.Fatalf("seed vault: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM sesame_sync3_vaults WHERE id = $1`, vaultID)
	})
}

func TestSyncStateKeepsTheLinkWhenTheTokenLookupFails(t *testing.T) {
	env := newSyncHostEnv(t)
	adminStore, err := adminstore.Open(context.Background(), os.Getenv("SESAME_TEST_DATABASE_URL"), bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatalf("open admin store: %v", err)
	}
	t.Cleanup(func() { _ = adminStore.Close() })
	store := &desktopLookupStore{lookupErr: errFictionalStoreOutage}
	handler := New(Config{
		AllowedOrigin: syncHostTestOrigin, AdminOrigin: "https://admin.example.invalid",
		Accounts: store, Admin: adminStore, Sync: newMemorySyncService(t), SyncOwnerPepper: syncOwnerTestPepper,
	})

	response := syncStateRequest(handler, env.nextIP())
	if response.Code != http.StatusServiceUnavailable || errorCode(t, response) != "desktop_link_check_unavailable" || response.Header().Get("Retry-After") == "" {
		t.Fatalf("store outage = %d %s", response.Code, response.Body.String())
	}
	store.lookupErr = accounts.ErrNotFound
	response = syncStateRequest(handler, env.nextIP())
	if response.Code != http.StatusUnauthorized || errorCode(t, response) != "not_authenticated" {
		t.Fatalf("unknown token = %d %s", response.Code, response.Body.String())
	}
}

func TestSyncRoutesAnswerRetryableWhenTheFlagCannotBeRead(t *testing.T) {
	env := newSyncHostEnv(t)
	closed := newClosedAdminStore(t)
	config := Config{
		AllowedOrigin: syncHostTestOrigin, AdminOrigin: "https://admin.example.invalid",
		Accounts: env.accounts, Admin: closed, Sync: newMemorySyncService(t), SyncOwnerPepper: syncOwnerTestPepper,
	}
	handler := New(config)
	for _, route := range syncRouteList {
		request := httptest.NewRequest(route.method, route.path, nil)
		request.RemoteAddr = env.nextIP()
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusServiceUnavailable || errorCode(t, recorder) != "sync_unavailable" || recorder.Header().Get("Retry-After") == "" {
			t.Fatalf("%s %s with an unreadable flag = %d %s retry %q", route.method, route.path, recorder.Code, recorder.Body.String(), recorder.Header().Get("Retry-After"))
		}
	}

	failingClosed := &api{config: config}
	if failingClosed.syncEnabled(context.Background()) || failingClosed.capabilityEnabled(context.Background(), "public_download") {
		t.Fatal("an unreadable flag store opened a capability")
	}
	status := httptest.NewRecorder()
	handler.ServeHTTP(status, httptest.NewRequest(http.MethodGet, "/v1/product/status", nil))
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"cloudSyncAvailable":false`) {
		t.Fatalf("product status with an unreadable flag = %d %s", status.Code, status.Body.String())
	}

	if _, err := env.db.ExecContext(context.Background(), `DELETE FROM sesame_feature_flags WHERE key = 'cloud_sync_available'`); err != nil {
		t.Fatal(err)
	}
	if response := syncStateRequest(env.handler, env.nextIP()); response.Code != http.StatusForbidden || errorCode(t, response) != "sync_unavailable" || response.Header().Get("Retry-After") != "" {
		t.Fatalf("a missing flag = %d %s", response.Code, response.Body.String())
	}
	env.setSyncFlag("false")
	if response := syncStateRequest(env.handler, env.nextIP()); response.Code != http.StatusForbidden || errorCode(t, response) != "sync_unavailable" {
		t.Fatalf("a flag that is off = %d %s", response.Code, response.Body.String())
	}
}

func (e *syncHostEnv) deleteAccountAs(t *testing.T, sync *syncsvc.Service, accountID string) *httptest.ResponseRecorder {
	t.Helper()
	passwordHash, err := accounts.HashPassword(deletionTestPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.ExecContext(context.Background(), `UPDATE sesame_accounts SET password_hash = $2 WHERE id = $1`, accountID, passwordHash); err != nil {
		t.Fatal(err)
	}
	token, tokenHash, err := accounts.NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.accounts.CreateSession(context.Background(), accountID, tokenHash, time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	config := e.api.config
	config.Sync = sync
	config.RecentAuthDuration = time.Hour
	deleting := &api{config: config, limits: &authLimiter{attempts: map[string]*limitEntry{}, recency: list.New()}, routes: newRouteRegistry()}
	body, err := json.Marshal(map[string]string{"password": deletionTestPassword})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/account/delete", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = e.nextIP()
	request.AddCookie(&http.Cookie{Name: deleting.sessionCookieName(), Value: token})
	recorder := httptest.NewRecorder()
	deleting.deleteAccount(recorder, request)
	return recorder
}

func TestDeletingAnAccountDeletesItsSyncVault(t *testing.T) {
	env := newSyncHostEnv(t)
	alpha := env.newAccount("acct-delete-sync-a", true)
	bravo := env.newAccount("acct-delete-sync-b", true)
	if created := env.createVault(alpha, "vault-delete-alpha-0001"); created.Code != http.StatusCreated {
		t.Fatalf("create alpha vault: %d %s", created.Code, created.Body.String())
	}
	if created := env.createVault(bravo, "vault-delete-bravo-0001"); created.Code != http.StatusCreated {
		t.Fatalf("create bravo vault: %d %s", created.Code, created.Body.String())
	}

	if response := env.deleteAccountAs(t, env.api.config.Sync, alpha.id); response.Code != http.StatusNoContent {
		t.Fatalf("delete account = %d %s", response.Code, response.Body.String())
	}
	if accountCount(t, env.db, alpha.id) != 0 || vaultCount(t, env.db, alpha.id) != 0 {
		t.Fatal("the account or its vault survived deletion")
	}
	if vaultCount(t, env.db, bravo.id) != 1 {
		t.Fatal("another account's vault was deleted")
	}
}

func TestDeletingAnAccountWithoutAVaultSucceeds(t *testing.T) {
	env := newSyncHostEnv(t)
	alpha := env.newAccount("acct-delete-novault", true)
	if response := env.deleteAccountAs(t, env.api.config.Sync, alpha.id); response.Code != http.StatusNoContent {
		t.Fatalf("delete account without a vault = %d %s", response.Code, response.Body.String())
	}
	if accountCount(t, env.db, alpha.id) != 0 {
		t.Fatal("the account survived deletion")
	}
}

func TestAccountDeletionStopsWhenTheVaultCannotBeDeleted(t *testing.T) {
	env := newSyncHostEnv(t)
	alpha := env.newAccount("acct-delete-sync-down", true)
	if created := env.createVault(alpha, "vault-delete-down-0001"); created.Code != http.StatusCreated {
		t.Fatalf("create vault: %d %s", created.Code, created.Body.String())
	}
	response := env.deleteAccountAs(t, newClosedDatabaseSyncService(t), alpha.id)
	if response.Code != http.StatusServiceUnavailable || errorCode(t, response) != "account_delete_unavailable" {
		t.Fatalf("delete with Sync down = %d %s", response.Code, response.Body.String())
	}
	if accountCount(t, env.db, alpha.id) != 1 || vaultCount(t, env.db, alpha.id) != 1 {
		t.Fatal("a failed vault deletion still removed the account or the vault")
	}
}

func newAdminDeleteEnv(t *testing.T, sync *syncsvc.Service) (*supportTestEnv, adminstore.Account, string) {
	t.Helper()
	env := newSupportTestEnv(t, func(config *Config) {
		config.AdminStepUpTTL = time.Minute
		config.DeploymentProfile = DeploymentProfileProject
		config.Sync = sync
		config.SyncOwnerPepper = syncOwnerTestPepper
	})
	actor := seedStepUpAdmin(t, env, "admin-delete-sync", "admin-delete-sync@example.invalid", adminstore.RoleSuper)
	return env, actor, stepUpSession(t, env, actor, time.Now().UTC(), time.Now().UTC().UnixNano())
}

func TestAdminUserDeleteDeletesTheSyncVault(t *testing.T) {
	if os.Getenv("SESAME_TEST_DATABASE_URL") == "" {
		t.Skip("SESAME_TEST_DATABASE_URL is required")
	}
	syncStore, err := accounts.Open(context.Background(), os.Getenv("SESAME_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatalf("open sync database: %v", err)
	}
	t.Cleanup(func() { _ = syncStore.Close() })
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	service, err := syncsvc.New(pgstore.New(syncStore.DB()), syncsvc.Config{ReceiptKey: key})
	if err != nil {
		t.Fatal(err)
	}
	env, _, token := newAdminDeleteEnv(t, service)
	env.seedAccount(t, "acct-admin-delete-sync", "admin-delete-sync-user@example.invalid")
	env.seedAccount(t, "acct-admin-delete-keep", "admin-delete-keep-user@example.invalid")
	seedVaultRow(t, env.db, "vault-admin-delete-0001", "acct-admin-delete-sync")
	seedVaultRow(t, env.db, "vault-admin-delete-0002", "acct-admin-delete-keep")

	response := adminSessionRequest(t, env, token, http.MethodDelete, "/v1/admin/users/acct-admin-delete-sync", nil)
	if response.Code != http.StatusNoContent {
		t.Fatalf("admin delete = %d %s", response.Code, response.Body.String())
	}
	if accountCount(t, env.db, "acct-admin-delete-sync") != 0 || vaultCount(t, env.db, "acct-admin-delete-sync") != 0 {
		t.Fatal("the account or its vault survived an admin delete")
	}
	if vaultCount(t, env.db, "acct-admin-delete-keep") != 1 {
		t.Fatal("another account's vault was deleted")
	}
}

func TestAdminUserDeleteKeepsTheAccountWhenTheVaultCannotBeDeleted(t *testing.T) {
	if os.Getenv("SESAME_TEST_DATABASE_URL") == "" {
		t.Skip("SESAME_TEST_DATABASE_URL is required")
	}
	env, actor, token := newAdminDeleteEnv(t, newClosedDatabaseSyncService(t))
	env.seedAccount(t, "acct-admin-delete-down", "admin-delete-down-user@example.invalid")
	seedVaultRow(t, env.db, "vault-admin-delete-0003", "acct-admin-delete-down")

	response := adminSessionRequest(t, env, token, http.MethodDelete, "/v1/admin/users/acct-admin-delete-down", nil)
	if response.Code != http.StatusServiceUnavailable || errorCode(t, response) != "admin_action_unavailable" {
		t.Fatalf("admin delete with Sync down = %d %s", response.Code, response.Body.String())
	}
	if accountCount(t, env.db, "acct-admin-delete-down") != 1 || vaultCount(t, env.db, "acct-admin-delete-down") != 1 {
		t.Fatal("a failed vault deletion still removed the account or the vault")
	}
	if count := countAdminAudit(t, env, "user.delete", actor.ID); count != 0 {
		t.Fatalf("user.delete audit rows after a refused delete = %d", count)
	}
}
