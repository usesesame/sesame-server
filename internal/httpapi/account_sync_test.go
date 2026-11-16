package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"usesesame.app/backend/internal/accounts"
	"usesesame.app/backend/internal/syncsvc"
)

const accountSyncTestOrigin = "https://account.example.invalid"
const accountSyncTestCSRF = "fictional-account-sync-csrf"

type accountSyncTestStore struct {
	accounts.Store
	accounts.AccountSecurityStore
	authenticatedAt time.Time
	events          []accounts.AccountEvent
}

func (s *accountSyncTestStore) SessionForToken(context.Context, []byte) (accounts.User, accounts.SessionInfo, error) {
	return accounts.User{ID: "acct-sync-unit", Email: "sync-unit@example.invalid"}, accounts.SessionInfo{AuthenticatedAt: s.authenticatedAt}, nil
}

func (s *accountSyncTestStore) RecordAccountEvent(_ context.Context, event accounts.AccountEvent) error {
	s.events = append(s.events, event)
	return nil
}

func (s *accountSyncTestStore) AccountEvents(context.Context, string, int) ([]accounts.AccountEvent, error) {
	return s.events, nil
}

type accountSyncTestClient struct {
	t       *testing.T
	handler http.Handler
	store   *accountSyncTestStore
	address atomic.Int64
}

func newAccountSyncTestClient(t *testing.T, sync *syncsvc.Service, pepper string) *accountSyncTestClient {
	t.Helper()
	store := &accountSyncTestStore{authenticatedAt: time.Now()}
	handler := New(Config{AllowedOrigin: accountSyncTestOrigin, Accounts: store, Sync: sync, SyncOwnerPepper: pepper})
	return &accountSyncTestClient{t: t, handler: handler, store: store}
}

type accountSyncRequest struct {
	method  string
	origin  string
	csrf    string
	cookie  bool
	body    string
	address string
}

func (c *accountSyncTestClient) send(options accountSyncRequest) *httptest.ResponseRecorder {
	c.t.Helper()
	request := httptest.NewRequest(options.method, "/v1/account/sync-vault", strings.NewReader(options.body))
	if options.body == "" {
		request = httptest.NewRequest(options.method, "/v1/account/sync-vault", nil)
	}
	request.RemoteAddr = options.address
	if request.RemoteAddr == "" {
		request.RemoteAddr = "192.0.2." + strconv.FormatInt(c.address.Add(1), 10) + ":40000"
	}
	if options.origin != "" {
		request.Header.Set("Origin", options.origin)
	}
	if options.csrf != "" {
		request.Header.Set("X-Sesame-CSRF", options.csrf)
		request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: accountSyncTestCSRF})
	}
	if options.cookie {
		request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "fictional-account-sync-session"})
	}
	recorder := httptest.NewRecorder()
	c.handler.ServeHTTP(recorder, request)
	return recorder
}

func (c *accountSyncTestClient) read() *httptest.ResponseRecorder {
	return c.send(accountSyncRequest{method: http.MethodGet, origin: accountSyncTestOrigin, cookie: true})
}

func (c *accountSyncTestClient) remove() *httptest.ResponseRecorder {
	return c.send(accountSyncRequest{method: http.MethodDelete, origin: accountSyncTestOrigin, csrf: accountSyncTestCSRF, cookie: true})
}

func decodeAccountSyncStatus(t *testing.T, response *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var status map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode %q: %v", response.Body.String(), err)
	}
	return status
}

func TestAccountSyncVaultWithoutASyncStoreReportsUnavailableAndDeletesNothing(t *testing.T) {
	for name, client := range map[string]*accountSyncTestClient{
		"no service":   newAccountSyncTestClient(t, nil, syncOwnerTestPepper),
		"short pepper": newAccountSyncTestClient(t, newMemorySyncService(t), "too-short"),
	} {
		response := client.read()
		if response.Code != http.StatusOK {
			t.Fatalf("%s: GET = %d %s", name, response.Code, response.Body.String())
		}
		status := decodeAccountSyncStatus(t, response)
		if status["available"] != false || status["hasVault"] != false || status["approvedDevices"] != float64(0) || status["updatedAt"] != nil {
			t.Fatalf("%s: status = %v", name, status)
		}
		if removed := client.remove(); removed.Code != http.StatusNoContent {
			t.Fatalf("%s: DELETE = %d %s", name, removed.Code, removed.Body.String())
		}
		if len(client.store.events) != 0 {
			t.Fatalf("%s: recorded %v without deleting a vault", name, client.store.events)
		}
	}
}

func TestAccountSyncVaultWithoutAVaultAnswersNoContentAndRecordsNothing(t *testing.T) {
	client := newAccountSyncTestClient(t, newMemorySyncService(t), syncOwnerTestPepper)
	response := client.read()
	status := decodeAccountSyncStatus(t, response)
	if response.Code != http.StatusOK || status["available"] != true || status["hasVault"] != false || status["updatedAt"] != nil {
		t.Fatalf("GET = %d %v", response.Code, status)
	}
	if removed := client.remove(); removed.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d %s", removed.Code, removed.Body.String())
	}
	if len(client.store.events) != 0 {
		t.Fatalf("recorded %v without deleting a vault", client.store.events)
	}
}

func TestAccountSyncVaultRefusesRequestsOutsideTheWebsiteSessionPolicy(t *testing.T) {
	client := newAccountSyncTestClient(t, newMemorySyncService(t), syncOwnerTestPepper)
	cases := []struct {
		name    string
		request accountSyncRequest
		status  int
		code    string
	}{
		{"read without an origin", accountSyncRequest{method: http.MethodGet, cookie: true}, http.StatusForbidden, "origin_not_allowed"},
		{"read from another origin", accountSyncRequest{method: http.MethodGet, origin: "https://elsewhere.example.invalid", cookie: true}, http.StatusForbidden, "origin_not_allowed"},
		{"read without a session", accountSyncRequest{method: http.MethodGet, origin: accountSyncTestOrigin}, http.StatusUnauthorized, "not_authenticated"},
		{"delete from another origin", accountSyncRequest{method: http.MethodDelete, origin: "https://elsewhere.example.invalid", csrf: accountSyncTestCSRF, cookie: true}, http.StatusForbidden, "origin_not_allowed"},
		{"delete without a CSRF token", accountSyncRequest{method: http.MethodDelete, origin: accountSyncTestOrigin, cookie: true}, http.StatusForbidden, "invalid_csrf"},
		{"delete with a wrong CSRF token", accountSyncRequest{method: http.MethodDelete, origin: accountSyncTestOrigin, csrf: "fictional-other-csrf-token-0", cookie: true}, http.StatusForbidden, "invalid_csrf"},
		{"delete without a session", accountSyncRequest{method: http.MethodDelete, origin: accountSyncTestOrigin, csrf: accountSyncTestCSRF}, http.StatusUnauthorized, "not_authenticated"},
		{"delete with a body", accountSyncRequest{method: http.MethodDelete, origin: accountSyncTestOrigin, csrf: accountSyncTestCSRF, cookie: true, body: `{"confirm":true}`}, http.StatusUnsupportedMediaType, "request_body_not_supported"},
	}
	for _, tc := range cases {
		response := client.send(tc.request)
		if response.Code != tc.status || errorCode(t, response) != tc.code {
			t.Fatalf("%s = %d %s", tc.name, response.Code, response.Body.String())
		}
	}

	client.store.authenticatedAt = time.Now().Add(-time.Hour)
	if response := client.remove(); response.Code != http.StatusForbidden || errorCode(t, response) != "recent_auth_required" {
		t.Fatalf("delete with a stale session = %d %s", response.Code, response.Body.String())
	}
	if response := client.read(); response.Code != http.StatusOK {
		t.Fatalf("reading needs no recent authentication: %d %s", response.Code, response.Body.String())
	}
	if len(client.store.events) != 0 {
		t.Fatalf("a refused request recorded %v", client.store.events)
	}
}

func TestAccountSyncVaultDeleteIsRateLimitedPerAddressAndPerAccount(t *testing.T) {
	client := newAccountSyncTestClient(t, newMemorySyncService(t), syncOwnerTestPepper)
	address := "198.51.100.7:40000"
	for attempt := 1; attempt <= accountSyncVaultDeleteAddressLimit; attempt++ {
		request := accountSyncRequest{method: http.MethodDelete, origin: accountSyncTestOrigin, csrf: accountSyncTestCSRF, cookie: true, address: address}
		if response := client.send(request); response.Code != http.StatusNoContent {
			t.Fatalf("delete %d = %d %s", attempt, response.Code, response.Body.String())
		}
	}
	limited := client.send(accountSyncRequest{method: http.MethodDelete, origin: accountSyncTestOrigin, csrf: accountSyncTestCSRF, cookie: true, address: address})
	if limited.Code != http.StatusTooManyRequests || limited.Header().Get("Retry-After") == "" {
		t.Fatalf("the address limit did not stop deletes: %d %s", limited.Code, limited.Body.String())
	}
	if response := client.remove(); response.Code != http.StatusTooManyRequests {
		t.Fatalf("the account limit did not stop a delete from a new address: %d %s", response.Code, response.Body.String())
	}
	if response := client.read(); response.Code != http.StatusOK {
		t.Fatalf("exhausting deletes also stopped reads: %d", response.Code)
	}
}
