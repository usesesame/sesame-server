package server_test

import (
	"context"
	"crypto/rand"
	"net/http"
	"path/filepath"
	"testing"

	"usesesame.app/backend/internal/selfhost/server"
	"usesesame.app/backend/internal/selfhost/sqlitestore"
)

func newSQLiteEnv(t *testing.T, options ...option) *env {
	t.Helper()
	e := newEnv(t)
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	store, err := sqlitestore.Open(context.Background(), sqlitestore.Options{
		Path:     filepath.Join(t.TempDir(), "sesame.db"),
		AdminKey: key,
		Now:      e.clock.Now,
		Flags:    server.FlagDefinitions(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cfg := e.cfg
	cfg.Store = store
	cfg.Updates = nil
	for _, apply := range options {
		apply(&cfg)
	}
	e.cfg = cfg
	e.handler = server.New(cfg)
	e.setupStore = store
	return e
}

func TestAgainstTheSQLiteStore(t *testing.T) {
	e := newSQLiteEnv(t)
	issue, err := e.setupStore.StartFirstSetup(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if decodeBody(t, e.do(req{path: "/config.json"}))["setupRequired"] != true {
		t.Fatal("setup should be required")
	}
	o := e.completeSetup(issue.Token, "Dana")
	reuse := e.do(req{method: http.MethodPost, path: "/v1/owner/setup", body: map[string]string{"token": issue.Token, "name": "Mallory", "password": testPassword, "code": e.totp(o.secret)}})
	expectError(t, reuse, http.StatusBadRequest, "setup_token_invalid")

	expectStatus(t, o.do(req{path: "/v1/owner/session"}), http.StatusOK)
	expectError(t, o.do(req{method: http.MethodPost, path: "/v1/owner/members", body: map[string]string{"name": "Ana"}, header: map[string]string{"X-Sesame-CSRF": "wrong"}}), http.StatusForbidden, "invalid_csrf")

	code := e.totp(o.secret)
	login := e.do(req{method: http.MethodPost, path: "/v1/owner/login", body: map[string]string{"name": "dana", "password": testPassword, "code": code}})
	expectStatus(t, login, http.StatusOK)
	replay := e.do(req{method: http.MethodPost, path: "/v1/owner/login", body: map[string]string{"name": "Dana", "password": testPassword, "code": code}})
	expectError(t, replay, http.StatusUnauthorized, "invalid_credentials")
	unknown := e.do(req{method: http.MethodPost, path: "/v1/owner/login", body: map[string]string{"name": "Nobody", "password": testPassword, "code": e.totp(o.secret)}})
	wrongPassword := e.do(req{method: http.MethodPost, path: "/v1/owner/login", body: map[string]string{"name": "Dana", "password": "an entirely different password", "code": e.totp(o.secret)}})
	expectError(t, unknown, http.StatusUnauthorized, "invalid_credentials")
	if unknown.Body.String() != wrongPassword.Body.String() || wrongPassword.Code != http.StatusUnauthorized {
		t.Fatalf("login answers differ: %s vs %s", unknown.Body.String(), wrongPassword.Body.String())
	}

	token, deviceID := e.pairedDevice(o)
	expectStatus(t, e.do(req{path: "/v1/desktop/status", header: bearer(token)}), http.StatusOK)
	expectStatus(t, e.do(req{method: http.MethodPost, path: "/v1/desktop/heartbeat", header: bearer(token), body: map[string]any{"protocolVersion": 1, "appVersion": "0.3.0", "platform": "linux"}}), http.StatusOK)
	o.stepUp()
	expectStatus(t, o.do(req{method: http.MethodDelete, path: "/v1/owner/devices/" + deviceID}), http.StatusNoContent)
	expectError(t, e.do(req{path: "/v1/desktop/status", header: bearer(token)}), http.StatusUnauthorized, "not_authenticated")
	expectError(t, e.do(req{path: "/v1/desktop/config", header: bearer(token)}), http.StatusUnauthorized, "not_authenticated")

	expectError(t, o.do(req{method: http.MethodDelete, path: "/v1/owner/owners/" + o.id}), http.StatusConflict, "last_owner")

	audit := decodeBody(t, o.do(req{path: "/v1/owner/audit"}))
	if audit["chain"].(map[string]any)["ok"] != true || len(audit["entries"].([]any)) == 0 {
		t.Fatalf("audit = %v", audit)
	}
	system := decodeBody(t, o.do(req{path: "/v1/owner/system"}))
	if system["auditChainOk"] != true {
		t.Fatalf("system = %v", system)
	}
	flags := decodeBody(t, o.do(req{path: "/v1/owner/flags"}))["flags"].([]any)
	if len(flags) != 1 || flags[0].(map[string]any)["enabled"] != true {
		t.Fatalf("flags = %v", flags)
	}
	expectStatus(t, o.do(req{method: http.MethodPost, path: "/v1/owner/export"}), http.StatusOK)
	expectStatus(t, e.do(req{path: "/readyz"}), http.StatusOK)
}
