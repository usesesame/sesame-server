package httpapi

import (
	"container/list"
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"usesesame.app/backend/internal/accounts"
)

type rateLimitPeer struct {
	remoteAddr string
	forwarded  string
}

func newRateLimitUnitAPI(trustedProxies ...netip.Prefix) *api {
	return &api{
		config: Config{TrustedProxies: trustedProxies},
		limits: &authLimiter{attempts: map[string]*limitEntry{}, recency: list.New()},
	}
}

func rateLimitAttempt(service *api, peer rateLimitPeer) bool {
	request := httptest.NewRequest(http.MethodPost, "/v1/auth/login", nil)
	request.RemoteAddr = peer.remoteAddr
	if peer.forwarded != "" {
		request.Header.Set("X-Forwarded-For", peer.forwarded)
	}
	response := httptest.NewRecorder()
	return service.allowAuthAttempt(response, request, "test-login")
}

func exhaustAuthAttempts(t *testing.T, service *api, peer rateLimitPeer) {
	t.Helper()
	for attempt := 1; attempt <= 8; attempt++ {
		if !rateLimitAttempt(service, peer) {
			t.Fatalf("attempt %d from %s was limited", attempt, peer.remoteAddr)
		}
	}
}

func TestAuthAttemptsShareAnIPv6NetworkBudget(t *testing.T) {
	service := newRateLimitUnitAPI()
	exhaustAuthAttempts(t, service, rateLimitPeer{remoteAddr: "[2001:db8:0:1::1]:40000"})
	if rateLimitAttempt(service, rateLimitPeer{remoteAddr: "[2001:db8:0:1::2]:40000"}) {
		t.Fatal("a second address in the same /64 started a fresh auth budget")
	}
	if rateLimitAttempt(service, rateLimitPeer{remoteAddr: "[2001:db8:0:1::1]:50000"}) {
		t.Fatal("the spent /64 started a fresh auth budget")
	}
}

func TestAuthAttemptsSeparateIPv6Networks(t *testing.T) {
	service := newRateLimitUnitAPI()
	exhaustAuthAttempts(t, service, rateLimitPeer{remoteAddr: "[2001:db8:0:1::1]:40000"})
	if !rateLimitAttempt(service, rateLimitPeer{remoteAddr: "[2001:db8:0:2::1]:40000"}) {
		t.Fatal("an address in a different /64 was denied")
	}
}

func TestAuthAttemptsSeparateIPv4Addresses(t *testing.T) {
	service := newRateLimitUnitAPI()
	exhaustAuthAttempts(t, service, rateLimitPeer{remoteAddr: "192.0.2.10:40000"})
	if !rateLimitAttempt(service, rateLimitPeer{remoteAddr: "192.0.2.11:40000"}) {
		t.Fatal("a different IPv4 address was denied")
	}
	if rateLimitAttempt(service, rateLimitPeer{remoteAddr: "192.0.2.10:40000"}) {
		t.Fatal("the spent IPv4 address started a fresh auth budget")
	}
}

func TestAuthAttemptsUseTheForwardedAddressOfATrustedProxy(t *testing.T) {
	service := newRateLimitUnitAPI(netip.MustParsePrefix("10.0.0.0/8"))
	exhaustAuthAttempts(t, service, rateLimitPeer{remoteAddr: "10.0.0.5:40000", forwarded: "203.0.113.10"})
	if rateLimitAttempt(service, rateLimitPeer{remoteAddr: "10.0.0.6:40000", forwarded: "203.0.113.10"}) {
		t.Fatal("the same forwarded address got a fresh budget through another proxy peer")
	}
	if !rateLimitAttempt(service, rateLimitPeer{remoteAddr: "10.0.0.5:40000", forwarded: "203.0.113.11"}) {
		t.Fatal("a different forwarded address was denied")
	}
	if !rateLimitAttempt(service, rateLimitPeer{remoteAddr: "198.51.100.1:40000", forwarded: "203.0.113.10"}) {
		t.Fatal("a peer outside the trusted proxies chose the limiter key")
	}
}

func TestAuthAttemptsUseTheForwardedIPv6NetworkOfATrustedProxy(t *testing.T) {
	service := newRateLimitUnitAPI(netip.MustParsePrefix("10.0.0.0/8"))
	exhaustAuthAttempts(t, service, rateLimitPeer{remoteAddr: "10.0.0.5:40000", forwarded: "2001:db8:0:9::1"})
	if rateLimitAttempt(service, rateLimitPeer{remoteAddr: "10.0.0.5:40000", forwarded: "2001:db8:0:9::2"}) {
		t.Fatal("a second forwarded address in the same /64 started a fresh budget")
	}
	if !rateLimitAttempt(service, rateLimitPeer{remoteAddr: "10.0.0.5:40000", forwarded: "2001:db8:0:10::1"}) {
		t.Fatal("a forwarded address in a different /64 was denied")
	}
}

func TestAuthAttemptsSkipTrustedForwardedHops(t *testing.T) {
	service := newRateLimitUnitAPI(netip.MustParsePrefix("10.0.0.0/8"))
	exhaustAuthAttempts(t, service, rateLimitPeer{remoteAddr: "10.0.0.5:40000", forwarded: "203.0.113.20, 10.0.0.9"})
	if rateLimitAttempt(service, rateLimitPeer{remoteAddr: "10.0.0.5:40000", forwarded: "203.0.113.20, 10.0.0.7"}) {
		t.Fatal("the spent forwarded address started a fresh budget through another trusted hop")
	}
	if !rateLimitAttempt(service, rateLimitPeer{remoteAddr: "10.0.0.5:40000", forwarded: "203.0.113.21, 10.0.0.9"}) {
		t.Fatal("a different forwarded address was denied")
	}
}

type rateLimitAccountStub struct {
	accounts.Store
	accounts.AccountSecurityStore
	recoveries int
}

func (s *rateLimitAccountStub) CreatePasswordRecovery(context.Context, string, []byte, time.Time) (accounts.User, bool, error) {
	s.recoveries++
	return accounts.User{}, false, nil
}

type rateLimitEmailSender struct{}

func (s *rateLimitEmailSender) SendAccountEmail(context.Context, AccountEmail) error {
	return nil
}

func passwordRecoveryPost(t *testing.T, service *api, remoteAddr string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/v1/auth/password/recovery/request", strings.NewReader(`{"email":"recovery@example.invalid"}`))
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = remoteAddr
	response := httptest.NewRecorder()
	service.requestPasswordRecovery(response, request)
	return response
}

func TestPasswordRecoveryPeerBudgetFollowsTheClientNetwork(t *testing.T) {
	store := &rateLimitAccountStub{}
	service := newRateLimitUnitAPI()
	service.config.Accounts = store
	service.config.EmailSender = &rateLimitEmailSender{}
	for _, remoteAddr := range []string{"[2001:db8:0:1::1]:40000", "[2001:db8:0:1::2]:40000"} {
		if response := passwordRecoveryPost(t, service, remoteAddr); response.Code != http.StatusAccepted {
			t.Fatalf("recovery from %s = %d, want 202", remoteAddr, response.Code)
		}
	}
	if store.recoveries != 2 {
		t.Fatalf("recoveries = %d, want 2", store.recoveries)
	}
	response := passwordRecoveryPost(t, service, "[2001:db8:0:1::3]:40000")
	if response.Code != http.StatusAccepted || store.recoveries != 2 {
		t.Fatalf("a third address in the same /64 reached the recovery store: status %d, recoveries %d", response.Code, store.recoveries)
	}
	response = passwordRecoveryPost(t, service, "[2001:db8:0:2::1]:40000")
	if response.Code != http.StatusAccepted || store.recoveries != 3 {
		t.Fatalf("a different /64 did not reach the recovery store: status %d, recoveries %d", response.Code, store.recoveries)
	}
}
