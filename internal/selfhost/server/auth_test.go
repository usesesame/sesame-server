package server_test

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

type routeCase struct {
	method string
	path   string
	body   any
}

var ownerRoutes = []routeCase{
	{http.MethodPost, "/v1/owner/logout", nil},
	{http.MethodGet, "/v1/owner/session", nil},
	{http.MethodPost, "/v1/owner/step-up", map[string]string{"password": testPassword, "code": "123456"}},
	{http.MethodGet, "/v1/owner/owners", nil},
	{http.MethodPost, "/v1/owner/owners", map[string]string{"name": "Second"}},
	{http.MethodDelete, "/v1/owner/owners/own0001", nil},
	{http.MethodGet, "/v1/owner/members", nil},
	{http.MethodPost, "/v1/owner/members", map[string]string{"name": "Ana"}},
	{http.MethodPatch, "/v1/owner/members/mem0001", map[string]string{"name": "Ana"}},
	{http.MethodDelete, "/v1/owner/members/mem0001", nil},
	{http.MethodPost, "/v1/owner/pairings", map[string]any{"self": true}},
	{http.MethodGet, "/v1/owner/pairings", nil},
	{http.MethodDelete, "/v1/owner/pairings/pai0001", nil},
	{http.MethodGet, "/v1/owner/devices", nil},
	{http.MethodDelete, "/v1/owner/devices/dev0001", nil},
	{http.MethodGet, "/v1/owner/audit", nil},
	{http.MethodGet, "/v1/owner/settings", nil},
	{http.MethodPatch, "/v1/owner/settings", map[string]string{"name": "Home"}},
	{http.MethodGet, "/v1/owner/flags", nil},
	{http.MethodPatch, "/v1/owner/flags/desktop_linking_enabled", map[string]bool{"enabled": true}},
	{http.MethodGet, "/v1/owner/system", nil},
	{http.MethodPost, "/v1/owner/export", nil},
}

func TestEveryOwnerRouteRejectsAnonymousRequests(t *testing.T) {
	e := newEnv(t)
	for _, route := range ownerRoutes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			expectError(t, e.do(req{method: route.method, path: route.path, body: route.body}), http.StatusUnauthorized, "not_authenticated")
			forged := e.do(req{method: route.method, path: route.path, body: route.body, cookies: []*http.Cookie{{Name: "sesame_owner", Value: "forged-session-token-value-0000000000"}}, header: map[string]string{"X-Sesame-CSRF": "x"}})
			expectError(t, forged, http.StatusUnauthorized, "session_expired")
		})
	}
}

func TestUnsafeOwnerRoutesRequireTheConsoleOrigin(t *testing.T) {
	e := newEnv(t)
	o := e.createFirstOwner("Dana")
	for _, origin := range []string{"", "https://evil.example.net", "http://localhost:9999", "null"} {
		header := map[string]string{}
		if origin != "" {
			header["Origin"] = origin
		}
		recorder := o.do(req{method: http.MethodPost, path: "/v1/owner/members", body: map[string]string{"name": "Ana"}, noOrigin: true, header: header})
		expectError(t, recorder, http.StatusForbidden, "origin_not_allowed")
	}
	login := e.do(req{method: http.MethodPost, path: "/v1/owner/login", body: map[string]string{"name": "Dana", "password": testPassword, "code": "123456"}, noOrigin: true, header: map[string]string{"Origin": "https://evil.example.net"}})
	expectError(t, login, http.StatusForbidden, "origin_not_allowed")
	crossOriginRead := o.do(req{path: "/v1/owner/session", header: map[string]string{"Origin": "https://evil.example.net"}})
	expectError(t, crossOriginRead, http.StatusForbidden, "origin_not_allowed")
}

func TestWrongOrMissingCSRFIsRejectedOnEveryUnsafeOwnerRoute(t *testing.T) {
	e := newEnv(t)
	o := e.createFirstOwner("Dana")
	for _, route := range ownerRoutes {
		if route.method == http.MethodGet {
			continue
		}
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			for _, token := range []string{"", "wrong", o.csrf + "x", o.csrf[:len(o.csrf)-1]} {
				header := map[string]string{"X-Sesame-CSRF": token}
				expectError(t, o.do(req{method: route.method, path: route.path, body: route.body, header: header}), http.StatusForbidden, "invalid_csrf")
			}
		})
	}
	if o.csrf == "" {
		t.Fatal("session has no csrf token")
	}
	other := o.invite("Eli")
	crossSession := o.do(req{method: http.MethodPost, path: "/v1/owner/members", body: map[string]string{"name": "Ana"}, header: map[string]string{"X-Sesame-CSRF": other.csrf}})
	expectError(t, crossSession, http.StatusForbidden, "invalid_csrf")
}

func TestSensitiveRoutesNeedARecentSignIn(t *testing.T) {
	e := newEnv(t)
	o := e.createFirstOwner("Dana")
	member := o.do(req{method: http.MethodPost, path: "/v1/owner/members", body: map[string]string{"name": "Ana"}})
	expectStatus(t, member, http.StatusCreated)
	memberID := decodeBody(t, member)["member"].(map[string]any)["id"].(string)
	device, deviceID := e.pairedDevice(o)
	_ = device

	e.clock.Advance(11 * time.Minute)
	stale := []routeCase{
		{http.MethodPost, "/v1/owner/owners", map[string]string{"name": "Second"}},
		{http.MethodDelete, "/v1/owner/owners/" + o.id, nil},
		{http.MethodDelete, "/v1/owner/members/" + memberID, nil},
		{http.MethodDelete, "/v1/owner/devices/" + deviceID, nil},
		{http.MethodPatch, "/v1/owner/settings", map[string]string{"name": "Renamed"}},
		{http.MethodPost, "/v1/owner/export", nil},
		{http.MethodPost, "/v1/owner/pairings", map[string]any{"memberId": memberID}},
	}
	for _, route := range stale {
		expectError(t, o.do(req{method: route.method, path: route.path, body: route.body}), http.StatusForbidden, "step_up_required")
	}
	expectStatus(t, o.do(req{method: http.MethodPost, path: "/v1/owner/pairings", body: map[string]any{"self": true}}), http.StatusCreated)
	expectStatus(t, o.do(req{method: http.MethodGet, path: "/v1/owner/devices"}), http.StatusOK)

	o.stepUp()
	expectStatus(t, o.do(req{method: http.MethodPatch, path: "/v1/owner/settings", body: map[string]string{"name": "Renamed"}}), http.StatusOK)
	expectStatus(t, o.do(req{method: http.MethodPost, path: "/v1/owner/pairings", body: map[string]any{"memberId": memberID}}), http.StatusCreated)
	expectStatus(t, o.do(req{method: http.MethodDelete, path: "/v1/owner/devices/" + deviceID}), http.StatusNoContent)

	e.clock.Advance(11 * time.Minute)
	expectError(t, o.do(req{method: http.MethodDelete, path: "/v1/owner/devices/" + deviceID}), http.StatusForbidden, "step_up_required")
}

func TestExpiredSessionIsRejectedAndTheCookieCleared(t *testing.T) {
	e := newEnv(t)
	o := e.createFirstOwner("Dana")
	expectStatus(t, o.do(req{path: "/v1/owner/session"}), http.StatusOK)
	e.clock.Advance(13 * time.Hour)
	recorder := o.do(req{path: "/v1/owner/session"})
	expectError(t, recorder, http.StatusUnauthorized, "session_expired")
	cleared := false
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == "sesame_owner" && cookie.MaxAge < 0 && cookie.Value == "" {
			cleared = true
		}
	}
	if !cleared {
		t.Fatalf("expired session did not clear its cookie: %v", recorder.Result().Header["Set-Cookie"])
	}
}

func TestLogoutEndsTheSession(t *testing.T) {
	e := newEnv(t)
	o := e.createFirstOwner("Dana")
	expectStatus(t, o.do(req{method: http.MethodPost, path: "/v1/owner/logout"}), http.StatusNoContent)
	expectError(t, o.do(req{path: "/v1/owner/session"}), http.StatusUnauthorized, "session_expired")
}

func TestReplayedTOTPCodeIsRejectedAtLoginAndStepUp(t *testing.T) {
	e := newEnv(t)
	o := e.createFirstOwner("Dana")
	code := e.totp(o.secret)
	first := e.do(req{method: http.MethodPost, path: "/v1/owner/login", body: map[string]string{"name": "Dana", "password": testPassword, "code": code}})
	expectStatus(t, first, http.StatusOK)
	replay := e.do(req{method: http.MethodPost, path: "/v1/owner/login", body: map[string]string{"name": "Dana", "password": testPassword, "code": code}})
	expectError(t, replay, http.StatusUnauthorized, "invalid_credentials")

	stepCode := e.totp(o.secret)
	expectStatus(t, o.do(req{method: http.MethodPost, path: "/v1/owner/step-up", body: map[string]string{"password": testPassword, "code": stepCode}}), http.StatusOK)
	again := o.do(req{method: http.MethodPost, path: "/v1/owner/step-up", body: map[string]string{"password": testPassword, "code": stepCode}})
	expectError(t, again, http.StatusUnauthorized, "invalid_credentials")
}

func TestStepUpRejectsAWrongPasswordOrCode(t *testing.T) {
	e := newEnv(t)
	o := e.createFirstOwner("Dana")
	wrongPassword := o.do(req{method: http.MethodPost, path: "/v1/owner/step-up", body: map[string]string{"password": "not the right password", "code": e.totp(o.secret)}})
	expectError(t, wrongPassword, http.StatusUnauthorized, "invalid_credentials")
	wrongCode := o.do(req{method: http.MethodPost, path: "/v1/owner/step-up", body: map[string]string{"password": testPassword, "code": "000000"}})
	expectError(t, wrongCode, http.StatusUnauthorized, "invalid_credentials")
	malformed := o.do(req{method: http.MethodPost, path: "/v1/owner/step-up", body: map[string]string{"password": testPassword, "code": "12"}})
	expectError(t, malformed, http.StatusUnauthorized, "invalid_credentials")
}

func TestLoginGivesTheSameAnswerForUnknownNamesAndWrongCredentials(t *testing.T) {
	e := newEnv(t)
	o := e.createFirstOwner("Dana")
	attempts := []map[string]string{
		{"name": "Nobody", "password": testPassword, "code": e.totp(o.secret)},
		{"name": "Dana", "password": "a different password!", "code": e.totp(o.secret)},
		{"name": "Dana", "password": testPassword, "code": "000000"},
		{"name": "Nobody", "password": "short", "code": "abc"},
		{"name": "", "password": testPassword, "code": e.totp(o.secret)},
		{"name": strings.Repeat("x", 300), "password": testPassword, "code": e.totp(o.secret)},
	}
	var reference string
	for index, attempt := range attempts {
		remote := "198.51.100." + string(rune('1'+index)) + ":4000"
		recorder := e.do(req{method: http.MethodPost, path: "/v1/owner/login", body: attempt, remote: remote})
		expectError(t, recorder, http.StatusUnauthorized, "invalid_credentials")
		if reference == "" {
			reference = recorder.Body.String()
		}
		if recorder.Body.String() != reference {
			t.Fatalf("attempt %d body differs:\n%s\n%s", index, recorder.Body.String(), reference)
		}
		if len(recorder.Result().Cookies()) != 0 {
			t.Fatalf("attempt %d set a cookie", index)
		}
	}
}

func TestLoginIsRateLimitedPerNameAndPerAddress(t *testing.T) {
	e := newEnv(t)
	e.createFirstOwner("Dana")
	attempt := map[string]string{"name": "Dana", "password": "wrong password here", "code": "000000"}
	for index := 0; index < 5; index++ {
		expectError(t, e.do(req{method: http.MethodPost, path: "/v1/owner/login", body: attempt}), http.StatusUnauthorized, "invalid_credentials")
	}
	limited := e.do(req{method: http.MethodPost, path: "/v1/owner/login", body: attempt})
	expectError(t, limited, http.StatusTooManyRequests, "too_many_attempts")
	if limited.Header().Get("Retry-After") == "" {
		t.Fatal("rate limited response has no Retry-After")
	}
	otherAddress := e.do(req{method: http.MethodPost, path: "/v1/owner/login", body: map[string]string{"name": "Someone else", "password": "wrong password here", "code": "000000"}, remote: "203.0.113.9:1000"})
	expectError(t, otherAddress, http.StatusUnauthorized, "invalid_credentials")

	e.clock.Advance(2 * time.Minute)
	expectError(t, e.do(req{method: http.MethodPost, path: "/v1/owner/login", body: attempt}), http.StatusUnauthorized, "invalid_credentials")

	for index := 0; index < 30; index++ {
		e.do(req{method: http.MethodPost, path: "/v1/owner/login", body: map[string]string{"name": "Name " + string(rune('a'+index%26)) + string(rune('a'+index/26)), "password": "wrong password here", "code": "000000"}, remote: "203.0.113.50:1"})
	}
	byAddress := e.do(req{method: http.MethodPost, path: "/v1/owner/login", body: map[string]string{"name": "Fresh name", "password": "wrong password here", "code": "000000"}, remote: "203.0.113.50:1"})
	expectError(t, byAddress, http.StatusTooManyRequests, "too_many_attempts")
}

func TestFailedLoginsFromOtherAddressesDoNotLockTheOwnerOut(t *testing.T) {
	e := newEnv(t)
	o := e.createFirstOwner("Dana")
	attempt := map[string]string{"name": "Dana", "password": "wrong password here", "code": "000000"}
	for index := 0; index < 8; index++ {
		e.do(req{method: http.MethodPost, path: "/v1/owner/login", body: attempt, remote: "203.0.113.9:1000"})
	}
	good := map[string]string{"name": "Dana", "password": testPassword, "code": e.totp(o.secret)}
	expectStatus(t, e.do(req{method: http.MethodPost, path: "/v1/owner/login", body: good}), http.StatusOK)
}

func TestSuccessfulLoginClearsTheFailureCountForThatAddress(t *testing.T) {
	e := newEnv(t)
	o := e.createFirstOwner("Dana")
	bad := map[string]string{"name": "Dana", "password": "wrong password here", "code": "000000"}
	for round := 0; round < 3; round++ {
		for index := 0; index < 4; index++ {
			expectError(t, e.do(req{method: http.MethodPost, path: "/v1/owner/login", body: bad}), http.StatusUnauthorized, "invalid_credentials")
		}
		good := map[string]string{"name": "Dana", "password": testPassword, "code": e.totp(o.secret)}
		expectStatus(t, e.do(req{method: http.MethodPost, path: "/v1/owner/login", body: good}), http.StatusOK)
		e.clock.Advance(time.Second)
	}
}

func TestAddressLimitIsCheckedBeforeAnyPerNameRowExists(t *testing.T) {
	e := newEnv(t)
	for index := 0; index < 60; index++ {
		name := "Name " + string(rune('a'+index%26)) + string(rune('a'+index/26))
		e.do(req{method: http.MethodPost, path: "/v1/owner/login", body: map[string]string{"name": name, "password": "wrong password here", "code": "000000"}, remote: "203.0.113.50:1"})
	}
	e.store.mu.Lock()
	rows := len(e.store.limits)
	e.store.mu.Unlock()
	if rows != 21 {
		t.Fatalf("rate limit rows = %d, want 21 (one per address and 20 per name)", rows)
	}
}

func TestForwardedAddressIsTrustedOnlyFromConfiguredProxies(t *testing.T) {
	attempt := func(forwarded string) map[string]string {
		return map[string]string{"name": forwarded, "password": "wrong password here", "code": "000000"}
	}
	untrusted := newEnv(t)
	for index := 0; index < 20; index++ {
		untrusted.do(req{method: http.MethodPost, path: "/v1/owner/login", body: attempt("n" + string(rune('a'+index))), header: map[string]string{"X-Forwarded-For": "192.0.2." + string(rune('1'+index%9))}})
	}
	blocked := untrusted.do(req{method: http.MethodPost, path: "/v1/owner/login", body: attempt("extra"), header: map[string]string{"X-Forwarded-For": "192.0.2.77"}})
	expectError(t, blocked, http.StatusTooManyRequests, "too_many_attempts")

	trusted := newEnv(t, withProxies("172.30.0.0/24"))
	for index := 0; index < 20; index++ {
		trusted.do(req{method: http.MethodPost, path: "/v1/owner/login", body: attempt("n" + string(rune('a'+index))), remote: "172.30.0.2:5000", header: map[string]string{"X-Forwarded-For": "192.0.2.1"}})
	}
	sameClient := trusted.do(req{method: http.MethodPost, path: "/v1/owner/login", body: attempt("extra"), remote: "172.30.0.2:5000", header: map[string]string{"X-Forwarded-For": "192.0.2.1"}})
	expectError(t, sameClient, http.StatusTooManyRequests, "too_many_attempts")
	otherClient := trusted.do(req{method: http.MethodPost, path: "/v1/owner/login", body: attempt("extra"), remote: "172.30.0.2:5000", header: map[string]string{"X-Forwarded-For": "192.0.2.2"}})
	expectError(t, otherClient, http.StatusUnauthorized, "invalid_credentials")
	spoofed := trusted.do(req{method: http.MethodPost, path: "/v1/owner/login", body: attempt("extra"), remote: "172.30.0.2:5000", header: map[string]string{"X-Forwarded-For": "192.0.2.2, 192.0.2.1"}})
	expectError(t, spoofed, http.StatusTooManyRequests, "too_many_attempts")
}

func TestSetupTokenWorksOnce(t *testing.T) {
	e := newEnv(t)
	issue, err := e.store.StartFirstSetup(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	expectStatus(t, e.do(req{path: "/config.json"}), http.StatusOK)
	if decodeBody(t, e.do(req{path: "/config.json"}))["setupRequired"] != true {
		t.Fatal("setup should be required before the first owner exists")
	}
	o := e.completeSetup(issue.Token, "Dana")
	if o.cookie == nil {
		t.Fatal("setup created no session")
	}
	if decodeBody(t, e.do(req{path: "/config.json"}))["setupRequired"] != false {
		t.Fatal("setup should not be required after the first owner exists")
	}
	secret := o.secret
	reuse := e.do(req{method: http.MethodPost, path: "/v1/owner/setup", body: map[string]string{"token": issue.Token, "name": "Mallory", "password": testPassword, "code": e.totp(secret)}})
	expectError(t, reuse, http.StatusBadRequest, "setup_token_invalid")
	details := e.do(req{method: http.MethodPost, path: "/v1/owner/setup/details", body: map[string]string{"token": issue.Token}})
	expectError(t, details, http.StatusBadRequest, "setup_token_invalid")
}

func TestSetupRejectsBadTokensExpiredTokensAndBadInput(t *testing.T) {
	e := newEnv(t)
	issue, err := e.store.StartFirstSetup(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	details := e.do(req{method: http.MethodPost, path: "/v1/owner/setup/details", body: map[string]string{"token": issue.Token}})
	secret := decodeBody(t, details)["totpSecret"].(string)
	if !strings.HasPrefix(decodeBody(t, details)["totpUri"].(string), "otpauth://totp/") {
		t.Fatal("details have no otpauth uri")
	}
	cases := []struct {
		name string
		body map[string]string
		code string
	}{
		{"unknown token", map[string]string{"token": strings.Repeat("a", 43), "name": "Dana", "password": testPassword, "code": "123456"}, "setup_token_invalid"},
		{"short token", map[string]string{"token": "short", "name": "Dana", "password": testPassword, "code": "123456"}, "invalid_setup"},
		{"short password", map[string]string{"token": issue.Token, "name": "Dana", "password": "short", "code": "123456"}, "invalid_setup"},
		{"blank name", map[string]string{"token": issue.Token, "name": "  ", "password": testPassword, "code": "123456"}, "invalid_setup"},
		{"letters in code", map[string]string{"token": issue.Token, "name": "Dana", "password": testPassword, "code": "abcdef"}, "invalid_setup"},
		{"wrong code", map[string]string{"token": issue.Token, "name": "Dana", "password": testPassword, "code": "000000"}, "invalid_setup"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			expectError(t, e.do(req{method: http.MethodPost, path: "/v1/owner/setup", body: c.body, remote: "203.0.113." + string(rune('1'+len(c.name)%8)) + ":1"}), http.StatusBadRequest, c.code)
		})
	}
	e.clock.Advance(25 * time.Hour)
	expired := e.do(req{method: http.MethodPost, path: "/v1/owner/setup", body: map[string]string{"token": issue.Token, "name": "Dana", "password": testPassword, "code": e.totp(secret)}})
	expectError(t, expired, http.StatusBadRequest, "setup_token_invalid")
	if e.do(req{path: "/v1/owner/session"}).Code != http.StatusUnauthorized {
		t.Fatal("a rejected setup must not leave a session")
	}
}

func TestLastActiveOwnerCannotBeRemoved(t *testing.T) {
	e := newEnv(t)
	first := e.createFirstOwner("Dana")
	first.stepUp()
	expectError(t, first.do(req{method: http.MethodDelete, path: "/v1/owner/owners/" + first.id}), http.StatusConflict, "last_owner")

	invited := first.do(req{method: http.MethodPost, path: "/v1/owner/owners", body: map[string]string{"name": "Eli"}})
	expectStatus(t, invited, http.StatusCreated)
	body := decodeBody(t, invited)
	if !strings.HasPrefix(body["link"].(string), "http://localhost:8787/setup#token=") || !strings.HasSuffix(body["link"].(string), body["setupToken"].(string)) {
		t.Fatalf("setup link %v does not carry the token in the fragment", body["link"])
	}
	second := e.completeSetup(body["setupToken"].(string), "Eli")

	listed := decodeBody(t, first.do(req{path: "/v1/owner/owners"}))["owners"].([]any)
	if len(listed) != 2 {
		t.Fatalf("owners = %d, want 2", len(listed))
	}
	first.stepUp()
	expectStatus(t, first.do(req{method: http.MethodDelete, path: "/v1/owner/owners/" + second.id}), http.StatusNoContent)
	expectError(t, second.do(req{path: "/v1/owner/session"}), http.StatusUnauthorized, "session_expired")
	expectError(t, first.do(req{method: http.MethodDelete, path: "/v1/owner/owners/" + first.id}), http.StatusConflict, "last_owner")
	expectError(t, first.do(req{method: http.MethodDelete, path: "/v1/owner/owners/does-not-exist"}), http.StatusNotFound, "not_found")
	expectError(t, first.do(req{method: http.MethodDelete, path: "/v1/owner/owners/bad%20id"}), http.StatusNotFound, "not_found")
}

func TestOwnerCookieAttributesOnPlainLoopback(t *testing.T) {
	e := newEnv(t)
	o := e.createFirstOwner("Dana")
	cookie := o.cookie
	if cookie.Name != "sesame_owner" {
		t.Fatalf("cookie name = %q", cookie.Name)
	}
	if !cookie.HttpOnly || cookie.Secure || cookie.Path != "/" || cookie.Domain != "" || cookie.SameSite != http.SameSiteStrictMode || cookie.MaxAge <= 0 {
		t.Fatalf("unexpected attributes: %+v", cookie)
	}
}

func TestOwnerCookieAttributesOverHTTPS(t *testing.T) {
	e := newEnv(t, withPublicURL("https://sesame.example.net"))
	o := e.createFirstOwner("Dana")
	cookie := o.cookie
	if cookie.Name != "__Host-sesame_owner" {
		t.Fatalf("cookie name = %q", cookie.Name)
	}
	if !cookie.HttpOnly || !cookie.Secure || cookie.Path != "/" || cookie.Domain != "" || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("unexpected attributes: %+v", cookie)
	}
	insecureName := e.do(req{path: "/v1/owner/session", cookies: []*http.Cookie{{Name: "sesame_owner", Value: cookie.Value}}})
	expectError(t, insecureName, http.StatusUnauthorized, "not_authenticated")
	logout := o.do(req{method: http.MethodPost, path: "/v1/owner/logout"})
	cleared := logout.Result().Cookies()
	if len(cleared) != 1 || cleared[0].MaxAge >= 0 || !cleared[0].Secure || !cleared[0].HttpOnly || cleared[0].Path != "/" {
		t.Fatalf("logout cookie = %+v", cleared)
	}
	if got := logout.Header().Get("Strict-Transport-Security"); got != "max-age=31536000" {
		t.Fatalf("Strict-Transport-Security = %q, want max-age=31536000 without includeSubDomains", got)
	}
}
