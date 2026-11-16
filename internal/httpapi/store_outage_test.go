package httpapi

import (
	"container/list"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"usesesame.app/backend/internal/accounts"
)

var errFictionalStoreOutage = errors.New("fictional connection reset by the database")

type desktopLookupStore struct {
	accounts.Store
	accounts.DesktopStore
	lookupErr error
}

func (s *desktopLookupStore) DesktopConnectionForToken(context.Context, []byte) (accounts.DesktopConnection, error) {
	return accounts.DesktopConnection{}, s.lookupErr
}

func desktopStatusRequest(handler http.Handler, remote string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "/v1/desktop/status", nil)
	request.RemoteAddr = remote
	request.Header.Set("Authorization", "Sesame fictional-desktop-token")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestDesktopStatusKeepsTheLinkWhenTheTokenLookupFails(t *testing.T) {
	store := &desktopLookupStore{lookupErr: errFictionalStoreOutage}
	handler := New(Config{AllowedOrigin: "https://account.example.invalid", Accounts: store})

	response := desktopStatusRequest(handler, "192.0.2.10:40000")
	if response.Code != http.StatusServiceUnavailable || errorCode(t, response) != "desktop_link_check_unavailable" || response.Header().Get("Retry-After") == "" {
		t.Fatalf("store outage = %d %q retry %q", response.Code, response.Body.String(), response.Header().Get("Retry-After"))
	}

	store.lookupErr = accounts.ErrNotFound
	response = desktopStatusRequest(handler, "192.0.2.11:40000")
	if response.Code != http.StatusUnauthorized || errorCode(t, response) != "not_authenticated" {
		t.Fatalf("unknown token = %d %q", response.Code, response.Body.String())
	}
}

func TestSyncAuthenticationKeepsTheLinkWhenTheTokenLookupFails(t *testing.T) {
	store := &desktopLookupStore{lookupErr: errFictionalStoreOutage}
	host := hostedSyncHost{api: &api{
		config: Config{Accounts: store, SyncOwnerPepper: syncOwnerTestPepper},
		limits: &authLimiter{attempts: map[string]*limitEntry{}, recency: list.New()},
		routes: newRouteRegistry(),
	}}
	authenticate := func(remote string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, "/v1/sync/state", nil)
		request.RemoteAddr = remote
		request.Header.Set("Authorization", "Sesame fictional-desktop-token")
		recorder := httptest.NewRecorder()
		if _, ok := host.Authenticate(recorder, request, "state"); ok {
			t.Fatal("a desktop was authenticated without a connection")
		}
		return recorder
	}

	response := authenticate("192.0.2.20:40000")
	if response.Code != http.StatusServiceUnavailable || errorCode(t, response) != "desktop_link_check_unavailable" || response.Header().Get("Retry-After") == "" {
		t.Fatalf("store outage = %d %q", response.Code, response.Body.String())
	}

	store.lookupErr = accounts.ErrNotFound
	response = authenticate("192.0.2.21:40000")
	if response.Code != http.StatusUnauthorized || errorCode(t, response) != "not_authenticated" {
		t.Fatalf("unknown token = %d %q", response.Code, response.Body.String())
	}
}
