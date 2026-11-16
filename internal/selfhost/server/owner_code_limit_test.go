package server_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"usesesame.app/backend/internal/authkit"
	"usesesame.app/backend/internal/selfhost/server"
)

func (e *env) wrongCode(secret string) string {
	e.t.Helper()
	e.clock.Advance(31 * time.Second)
	near := map[string]bool{}
	for _, offset := range []time.Duration{-30 * time.Second, 0, 30 * time.Second} {
		code, ok := authkit.TOTPCode(secret, e.clock.Now().Add(offset))
		if !ok {
			e.t.Fatal("cannot compute a TOTP code")
		}
		near[code] = true
	}
	for candidate := 0; candidate < 1000; candidate++ {
		code := fmt.Sprintf("%06d", candidate)
		if !near[code] {
			return code
		}
	}
	e.t.Fatal("no wrong code found")
	return ""
}

func (e *env) signIn(name, password, code, remote string) *httptest.ResponseRecorder {
	return e.do(req{method: http.MethodPost, path: "/v1/owner/login", body: map[string]string{"name": name, "password": password, "code": code}, remote: remote})
}

func address(index int) string {
	return fmt.Sprintf("198.51.100.%d:4000", 10+index)
}

func expectSameAnswer(t *testing.T, got, want *httptest.ResponseRecorder) {
	t.Helper()
	if got.Code != want.Code || got.Body.String() != want.Body.String() {
		t.Fatalf("answers differ: %d %s and %d %s", got.Code, got.Body.String(), want.Code, want.Body.String())
	}
	for _, header := range []string{"Content-Type", "Retry-After", "Set-Cookie", "Cache-Control"} {
		if got.Header().Get(header) != want.Header().Get(header) {
			t.Fatalf("%s differs: %q and %q", header, got.Header().Get(header), want.Header().Get(header))
		}
	}
}

func countAudit(e *env, action string) int {
	e.store.mu.Lock()
	defer e.store.mu.Unlock()
	count := 0
	for _, entry := range e.store.audit {
		if entry.Action == action {
			count++
		}
	}
	return count
}

func TestRejectedCodesFromManyAddressesStopSignInForThatOwner(t *testing.T) {
	e := newEnv(t)
	o := e.createFirstOwner("Dana")
	wrongPassword := e.signIn("Dana", "a different password!", e.totp(o.secret), address(100))
	expectError(t, wrongPassword, http.StatusUnauthorized, "invalid_credentials")
	for attempt := 0; attempt < 10; attempt++ {
		rejected := e.signIn("Dana", testPassword, e.wrongCode(o.secret), address(attempt))
		expectSameAnswer(t, rejected, wrongPassword)
	}
	if got := countAudit(e, "owner.login_code_limit_reached"); got != 1 {
		t.Fatalf("limit audit entries = %d, want 1", got)
	}
	refused := e.signIn("Dana", testPassword, e.totp(o.secret), address(50))
	expectError(t, refused, http.StatusTooManyRequests, "too_many_attempts")
	if refused.Header().Get("Retry-After") == "" {
		t.Fatal("refused sign-in has no Retry-After")
	}
	expectError(t, e.signIn("dana", "a different password!", e.totp(o.secret), address(51)), http.StatusTooManyRequests, "too_many_attempts")
	if got := countAudit(e, "owner.login_code_limit_reached"); got != 1 {
		t.Fatalf("limit audit entries = %d after refusals, want 1", got)
	}

	e.clock.Advance(time.Hour)
	expectStatus(t, e.signIn("Dana", testPassword, e.totp(o.secret), address(52)), http.StatusOK)
}

func TestSuccessfulSignInDoesNotResetTheCodeCeiling(t *testing.T) {
	e := newEnv(t)
	o := e.createFirstOwner("Dana")
	for attempt := 0; attempt < 9; attempt++ {
		expectError(t, e.signIn("Dana", testPassword, e.wrongCode(o.secret), address(attempt)), http.StatusUnauthorized, "invalid_credentials")
	}
	expectStatus(t, e.signIn("Dana", testPassword, e.totp(o.secret), address(20)), http.StatusOK)
	expectError(t, e.signIn("Dana", testPassword, e.wrongCode(o.secret), address(21)), http.StatusUnauthorized, "invalid_credentials")
	expectError(t, e.signIn("Dana", testPassword, e.totp(o.secret), address(22)), http.StatusTooManyRequests, "too_many_attempts")
}

func TestReplayedCodesCountTowardTheCodeCeiling(t *testing.T) {
	e := newEnv(t)
	o := e.createFirstOwner("Dana")
	code := e.totp(o.secret)
	expectStatus(t, e.signIn("Dana", testPassword, code, address(0)), http.StatusOK)
	for attempt := 1; attempt <= 10; attempt++ {
		expectError(t, e.signIn("Dana", testPassword, code, address(attempt)), http.StatusUnauthorized, "invalid_credentials")
	}
	e.clock.Advance(31 * time.Second)
	expectError(t, e.signIn("Dana", testPassword, e.totp(o.secret), address(30)), http.StatusTooManyRequests, "too_many_attempts")
}

func TestWrongPasswordsNeverReachTheCodeCeiling(t *testing.T) {
	e := newEnv(t)
	o := e.createFirstOwner("Dana")
	for attempt := 0; attempt < 40; attempt++ {
		code := e.totp(o.secret)
		if attempt%2 == 1 {
			code = e.wrongCode(o.secret)
		}
		expectError(t, e.signIn("Dana", "a different password!", code, address(attempt)), http.StatusUnauthorized, "invalid_credentials")
	}
	if got := countAudit(e, "owner.login_code_limit_reached"); got != 0 {
		t.Fatalf("wrong passwords produced %d limit audit entries", got)
	}
	expectStatus(t, e.signIn("Dana", testPassword, e.totp(o.secret), address(60)), http.StatusOK)
}

func TestCodeCeilingSurvivesARestartOnTheSameStore(t *testing.T) {
	e := newSQLiteEnv(t)
	issue, err := e.setupStore.StartFirstSetup(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	o := e.completeSetup(issue.Token, "Dana")
	wrongPassword := e.signIn("Dana", "a different password!", e.totp(o.secret), address(100))
	for attempt := 0; attempt < 10; attempt++ {
		expectSameAnswer(t, e.signIn("Dana", testPassword, e.wrongCode(o.secret), address(attempt)), wrongPassword)
	}

	e.handler = server.New(e.cfg)
	refused := e.signIn("Dana", testPassword, e.totp(o.secret), address(50))
	expectError(t, refused, http.StatusTooManyRequests, "too_many_attempts")
	if refused.Header().Get("Retry-After") == "" {
		t.Fatal("refused sign-in has no Retry-After")
	}
	page, err := e.cfg.Store.ListAudit(t.Context(), 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	reached := 0
	for _, entry := range page.Entries {
		if entry.Action == "owner.login_code_limit_reached" {
			reached++
			if entry.Target != o.id {
				t.Fatalf("audit target %q, want %q", entry.Target, o.id)
			}
		}
	}
	if reached != 1 {
		t.Fatalf("limit audit entries = %d, want 1", reached)
	}

	e.clock.Advance(time.Hour)
	e.handler = server.New(e.cfg)
	expectStatus(t, e.signIn("Dana", testPassword, e.totp(o.secret), address(51)), http.StatusOK)
}

func TestConcurrentRejectedCodesCannotPassTheCeiling(t *testing.T) {
	e := newEnv(t)
	o := e.createFirstOwner("Dana")
	code := e.wrongCode(o.secret)
	const attempts = 40
	answers := make(chan int, attempts)
	for attempt := 0; attempt < attempts; attempt++ {
		go func() {
			answers <- e.signIn("Dana", testPassword, code, address(attempt)).Code
		}()
	}
	rejected, refused := 0, 0
	for attempt := 0; attempt < attempts; attempt++ {
		switch <-answers {
		case http.StatusUnauthorized:
			rejected++
		case http.StatusTooManyRequests:
			refused++
		}
	}
	if rejected != 10 || refused != attempts-10 {
		t.Fatalf("%d codes were checked and %d refused, want 10 and %d", rejected, refused, attempts-10)
	}
}
