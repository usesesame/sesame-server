package server_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"usesesame.app/backend/internal/selfhost"
	"usesesame.app/backend/internal/selfhost/server"
	"usesesame.app/backend/internal/selfhost/updates"
)

const feedKeyID = "test-feed-key"

type feedHost struct {
	*httptest.Server
	t       *testing.T
	public  ed25519.PublicKey
	private ed25519.PrivateKey
	mu      sync.Mutex
	body    []byte
	hits    atomic.Int32
	life    time.Duration
}

func newFeedHost(t *testing.T) *feedHost {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	host := &feedHost{t: t, public: public, private: private}
	host.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		host.hits.Add(1)
		host.mu.Lock()
		body := host.body
		host.mu.Unlock()
		_, _ = w.Write(body)
	}))
	t.Cleanup(host.Close)
	return host
}

func (h *feedHost) lifetime() time.Duration {
	if h.life > 0 {
		return h.life
	}
	return 30 * 24 * time.Hour
}

func (h *feedHost) publish(sequence int64, version string, mutate ...func(*updates.Release)) {
	h.t.Helper()
	issued := time.Date(2026, 10, 10, 8, 0, 0, 0, time.UTC)
	release := updates.Release{
		Version:     version,
		PublishedAt: issued.Add(-time.Hour),
		NotesURL:    "https://example.net/notes/" + version,
		Security:    true,
		Images:      []updates.Image{{Ref: "registry.example.net/sesame/server@sha256:" + strings.Repeat("a", 64)}},
		Binaries:    []updates.Binary{{OS: "linux", Arch: "amd64", URL: "https://downloads.example.net/sesame-server", SHA256: strings.Repeat("b", 64)}},
	}
	for _, change := range mutate {
		change(&release)
	}
	payload := updates.Payload{
		SchemaVersion: 1, Sequence: sequence, IssuedAt: issued, ExpiresAt: issued.Add(h.lifetime()),
		Products: []updates.Product{{ID: updates.ServerProductID, Channels: map[string]updates.Release{"stable": release}}},
	}
	body, err := updates.Sign(payload, feedKeyID, h.private)
	if err != nil {
		h.t.Fatal(err)
	}
	h.mu.Lock()
	h.body = body
	h.mu.Unlock()
}

type configClock struct{ now func() time.Time }

func (c configClock) Now() time.Time                         { return c.now() }
func (c configClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

func withUpdateFeed(host *feedHost, kind updates.InstallKind) option {
	return func(cfg *server.Config) {
		service, err := updates.New(updates.Options{
			Store:       cfg.Store,
			Version:     cfg.Version,
			Keys:        updates.Keys{feedKeyID: host.public},
			FeedURL:     host.URL + "/feed.json",
			Client:      host.Client(),
			Clock:       configClock{now: cfg.Now},
			InstallKind: kind,
			Platform:    updates.Platform{OS: "linux", Arch: "amd64", Executable: "/usr/local/bin/sesame-server"},
			Jitter:      func(time.Duration) time.Duration { return 0 },
		})
		if err != nil {
			panic(err)
		}
		cfg.Updates = service
	}
}

func updatesDoc(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	expectStatus(t, recorder, http.StatusOK)
	return decodeBody(t, recorder)
}

func TestUpdateRoutesNeedASession(t *testing.T) {
	e := newEnv(t)
	for _, call := range []req{
		{path: "/v1/owner/updates"},
		{method: http.MethodPatch, path: "/v1/owner/updates", body: map[string]any{"enabled": true}},
		{method: http.MethodPost, path: "/v1/owner/updates/check"},
	} {
		expectError(t, e.do(call), http.StatusUnauthorized, "not_authenticated")
	}
}

func TestUpdateRoutesNeedCSRFOriginAndHost(t *testing.T) {
	host := newFeedHost(t)
	host.publish(5, "10.0.0")
	e := newEnv(t, withUpdateFeed(host, updates.InstallContainer))
	o := e.createFirstOwner("Dana")
	o.stepUp()
	patch := func(extra req) req {
		extra.method, extra.path, extra.body = http.MethodPatch, "/v1/owner/updates", map[string]any{"enabled": true}
		return extra
	}
	check := func(extra req) req {
		extra.method, extra.path = http.MethodPost, "/v1/owner/updates/check"
		return extra
	}
	for name, build := range map[string]func(req) req{"patch": patch, "check": check} {
		t.Run(name, func(t *testing.T) {
			expectError(t, o.do(build(req{header: map[string]string{"X-Sesame-CSRF": "wrong"}})), http.StatusForbidden, "invalid_csrf")
			expectError(t, o.do(build(req{header: map[string]string{"X-Sesame-CSRF": ""}})), http.StatusForbidden, "invalid_csrf")
			expectError(t, o.do(build(req{header: map[string]string{"Origin": "https://evil.example"}})), http.StatusForbidden, "origin_not_allowed")
			expectError(t, o.do(build(req{noOrigin: true})), http.StatusForbidden, "origin_not_allowed")
			expectError(t, o.do(build(req{host: "evil.example"})), http.StatusMisdirectedRequest, "host_mismatch")
		})
	}
	expectError(t, o.do(req{path: "/v1/owner/updates", host: "evil.example"}), http.StatusMisdirectedRequest, "host_mismatch")
	expectError(t, o.do(req{path: "/v1/owner/updates", header: map[string]string{"Origin": "https://evil.example"}}), http.StatusForbidden, "origin_not_allowed")
	if host.hits.Load() != 0 {
		t.Fatalf("refused requests still reached the feed %d times", host.hits.Load())
	}
	if settings, _ := e.store.UpdateSettings(t.Context()); settings.Choice != selfhost.UpdatesUnset {
		t.Fatalf("a refused request changed the setting: %+v", settings)
	}
}

func TestUpdateSettingsNeedStepUp(t *testing.T) {
	host := newFeedHost(t)
	host.publish(5, "10.0.0")
	e := newEnv(t, withUpdateFeed(host, updates.InstallContainer))
	o := e.createFirstOwner("Dana")
	e.clock.Advance(selfhost.RecentAuthWindow + time.Minute)
	expectError(t, o.do(req{method: http.MethodPatch, path: "/v1/owner/updates", body: map[string]any{"enabled": true}}), http.StatusForbidden, "step_up_required")
	if host.hits.Load() != 0 {
		t.Fatal("a request without step-up reached the feed")
	}
	if settings, _ := e.store.UpdateSettings(t.Context()); settings.Choice != selfhost.UpdatesUnset {
		t.Fatalf("settings = %+v", settings)
	}
	o.stepUp()
	expectStatus(t, o.do(req{method: http.MethodPatch, path: "/v1/owner/updates", body: map[string]any{"enabled": true}}), http.StatusOK)
}

func TestUpdateSettingsValidation(t *testing.T) {
	host := newFeedHost(t)
	host.publish(5, "10.0.0")
	e := newEnv(t, withUpdateFeed(host, updates.InstallContainer))
	o := e.createFirstOwner("Dana")
	o.stepUp()
	bad := map[string]req{
		"empty object":    {body: map[string]any{}},
		"null enabled":    {raw: `{"enabled":null}`},
		"string enabled":  {body: map[string]any{"enabled": "yes"}},
		"number enabled":  {body: map[string]any{"enabled": 1}},
		"unknown channel": {body: map[string]any{"channel": "nightly"}},
		"bad channel":     {body: map[string]any{"channel": "Stable!"}},
		"empty channel":   {body: map[string]any{"channel": ""}},
		"number channel":  {body: map[string]any{"channel": 3}},
		"unknown field":   {body: map[string]any{"enabled": true, "extra": 1}},
		"not json":        {raw: "enabled=true"},
		"array":           {raw: "[true]"},
		"trailing data":   {raw: `{"enabled":true} {}`},
	}
	for name, call := range bad {
		t.Run(name, func(t *testing.T) {
			call.method, call.path = http.MethodPatch, "/v1/owner/updates"
			recorder := o.do(call)
			if recorder.Code != http.StatusBadRequest || errorCode(t, recorder) != "invalid_updates" {
				t.Fatalf("status %d body %s", recorder.Code, recorder.Body.String())
			}
		})
	}
	expectError(t, o.do(req{method: http.MethodPatch, path: "/v1/owner/updates", raw: `{"enabled":true}`, noType: true}), http.StatusUnsupportedMediaType, "json_required")
	if host.hits.Load() != 0 {
		t.Fatalf("invalid requests reached the feed %d times", host.hits.Load())
	}
	if settings, _ := e.store.UpdateSettings(t.Context()); settings.Choice != selfhost.UpdatesUnset || settings.Channel != "stable" {
		t.Fatalf("settings = %+v", settings)
	}
}

func TestUpdatesDocumentWhileUnsetMakesNoRequest(t *testing.T) {
	host := newFeedHost(t)
	host.publish(5, "10.0.0")
	e := newEnv(t, withUpdateFeed(host, updates.InstallContainer))
	o := e.createFirstOwner("Dana")
	doc := updatesDoc(t, o.do(req{path: "/v1/owner/updates"}))
	if doc["configured"] != true || doc["enabled"] != nil || doc["channel"] != "stable" || doc["installKind"] != "container" || doc["latest"] != nil || doc["available"] != false || doc["checkedAt"] != nil || doc["error"] != "" {
		t.Fatalf("document = %v", doc)
	}
	current := doc["current"].(map[string]any)
	if current["product"] != "sesame-server" || current["version"] != "9.9.9-test" {
		t.Fatalf("current = %v", current)
	}
	if commands, ok := doc["commands"].([]any); !ok || len(commands) != 0 {
		t.Fatalf("commands = %v", doc["commands"])
	}
	if system := updatesDoc(t, o.do(req{path: "/v1/owner/system"})); system["updateAvailable"] != false {
		t.Fatalf("system = %v", system)
	}
	e.clock.Advance(time.Hour)
	expectError(t, o.do(req{method: http.MethodPost, path: "/v1/owner/updates/check"}), http.StatusConflict, "updates_off")
	o.stepUp()
	updatesDoc(t, o.do(req{method: http.MethodPatch, path: "/v1/owner/updates", body: map[string]any{"enabled": false}}))
	expectError(t, o.do(req{method: http.MethodPost, path: "/v1/owner/updates/check"}), http.StatusConflict, "updates_off")
	if host.hits.Load() != 0 {
		t.Fatalf("the feed was requested %d times while checks were unset or off", host.hits.Load())
	}
	if doc := updatesDoc(t, o.do(req{path: "/v1/owner/updates"})); doc["enabled"] != false || doc["latest"] != nil {
		t.Fatalf("document = %v", doc)
	}
}

func TestUpdatesNotConfiguredReportsItAndMakesNoRequest(t *testing.T) {
	e := newEnv(t)
	o := e.createFirstOwner("Dana")
	for round, setting := range []any{nil, false} {
		if setting != nil {
			o.stepUp()
			updatesDoc(t, o.do(req{method: http.MethodPatch, path: "/v1/owner/updates", body: map[string]any{"enabled": setting}}))
		}
		e.clock.Advance(time.Hour + time.Minute)
		doc := updatesDoc(t, o.do(req{method: http.MethodPost, path: "/v1/owner/updates/check"}))
		if doc["configured"] != false || doc["error"] != "not_configured" || doc["latest"] != nil {
			t.Fatalf("round %d: unconfigured check document = %v", round, doc)
		}
	}
	doc := updatesDoc(t, o.do(req{path: "/v1/owner/updates"}))
	if doc["configured"] != false || doc["error"] != "not_configured" || doc["latest"] != nil {
		t.Fatalf("document = %v", doc)
	}
	o.stepUp()
	doc = updatesDoc(t, o.do(req{method: http.MethodPatch, path: "/v1/owner/updates", body: map[string]any{"enabled": true}}))
	if doc["configured"] != false || doc["enabled"] != true || doc["error"] != "not_configured" || doc["latest"] != nil {
		t.Fatalf("document = %v", doc)
	}
	e.clock.Advance(time.Hour + time.Minute)
	doc = updatesDoc(t, o.do(req{method: http.MethodPost, path: "/v1/owner/updates/check"}))
	if doc["configured"] != false || doc["error"] != "not_configured" {
		t.Fatalf("document = %v", doc)
	}
}

func TestTurningChecksOnFetchesAndReportsTheUpdate(t *testing.T) {
	host := newFeedHost(t)
	host.publish(5, "10.0.0")
	e := newEnv(t, withUpdateFeed(host, updates.InstallContainer))
	o := e.createFirstOwner("Dana")
	o.stepUp()
	doc := updatesDoc(t, o.do(req{method: http.MethodPatch, path: "/v1/owner/updates", body: map[string]any{"enabled": true}}))
	if host.hits.Load() != 1 {
		t.Fatalf("feed requests = %d, want 1", host.hits.Load())
	}
	if doc["enabled"] != true || doc["available"] != true || doc["error"] != "" || doc["checkedAt"] == nil {
		t.Fatalf("document = %v", doc)
	}
	latest := doc["latest"].(map[string]any)
	if latest["version"] != "10.0.0" || latest["security"] != true || latest["notesUrl"] != "https://example.net/notes/10.0.0" || latest["publishedAt"] == nil {
		t.Fatalf("latest = %v", latest)
	}
	if images := latest["images"].([]any); len(images) != 1 || images[0].(map[string]any)["ref"] != "registry.example.net/sesame/server@sha256:"+strings.Repeat("a", 64) {
		t.Fatalf("images = %v", latest["images"])
	}
	if binaries := latest["binaries"].([]any); len(binaries) != 1 || binaries[0].(map[string]any)["sha256"] != strings.Repeat("b", 64) {
		t.Fatalf("binaries = %v", latest["binaries"])
	}
	commands := doc["commands"].([]any)
	first := commands[0].(map[string]any)
	if len(commands) != 5 || first["text"] != "SESAME_IMAGE_TAG=10.0.0" || first["label"] == "" || commands[1].(map[string]any)["text"] != "docker compose pull" || commands[4].(map[string]any)["text"] != "docker compose up -d" {
		t.Fatalf("commands = %v", commands)
	}
	if system := updatesDoc(t, o.do(req{path: "/v1/owner/system"})); system["updateAvailable"] != true {
		t.Fatalf("system = %v", system)
	}
	if again := updatesDoc(t, o.do(req{path: "/v1/owner/updates"})); again["available"] != true || host.hits.Load() != 1 {
		t.Fatalf("a read made a request: hits %d", host.hits.Load())
	}
	audit := decodeBody(t, o.do(req{path: "/v1/owner/audit"}))
	found := false
	for _, entry := range audit["entries"].([]any) {
		if entry.(map[string]any)["action"] == "updates.updated" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the setting change was not audited: %v", audit["entries"])
	}
}

func TestBinaryInstallsGetBinaryCommands(t *testing.T) {
	host := newFeedHost(t)
	host.publish(5, "10.0.0")
	e := newEnv(t, withUpdateFeed(host, updates.InstallBinary))
	o := e.createFirstOwner("Dana")
	o.stepUp()
	doc := updatesDoc(t, o.do(req{method: http.MethodPatch, path: "/v1/owner/updates", body: map[string]any{"enabled": true}}))
	commands := doc["commands"].([]any)
	if doc["installKind"] != "binary" || len(commands) != 4 || !strings.Contains(commands[0].(map[string]any)["text"].(string), "'https://downloads.example.net/sesame-server'") || !strings.Contains(commands[1].(map[string]any)["text"].(string), strings.Repeat("b", 64)) {
		t.Fatalf("document = %v", doc)
	}
}

func TestCheckRouteReportsRollbackAndKeepsTheLastResult(t *testing.T) {
	host := newFeedHost(t)
	host.publish(9, "10.0.0")
	e := newEnv(t, withUpdateFeed(host, updates.InstallContainer))
	o := e.createFirstOwner("Dana")
	o.stepUp()
	updatesDoc(t, o.do(req{method: http.MethodPatch, path: "/v1/owner/updates", body: map[string]any{"enabled": true}}))
	host.publish(3, "10.5.0")
	doc := updatesDoc(t, o.do(req{method: http.MethodPost, path: "/v1/owner/updates/check"}))
	if doc["error"] != "feed_rollback" || doc["latest"].(map[string]any)["version"] != "10.0.0" || doc["available"] != true {
		t.Fatalf("document = %v", doc)
	}
	host.publish(9, "10.1.0")
	doc = updatesDoc(t, o.do(req{method: http.MethodPost, path: "/v1/owner/updates/check"}))
	if doc["error"] != "" || doc["latest"].(map[string]any)["version"] != "10.1.0" {
		t.Fatalf("the same sequence was not accepted: %v", doc)
	}
}

func TestCheckRouteIsRateLimitedPerOwner(t *testing.T) {
	host := newFeedHost(t)
	host.publish(5, "10.0.0")
	e := newEnv(t, withUpdateFeed(host, updates.InstallContainer))
	first := e.createFirstOwner("Dana")
	first.stepUp()
	updatesDoc(t, first.do(req{method: http.MethodPatch, path: "/v1/owner/updates", body: map[string]any{"enabled": true}}))
	hits := host.hits.Load()
	for round := 0; round < 3; round++ {
		updatesDoc(t, first.do(req{method: http.MethodPost, path: "/v1/owner/updates/check"}))
	}
	limited := first.do(req{method: http.MethodPost, path: "/v1/owner/updates/check"})
	expectError(t, limited, http.StatusTooManyRequests, "too_many_attempts")
	if limited.Header().Get("Retry-After") == "" {
		t.Fatal("the rate limit response has no Retry-After")
	}
	if host.hits.Load() != hits+3 {
		t.Fatalf("feed requests = %d, want %d: a limited call must not reach the feed", host.hits.Load(), hits+3)
	}
	second := first.invite("Eli")
	updatesDoc(t, second.do(req{method: http.MethodPost, path: "/v1/owner/updates/check"}))
	e.clock.Advance(time.Hour + time.Minute)
	updatesDoc(t, first.do(req{method: http.MethodPost, path: "/v1/owner/updates/check"}))
}

func TestUnreachableFeedIsReportedByTheCheckRoute(t *testing.T) {
	host := newFeedHost(t)
	host.publish(5, "10.0.0")
	e := newEnv(t, withUpdateFeed(host, updates.InstallContainer))
	o := e.createFirstOwner("Dana")
	o.stepUp()
	updatesDoc(t, o.do(req{method: http.MethodPatch, path: "/v1/owner/updates", body: map[string]any{"enabled": true}}))
	host.Close()
	doc := updatesDoc(t, o.do(req{method: http.MethodPost, path: "/v1/owner/updates/check"}))
	if doc["error"] != "feed_unreachable" || doc["latest"].(map[string]any)["version"] != "10.0.0" || doc["available"] != true {
		t.Fatalf("document = %v", doc)
	}
}

func TestChannelChangeSelectsAnotherRelease(t *testing.T) {
	host := newFeedHost(t)
	host.publish(5, "10.0.0")
	e := newEnv(t, withUpdateFeed(host, updates.InstallContainer))
	o := e.createFirstOwner("Dana")
	o.stepUp()
	updatesDoc(t, o.do(req{method: http.MethodPatch, path: "/v1/owner/updates", body: map[string]any{"enabled": true}}))
	doc := updatesDoc(t, o.do(req{method: http.MethodPatch, path: "/v1/owner/updates", body: map[string]any{"channel": "beta"}}))
	if doc["channel"] != "beta" || doc["latest"] != nil || doc["available"] != false || doc["enabled"] != true {
		t.Fatalf("document = %v", doc)
	}
	if host.hits.Load() != 1 {
		t.Fatalf("a channel change fetched the feed: %d", host.hits.Load())
	}
}

func completeSetupWith(t *testing.T, e *env, extra map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	issue, err := e.setupStore.StartFirstSetup(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	details := e.do(req{method: http.MethodPost, path: "/v1/owner/setup/details", body: map[string]string{"token": issue.Token}})
	expectStatus(t, details, http.StatusOK)
	secret, _ := decodeBody(t, details)["totpSecret"].(string)
	body := map[string]any{"token": issue.Token, "name": "Dana", "password": testPassword, "code": e.totp(secret)}
	for key, value := range extra {
		body[key] = value
	}
	return e.do(req{method: http.MethodPost, path: "/v1/owner/setup", body: body})
}

func ownerFromSetup(e *env, recorder *httptest.ResponseRecorder) *owner {
	body := decodeBody(e.t, recorder)
	view, _ := body["owner"].(map[string]any)
	id, _ := view["id"].(string)
	csrf, _ := body["csrfToken"].(string)
	return &owner{e: e, id: id, name: "Dana", cookie: sessionCookie(e.t, recorder), csrf: csrf}
}

func TestSetupRecordsTheUpdateChoice(t *testing.T) {
	for name, tc := range map[string]struct {
		extra   map[string]any
		enabled any
		hits    int32
	}{
		"absent leaves it unset": {nil, nil, 0},
		"true turns it on":       {map[string]any{"updateChecks": true}, true, 1},
		"false turns it off":     {map[string]any{"updateChecks": false}, false, 0},
	} {
		t.Run(name, func(t *testing.T) {
			host := newFeedHost(t)
			host.publish(5, "10.0.0")
			e := newEnv(t, withUpdateFeed(host, updates.InstallContainer))
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan struct{})
			go func() { e.cfg.Updates.Run(ctx); close(done) }()
			defer func() { cancel(); <-done }()
			recorder := completeSetupWith(t, e, tc.extra)
			expectStatus(t, recorder, http.StatusCreated)
			o := ownerFromSetup(e, recorder)
			doc := updatesDoc(t, o.do(req{path: "/v1/owner/updates"}))
			if doc["enabled"] != tc.enabled {
				t.Fatalf("enabled = %v, want %v", doc["enabled"], tc.enabled)
			}
			if tc.hits == 1 {
				deadline := time.Now().Add(5 * time.Second)
				for host.hits.Load() < 1 && time.Now().Before(deadline) {
					time.Sleep(10 * time.Millisecond)
				}
			} else {
				time.Sleep(50 * time.Millisecond)
			}
			if host.hits.Load() != tc.hits {
				t.Fatalf("feed requests = %d, want %d", host.hits.Load(), tc.hits)
			}
			if tc.hits == 1 {
				deadline := time.Now().Add(5 * time.Second)
				for time.Now().Before(deadline) {
					if doc := updatesDoc(t, o.do(req{path: "/v1/owner/updates"})); doc["latest"] != nil {
						return
					}
					time.Sleep(10 * time.Millisecond)
				}
				t.Fatal("the check started by setup did not store a result")
			}
		})
	}
}

func TestSetupRejectsANonBooleanUpdateChoice(t *testing.T) {
	e := newEnv(t)
	for _, value := range []any{"yes", 1, "true", []bool{true}} {
		recorder := completeSetupWith(t, e, map[string]any{"updateChecks": value})
		expectError(t, recorder, http.StatusBadRequest, "invalid_request")
	}
	if required, _ := e.store.SetupRequired(t.Context()); !required {
		t.Fatal("a rejected setup request created the owner")
	}
}

func TestInvitedSetupCannotChangeTheUpdateChoice(t *testing.T) {
	host := newFeedHost(t)
	host.publish(5, "10.0.0")
	e := newEnv(t, withUpdateFeed(host, updates.InstallContainer))
	first := e.createFirstOwner("Dana")
	first.stepUp()
	invited := first.do(req{method: http.MethodPost, path: "/v1/owner/owners", body: map[string]string{"name": "Eli"}})
	expectStatus(t, invited, http.StatusCreated)
	token := decodeBody(t, invited)["setupToken"].(string)
	details := e.do(req{method: http.MethodPost, path: "/v1/owner/setup/details", body: map[string]string{"token": token}})
	secret, _ := decodeBody(t, details)["totpSecret"].(string)
	done := e.do(req{method: http.MethodPost, path: "/v1/owner/setup", body: map[string]any{"token": token, "name": "Eli", "password": testPassword, "code": e.totp(secret), "updateChecks": true}})
	expectStatus(t, done, http.StatusCreated)
	if settings, _ := e.store.UpdateSettings(t.Context()); settings.Choice != selfhost.UpdatesUnset {
		t.Fatalf("an invited owner changed the choice: %+v", settings)
	}
}

func TestUpdateRoutesAgainstTheSQLiteStore(t *testing.T) {
	host := newFeedHost(t)
	host.publish(5, "10.0.0")
	e := newSQLiteEnv(t, withUpdateFeed(host, updates.InstallBinary))
	recorder := completeSetupWith(t, e, map[string]any{"updateChecks": false})
	expectStatus(t, recorder, http.StatusCreated)
	o := ownerFromSetup(e, recorder)
	if doc := updatesDoc(t, o.do(req{path: "/v1/owner/updates"})); doc["enabled"] != false || doc["latest"] != nil {
		t.Fatalf("document = %v", doc)
	}
	if host.hits.Load() != 0 {
		t.Fatalf("feed requests = %d", host.hits.Load())
	}
	expectError(t, o.do(req{method: http.MethodPost, path: "/v1/owner/updates/check"}), http.StatusConflict, "updates_off")
	e.clock.Advance(selfhost.RecentAuthWindow + time.Minute)
	expectError(t, o.do(req{method: http.MethodPatch, path: "/v1/owner/updates", body: map[string]any{"enabled": true}}), http.StatusForbidden, "step_up_required")
	state, ok := e.setupStore.(selfhost.UpdateStore)
	if !ok {
		t.Fatal("the store does not hold update settings")
	}
	settings, err := state.UpdateSettings(t.Context())
	if err != nil || settings.Choice != selfhost.UpdatesOff {
		t.Fatalf("settings = %+v, %v", settings, err)
	}
	if _, err := state.ConfigureUpdates(t.Context(), selfhost.SystemActor, selfhost.UpdateChange{Enabled: boolPointer(true)}); err != nil {
		t.Fatal(err)
	}
	doc := updatesDoc(t, o.do(req{method: http.MethodPost, path: "/v1/owner/updates/check"}))
	if doc["available"] != true || doc["installKind"] != "binary" || len(doc["commands"].([]any)) != 4 {
		t.Fatalf("document = %v", doc)
	}
	host.publish(2, "10.9.0")
	doc = updatesDoc(t, o.do(req{method: http.MethodPost, path: "/v1/owner/updates/check"}))
	if doc["error"] != "feed_rollback" || doc["latest"].(map[string]any)["version"] != "10.0.0" {
		t.Fatalf("document = %v", doc)
	}
	system := updatesDoc(t, o.do(req{path: "/v1/owner/system"}))
	if system["updateAvailable"] != true || system["auditChainOk"] != true {
		t.Fatalf("system = %v", system)
	}
}

func boolPointer(value bool) *bool { return &value }

func TestExpiredStoredFeedStopsOfferingTheUpdate(t *testing.T) {
	host := newFeedHost(t)
	host.life = 2 * time.Hour
	host.publish(5, "10.0.0")
	e := newEnv(t, withUpdateFeed(host, updates.InstallContainer))
	o := e.createFirstOwner("Dana")
	o.stepUp()
	doc := updatesDoc(t, o.do(req{method: http.MethodPatch, path: "/v1/owner/updates", body: map[string]any{"enabled": true}}))
	if doc["available"] != true {
		t.Fatalf("setup: %v", doc)
	}
	e.clock.Advance(2 * time.Hour)
	doc = updatesDoc(t, o.do(req{path: "/v1/owner/updates"}))
	if doc["available"] != false || doc["error"] != "feed_expired" || len(doc["commands"].([]any)) != 0 || doc["latest"].(map[string]any)["version"] != "10.0.0" {
		t.Fatalf("document = %v", doc)
	}
	if system := updatesDoc(t, o.do(req{path: "/v1/owner/system"})); system["updateAvailable"] != false {
		t.Fatalf("system = %v", system)
	}
}
