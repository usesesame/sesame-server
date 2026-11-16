package httpapi

import (
	"container/list"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"usesesame.app/backend/internal/accounts"
)

func shortenSyncFlagCache(t *testing.T, window time.Duration) {
	t.Helper()
	original := syncFlagCacheDuration
	syncFlagCacheDuration = window
	t.Cleanup(func() { syncFlagCacheDuration = original })
}

type busyPasswordWork struct {
	verifies int
	dummies  int
	hashes   int
}

func installBusyPasswordWork(t *testing.T) *busyPasswordWork {
	t.Helper()
	calls := &busyPasswordWork{}
	verify, dummy, hash := verifyPasswordWork, dummyPasswordWork, hashPasswordWork
	verifyPasswordWork = func(context.Context, string, string) (bool, error) {
		calls.verifies++
		return false, accounts.ErrPasswordWorkBusy
	}
	dummyPasswordWork = func(context.Context) error {
		calls.dummies++
		return accounts.ErrPasswordWorkBusy
	}
	hashPasswordWork = func(context.Context, string) (string, error) {
		calls.hashes++
		return "", accounts.ErrPasswordWorkBusy
	}
	t.Cleanup(func() { verifyPasswordWork, dummyPasswordWork, hashPasswordWork = verify, dummy, hash })
	return calls
}

type passwordTestStore struct {
	accounts.Store
	accounts.AccountSecurityStore
	known     bool
	sessions  int
	verifyErr error
}

func (s *passwordTestStore) FindByEmail(context.Context, string) (accounts.User, string, error) {
	if !s.known {
		return accounts.User{}, "", accounts.ErrNotFound
	}
	return accounts.User{ID: "acct-capacity", Email: "capacity@example.invalid", EmailVerified: true}, "$argon2id$v=19$m=19456,t=2,p=1$ZmljdGlvbmFsLXNhbHQtMQ$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", nil
}

func (s *passwordTestStore) FindByID(context.Context, string) (accounts.User, string, error) {
	return s.FindByEmail(context.Background(), "")
}

func (s *passwordTestStore) CreateSession(context.Context, string, []byte, time.Time) error {
	s.sessions++
	return nil
}

func (s *passwordTestStore) SessionForToken(context.Context, []byte) (accounts.User, accounts.SessionInfo, error) {
	return accounts.User{ID: "acct-capacity", Email: "capacity@example.invalid", EmailVerified: true}, accounts.SessionInfo{AuthenticatedAt: time.Now()}, nil
}

func (s *passwordTestStore) VerifyEmail(context.Context, accounts.TokenSessionRotation) (accounts.User, error) {
	return accounts.User{}, s.verifyErr
}

func newPasswordTestAPI(store *passwordTestStore) *api {
	return &api{
		config: Config{Accounts: store, AllowedOrigin: "https://account.example.invalid", RegistrationMode: "public", SessionDuration: time.Hour, RecentAuthDuration: time.Hour},
		limits: &authLimiter{attempts: map[string]*limitEntry{}, recency: list.New()},
	}
}

func jsonRequest(method, path, body string) *http.Request {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = "198.51.100.20:40000"
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "fictional-session"})
	return request
}

func assertPasswordWorkBusy(t *testing.T, name string, response *httptest.ResponseRecorder) {
	t.Helper()
	if response.Code != http.StatusServiceUnavailable || errorCode(t, response) != "password_check_busy" || response.Header().Get("Retry-After") != "2" {
		t.Fatalf("%s = %d %s retry %q, want 503 password_check_busy with Retry-After 2", name, response.Code, response.Body.String(), response.Header().Get("Retry-After"))
	}
	if response.Header().Get("Set-Cookie") != "" {
		t.Fatalf("%s set a cookie while password work was busy", name)
	}
}

func TestBusyPasswordWorkIsNeverAWrongPassword(t *testing.T) {
	cases := []struct {
		name    string
		known   bool
		handler func(*api) http.HandlerFunc
		request *http.Request
	}{
		{"login with a known email", true, func(a *api) http.HandlerFunc { return a.login }, jsonRequest(http.MethodPost, "/v1/auth/login", `{"email":"capacity@example.invalid","password":"fictional password"}`)},
		{"login with an unknown email", false, func(a *api) http.HandlerFunc { return a.login }, jsonRequest(http.MethodPost, "/v1/auth/login", `{"email":"nobody@example.invalid","password":"fictional password"}`)},
		{"register", false, func(a *api) http.HandlerFunc { return a.register }, jsonRequest(http.MethodPost, "/v1/auth/register", `{"email":"new@example.invalid","password":"fictional long password","termsAccepted":true,"privacyAcknowledged":true,"termsVersion":"`+termsVersion+`","privacyVersion":"`+privacyVersion+`"}`)},
		{"reauthenticate", true, func(a *api) http.HandlerFunc { return a.reauthenticate }, jsonRequest(http.MethodPost, "/v1/account/reauthenticate", `{"password":"fictional password"}`)},
		{"change password", true, func(a *api) http.HandlerFunc { return a.changePassword }, jsonRequest(http.MethodPost, "/v1/account/password", `{"currentPassword":"fictional password","newPassword":"fictional new password"}`)},
		{"set up a password", true, func(a *api) http.HandlerFunc { return a.setupPassword }, jsonRequest(http.MethodPost, "/v1/account/password/setup", `{"newPassword":"fictional new password"}`)},
		{"delete the account", true, func(a *api) http.HandlerFunc { return a.deleteAccount }, jsonRequest(http.MethodPost, "/v1/account/delete", `{"password":"fictional password"}`)},
		{"recover a password", true, func(a *api) http.HandlerFunc { return a.confirmPasswordRecovery }, jsonRequest(http.MethodPost, "/v1/auth/password/recovery/confirm", `{"token":"`+strings.Repeat("a", 43)+`","newPassword":"fictional new password"}`)},
	}
	for _, tc := range cases {
		calls := installBusyPasswordWork(t)
		store := &passwordTestStore{known: tc.known}
		service := newPasswordTestAPI(store)
		response := httptest.NewRecorder()
		tc.handler(service)(response, tc.request)
		assertPasswordWorkBusy(t, tc.name, response)
		if calls.verifies+calls.dummies+calls.hashes != 1 {
			t.Fatalf("%s ran %+v password operations, want exactly one", tc.name, *calls)
		}
		if store.sessions != 0 {
			t.Fatalf("%s created a session while password work was busy", tc.name)
		}
	}
}

func TestBusyPasswordWorkDuringEmailVerificationIsNotAWrongPassword(t *testing.T) {
	store := &passwordTestStore{verifyErr: accounts.ErrPasswordWorkBusy}
	service := newPasswordTestAPI(store)
	response := httptest.NewRecorder()
	service.confirmEmailVerification(response, jsonRequest(http.MethodPost, "/v1/auth/email/verification/confirm", `{"token":"`+strings.Repeat("a", 43)+`","password":"fictional password"}`))
	assertPasswordWorkBusy(t, "email verification", response)
}

func TestLoginStillAnswersAWrongPasswordAsWrong(t *testing.T) {
	verify := verifyPasswordWork
	verifyPasswordWork = func(context.Context, string, string) (bool, error) { return false, nil }
	t.Cleanup(func() { verifyPasswordWork = verify })
	store := &passwordTestStore{known: true}
	response := httptest.NewRecorder()
	newPasswordTestAPI(store).login(response, jsonRequest(http.MethodPost, "/v1/auth/login", `{"email":"capacity@example.invalid","password":"fictional password"}`))
	if response.Code != http.StatusUnauthorized || errorCode(t, response) != "invalid_credentials" {
		t.Fatalf("wrong password = %d %s, want 401 invalid_credentials", response.Code, response.Body.String())
	}
}

type countingRateLimitStore struct {
	accounts.Store
	mu       sync.Mutex
	calls    int
	allowed  bool
	retry    time.Duration
	failNext error
}

func (s *countingRateLimitStore) ConsumeRateLimit(context.Context, string, int, time.Duration) (bool, time.Duration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.failNext != nil {
		err := s.failNext
		s.failNext = nil
		return false, 0, err
	}
	return s.allowed, s.retry, nil
}

func (s *countingRateLimitStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func newRefusalTestAPI(store *countingRateLimitStore) *api {
	return &api{
		config:  Config{Accounts: store},
		limits:  &authLimiter{attempts: map[string]*limitEntry{}, recency: list.New()},
		refused: newRefusals(),
	}
}

func limitAttempt(service *api, key string, limit int, window time.Duration) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/v1/auth/login", nil)
	request.RemoteAddr = "198.51.100.30:40000"
	response := httptest.NewRecorder()
	service.allowKeyed(response, request, key, limit, window)
	return response
}

func TestARefusedClientIsAnsweredWithoutTheDatabaseUntilItsWindowEnds(t *testing.T) {
	store := &countingRateLimitStore{allowed: false, retry: 150 * time.Millisecond}
	service := newRefusalTestAPI(store)
	first := limitAttempt(service, "login:198.51.100.30", 8, time.Minute)
	if first.Code != http.StatusTooManyRequests || store.count() != 1 {
		t.Fatalf("first refusal = %d after %d database calls", first.Code, store.count())
	}
	for range 20 {
		again := limitAttempt(service, "login:198.51.100.30", 8, time.Minute)
		if again.Code != http.StatusTooManyRequests || again.Header().Get("Retry-After") == "" {
			t.Fatalf("remembered refusal = %d retry %q", again.Code, again.Header().Get("Retry-After"))
		}
	}
	if store.count() != 1 {
		t.Fatalf("database calls = %d while the client was already refused, want 1", store.count())
	}
	time.Sleep(200 * time.Millisecond)
	store.allowed = true
	if after := limitAttempt(service, "login:198.51.100.30", 8, time.Minute); after.Code != http.StatusOK || store.count() != 2 {
		t.Fatalf("after the window = %d with %d database calls, want the database asked again", after.Code, store.count())
	}
}

func TestTheRefusalCacheNeverAllowsWhatTheDatabaseRefuses(t *testing.T) {
	store := &countingRateLimitStore{allowed: true, retry: time.Minute}
	service := newRefusalTestAPI(store)
	for range 5 {
		if response := limitAttempt(service, "login:198.51.100.31", 8, time.Minute); response.Code != http.StatusOK {
			t.Fatalf("allowed attempt = %d", response.Code)
		}
	}
	if store.count() != 5 {
		t.Fatalf("database calls = %d, want every allowed attempt counted by the database", store.count())
	}
	store.allowed = false
	if response := limitAttempt(service, "login:198.51.100.31", 8, time.Minute); response.Code != http.StatusTooManyRequests {
		t.Fatalf("database refusal = %d, want 429", response.Code)
	}
	store.failNext = errors.New("database unavailable")
	store.allowed = true
	if response := limitAttempt(service, "login:198.51.100.32", 8, time.Minute); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("an unreadable limit = %d, want 503", response.Code)
	}
	if response := limitAttempt(service, "login:198.51.100.32", 8, time.Minute); response.Code != http.StatusOK {
		t.Fatalf("an outage was remembered as a refusal: %d", response.Code)
	}
}

func TestRememberedRefusalsAreKeptPerLimitAndWindow(t *testing.T) {
	store := &countingRateLimitStore{allowed: false, retry: time.Minute}
	service := newRefusalTestAPI(store)
	limitAttempt(service, "shared-key", 5, time.Minute)
	store.allowed = true
	if response := limitAttempt(service, "shared-key", 50, time.Hour); response.Code != http.StatusOK {
		t.Fatalf("a refusal under one limit refused another limit on the same key: %d", response.Code)
	}
}

func TestRememberedRefusalsStayBounded(t *testing.T) {
	cache := newRefusals()
	now := time.Now()
	for index := range maxRememberedRefusals + 500 {
		cache.remember("key-"+time.Duration(index).String(), now.Add(time.Hour), now)
	}
	if size := cache.size(); size != maxRememberedRefusals {
		t.Fatalf("remembered %d refusals, want at most %d", size, maxRememberedRefusals)
	}
	later := now.Add(2 * time.Hour)
	cache.remember("fresh", later.Add(time.Minute), later)
	if _, refused := cache.active("fresh", later); !refused {
		t.Fatal("a full cache did not make room once its entries expired")
	}
	if _, refused := cache.active("key-0s", later); refused {
		t.Fatal("an expired refusal was still active")
	}
	cache.remember("past", now.Add(-time.Second), now)
	if _, refused := cache.active("past", now); refused {
		t.Fatal("a refusal that already ended was remembered")
	}
}

func TestOnlySyncTransfersRunWithoutTheRequestDeadline(t *testing.T) {
	built := &api{config: Config{}, limits: &authLimiter{attempts: map[string]*limitEntry{}, recency: list.New()}, routes: newRouteRegistry()}
	mux := http.NewServeMux()
	built.registerSyncRoutes(mux)
	streamed := map[string]bool{}
	for pattern, policy := range built.routes.policies {
		if policy.streamsBody {
			streamed[pattern] = true
		}
	}
	if len(streamed) != 2 || !streamed["POST /v1/sync/snapshots"] || !streamed["GET /v1/sync/snapshot"] {
		t.Fatalf("routes without the request deadline = %v, want only the two Sync transfers", streamed)
	}
}

func TestRequestDeadlineAppliesOutsideStreamedRoutes(t *testing.T) {
	service := &api{config: Config{}, limits: &authLimiter{attempts: map[string]*limitEntry{}, recency: list.New()}, routes: newRouteRegistry()}
	mux := http.NewServeMux()
	deadlines := map[string]time.Duration{}
	record := func(name string) http.HandlerFunc {
		return func(response http.ResponseWriter, request *http.Request) {
			if deadline, ok := request.Context().Deadline(); ok {
				deadlines[name] = time.Until(deadline)
			} else {
				deadlines[name] = -1
			}
			response.WriteHeader(http.StatusNoContent)
		}
	}
	service.route(mux, routePolicy{audience: audiencePublicMetadata}, "GET /v1/short", record("short"))
	service.route(mux, routePolicy{audience: audiencePublicMetadata, streamsBody: true}, "GET /v1/stream", record("stream"))
	handler := service.secureMux(mux)
	for _, path := range []string{"/v1/short", "/v1/stream"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusNoContent {
			t.Fatalf("%s = %d %s", path, response.Code, response.Body.String())
		}
	}
	if remaining := deadlines["short"]; remaining <= requestDeadline-time.Second || remaining > requestDeadline {
		t.Fatalf("short route deadline in %s, want about %s", remaining, requestDeadline)
	}
	if deadlines["stream"] != -1 {
		t.Fatalf("streamed route had a deadline in %s", deadlines["stream"])
	}
}
