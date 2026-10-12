package server_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"usesesame.app/backend/internal/selfhost"
	"usesesame.app/backend/internal/selfhost/config"
	"usesesame.app/backend/internal/selfhost/server"
)

func TestHostGuardRefusesAuthRoutesForOtherHosts(t *testing.T) {
	e := newEnv(t)
	o := e.createFirstOwner("Dana")
	token, _ := e.pairedDevice(o)
	hosts := []string{"evil.example.net", "localhost:9999", "127.0.0.1:8787", "localhost.evil.example.net:8787", "localhost:8787.evil.example.net", "localhost:8787@evil.example.net"}
	routes := append([]routeCase{
		{http.MethodPost, "/v1/owner/login", map[string]string{"name": "Dana", "password": testPassword, "code": "123456"}},
		{http.MethodPost, "/v1/owner/setup", map[string]string{"token": strings.Repeat("a", 43)}},
		{http.MethodPost, "/v1/owner/setup/details", map[string]string{"token": strings.Repeat("a", 43)}},
		{http.MethodPost, "/v1/desktop/link", map[string]string{"code": strings.Repeat("a", 43), "deviceName": "Laptop"}},
		{http.MethodGet, "/v1/desktop/status", nil},
		{http.MethodGet, "/v1/desktop/config", nil},
		{http.MethodPost, "/v1/desktop/heartbeat", map[string]any{"protocolVersion": 1}},
		{http.MethodDelete, "/v1/desktop/connection", nil},
	}, ownerRoutes...)
	for _, route := range routes {
		for index, host := range hosts {
			header := map[string]string{}
			if strings.HasPrefix(route.path, "/v1/desktop") {
				header = bearer(token)
			}
			recorder := o.do(req{method: route.method, path: route.path, body: route.body, host: host, header: header, remote: "203.0.113." + string(rune('1'+index)) + ":9"})
			expectError(t, recorder, http.StatusMisdirectedRequest, "host_mismatch")
		}
	}
	expectStatus(t, e.do(req{path: "/livez", host: "evil.example.net"}), http.StatusOK)
	expectStatus(t, e.do(req{path: "/v1/instance", host: "evil.example.net"}), http.StatusOK)
	expectStatus(t, e.do(req{path: "/v1/capabilities", host: "evil.example.net"}), http.StatusOK)
	expectStatus(t, e.do(req{path: "/", host: "evil.example.net"}), http.StatusOK)
}

func TestHostGuardAcceptsTheConfiguredHostSpellings(t *testing.T) {
	https := newEnv(t, withPublicURL("https://Sesame.Example.net"))
	https.createFirstOwner("Dana")
	for _, host := range []string{"sesame.example.net", "SESAME.example.net", "sesame.example.net:443", "sesame.example.net."} {
		recorder := https.do(req{path: "/v1/owner/session", host: host})
		expectError(t, recorder, http.StatusUnauthorized, "not_authenticated")
	}
	expectError(t, https.do(req{path: "/v1/owner/session", host: "sesame.example.net:8443"}), http.StatusMisdirectedRequest, "host_mismatch")

	loopback := newEnv(t, withPublicURL("http://[::1]:8787"))
	expectError(t, loopback.do(req{path: "/v1/owner/session", host: "[::1]:8787"}), http.StatusUnauthorized, "not_authenticated")
	expectError(t, loopback.do(req{path: "/v1/owner/session", host: "[::1]"}), http.StatusMisdirectedRequest, "host_mismatch")
}

func TestPlainHTTPIsRefusedOnNonLoopbackPublicURLs(t *testing.T) {
	e := newEnv(t)
	for _, raw := range []string{"http://sesame.example.net", "http://192.0.2.10:8787", "http://localhost.example.net"} {
		if _, err := config.ParsePublicURL(config.EnvPublicURL, raw); err == nil {
			t.Fatalf("the config package accepted %s", raw)
		}
		cfg := e.cfg
		cfg.PublicURL = config.PublicURL{Origin: raw, Host: strings.TrimPrefix(raw, "http://"), Secure: false}
		if err := cfg.Validate(); err == nil {
			t.Fatalf("Validate accepted %s", raw)
		}
		func() {
			defer func() {
				if recover() == nil {
					t.Fatalf("New built a handler for %s", raw)
				}
			}()
			server.New(cfg)
		}()
	}
	for _, mutate := range []func(*server.Config){
		func(cfg *server.Config) { cfg.Store = nil },
		func(cfg *server.Config) { cfg.SigningKey = ed25519.PrivateKey{1} },
		func(cfg *server.Config) { cfg.IPPepper = []byte("short") },
		func(cfg *server.Config) { cfg.PublicURL = config.PublicURL{} },
		func(cfg *server.Config) { cfg.PublicURL.Secure = true },
	} {
		cfg := e.cfg
		mutate(&cfg)
		if err := cfg.Validate(); err == nil {
			t.Fatal("an invalid config passed validation")
		}
	}
	if err := e.cfg.Validate(); err != nil {
		t.Fatalf("a valid config failed: %v", err)
	}
}

func TestSettingsCannotMoveTheServerToAnUnsafeOrOtherAddress(t *testing.T) {
	e := newEnv(t)
	o := e.createFirstOwner("Dana")
	o.stepUp()
	for url, code := range map[string]string{
		"http://sesame.example.net":  "invalid_public_url",
		"ftp://sesame.example.net":   "invalid_public_url",
		"https://sesame.example.net": "public_url_mismatch",
		"https://u:p@localhost:8787": "invalid_public_url",
	} {
		expectError(t, o.do(req{method: http.MethodPatch, path: "/v1/owner/settings", body: map[string]string{"publicUrl": url}}), http.StatusBadRequest, code)
	}
	ok := o.do(req{method: http.MethodPatch, path: "/v1/owner/settings", body: map[string]string{"name": "Family server", "publicUrl": "http://localhost:8787"}})
	expectStatus(t, ok, http.StatusOK)
	body := decodeBody(t, ok)
	if body["name"] != "Family server" || body["publicUrl"] != "http://localhost:8787" {
		t.Fatalf("settings = %v", body)
	}
	expectError(t, o.do(req{method: http.MethodPatch, path: "/v1/owner/settings", body: map[string]string{}}), http.StatusBadRequest, "invalid_request")
	expectError(t, o.do(req{method: http.MethodPatch, path: "/v1/owner/settings", body: map[string]string{"name": "\x07bell"}}), http.StatusBadRequest, "invalid_name")
	if decodeBody(t, o.do(req{path: "/v1/owner/settings"}))["name"] != "Family server" {
		t.Fatal("settings did not persist")
	}
}

func TestBodyLimitsAndStrictJSON(t *testing.T) {
	e := newEnv(t)
	o := e.createFirstOwner("Dana")
	token, _ := e.pairedDevice(o)
	big := `{"name":"` + strings.Repeat("x", 20*1024) + `"}`
	cases := []struct {
		name   string
		r      req
		status int
		code   string
	}{
		{"oversized owner body", req{method: http.MethodPost, path: "/v1/owner/members", raw: big}, http.StatusRequestEntityTooLarge, "request_too_large"},
		{"oversized without length", req{method: http.MethodPost, path: "/v1/owner/members", raw: big, header: map[string]string{"Content-Length": "-1"}}, http.StatusRequestEntityTooLarge, "request_too_large"},
		{"oversized login", req{method: http.MethodPost, path: "/v1/owner/login", raw: big}, http.StatusRequestEntityTooLarge, "request_too_large"},
		{"oversized desktop link", req{method: http.MethodPost, path: "/v1/desktop/link", raw: big}, http.StatusRequestEntityTooLarge, "request_too_large"},
		{"oversized heartbeat", req{method: http.MethodPost, path: "/v1/desktop/heartbeat", raw: big, header: bearer(token)}, http.StatusRequestEntityTooLarge, "request_too_large"},
		{"malformed json", req{method: http.MethodPost, path: "/v1/owner/members", raw: `{"name":`}, http.StatusBadRequest, "invalid_request"},
		{"not json", req{method: http.MethodPost, path: "/v1/owner/members", raw: `name=Ana`}, http.StatusBadRequest, "invalid_request"},
		{"empty body", req{method: http.MethodPost, path: "/v1/owner/members", raw: ``, noType: false, body: nil}, http.StatusUnsupportedMediaType, "json_required"},
		{"unknown field", req{method: http.MethodPost, path: "/v1/owner/members", raw: `{"name":"Ana","admin":true}`}, http.StatusBadRequest, "invalid_request"},
		{"trailing value", req{method: http.MethodPost, path: "/v1/owner/members", raw: `{"name":"Ana"}{"name":"Bo"}`}, http.StatusBadRequest, "invalid_request"},
		{"trailing garbage", req{method: http.MethodPost, path: "/v1/owner/members", raw: `{"name":"Ana"} x`}, http.StatusBadRequest, "invalid_request"},
		{"wrong type", req{method: http.MethodPost, path: "/v1/owner/members", raw: `{"name":42}`}, http.StatusBadRequest, "invalid_request"},
		{"array body", req{method: http.MethodPost, path: "/v1/owner/members", raw: `[]`}, http.StatusBadRequest, "invalid_request"},
		{"wrong content type", req{method: http.MethodPost, path: "/v1/owner/members", raw: `{"name":"Ana"}`, header: map[string]string{"Content-Type": "text/plain"}}, http.StatusUnsupportedMediaType, "json_required"},
		{"form content type", req{method: http.MethodPost, path: "/v1/owner/members", raw: `name=Ana`, header: map[string]string{"Content-Type": "application/x-www-form-urlencoded"}}, http.StatusUnsupportedMediaType, "json_required"},
		{"desktop malformed", req{method: http.MethodPost, path: "/v1/desktop/link", raw: `{"code":`}, http.StatusBadRequest, "invalid_desktop_link"},
		{"desktop unknown field", req{method: http.MethodPost, path: "/v1/desktop/link", raw: `{"code":"x","deviceName":"y","extra":1}`}, http.StatusBadRequest, "invalid_desktop_link"},
		{"desktop wrong type", req{method: http.MethodPost, path: "/v1/desktop/link", raw: `{"code":"x","deviceName":"y"}`, header: map[string]string{"Content-Type": "text/plain"}}, http.StatusUnsupportedMediaType, "json_required"},
		{"setup malformed", req{method: http.MethodPost, path: "/v1/owner/setup", raw: `{`}, http.StatusBadRequest, "invalid_request"},
		{"blank member name", req{method: http.MethodPost, path: "/v1/owner/members", raw: `{"name":"   "}`}, http.StatusBadRequest, "invalid_name"},
	}
	for index, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := c.r
			r.remote = "203.0.113." + string(rune('1'+index%9)) + ":" + string(rune('1'+index/9)) + "000"
			if strings.HasPrefix(r.path, "/v1/owner/members") {
				r.cookies = []*http.Cookie{o.cookie}
				if r.header == nil {
					r.header = map[string]string{}
				}
				r.header["X-Sesame-CSRF"] = o.csrf
			}
			expectError(t, e.do(r), c.status, c.code)
		})
	}
	if len(e.store.members) != 0 {
		t.Fatalf("a rejected request created %d members", len(e.store.members))
	}
}

func TestMethodsAndUnknownAPIPaths(t *testing.T) {
	e := newEnv(t)
	put := e.do(req{method: http.MethodPut, path: "/v1/owner/login"})
	expectError(t, put, http.StatusMethodNotAllowed, "method_not_allowed")
	if put.Header().Get("Allow") != "POST" {
		t.Fatalf("Allow = %q", put.Header().Get("Allow"))
	}
	expectError(t, e.do(req{method: http.MethodPost, path: "/livez"}), http.StatusMethodNotAllowed, "method_not_allowed")
	expectError(t, e.do(req{path: "/v1/owner/nothing"}), http.StatusNotFound, "not_found")
	expectError(t, e.do(req{path: "/v1/desktop/updates"}), http.StatusNotFound, "not_found")
	expectError(t, e.do(req{path: "/v1/admin/overview"}), http.StatusNotFound, "not_found")
	expectError(t, e.do(req{path: "/v1/"}), http.StatusNotFound, "not_found")
	expectError(t, e.do(req{method: http.MethodOptions, path: "/v1/owner/login"}), http.StatusMethodNotAllowed, "method_not_allowed")
	preflight := e.do(req{method: http.MethodOptions, path: "/v1/owner/members", header: map[string]string{"Origin": "https://evil.example.net", "Access-Control-Request-Method": "POST"}})
	if preflight.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("the API must not grant cross-origin access")
	}
}

func TestEveryResponseCarriesTheAPIHeadersAndARequestID(t *testing.T) {
	e := newEnv(t)
	for _, path := range []string{"/livez", "/v1/instance", "/v1/owner/session", "/v1/nope", "/config.json"} {
		recorder := e.do(req{path: path})
		header := recorder.Header()
		if header.Get("Content-Security-Policy") != "default-src 'none'; frame-ancestors 'none'" || header.Get("Cache-Control") != "no-store" ||
			header.Get("X-Content-Type-Options") != "nosniff" || header.Get("Referrer-Policy") != "no-referrer" || header.Get("X-Frame-Options") != "DENY" {
			t.Fatalf("%s headers = %v", path, header)
		}
		if header.Get("Strict-Transport-Security") != "" {
			t.Fatalf("%s sends HSTS over plain http", path)
		}
		if len(header.Get("X-Request-ID")) < 16 {
			t.Fatalf("%s has no request id", path)
		}
	}
	kept := e.do(req{path: "/livez", header: map[string]string{"X-Request-ID": "client-request-0001"}})
	if kept.Header().Get("X-Request-ID") != "client-request-0001" {
		t.Fatal("a valid request id was not kept")
	}
	for _, bad := range []string{"short", "has spaces in it 123456", strings.Repeat("a", 65), "bad\u00e9chars-0123456789"} {
		replaced := e.do(req{path: "/livez", header: map[string]string{"X-Request-ID": bad}})
		if got := replaced.Header().Get("X-Request-ID"); got == bad || len(got) < 16 {
			t.Fatalf("request id %q was not replaced (%q)", bad, got)
		}
	}
}

type captureHandler struct {
	mu      *sync.Mutex
	records *[]string
}

func (h captureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h captureHandler) Handle(_ context.Context, record slog.Record) error {
	var line strings.Builder
	line.WriteString(record.Message)
	record.Attrs(func(attr slog.Attr) bool {
		line.WriteString(" " + attr.Key + "=" + attr.Value.String())
		return true
	})
	h.mu.Lock()
	*h.records = append(*h.records, line.String())
	h.mu.Unlock()
	return nil
}
func (h captureHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return captureHandler{mu: h.mu, records: h.records}.withAttrs(attrs)
}
func (h captureHandler) withAttrs(attrs []slog.Attr) slog.Handler {
	return attrHandler{base: h, attrs: attrs}
}
func (h captureHandler) WithGroup(string) slog.Handler { return h }

type attrHandler struct {
	base  captureHandler
	attrs []slog.Attr
}

func (h attrHandler) Enabled(ctx context.Context, level slog.Level) bool { return true }
func (h attrHandler) Handle(ctx context.Context, record slog.Record) error {
	record.AddAttrs(h.attrs...)
	return h.base.Handle(ctx, record)
}
func (h attrHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return attrHandler{base: h.base, attrs: append(append([]slog.Attr{}, h.attrs...), attrs...)}
}
func (h attrHandler) WithGroup(string) slog.Handler { return h }

func TestAccessLogHasRequestIDsAndNoSecrets(t *testing.T) {
	var mu sync.Mutex
	var records []string
	previous := slog.Default()
	slog.SetDefault(slog.New(captureHandler{mu: &mu, records: &records}))
	defer slog.SetDefault(previous)

	e := newEnv(t)
	o := e.createFirstOwner("Dana")
	e.do(req{path: "/livez", header: map[string]string{"X-Request-ID": "trace-request-000001"}})
	o.do(req{path: "/v1/owner/audit?cursor=987654&limit=5"})
	e.do(req{method: http.MethodPost, path: "/v1/owner/login", body: map[string]string{"name": "Dana", "password": "very secret password value", "code": "000000"}})
	e.do(req{path: "/some/console/page?x=secretquery"})

	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(records, "\n")
	for _, secret := range []string{"987654", "very secret password value", o.cookie.Value, o.csrf, "secretquery", "198.51.100.7"} {
		if strings.Contains(joined, secret) {
			t.Fatalf("access log leaks %q:\n%s", secret, joined)
		}
	}
	for _, want := range []string{"requestId=trace-request-000001", "route=GET /v1/owner/audit", "route=POST /v1/owner/login", "route=static", "status=200", "status=401", "durationMs="} {
		if !strings.Contains(joined, want) {
			t.Fatalf("access log has no %q:\n%s", want, joined)
		}
	}
}

func TestMetricsAreOffByDefaultAndPrivateWhenOn(t *testing.T) {
	off := newEnv(t)
	expectError(t, off.do(req{path: "/metrics", remote: "127.0.0.1:5000"}), http.StatusNotFound, "not_found")

	on := newEnv(t, withMetrics())
	on.createFirstOwner("Dana")
	on.do(req{path: "/livez"})
	scrape := on.do(req{path: "/metrics", remote: "10.1.2.3:5000"})
	expectStatus(t, scrape, http.StatusOK)
	text := scrape.Body.String()
	for _, want := range []string{"sesame_build_info{version=\"9.9.9-test\"} 1", "sesame_http_requests_total{route=\"GET /livez\",method=\"GET\",status=\"200\"} 1", "route=\"POST /v1/owner/setup\""} {
		if !strings.Contains(text, want) {
			t.Fatalf("metrics lack %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Dana") {
		t.Fatal("metrics leak a name")
	}
	expectError(t, on.do(req{path: "/metrics", remote: "203.0.113.5:5000"}), http.StatusForbidden, "metrics_forbidden")
	expectError(t, on.do(req{path: "/metrics", remote: "127.0.0.1:5000", header: map[string]string{"X-Forwarded-For": "203.0.113.5"}}), http.StatusForbidden, "metrics_forbidden")
	expectError(t, on.do(req{path: "/metrics", remote: "127.0.0.1:5000", header: map[string]string{"Forwarded": "for=203.0.113.5"}}), http.StatusForbidden, "metrics_forbidden")
}

func TestConsoleIsServedWithItsOwnContentSecurityPolicy(t *testing.T) {
	e := newEnv(t, withPublicURL("https://sesame.example.net"))
	const csp = "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; font-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"
	for _, path := range []string{"/", "/index.html", "/setup", "/pair", "/devices/anything/deep", "/assets/app.js", "/assets/app.css", "/favicon.svg", "/assets/dir"} {
		recorder := e.do(req{path: path})
		expectStatus(t, recorder, http.StatusOK)
		header := recorder.Header()
		if header.Get("Content-Security-Policy") != csp {
			t.Fatalf("%s CSP = %q", path, header.Get("Content-Security-Policy"))
		}
		if header.Get("X-Content-Type-Options") != "nosniff" || header.Get("X-Frame-Options") != "DENY" || header.Get("Referrer-Policy") != "no-referrer" || header.Get("Strict-Transport-Security") == "" {
			t.Fatalf("%s headers = %v", path, header)
		}
		if len(header.Values("Content-Security-Policy")) != 1 {
			t.Fatalf("%s sends several CSP headers: %v", path, header.Values("Content-Security-Policy"))
		}
	}
	index := e.do(req{path: "/setup"})
	if !strings.Contains(index.Body.String(), "<title>console</title>") || index.Header().Get("Cache-Control") != "no-cache" || !strings.HasPrefix(index.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("fallback response = %v %q", index.Header(), index.Body.String())
	}
	asset := e.do(req{path: "/assets/app.js"})
	if asset.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" || !strings.HasPrefix(asset.Header().Get("Content-Type"), "text/javascript") {
		t.Fatalf("asset headers = %v", asset.Header())
	}
	if e.do(req{path: "/favicon.svg"}).Header().Get("Content-Type") != "image/svg+xml" {
		t.Fatal("svg content type")
	}
	head := e.do(req{method: http.MethodHead, path: "/assets/app.js"})
	if head.Code != http.StatusOK || head.Body.Len() != 0 {
		t.Fatalf("HEAD = %d %q", head.Code, head.Body.String())
	}
}

func TestTrailingSlashConsoleRoutesRedirectToTheBarePath(t *testing.T) {
	e := newEnv(t, withPublicURL("https://sesame.example.net"))
	cases := map[string]string{
		"/setup/":                     "/setup",
		"/pair/":                      "/pair",
		"/pair/?code=ab12&x=y%20z":    "/pair?code=ab12&x=y%20z",
		"/devices/anything/deep/":     "/devices/anything/deep",
		"/assets/":                    "/assets",
		"/setup/?":                    "/setup",
		"/a%2Fb/":                     "/a%2Fb",
		"/%2e%2e/":                    "/%2e%2e",
		"/..%2fsecret/":               "/..%2fsecret",
		"/%5C%5Cexample.net/":         "/%5C%5Cexample.net",
		"/%2F%2Fexample.net/":         "/%2F%2Fexample.net",
		"/setup/?next=//example.net/": "/setup?next=//example.net/",
	}
	for requested, want := range cases {
		recorder := e.do(req{path: requested})
		if recorder.Code != http.StatusPermanentRedirect {
			t.Fatalf("%s -> %d %q", requested, recorder.Code, recorder.Body.String())
		}
		if got := recorder.Header().Get("Location"); got != want {
			t.Fatalf("%s redirected to %q, want %q", requested, got, want)
		}
		head := e.do(req{method: http.MethodHead, path: requested})
		if head.Code != http.StatusPermanentRedirect || head.Header().Get("Location") != want || head.Body.Len() != 0 {
			t.Fatalf("HEAD %s -> %d %q %q", requested, head.Code, head.Header().Get("Location"), head.Body.String())
		}
	}
	for _, bare := range []string{"/setup", "/pair?code=ab12", "/"} {
		expectStatus(t, e.do(req{path: bare}), http.StatusOK)
	}
}

func TestTrailingSlashRedirectsNeverLeaveTheOrigin(t *testing.T) {
	e := newEnv(t, withPublicURL("https://sesame.example.net"))
	for _, requested := range []string{
		"//example.net/", "//example.net//", "///example.net/", "////", "//", "/\\example.net/", "/\\\\example.net/",
		"/setup//example.net/", "//example.net/setup/", "/../", "/./", "/setup/../", "/assets/../../", "/%2e%2e/%2e%2e/", "/%00/", "/%0d%0aLocation:%20https:%2f%2fexample.net/",
		"/http://example.net/", "/https:/example.net/", "/@example.net/", "/;@example.net/",
	} {
		recorder := e.do(req{path: requested})
		location := recorder.Header().Get("Location")
		if location == "" {
			continue
		}
		parsed, err := url.Parse(location)
		if err != nil {
			t.Fatalf("%s -> unparseable Location %q", requested, location)
		}
		if parsed.Scheme != "" || parsed.Host != "" || !strings.HasPrefix(location, "/") || strings.HasPrefix(location, "//") || strings.ContainsAny(location, "\\\r\n") {
			t.Fatalf("%s -> %d redirects off origin to %q", requested, recorder.Code, location)
		}
		if recorder.Code != http.StatusPermanentRedirect && recorder.Code != http.StatusMovedPermanently && recorder.Code != http.StatusTemporaryRedirect {
			t.Fatalf("%s -> %d with Location %q", requested, recorder.Code, location)
		}
	}
}

func TestTrailingSlashRedirectLeavesNonStaticRoutesAlone(t *testing.T) {
	e := newEnv(t)
	expectError(t, e.do(req{path: "/v1/owner/unknown/"}), http.StatusNotFound, "not_found")
	expectError(t, e.do(req{method: http.MethodPost, path: "/setup/"}), http.StatusMethodNotAllowed, "method_not_allowed")
}

func TestConsoleFallbackNeverShadowsTheAPI(t *testing.T) {
	e := newEnv(t)
	api := e.do(req{path: "/v1/owner/unknown"})
	expectError(t, api, http.StatusNotFound, "not_found")
	if api.Header().Get("Content-Security-Policy") != "default-src 'none'; frame-ancestors 'none'" {
		t.Fatalf("API 404 CSP = %q", api.Header().Get("Content-Security-Policy"))
	}
	expectError(t, e.do(req{path: "/assets/missing.js"}), http.StatusNotFound, "not_found")
	expectError(t, e.do(req{path: "/robots.txt"}), http.StatusNotFound, "not_found")
	expectError(t, e.do(req{method: http.MethodPost, path: "/devices"}), http.StatusMethodNotAllowed, "method_not_allowed")
	expectError(t, e.do(req{method: http.MethodDelete, path: "/"}), http.StatusMethodNotAllowed, "method_not_allowed")
	for _, path := range []string{"/../etc/passwd", "/assets/../../secret", "/%2e%2e/secret", "/assets/%2e%2e/%2e%2e/secret", "/..%2fsecret", "/%00", "/assets\\app.js"} {
		recorder := e.do(req{path: path})
		if recorder.Code == http.StatusMovedPermanently || recorder.Code == http.StatusTemporaryRedirect {
			continue
		}
		if strings.Contains(recorder.Body.String(), "secret") || (recorder.Code != http.StatusOK && recorder.Code != http.StatusNotFound && recorder.Code != http.StatusMovedPermanently && recorder.Code != http.StatusTemporaryRedirect && recorder.Code != http.StatusBadRequest) {
			t.Fatalf("%s -> %d %q", path, recorder.Code, recorder.Body.String())
		}
	}
}

func TestMissingConsoleIsReportedAsAnError(t *testing.T) {
	e := newEnv(t, func(cfg *server.Config) { cfg.Console = fstest.MapFS{} })
	expectError(t, e.do(req{path: "/"}), http.StatusNotFound, "console_missing")
}

func TestEmbeddedConsoleServesByDefault(t *testing.T) {
	e := newEnv(t, func(cfg *server.Config) { cfg.Console = nil })
	recorder := e.do(req{path: "/"})
	expectStatus(t, recorder, http.StatusOK)
	if !bytes.Contains(recorder.Body.Bytes(), []byte("Sesame")) {
		t.Fatalf("embedded console body = %q", recorder.Body.String())
	}
}

func verifyCounts(e *env) (int, int) {
	e.store.mu.Lock()
	defer e.store.mu.Unlock()
	return e.store.fullVerifies, e.store.incrementalVerifies
}

func TestAuditAndSystemUseTheIncrementalCheckUnlessAFullOneIsAsked(t *testing.T) {
	e := newEnv(t)
	o := e.createFirstOwner("Dana")
	baseFull, baseIncremental := verifyCounts(e)
	for _, path := range []string{"/v1/owner/audit", "/v1/owner/system", "/v1/owner/audit?full=false", "/v1/owner/system?full=0"} {
		expectStatus(t, o.do(req{path: path}), http.StatusOK)
	}
	full, incremental := verifyCounts(e)
	if full != baseFull || incremental != baseIncremental+4 {
		t.Fatalf("default calls ran %d full and %d incremental checks", full-baseFull, incremental-baseIncremental)
	}
	for _, path := range []string{"/v1/owner/audit?full=true", "/v1/owner/system?full=1"} {
		expectStatus(t, o.do(req{path: path}), http.StatusOK)
	}
	full, incremental = verifyCounts(e)
	if full != baseFull+2 || incremental != baseIncremental+4 {
		t.Fatalf("full calls ran %d full and %d incremental checks", full-baseFull, incremental-baseIncremental)
	}
	for _, path := range []string{"/v1/owner/audit?full=yes", "/v1/owner/system?full=", "/v1/owner/system?full=2", "/v1/owner/audit?full=TRUE"} {
		recorder := o.do(req{path: path})
		if path == "/v1/owner/system?full=" {
			expectStatus(t, recorder, http.StatusOK)
			continue
		}
		expectError(t, recorder, http.StatusBadRequest, "invalid_full")
	}
}

func TestSystemWarningsAndOwnerReadViews(t *testing.T) {
	e := newEnv(t)
	o := e.createFirstOwner("Dana")
	system := decodeBody(t, o.do(req{path: "/v1/owner/system"}))
	if system["version"] != "9.9.9-test" || system["auditChainOk"] != true || system["schemaVersion"] != float64(1) || system["databaseBytes"] != float64(4096) {
		t.Fatalf("system = %v", system)
	}
	warnings := system["warnings"].([]any)
	joined := ""
	for _, warning := range warnings {
		joined += warning.(string) + "\n"
	}
	if !strings.Contains(joined, "No backup has been made yet") || !strings.Contains(joined, "plain http") {
		t.Fatalf("warnings = %q", joined)
	}

	e.do(req{path: "/v1/owner/session", remote: "10.0.0.8:1234", header: map[string]string{"X-Forwarded-For": "203.0.113.4"}})
	e.do(req{method: http.MethodPost, path: "/v1/owner/login", remote: "10.0.0.8:1234", body: map[string]string{"name": "x", "password": "wrong password here", "code": "000000"}, header: map[string]string{"X-Forwarded-For": "203.0.113.4"}})
	system = decodeBody(t, o.do(req{path: "/v1/owner/system"}))
	if !strings.Contains(strings.Join(anyStrings(system["warnings"]), "\n"), "SESAME_TRUSTED_PROXIES") {
		t.Fatalf("no trusted proxy warning in %v", system["warnings"])
	}

	flags := decodeBody(t, o.do(req{path: "/v1/owner/flags"}))["flags"].([]any)
	if len(flags) != 1 || flags[0].(map[string]any)["key"] != "desktop_linking_enabled" {
		t.Fatalf("flags = %v", flags)
	}
	expectError(t, o.do(req{method: http.MethodPatch, path: "/v1/owner/flags/unknown.flag", body: map[string]bool{"enabled": true}}), http.StatusNotFound, "not_found")
	expectError(t, o.do(req{method: http.MethodPatch, path: "/v1/owner/flags/desktop_linking_enabled", body: map[string]string{}}), http.StatusBadRequest, "invalid_request")
	expectError(t, o.do(req{method: http.MethodPatch, path: "/v1/owner/flags/Bad%20Key", body: map[string]bool{"enabled": true}}), http.StatusNotFound, "not_found")
}

func anyStrings(value any) []string {
	var out []string
	for _, item := range value.([]any) {
		out = append(out, item.(string))
	}
	return out
}

func TestAuditListingPagesAndRejectsBadCursors(t *testing.T) {
	e := newEnv(t)
	o := e.createFirstOwner("Dana")
	for index := 0; index < 5; index++ {
		o.pairing(true, "")
	}
	first := decodeBody(t, o.do(req{path: "/v1/owner/audit?limit=3"}))
	entries := first["entries"].([]any)
	if len(entries) != 3 || first["nextCursor"] == float64(0) {
		t.Fatalf("first page = %v", first)
	}
	chain := first["chain"].(map[string]any)
	if chain["ok"] != true || chain["headHash"] == "" {
		t.Fatalf("chain = %v", chain)
	}
	newest := entries[0].(map[string]any)["seq"].(float64)
	if newest <= entries[2].(map[string]any)["seq"].(float64) {
		t.Fatal("audit entries must be newest first")
	}
	cursor := first["nextCursor"].(float64)
	next := decodeBody(t, o.do(req{path: "/v1/owner/audit?limit=3&cursor=" + formatNumber(cursor)}))
	if next["entries"].([]any)[0].(map[string]any)["seq"].(float64) >= cursor {
		t.Fatalf("second page overlaps the first: %v", next)
	}
	for _, query := range []string{"cursor=-1", "cursor=abc", "limit=0", "limit=201", "limit=x"} {
		expectStatus(t, o.do(req{path: "/v1/owner/audit?" + query}), http.StatusBadRequest)
	}
}

func formatNumber(value float64) string { return strconv.FormatInt(int64(value), 10) }

func TestExportNeedsStepUpAndListsMembersDevicesAndAudit(t *testing.T) {
	e := newEnv(t)
	o := e.createFirstOwner("Dana")
	member := o.do(req{method: http.MethodPost, path: "/v1/owner/members", body: map[string]string{"name": "Ana"}})
	expectStatus(t, member, http.StatusCreated)
	e.pairedDevice(o)
	e.clock.Advance(11 * time.Minute)
	expectError(t, o.do(req{method: http.MethodPost, path: "/v1/owner/export"}), http.StatusForbidden, "step_up_required")
	o.stepUp()
	recorder := o.do(req{method: http.MethodPost, path: "/v1/owner/export"})
	expectStatus(t, recorder, http.StatusOK)
	if !strings.HasPrefix(recorder.Header().Get("Content-Disposition"), "attachment; filename=\"sesame-export-") {
		t.Fatalf("Content-Disposition = %q", recorder.Header().Get("Content-Disposition"))
	}
	body := decodeBody(t, recorder)
	if body["format"] != "sesame-selfhost-export-v1" || len(body["members"].([]any)) != 1 || len(body["devices"].([]any)) != 1 {
		t.Fatalf("export = %v", body)
	}
	audit := body["audit"].(map[string]any)
	entries := audit["entries"].([]any)
	if len(entries) < 4 || entries[0].(map[string]any)["seq"].(float64) >= entries[len(entries)-1].(map[string]any)["seq"].(float64) {
		t.Fatalf("audit entries are not oldest first: %v", entries)
	}
	last := entries[len(entries)-1].(map[string]any)
	if last["action"] != "export.created" {
		t.Fatalf("the export itself is not in the audit log: %v", last)
	}
	for _, secret := range []string{"accessToken", "totpSecret", "password", "csrf"} {
		if strings.Contains(recorder.Body.String(), secret) {
			t.Fatalf("export contains %q", secret)
		}
	}
}

type slowAuditStore struct {
	selfhost.Store
	delay time.Duration
}

func (s slowAuditStore) VerifyAudit(ctx context.Context) (selfhost.AuditReport, error) {
	time.Sleep(s.delay)
	return s.Store.VerifyAudit(ctx)
}

func TestExportOutlivesTheServerWriteTimeout(t *testing.T) {
	e := newEnv(t)
	o := e.createFirstOwner("Dana")
	cfg := e.cfg
	cfg.Store = slowAuditStore{Store: e.store, delay: 300 * time.Millisecond}
	live := httptest.NewUnstartedServer(server.New(cfg))
	live.Config.WriteTimeout = 150 * time.Millisecond
	live.Start()
	defer live.Close()

	request, err := http.NewRequest(http.MethodPost, live.URL+"/v1/owner/export", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = e.host
	request.Header.Set("Origin", e.origin)
	request.Header.Set("X-Sesame-CSRF", o.csrf)
	request.AddCookie(o.cookie)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("export request failed: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusOK || !strings.Contains(string(body), "sesame-selfhost-export-v1") {
		t.Fatalf("export status %d, err %v, body %q", response.StatusCode, err, body)
	}
}

func TestHTTPServerTimeoutsBoundSlowClientsButAllowExports(t *testing.T) {
	httpServer := server.NewHTTPServer(http.NotFoundHandler())
	if httpServer.ReadHeaderTimeout <= 0 || httpServer.ReadTimeout <= 0 || httpServer.WriteTimeout <= 0 || httpServer.IdleTimeout <= 0 {
		t.Fatalf("timeouts are not all set: %+v", httpServer)
	}
	if httpServer.ReadHeaderTimeout > httpServer.ReadTimeout {
		t.Fatalf("read header timeout %v is longer than read timeout %v", httpServer.ReadHeaderTimeout, httpServer.ReadTimeout)
	}
	if httpServer.MaxHeaderBytes <= 0 || httpServer.Handler == nil {
		t.Fatalf("server is not fully configured: %+v", httpServer)
	}
}
