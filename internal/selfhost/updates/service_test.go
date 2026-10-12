package updates

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"usesesame.app/backend/internal/selfhost"
	"usesesame.app/backend/internal/selfhost/sqlitestore"
)

var testOwner = selfhost.Actor{Kind: selfhost.ActorOwner, ID: "owner-1"}

type feedServer struct {
	*httptest.Server
	mu   sync.Mutex
	body []byte
	code int
	hits atomic.Int32
}

func newFeedServer(t *testing.T) *feedServer {
	t.Helper()
	feed := &feedServer{code: http.StatusOK}
	feed.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		feed.hits.Add(1)
		feed.mu.Lock()
		body, code := feed.body, feed.code
		feed.mu.Unlock()
		w.WriteHeader(code)
		_, _ = w.Write(body)
	}))
	t.Cleanup(feed.Close)
	return feed
}

func (f *feedServer) serve(body []byte, code int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.body, f.code = body, code
}

type fixture struct {
	t     *testing.T
	store *sqlitestore.Store
	pair  keyPair
	feed  *feedServer
	clock *fakeClock
	svc   *Service
}

func newFixture(t *testing.T, change ...func(*Options)) *fixture {
	t.Helper()
	clock := newFakeClock()
	store, err := sqlitestore.Open(t.Context(), sqlitestore.Options{
		Path:     filepath.Join(t.TempDir(), "sesame.db"),
		AdminKey: bytes.Repeat([]byte{7}, 32),
		Now:      clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	f := &fixture{t: t, store: store, pair: newKeyPair(t, testKeyID), feed: newFeedServer(t), clock: clock}
	options := Options{
		Store:       store,
		Version:     "0.1.0",
		Keys:        f.pair.keys(),
		FeedURL:     f.feed.URL + "/feed.json",
		Client:      f.feed.Client(),
		Clock:       clock,
		InstallKind: InstallContainer,
		Platform:    Platform{OS: "linux", Arch: "amd64", Executable: "/usr/local/bin/sesame-server"},
		Jitter:      func(time.Duration) time.Duration { return 0 },
	}
	for _, apply := range change {
		apply(&options)
	}
	f.svc, err = New(options)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) publish(payload Payload) {
	f.feed.serve(signed(f.t, payload, f.pair), http.StatusOK)
}

func (f *fixture) setEnabled(enabled bool) Status {
	f.t.Helper()
	status, err := f.svc.Configure(f.t.Context(), testOwner, selfhost.UpdateChange{Enabled: &enabled})
	if err != nil {
		f.t.Fatal(err)
	}
	return status
}

func (f *fixture) check() Status {
	f.t.Helper()
	status, err := f.svc.Check(f.t.Context())
	if err != nil {
		f.t.Fatal(err)
	}
	return status
}

func (f *fixture) auditActions() []string {
	f.t.Helper()
	page, err := f.store.ListAudit(f.t.Context(), 0, 100)
	if err != nil {
		f.t.Fatal(err)
	}
	actions := make([]string, 0, len(page.Entries))
	for _, entry := range page.Entries {
		actions = append(actions, entry.Action)
	}
	return actions
}

func countAction(actions []string, wanted string) int {
	count := 0
	for _, action := range actions {
		if action == wanted {
			count++
		}
	}
	return count
}

func TestNoRequestIsMadeWhileUnsetOffOrNotConfigured(t *testing.T) {
	f := newFixture(t)
	f.publish(samplePayload())

	status, err := f.svc.Check(t.Context())
	if !errors.Is(err, ErrOff) {
		t.Fatalf("a check while unset err = %v", err)
	}
	if status, err = f.svc.Status(t.Context()); err != nil || status.Enabled != nil || status.Latest != nil || status.Available || status.Error != "" || status.CheckedAt != nil {
		t.Fatalf("unset status = %+v, %v", status, err)
	}
	f.setEnabled(false)
	if _, err := f.svc.Check(t.Context()); !errors.Is(err, ErrOff) {
		t.Fatalf("a check while off err = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { f.svc.Run(ctx); close(done) }()
	f.clock.waitForTimer(t)
	f.clock.Advance(48 * time.Hour)
	f.clock.waitForTimer(t)
	cancel()
	<-done
	if f.feed.hits.Load() != 0 {
		t.Fatalf("the feed was requested %d times while checks were not turned on", f.feed.hits.Load())
	}

	unconfigured := newFixture(t, func(o *Options) { o.Keys = nil })
	unconfigured.publish(samplePayload())
	if unconfigured.svc.Configured() {
		t.Fatal("a service without keys reports itself configured")
	}
	if status := unconfigured.setEnabled(true); status.Configured || status.Error != CodeNotConfigured || status.Latest != nil {
		t.Fatalf("not configured status = %+v", status)
	}
	if status := unconfigured.check(); status.Error != CodeNotConfigured || status.Enabled == nil || !*status.Enabled {
		t.Fatalf("not configured check = %+v", status)
	}
	finished := make(chan struct{})
	go func() { unconfigured.svc.Run(t.Context()); close(finished) }()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return for an unconfigured service")
	}
	unconfigured.svc.Trigger()
	if unconfigured.feed.hits.Load() != 0 {
		t.Fatalf("an unconfigured service requested the feed %d times", unconfigured.feed.hits.Load())
	}
}

func TestValidFeedProducesAnUpdateWithCommands(t *testing.T) {
	f := newFixture(t)
	f.publish(samplePayload())
	status := f.setEnabled(true)
	if f.feed.hits.Load() != 1 {
		t.Fatalf("turning checks on made %d requests, want 1", f.feed.hits.Load())
	}
	if !status.Configured || status.Enabled == nil || !*status.Enabled || status.Channel != "stable" || status.InstallKind != "container" {
		t.Fatalf("status = %+v", status)
	}
	if status.Current != (Current{Product: ServerProductID, Version: "0.1.0"}) || status.Error != "" || status.CheckedAt == nil || !status.CheckedAt.Equal(testNow) {
		t.Fatalf("status = %+v", status)
	}
	if status.Latest == nil || status.Latest.Version != "0.2.0" || !status.Latest.Security || status.Latest.MinimumFrom != "0.1.0" || !status.Available {
		t.Fatalf("latest = %+v available %v", status.Latest, status.Available)
	}
	if len(status.Latest.Images) != 1 || len(status.Latest.Binaries) != 2 || len(status.Commands) != 5 || status.Commands[1].Text != "docker compose pull" {
		t.Fatalf("status = %+v", status)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	var shape map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &shape); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"configured", "enabled", "channel", "installKind", "current", "latest", "available", "checkedAt", "error", "commands"} {
		if _, present := shape[key]; !present {
			t.Errorf("the status document lacks %q", key)
		}
	}
	if len(shape) != 10 {
		t.Errorf("the status document has %d keys: %s", len(shape), encoded)
	}
}

func TestBinaryInstallGetsBinaryCommands(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.InstallKind = InstallBinary })
	f.publish(samplePayload())
	status := f.setEnabled(true)
	if status.InstallKind != "binary" || len(status.Commands) != 4 || status.Commands[0].Text != "curl -fsSLo sesame-server.new --proto '=https' --proto-redir '=https' 'https://downloads.example.net/sesame-server-linux-amd64'" {
		t.Fatalf("status = %+v", status)
	}
}

func TestUnsetStatusSerialisesNullsAndEmptyLists(t *testing.T) {
	f := newFixture(t)
	status, err := f.svc.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	var shape map[string]any
	if err := json.Unmarshal(encoded, &shape); err != nil {
		t.Fatal(err)
	}
	if value, present := shape["enabled"]; !present || value != nil {
		t.Errorf("enabled = %v", value)
	}
	if value, present := shape["latest"]; !present || value != nil {
		t.Errorf("latest = %v", value)
	}
	if value, present := shape["checkedAt"]; !present || value != nil {
		t.Errorf("checkedAt = %v", value)
	}
	if commands, ok := shape["commands"].([]any); !ok || len(commands) != 0 {
		t.Errorf("commands = %v", shape["commands"])
	}
}

func TestPrereleaseHandlingPerChannel(t *testing.T) {
	f := newFixture(t)
	onlyPrerelease := samplePayload().withStable(func(r *Release) { r.Version = "0.3.0-rc.1" })
	f.publish(onlyPrerelease)
	status := f.setEnabled(true)
	if status.Latest != nil || status.Available || len(status.Commands) != 0 {
		t.Fatalf("a prerelease was offered on the stable channel: %+v", status)
	}
	beta := "beta"
	status, err := f.svc.Configure(t.Context(), testOwner, selfhost.UpdateChange{Channel: &beta})
	if err != nil {
		t.Fatal(err)
	}
	if status.Channel != "beta" || status.Latest == nil || status.Latest.Version != "0.3.0-beta.1" || !status.Available {
		t.Fatalf("beta status = %+v", status)
	}
	unknown := "nightly"
	if _, err := f.svc.Configure(t.Context(), testOwner, selfhost.UpdateChange{Channel: &unknown}); !errors.Is(err, selfhost.ErrInvalidInput) {
		t.Fatalf("an unknown channel err = %v", err)
	}
	if status, _ := f.svc.Status(t.Context()); status.Channel != "beta" {
		t.Fatalf("a refused channel changed the setting: %+v", status)
	}
}

func TestAvailabilityFollowsTheInstalledVersion(t *testing.T) {
	cases := []struct {
		current   string
		available bool
	}{
		{"0.1.0", true}, {"0.2.0-dev", true}, {"0.2.0", false}, {"0.3.0", false}, {"unknown", false}, {"", false},
	}
	for _, tc := range cases {
		f := newFixture(t, func(o *Options) { o.Version = tc.current })
		f.publish(samplePayload())
		status := f.setEnabled(true)
		if status.Available != tc.available || status.Latest == nil {
			t.Errorf("current %q: available %v latest %+v", tc.current, status.Available, status.Latest)
		}
		if !tc.available && len(status.Commands) != 0 {
			t.Errorf("current %q: commands %v without an update", tc.current, status.Commands)
		}
		if f.svc.Available(t.Context()) != tc.available {
			t.Errorf("current %q: Available disagrees with Status", tc.current)
		}
	}
}

func TestSameSequenceIsAcceptedAndHigherReplacesIt(t *testing.T) {
	f := newFixture(t)
	f.publish(samplePayload())
	f.setEnabled(true)
	f.clock.Advance(time.Hour)
	status := f.check()
	if status.Error != "" || !status.CheckedAt.Equal(testNow.Add(time.Hour)) {
		t.Fatalf("the same sequence again: %+v", status)
	}
	newer := samplePayload().with(func(p *Payload) { p.Sequence = 6 }).withStable(func(r *Release) { r.Version = "0.2.1" })
	f.publish(newer)
	if status := f.check(); status.Error != "" || status.Latest.Version != "0.2.1" {
		t.Fatalf("a higher sequence: %+v", status)
	}
	settings, err := f.store.UpdateSettings(t.Context())
	if err != nil || settings.Sequences[testKeyID] != 6 {
		t.Fatalf("settings = %+v, %v", settings, err)
	}
}

func TestOlderSequenceIsARollback(t *testing.T) {
	f := newFixture(t)
	newer := samplePayload().with(func(p *Payload) { p.Sequence = 9 }).withStable(func(r *Release) { r.Version = "0.2.5" })
	f.publish(newer)
	f.setEnabled(true)
	f.publish(samplePayload())
	for round := 0; round < 2; round++ {
		status := f.check()
		if status.Error != selfhost.UpdateErrorRollback {
			t.Fatalf("round %d: error = %q", round, status.Error)
		}
		if status.Latest == nil || status.Latest.Version != "0.2.5" {
			t.Fatalf("round %d: the rolled back feed replaced the last good one: %+v", round, status.Latest)
		}
	}
	settings, _ := f.store.UpdateSettings(t.Context())
	if settings.Sequences[testKeyID] != 9 {
		t.Fatalf("highest sequence = %d", settings.Sequences[testKeyID])
	}
	if got := countAction(f.auditActions(), "updates.rollback_rejected"); got != 1 {
		t.Fatalf("the rollback was audited %d times, want once", got)
	}
	f.publish(newer)
	if status := f.check(); status.Error != "" || status.Latest.Version != "0.2.5" {
		t.Fatalf("recovery after a rollback: %+v", status)
	}
}

func TestRollbackStaysRejectedAfterTurningChecksOffAndOn(t *testing.T) {
	f := newFixture(t)
	f.publish(samplePayload().with(func(p *Payload) { p.Sequence = 9 }))
	f.setEnabled(true)
	f.setEnabled(false)
	f.publish(samplePayload())
	status := f.setEnabled(true)
	if status.Error != selfhost.UpdateErrorRollback {
		t.Fatalf("status = %+v", status)
	}
}

func TestFailedChecksKeepTheLastGoodResult(t *testing.T) {
	f := newFixture(t)
	f.publish(samplePayload())
	f.setEnabled(true)

	other := newKeyPair(t, testKeyID)
	cases := []struct {
		name  string
		serve func()
		code  string
	}{
		{"server error", func() { f.feed.serve([]byte("down"), http.StatusServiceUnavailable) }, selfhost.UpdateErrorUnreachable},
		{"garbage", func() { f.feed.serve([]byte("<html>"), http.StatusOK) }, selfhost.UpdateErrorInvalid},
		{"wrong key", func() { f.feed.serve(signed(t, samplePayload(), other), http.StatusOK) }, selfhost.UpdateErrorInvalid},
		{"oversize", func() { f.feed.serve(bytes.Repeat([]byte(" "), MaxFeedBytes+1), http.StatusOK) }, selfhost.UpdateErrorInvalid},
		{"expired", func() {
			f.publish(samplePayload().with(func(p *Payload) {
				p.IssuedAt = testNow.Add(-40 * 24 * time.Hour)
				p.ExpiresAt = testNow.Add(-24 * time.Hour)
			}))
		}, selfhost.UpdateErrorExpired},
		{"wrong product", func() { f.publish(samplePayload().with(func(p *Payload) { p.Products[0].ID = "other-product" })) }, selfhost.UpdateErrorInvalid},
		{"binary over http", func() {
			f.publish(samplePayload().withStable(func(r *Release) { r.Binaries[0].URL = "http://downloads.example.net/x" }))
		}, selfhost.UpdateErrorInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.serve()
			status := f.check()
			if status.Error != tc.code {
				t.Fatalf("error = %q, want %q", status.Error, tc.code)
			}
			if status.Latest == nil || status.Latest.Version != "0.2.0" || !status.Available || len(status.Commands) == 0 {
				t.Fatalf("the last good result was lost: %+v", status)
			}
			if status.CheckedAt == nil || !status.CheckedAt.Equal(testNow) {
				t.Fatalf("checkedAt moved on a failure: %v", status.CheckedAt)
			}
		})
	}
	f.publish(samplePayload())
	if status := f.check(); status.Error != "" {
		t.Fatalf("a good check did not clear the error: %+v", status)
	}
}

func TestUnreachableFeedKeepsTheLastResult(t *testing.T) {
	f := newFixture(t)
	f.publish(samplePayload())
	f.setEnabled(true)
	f.feed.Close()
	status := f.check()
	if status.Error != selfhost.UpdateErrorUnreachable || status.Latest == nil || status.Latest.Version != "0.2.0" {
		t.Fatalf("status = %+v", status)
	}
}

func TestFirstCheckFailureLeavesNothingToShow(t *testing.T) {
	f := newFixture(t)
	f.feed.serve([]byte("nope"), http.StatusNotFound)
	status := f.setEnabled(true)
	if status.Error != selfhost.UpdateErrorUnreachable || status.Latest != nil || status.Available || status.CheckedAt != nil {
		t.Fatalf("status = %+v", status)
	}
}

func TestStoredFeedIsReverifiedAgainstThePinnedKeys(t *testing.T) {
	f := newFixture(t)
	f.publish(samplePayload())
	f.setEnabled(true)
	rotated := newKeyPair(t, testKeyID)
	other, err := New(Options{Store: f.store, Version: "0.1.0", Keys: rotated.keys(), FeedURL: f.feed.URL, Clock: f.clock})
	if err != nil {
		t.Fatal(err)
	}
	status, err := other.Status(t.Context())
	if err != nil || status.Latest != nil || status.Available {
		t.Fatalf("a stored feed that does not verify was shown: %+v, %v", status, err)
	}
	settings, _ := f.store.UpdateSettings(t.Context())
	var envelope Envelope
	if err := json.Unmarshal(settings.Feed, &envelope); err != nil {
		t.Fatal(err)
	}
	envelope.Payload = envelope.Payload[:len(envelope.Payload)-4] + "AAAA"
	forged, _ := json.Marshal(envelope)
	if err := f.store.RecordUpdateFeed(t.Context(), selfhost.UpdateFeed{Raw: forged, KeyID: testKeyID, Sequence: 5, CheckedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	if status, _ := f.svc.Status(t.Context()); status.Latest != nil || status.Available {
		t.Fatalf("a tampered stored feed was shown: %+v", status)
	}
}

func TestTurningChecksOnChecksOnce(t *testing.T) {
	f := newFixture(t)
	f.publish(samplePayload())
	f.setEnabled(true)
	f.setEnabled(true)
	channel := "beta"
	if _, err := f.svc.Configure(t.Context(), testOwner, selfhost.UpdateChange{Channel: &channel}); err != nil {
		t.Fatal(err)
	}
	if f.feed.hits.Load() != 1 {
		t.Fatalf("requests = %d, want 1", f.feed.hits.Load())
	}
	f.setEnabled(false)
	f.setEnabled(true)
	if f.feed.hits.Load() != 2 {
		t.Fatalf("requests after switching off and on = %d, want 2", f.feed.hits.Load())
	}
	actions := f.auditActions()
	if got := countAction(actions, "updates.updated"); got != 4 {
		t.Fatalf("setting changes audited %d times, want 4: %v", got, actions)
	}
}

func TestStatusWhileOffHidesTheCachedResult(t *testing.T) {
	f := newFixture(t)
	f.publish(samplePayload())
	f.setEnabled(true)
	status := f.setEnabled(false)
	if status.Enabled == nil || *status.Enabled || status.Latest != nil || status.Available || status.Error != "" || len(status.Commands) != 0 {
		t.Fatalf("status = %+v", status)
	}
	if status := f.setEnabled(true); status.Latest == nil {
		t.Fatalf("turning checks back on lost the result: %+v", status)
	}
}

func TestSchedulerChecksDailyWithJitter(t *testing.T) {
	f := newFixture(t, func(o *Options) {
		o.Jitter = func(max time.Duration) time.Duration { return max / 2 }
	})
	f.publish(samplePayload())
	f.setEnabled(true)
	if f.feed.hits.Load() != 1 {
		t.Fatalf("setup request count = %d", f.feed.hits.Load())
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { f.svc.Run(ctx); close(done) }()
	if wait := f.clock.waitForTimer(t); wait != 24*time.Hour+30*time.Minute {
		t.Fatalf("first wait = %v, want 24h30m", wait)
	}
	if f.feed.hits.Load() != 1 {
		t.Fatalf("the scheduler checked at start although the result was fresh: %d", f.feed.hits.Load())
	}
	f.clock.Advance(24 * time.Hour)
	select {
	case <-f.clock.registered:
		t.Fatal("the scheduler woke before the interval and jitter had passed")
	case <-time.After(100 * time.Millisecond):
	}
	f.clock.Advance(30 * time.Minute)
	if wait := f.clock.waitForTimer(t); wait != 24*time.Hour+30*time.Minute {
		t.Fatalf("second wait = %v", wait)
	}
	if f.feed.hits.Load() != 2 {
		t.Fatalf("the daily check did not run: %d", f.feed.hits.Load())
	}
	cancel()
	<-done
}

func TestSchedulerRetriesSoonerAfterUnreachableAndOnTrigger(t *testing.T) {
	f := newFixture(t)
	f.feed.serve([]byte("down"), http.StatusBadGateway)
	f.setEnabled(true)
	hits := f.feed.hits.Load()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { f.svc.Run(ctx); close(done) }()
	wait := f.clock.waitForTimer(t)
	if wait < time.Hour || wait > time.Hour+15*time.Minute {
		t.Fatalf("retry wait after an unreachable feed = %v", wait)
	}
	f.publish(samplePayload())
	f.svc.Trigger()
	f.clock.waitForTimer(t)
	if f.feed.hits.Load() != hits+2 {
		t.Fatalf("requests = %d, want %d", f.feed.hits.Load(), hits+2)
	}
	if status, _ := f.svc.Status(t.Context()); status.Error != "" || status.Latest == nil {
		t.Fatalf("the triggered check did not store the feed: %+v", status)
	}
	cancel()
	<-done
}

func TestSchedulerPicksUpAChoiceMadeLater(t *testing.T) {
	f := newFixture(t)
	f.publish(samplePayload())
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { f.svc.Run(ctx); close(done) }()
	f.clock.waitForTimer(t)
	if f.feed.hits.Load() != 0 {
		t.Fatalf("requests while unset = %d", f.feed.hits.Load())
	}
	enabled := true
	if _, err := f.store.ConfigureUpdates(t.Context(), testOwner, selfhost.UpdateChange{Enabled: &enabled}); err != nil {
		t.Fatal(err)
	}
	f.svc.Trigger()
	f.clock.waitForTimer(t)
	if f.feed.hits.Load() != 1 {
		t.Fatalf("requests after turning on = %d", f.feed.hits.Load())
	}
	cancel()
	<-done
}

func TestRunStopsPromptlyOnCancel(t *testing.T) {
	f := newFixture(t)
	f.publish(samplePayload())
	f.setEnabled(true)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { f.svc.Run(ctx); close(done) }()
	f.clock.waitForTimer(t)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestNewRejectsBadOptions(t *testing.T) {
	f := newFixture(t)
	if _, err := New(Options{}); err == nil {
		t.Fatal("a service without a store was accepted")
	}
	if _, err := New(Options{Store: f.store, FeedURL: "http://example.net/feed.json"}); err == nil {
		t.Fatal("an http feed address was accepted")
	}
	if _, err := New(Options{Store: f.store, Product: "unknown-product"}); err == nil {
		t.Fatal("an unregistered product was accepted")
	}
	service, err := New(Options{Store: f.store, Version: "0.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	if service.Configured() {
		t.Fatal("a service with no keys is configured")
	}
}

type staleStore struct{ selfhost.UpdateStore }

func (s staleStore) UpdateSettings(ctx context.Context) (selfhost.UpdateSettings, error) {
	settings, err := s.UpdateStore.UpdateSettings(ctx)
	settings.Sequences = nil
	return settings, err
}

func TestStoreCatchesARollbackThatRacedPastTheServiceCheck(t *testing.T) {
	f := newFixture(t)
	f.publish(samplePayload().with(func(p *Payload) { p.Sequence = 9 }))
	f.setEnabled(true)
	racing, err := New(Options{Store: staleStore{f.store}, Version: "0.1.0", Keys: f.pair.keys(), FeedURL: f.feed.URL, Client: f.feed.Client(), Clock: f.clock})
	if err != nil {
		t.Fatal(err)
	}
	f.publish(samplePayload())
	status, err := racing.Check(t.Context())
	if err != nil || status.Error != selfhost.UpdateErrorRollback {
		t.Fatalf("status = %+v, err = %v", status, err)
	}
	settings, _ := f.store.UpdateSettings(t.Context())
	if settings.Sequences[testKeyID] != 9 {
		t.Fatalf("the store accepted a lower sequence: %d", settings.Sequences[testKeyID])
	}
}

func TestDefaultJitterStaysInRange(t *testing.T) {
	for round := 0; round < 200; round++ {
		if got := randomJitter(time.Hour); got < 0 || got >= time.Hour {
			t.Fatalf("jitter = %v", got)
		}
	}
	if randomJitter(0) != 0 || randomJitter(-time.Second) != 0 {
		t.Fatal("jitter without a range must be zero")
	}
}
