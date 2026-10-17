package httpapi

import (
	"bytes"
	"container/list"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"usesesame.app/backend/internal/accounts"
	adminstore "usesesame.app/backend/internal/admin"
	"usesesame.app/backend/internal/syncproto"
	"usesesame.app/backend/internal/syncsvc"
	"usesesame.app/backend/internal/syncsvc/pgstore"
)

const syncHostTestOrigin = "https://account.example.invalid"

type syncHostEnv struct {
	t         *testing.T
	api       *api
	handler   http.Handler
	db        *sql.DB
	accounts  *accounts.PostgresStore
	ipPrefix  string
	ipCounter int64
}

type syncHostAccount struct {
	id        string
	token     string
	deviceID  string
	signingID string
	key       ed25519.PrivateKey
}

func newSyncHostEnv(t *testing.T) *syncHostEnv {
	t.Helper()
	databaseURL := os.Getenv("SESAME_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("SESAME_TEST_DATABASE_URL is required")
	}
	ctx := context.Background()
	accountStore, err := accounts.Open(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open account store: %v", err)
	}
	t.Cleanup(func() { _ = accountStore.Close() })
	db := accountStore.DB()
	lockDatabaseTests(t, db)
	adminStore, err := adminstore.Open(ctx, databaseURL, bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatalf("open admin store: %v", err)
	}
	t.Cleanup(func() { _ = adminStore.Close() })
	if _, err := db.ExecContext(ctx, `
		TRUNCATE sesame_sync3_vaults, sesame_sync3_devices, sesame_sync3_device_list,
		         sesame_sync3_key_packages, sesame_sync3_headers, sesame_sync3_ciphertexts,
		         sesame_sync3_pairings, sesame_sync3_checkpoints, sesame_sync3_nonces
		RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate sync tables: %v", err)
	}
	_, receiptKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	service, err := syncsvc.New(pgstore.New(db), syncsvc.Config{ReceiptKey: receiptKey})
	if err != nil {
		t.Fatal(err)
	}
	config := Config{
		Accounts:      accountStore,
		Admin:         adminStore,
		Sync:          service,
		AllowedOrigin: syncHostTestOrigin,
		AdminOrigin:   "https://admin.example.invalid",
		WebBaseURL:    syncHostTestOrigin,
	}
	seed := time.Now().UnixNano()
	env := &syncHostEnv{
		t:        t,
		db:       db,
		accounts: accountStore,
		ipPrefix: fmt.Sprintf("10.%d.%d.", byte(seed>>16), byte(seed>>8)),
	}
	env.handler = New(config)
	env.api = &api{
		config: Config{
			Accounts: accountStore, Admin: adminStore, Sync: service, AllowedOrigin: syncHostTestOrigin,
			AdminOrigin: "https://admin.example.invalid", WebBaseURL: syncHostTestOrigin,
			DeploymentProfile: DeploymentProfileOperator,
		},
		limits: &authLimiter{attempts: make(map[string]*limitEntry), recency: list.New()},
		routes: newRouteRegistry(),
	}
	env.setSyncFlag("true")
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `UPDATE sesame_feature_flags SET value = 'false' WHERE key = 'cloud_sync_available'`)
		if _, err := db.ExecContext(context.Background(), `DELETE FROM sesame_rate_limits WHERE key LIKE $1`, "%"+env.ipPrefix+"%"); err != nil {
			t.Error(err)
		}
	})
	return env
}

func (e *syncHostEnv) setSyncFlag(value string) {
	e.t.Helper()
	if _, err := e.db.ExecContext(context.Background(), `
		INSERT INTO sesame_feature_flags (key, value) VALUES ('cloud_sync_available', $1)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, value); err != nil {
		e.t.Fatalf("set the Sync flag: %v", err)
	}
}

func (e *syncHostEnv) nextIP() string {
	return e.ipPrefix + fmt.Sprintf("%d", atomic.AddInt64(&e.ipCounter, 1)) + ":40000"
}

func (e *syncHostEnv) seedAccount(id string, entitled bool) {
	e.t.Helper()
	ctx := context.Background()
	if _, err := e.db.ExecContext(ctx, `DELETE FROM sesame_accounts WHERE id = $1`, id); err != nil {
		e.t.Fatal(err)
	}
	clearLimits := func() {
		if _, err := e.db.ExecContext(context.Background(), `DELETE FROM sesame_rate_limits WHERE key LIKE $1`, "%:account:"+id); err != nil {
			e.t.Error(err)
		}
	}
	clearLimits()
	e.t.Cleanup(clearLimits)
	verified := "NULL"
	if entitled {
		verified = "NOW()"
	}
	if _, err := e.db.ExecContext(ctx, `INSERT INTO sesame_accounts (id, email, password_hash, beta_access, email_verified_at) VALUES ($1, $2, 'fictional-unused-hash', $3, `+verified+`)`,
		id, id+"@example.invalid", entitled); err != nil {
		e.t.Fatalf("seed account %s: %v", id, err)
	}
	e.t.Cleanup(func() {
		if _, err := e.db.ExecContext(context.Background(), `DELETE FROM sesame_accounts WHERE id = $1`, id); err != nil {
			e.t.Error(err)
		}
	})
}

func (e *syncHostEnv) link(accountID string) (token, deviceID string) {
	e.t.Helper()
	ctx := context.Background()
	_, codeHash, err := accounts.NewSessionToken()
	if err != nil {
		e.t.Fatal(err)
	}
	var generation int64
	if err := e.db.QueryRowContext(ctx, `SELECT security_generation FROM sesame_accounts WHERE id = $1`, accountID).Scan(&generation); err != nil {
		e.t.Fatalf("read account security generation: %v", err)
	}
	if _, err := e.accounts.CreateOrReplaceDesktopLink(ctx, accountID, generation, codeHash, time.Now().UTC().Add(10*time.Minute)); err != nil {
		e.t.Fatalf("create the link code: %v", err)
	}
	token, tokenHash, err := accounts.NewSessionToken()
	if err != nil {
		e.t.Fatal(err)
	}
	connection, err := e.accounts.RedeemDesktopLink(ctx, codeHash, "Fictional desktop", tokenHash, time.Now().UTC().Add(90*24*time.Hour))
	if err != nil {
		e.t.Fatalf("redeem the link: %v", err)
	}
	return token, connection.DeviceID
}

func (e *syncHostEnv) newAccount(id string, entitled bool) syncHostAccount {
	e.t.Helper()
	e.seedAccount(id, entitled)
	token, deviceID := e.link(id)
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		e.t.Fatal(err)
	}
	return syncHostAccount{id: id, token: token, deviceID: deviceID, signingID: "device" + strings.Repeat(string(id[len(id)-1]), 15), key: key}
}

func (e *syncHostEnv) authenticate(operation, token, remote string) (*httptest.ResponseRecorder, syncsvc.Caller, bool) {
	e.t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/v1/sync/"+operation, nil)
	request.RemoteAddr = remote
	if token != "" {
		request.Header.Set("Authorization", "Sesame "+token)
	}
	recorder := httptest.NewRecorder()
	caller, ok := hostedSyncHost{api: e.api}.Authenticate(recorder, request, operation)
	return recorder, caller, ok
}

func TestHostedSyncHostMapsTheAccountAndTheDesktopConnectionToACaller(t *testing.T) {
	env := newSyncHostEnv(t)
	alpha := env.newAccount("sync-host-alpha", true)

	recorder, caller, ok := env.authenticate("state", alpha.token, env.nextIP())
	if !ok {
		t.Fatalf("a linked desktop was refused: %d %s", recorder.Code, recorder.Body.String())
	}
	want := syncsvc.Caller{OwnerKey: alpha.id, TokenDeviceID: alpha.deviceID, Token: alpha.token}
	if caller != want {
		t.Fatalf("caller = %+v, want %+v", caller, want)
	}

	for name, token := range map[string]string{"no token": "", "an unknown token": "not-a-linked-token-at-all"} {
		recorder, caller, ok := env.authenticate("state", token, env.nextIP())
		if ok || caller != (syncsvc.Caller{}) || recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s: ok=%v caller=%+v status=%d %s", name, ok, caller, recorder.Code, recorder.Body.String())
		}
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/sync/state", nil)
	request.RemoteAddr = env.nextIP()
	request.Header.Set("Authorization", "Bearer "+alpha.token)
	denied := httptest.NewRecorder()
	if _, ok := (hostedSyncHost{api: env.api}).Authenticate(denied, request, "state"); ok || denied.Code != http.StatusUnauthorized {
		t.Fatalf("a bearer token was accepted: %d", denied.Code)
	}

	recorder, _, ok = env.authenticate("unknown-operation", alpha.token, env.nextIP())
	if ok || recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("an operation with no limit row was allowed: %d", recorder.Code)
	}

	if _, err := env.db.ExecContext(context.Background(), `UPDATE sesame_accounts SET suspended_at = NOW() WHERE id = $1`, alpha.id); err != nil {
		t.Fatal(err)
	}
	recorder, _, ok = env.authenticate("state", alpha.token, env.nextIP())
	if ok || recorder.Code != http.StatusLocked {
		t.Fatalf("a suspended account reached Sync: ok=%v status=%d", ok, recorder.Code)
	}
}

func TestHostedSyncHostAppliesTheRateLimitsPerOperationAndPerDevice(t *testing.T) {
	env := newSyncHostEnv(t)
	alpha := env.newAccount("sync-host-rate-a", true)
	secondToken, secondDevice := env.link(alpha.id)
	bravo := env.newAccount("sync-host-rate-b", true)

	limit := syncLimits["reset"]
	for attempt := 1; attempt <= limit.perDevice; attempt++ {
		if recorder, _, ok := env.authenticate("reset", alpha.token, env.nextIP()); !ok {
			t.Fatalf("reset %d was refused early: %d %s", attempt, recorder.Code, recorder.Body.String())
		}
	}
	recorder, _, ok := env.authenticate("reset", alpha.token, env.nextIP())
	if ok || recorder.Code != http.StatusTooManyRequests || recorder.Header().Get("Retry-After") == "" {
		t.Fatalf("the device limit did not stop reset: ok=%v status=%d", ok, recorder.Code)
	}
	if recorder, _, ok := env.authenticate("state", alpha.token, env.nextIP()); !ok {
		t.Fatalf("exhausting reset also stopped state: %d", recorder.Code)
	}
	if recorder, caller, ok := env.authenticate("reset", bravo.token, env.nextIP()); !ok || caller.OwnerKey != bravo.id {
		t.Fatalf("another account was stopped by this device's limit: %d", recorder.Code)
	}

	remaining := limit.perAccount - limit.perDevice - 1
	for attempt := 1; attempt <= remaining; attempt++ {
		if recorder, caller, ok := env.authenticate("reset", secondToken, env.nextIP()); !ok || caller.TokenDeviceID != secondDevice {
			t.Fatalf("second device reset %d was refused: %d", attempt, recorder.Code)
		}
	}
	recorder, _, ok = env.authenticate("reset", secondToken, env.nextIP())
	if ok || recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("the account limit did not stop a second device: ok=%v status=%d", ok, recorder.Code)
	}

	address := env.nextIP()
	for attempt := 1; attempt <= limit.perIP; attempt++ {
		env.authenticate("reset", bravo.token, address)
	}
	recorder, _, ok = env.authenticate("reset", bravo.token, address)
	if ok || recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("the address limit did not stop repeated resets: ok=%v status=%d", ok, recorder.Code)
	}
}

func TestHostedSyncHostChecksTheSubscriptionOnlyWhereAnOperationNeedsIt(t *testing.T) {
	env := newSyncHostEnv(t)
	paid := env.newAccount("sync-host-paid", true)
	lapsed := env.newAccount("sync-host-lapsed", false)

	for operation, limit := range syncLimits {
		if !limit.needsEntitlement {
			continue
		}
		if recorder, _, ok := env.authenticate(operation, paid.token, env.nextIP()); !ok {
			t.Fatalf("%s refused an entitled account: %d %s", operation, recorder.Code, recorder.Body.String())
		}
		recorder, caller, ok := env.authenticate(operation, lapsed.token, env.nextIP())
		if ok || caller != (syncsvc.Caller{}) || recorder.Code != http.StatusPaymentRequired || !strings.Contains(recorder.Body.String(), "sync_not_entitled") {
			t.Fatalf("%s let a lapsed account through: ok=%v %d %s", operation, ok, recorder.Code, recorder.Body.String())
		}
	}
	for operation, limit := range syncLimits {
		if limit.needsEntitlement {
			continue
		}
		if recorder, caller, ok := env.authenticate(operation, lapsed.token, env.nextIP()); !ok || caller.OwnerKey != lapsed.id {
			t.Fatalf("%s blocked a lapsed account from reading, removing or leaving: %d %s", operation, recorder.Code, recorder.Body.String())
		}
	}
}

func (e *syncHostEnv) signed(account syncHostAccount, method, target string, body any) *httptest.ResponseRecorder {
	e.t.Helper()
	var raw []byte
	if body != nil {
		var err error
		if raw, err = json.Marshal(body); err != nil {
			e.t.Fatal(err)
		}
	}
	nonce := make([]byte, syncproto.RequestNonceBytes)
	if _, err := rand.Read(nonce); err != nil {
		e.t.Fatal(err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(nonce)
	timestamp := uint64(time.Now().Unix())
	signature := ed25519.Sign(account.key, syncproto.RequestSignature{
		Method: method, Target: target, Body: raw, Token: account.token, Timestamp: timestamp, Nonce: encoded,
	}.SigningPayload())
	request := httptest.NewRequest(method, target, bytes.NewReader(raw))
	request.RemoteAddr = e.nextIP()
	request.Header.Set("Authorization", "Sesame "+account.token)
	request.Header.Set("Sesame-Request-Signature", fmt.Sprintf("%d.%s.%s", timestamp, encoded, base64.RawURLEncoding.EncodeToString(signature)))
	recorder := httptest.NewRecorder()
	e.handler.ServeHTTP(recorder, request)
	return recorder
}

func (e *syncHostEnv) read(account syncHostAccount, target string) *httptest.ResponseRecorder {
	e.t.Helper()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	request.RemoteAddr = e.nextIP()
	request.Header.Set("Authorization", "Sesame "+account.token)
	recorder := httptest.NewRecorder()
	e.handler.ServeHTTP(recorder, request)
	return recorder
}

func (e *syncHostEnv) createVault(account syncHostAccount, vaultID string) *httptest.ResponseRecorder {
	e.t.Helper()
	publicKey := base64.RawURLEncoding.EncodeToString(account.key.Public().(ed25519.PublicKey))
	entry := syncproto.SignDeviceListEntry(account.key, syncproto.DeviceListEntry{
		VaultID: vaultID, Index: 1, Kind: syncproto.EntryCreate,
		SignerDeviceID: account.signingID, SubjectDeviceID: account.signingID,
		SubjectSigningKey: publicKey, SubjectPackageKey: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)),
		SealedLabel: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 48)), EpochAfter: 1,
	})
	return e.signed(account, http.MethodPost, "/v1/sync/vault", map[string]any{"entry": entry})
}

func TestHostedSyncNeverShowsAnotherAccountsVault(t *testing.T) {
	env := newSyncHostEnv(t)
	alpha := env.newAccount("sync-host-owner-a", true)
	bravo := env.newAccount("sync-host-owner-b", true)
	const alphaVault = "vaultSyncHostAlpha01"
	const bravoVault = "vaultSyncHostBravo02"

	if created := env.createVault(alpha, alphaVault); created.Code != http.StatusCreated {
		t.Fatalf("alpha could not create a vault: %d %s", created.Code, created.Body.String())
	}
	state := env.read(alpha, "/v1/sync/state")
	if state.Code != http.StatusOK || !strings.Contains(state.Body.String(), alphaVault) {
		t.Fatalf("alpha cannot see its own vault: %d %s", state.Code, state.Body.String())
	}

	for _, target := range []string{
		"/v1/sync/state", "/v1/sync/device-list", "/v1/sync/devices", "/v1/sync/headers", "/v1/sync/snapshot",
		"/v1/sync/key-package", "/v1/sync/checkpoints", "/v1/sync/pairings/mine",
	} {
		seen := env.read(bravo, target)
		if seen.Code == http.StatusOK || strings.Contains(seen.Body.String(), alphaVault) || strings.Contains(seen.Body.String(), alpha.signingID) {
			t.Fatalf("bravo read alpha's vault through %s: %d %s", target, seen.Code, seen.Body.String())
		}
	}

	if reset := env.signed(bravo, http.MethodPost, "/v1/sync/reset", map[string]any{"vaultEpoch": 1}); reset.Code == http.StatusNoContent {
		t.Fatalf("bravo reset a vault it does not own")
	}
	if still := env.read(alpha, "/v1/sync/state"); still.Code != http.StatusOK {
		t.Fatalf("alpha's vault was changed by bravo: %d", still.Code)
	}

	if created := env.createVault(bravo, bravoVault); created.Code != http.StatusCreated {
		t.Fatalf("bravo could not create its own vault: %d %s", created.Code, created.Body.String())
	}
	bravoState := env.read(bravo, "/v1/sync/state")
	if !strings.Contains(bravoState.Body.String(), bravoVault) || strings.Contains(bravoState.Body.String(), alphaVault) {
		t.Fatalf("bravo's state is not its own: %s", bravoState.Body.String())
	}
	alphaState := env.read(alpha, "/v1/sync/state")
	if !strings.Contains(alphaState.Body.String(), alphaVault) || strings.Contains(alphaState.Body.String(), bravoVault) {
		t.Fatalf("alpha's state is not its own: %s", alphaState.Body.String())
	}

	if stolen := env.signed(syncHostAccount{id: bravo.id, token: bravo.token, deviceID: bravo.deviceID, signingID: bravo.signingID, key: alpha.key}, http.MethodPost, "/v1/sync/reset", map[string]any{"vaultEpoch": 1}); stolen.Code != http.StatusUnauthorized {
		t.Fatalf("a request signed by another account's key was accepted: %d %s", stolen.Code, stolen.Body.String())
	}
}

func TestHostedSyncRoutesCloseWhenTheFlagIsTurnedOff(t *testing.T) {
	env := newSyncHostEnv(t)
	alpha := env.newAccount("sync-host-flag", true)
	if state := env.read(alpha, "/v1/sync/state"); state.Code == http.StatusForbidden {
		t.Fatalf("Sync was closed while the flag was on: %s", state.Body.String())
	}
	env.setSyncFlag("false")
	for _, route := range syncRouteList {
		request := httptest.NewRequest(route.method, route.path, nil)
		request.RemoteAddr = env.nextIP()
		request.Header.Set("Authorization", "Sesame "+alpha.token)
		recorder := httptest.NewRecorder()
		env.handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), "sync_unavailable") {
			t.Fatalf("%s %s with the flag off = %d %s", route.method, route.path, recorder.Code, recorder.Body.String())
		}
	}
}
