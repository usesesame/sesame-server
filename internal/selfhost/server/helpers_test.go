package server_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"usesesame.app/backend/internal/authkit"
	"usesesame.app/backend/internal/selfhost"
	"usesesame.app/backend/internal/selfhost/config"
	"usesesame.app/backend/internal/selfhost/server"
)

const (
	testPassword = "correct horse battery staple"
	testPeer     = "198.51.100.7:4000"
)

type env struct {
	t          *testing.T
	clock      *clock
	store      *fakeStore
	setupStore interface {
		StartFirstSetup(context.Context) (selfhost.SetupIssue, error)
	}
	handler http.Handler
	cfg     server.Config
	public  ed25519.PublicKey
	origin  string
	host    string
}

type option func(*server.Config)

func withPublicURL(raw string) option {
	return func(cfg *server.Config) {
		parsed, err := config.ParsePublicURL(config.EnvPublicURL, raw)
		if err != nil {
			panic(err)
		}
		cfg.PublicURL = parsed
	}
}

func withMetrics() option { return func(cfg *server.Config) { cfg.Metrics = true } }

func withProxies(prefixes ...string) option {
	return func(cfg *server.Config) {
		for _, prefix := range prefixes {
			cfg.TrustedProxies = append(cfg.TrustedProxies, netip.MustParsePrefix(prefix))
		}
	}
}

func newEnv(t *testing.T, options ...option) *env {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pepper := make([]byte, 32)
	if _, err := rand.Read(pepper); err != nil {
		t.Fatal(err)
	}
	c := newClock()
	store := newFakeStore(c)
	parsed, err := config.ParsePublicURL(config.EnvPublicURL, "http://localhost:8787")
	if err != nil {
		t.Fatal(err)
	}
	cfg := server.Config{
		Store:      store,
		Version:    "9.9.9-test",
		Commit:     "abc1234",
		PublicURL:  parsed,
		SigningKey: private,
		IPPepper:   pepper,
		Console: fstest.MapFS{
			"index.html":          {Data: []byte("<!doctype html><title>console</title><script src=\"/assets/app.js\"></script>")},
			"assets/app.js":       {Data: []byte("console.log('app')")},
			"assets/app.css":      {Data: []byte("body{}")},
			"favicon.svg":         {Data: []byte("<svg xmlns=\"http://www.w3.org/2000/svg\"/>")},
			"assets/dir/inner.js": {Data: []byte("1")},
		},
		BackupInterval: 24 * time.Hour,
		Now:            c.Now,
	}
	for _, apply := range options {
		apply(&cfg)
	}
	return &env{t: t, clock: c, store: store, setupStore: store, handler: server.New(cfg), cfg: cfg, public: public, origin: cfg.PublicURL.Origin, host: cfg.PublicURL.Host}
}

type req struct {
	method   string
	path     string
	body     any
	raw      string
	header   map[string]string
	host     string
	remote   string
	cookies  []*http.Cookie
	noOrigin bool
	noType   bool
}

func (e *env) do(r req) *httptest.ResponseRecorder {
	e.t.Helper()
	var reader io.Reader
	switch {
	case r.raw != "":
		reader = strings.NewReader(r.raw)
	case r.body != nil:
		encoded, err := json.Marshal(r.body)
		if err != nil {
			e.t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	method := r.method
	if method == "" {
		method = http.MethodGet
	}
	request := httptest.NewRequest(method, r.path, reader)
	request.Host = e.host
	if r.host != "" {
		request.Host = r.host
	}
	request.RemoteAddr = testPeer
	if r.remote != "" {
		request.RemoteAddr = r.remote
	}
	if reader != nil && !r.noType {
		request.Header.Set("Content-Type", "application/json")
	}
	unsafe := method == http.MethodPost || method == http.MethodPatch || method == http.MethodPut || method == http.MethodDelete
	if unsafe && !r.noOrigin && strings.HasPrefix(r.path, "/v1/owner") {
		request.Header.Set("Origin", e.origin)
	}
	for key, value := range r.header {
		request.Header.Set(key, value)
	}
	for _, cookie := range r.cookies {
		request.AddCookie(cookie)
	}
	recorder := httptest.NewRecorder()
	e.handler.ServeHTTP(recorder, request)
	return recorder
}

func decodeBody(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not a JSON object: %v\n%s", err, recorder.Body.String())
	}
	return body
}

func errorCode(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	body := decodeBody(t, recorder)
	envelope, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("response has no error envelope: %s", recorder.Body.String())
	}
	if message, _ := envelope["message"].(string); message == "" {
		t.Fatalf("error envelope has no message: %s", recorder.Body.String())
	}
	code, _ := envelope["code"].(string)
	return code
}

func expectError(t *testing.T, recorder *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if recorder.Code != status {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, status, recorder.Body.String())
	}
	if got := errorCode(t, recorder); got != code {
		t.Fatalf("error code = %q, want %q: %s", got, code, recorder.Body.String())
	}
}

func expectStatus(t *testing.T, recorder *httptest.ResponseRecorder, status int) {
	t.Helper()
	if recorder.Code != status {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, status, recorder.Body.String())
	}
}

type owner struct {
	e      *env
	id     string
	name   string
	secret string
	cookie *http.Cookie
	csrf   string
}

func (e *env) totp(secret string) string {
	e.clock.Advance(31 * time.Second)
	code, ok := authkit.TOTPCode(secret, e.clock.Now())
	if !ok {
		e.t.Fatal("cannot compute a TOTP code")
	}
	return code
}

func (e *env) createFirstOwner(name string) *owner {
	e.t.Helper()
	issue, err := e.store.StartFirstSetup(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	return e.completeSetup(issue.Token, name)
}

func (e *env) completeSetup(token, name string) *owner {
	e.t.Helper()
	details := e.do(req{method: http.MethodPost, path: "/v1/owner/setup/details", body: map[string]string{"token": token}})
	expectStatus(e.t, details, http.StatusOK)
	secret, _ := decodeBody(e.t, details)["totpSecret"].(string)
	done := e.do(req{method: http.MethodPost, path: "/v1/owner/setup", body: map[string]string{"token": token, "name": name, "password": testPassword, "code": e.totp(secret)}})
	expectStatus(e.t, done, http.StatusCreated)
	result := decodeBody(e.t, done)
	ownerView, _ := result["owner"].(map[string]any)
	csrf, _ := result["csrfToken"].(string)
	id, _ := ownerView["id"].(string)
	return &owner{e: e, id: id, name: name, secret: secret, cookie: sessionCookie(e.t, done), csrf: csrf}
}

func sessionCookie(t *testing.T, recorder *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, cookie := range recorder.Result().Cookies() {
		if strings.HasSuffix(cookie.Name, "sesame_owner") && cookie.MaxAge >= 0 {
			return cookie
		}
	}
	t.Fatalf("no session cookie in %v", recorder.Result().Header["Set-Cookie"])
	return nil
}

func (o *owner) do(r req) *httptest.ResponseRecorder {
	o.e.t.Helper()
	r.cookies = append(r.cookies, o.cookie)
	if r.header == nil {
		r.header = map[string]string{}
	}
	if _, set := r.header["X-Sesame-CSRF"]; !set {
		r.header["X-Sesame-CSRF"] = o.csrf
	}
	return o.e.do(r)
}

func (o *owner) stepUp() {
	o.e.t.Helper()
	recorder := o.do(req{method: http.MethodPost, path: "/v1/owner/step-up", body: map[string]string{"password": testPassword, "code": o.e.totp(o.secret)}})
	expectStatus(o.e.t, recorder, http.StatusOK)
}

func (o *owner) pairing(self bool, memberID string) map[string]any {
	o.e.t.Helper()
	body := map[string]any{}
	if self {
		body["self"] = true
	}
	if memberID != "" {
		body["memberId"] = memberID
	}
	recorder := o.do(req{method: http.MethodPost, path: "/v1/owner/pairings", body: body})
	expectStatus(o.e.t, recorder, http.StatusCreated)
	return decodeBody(o.e.t, recorder)
}

func (e *env) link(code, name string) *httptest.ResponseRecorder {
	return e.do(req{method: http.MethodPost, path: "/v1/desktop/link", body: map[string]string{"code": code, "deviceName": name}})
}

func (e *env) pairedDevice(o *owner) (token, deviceID string) {
	e.t.Helper()
	issued := o.pairing(true, "")
	linked := e.link(issued["code"].(string), "Test laptop")
	expectStatus(e.t, linked, http.StatusCreated)
	body := decodeBody(e.t, linked)
	device, _ := body["device"].(map[string]any)
	return body["accessToken"].(string), device["deviceId"].(string)
}

func bearer(token string) map[string]string {
	return map[string]string{"Authorization": "Sesame " + token}
}

func (o *owner) invite(name string) *owner {
	o.e.t.Helper()
	o.stepUp()
	invited := o.do(req{method: http.MethodPost, path: "/v1/owner/owners", body: map[string]string{"name": name}})
	expectStatus(o.e.t, invited, http.StatusCreated)
	return o.e.completeSetup(decodeBody(o.e.t, invited)["setupToken"].(string), name)
}
