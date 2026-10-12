package updates

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os/exec"
	"strings"
	"testing"
	"time"

	"usesesame.app/backend/internal/selfhost"
)

func TestPoisonedSequenceOfOneKeyDoesNotBlockARotatedKey(t *testing.T) {
	f := newFixture(t)
	f.setEnabled(true)
	f.publish(samplePayload().with(func(p *Payload) { p.Sequence = MaxSequence }))
	if status := f.check(); status.Error != "" {
		t.Fatalf("setup: %q", status.Error)
	}
	rotated := newKeyPair(t, "rotated-2")
	f.feed.serve(signed(t, samplePayload().with(func(p *Payload) { p.Sequence = 6 }), rotated), 200)
	second, err := New(Options{Store: f.store, Version: "0.1.0", Keys: rotated.keys(), FeedURL: f.feed.URL + "/feed.json", Client: f.feed.Client(), Clock: f.clock})
	if err != nil {
		t.Fatal(err)
	}
	status, err := second.Check(t.Context())
	if err != nil || status.Error != "" || status.Latest == nil {
		t.Fatalf("a rotated key could not publish: %+v, %v", status, err)
	}
	if again, _ := f.svc.Check(t.Context()); again.Error != "" && again.Error != selfhost.UpdateErrorInvalid {
		t.Fatalf("old key check = %q", again.Error)
	}
}

func TestSequencesAreKeptPerKey(t *testing.T) {
	f := newFixture(t)
	f.setEnabled(true)
	f.publish(samplePayload().with(func(p *Payload) { p.Sequence = 40 }))
	f.check()
	second := newKeyPair(t, "second-key")
	both := Keys{f.pair.id: f.pair.public, second.id: second.public}
	svc, err := New(Options{Store: f.store, Version: "0.1.0", Keys: both, FeedURL: f.feed.URL + "/feed.json", Client: f.feed.Client(), Clock: f.clock})
	if err != nil {
		t.Fatal(err)
	}
	f.feed.serve(signed(t, samplePayload().with(func(p *Payload) { p.Sequence = 3 }), second), 200)
	if status, _ := svc.Check(t.Context()); status.Error != "" {
		t.Fatalf("the second key starts at its own sequence: %q", status.Error)
	}
	f.feed.serve(signed(t, samplePayload().with(func(p *Payload) { p.Sequence = 2 }), second), 200)
	if status, _ := svc.Check(t.Context()); status.Error != selfhost.UpdateErrorRollback {
		t.Fatalf("a lower sequence under the same key = %q", status.Error)
	}
	f.feed.serve(signed(t, samplePayload().with(func(p *Payload) { p.Sequence = 39 }), f.pair), 200)
	if status, _ := svc.Check(t.Context()); status.Error != selfhost.UpdateErrorRollback {
		t.Fatalf("the first key is still bound to 40: %q", status.Error)
	}
}

func TestResetSequencesLetsAKeyRestart(t *testing.T) {
	f := newFixture(t)
	f.setEnabled(true)
	f.publish(samplePayload().with(func(p *Payload) { p.Sequence = 500 }))
	f.check()
	f.publish(samplePayload())
	if status := f.check(); status.Error != selfhost.UpdateErrorRollback {
		t.Fatalf("setup: %q", status.Error)
	}
	cleared, err := f.store.ResetUpdateSequences(t.Context(), selfhost.SystemActor)
	if err != nil || cleared != 1 {
		t.Fatalf("cleared %d, %v", cleared, err)
	}
	if status := f.check(); status.Error != "" {
		t.Fatalf("after the reset: %q", status.Error)
	}
	if countAction(f.auditActions(), "updates.sequences_reset") != 1 {
		t.Fatalf("audit = %v", f.auditActions())
	}
}

func TestSequenceBounds(t *testing.T) {
	pair := newKeyPair(t, testKeyID)
	for _, sequence := range []int64{MaxSequence + 1, math.MaxInt64} {
		payload := samplePayload().with(func(p *Payload) { p.Sequence = sequence })
		if _, err := verifierFor(pair).Verify(signed(t, payload, pair), testNow); !errors.Is(err, ErrInvalid) {
			t.Fatalf("sequence %d err = %v", sequence, err)
		}
	}
	payload := samplePayload().with(func(p *Payload) { p.Sequence = MaxSequence })
	if _, err := verifierFor(pair).Verify(signed(t, payload, pair), testNow); err != nil {
		t.Fatal(err)
	}
}

func TestExpiredStoredFeedOffersNothing(t *testing.T) {
	f := newFixture(t)
	f.publish(samplePayload())
	f.setEnabled(true)
	f.clock.Advance(100 * 24 * time.Hour)
	f.feed.serve(nil, 503)
	status, err := f.svc.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if status.Available || len(status.Commands) != 0 || status.Error != selfhost.UpdateErrorExpired {
		t.Fatalf("status = %+v", status)
	}
	if f.svc.Available(t.Context()) {
		t.Fatal("Available is true for an expired stored feed")
	}
	f.publish(samplePayload().with(func(p *Payload) {
		p.Sequence = 6
		p.IssuedAt = f.clock.Now().Add(-time.Hour)
		p.ExpiresAt = f.clock.Now().Add(24 * time.Hour)
	}))
	if status := f.check(); status.Error != "" || !status.Available || len(status.Commands) == 0 {
		t.Fatalf("a fresh feed did not recover: %+v", status)
	}
}

func TestStartupSkipsTheCheckWhenTheResultIsFresh(t *testing.T) {
	f := newFixture(t)
	f.publish(samplePayload())
	f.setEnabled(true)
	before := f.feed.hits.Load()
	for round := 0; round < 3; round++ {
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() { f.svc.Run(ctx); close(done) }()
		wait := f.clock.waitForTimer(t)
		cancel()
		<-done
		if wait != 24*time.Hour {
			t.Fatalf("first wait = %v, want the rest of the interval", wait)
		}
	}
	if f.feed.hits.Load() != before {
		t.Fatalf("restarts made %d requests", f.feed.hits.Load()-before)
	}
}

func TestStartupWaitsForJitterBeforeAnOverdueCheck(t *testing.T) {
	f := newFixture(t, func(o *Options) {
		o.Jitter = func(max time.Duration) time.Duration { return max / 2 }
	})
	f.publish(samplePayload())
	f.setEnabled(true)
	f.clock.Advance(30 * time.Hour)
	before := f.feed.hits.Load()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { f.svc.Run(ctx); close(done) }()
	if wait := f.clock.waitForTimer(t); wait != 5*time.Minute {
		t.Fatalf("startup delay = %v, want half of ten minutes", wait)
	}
	if f.feed.hits.Load() != before {
		t.Fatal("the overdue check ran before the startup delay")
	}
	f.clock.Advance(5 * time.Minute)
	f.clock.waitForTimer(t)
	if f.feed.hits.Load() != before+1 {
		t.Fatalf("requests = %d, want one", f.feed.hits.Load()-before)
	}
	cancel()
	<-done
}

func TestStartupJitterNeverExceedsTenMinutes(t *testing.T) {
	var asked []time.Duration
	f := newFixture(t, func(o *Options) {
		o.Jitter = func(max time.Duration) time.Duration { asked = append(asked, max); return 0 }
	})
	f.publish(samplePayload())
	f.setEnabled(true)
	f.clock.Advance(48 * time.Hour)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { f.svc.Run(ctx); close(done) }()
	f.clock.waitForTimer(t)
	cancel()
	<-done
	if len(asked) == 0 || asked[0] != 10*time.Minute {
		t.Fatalf("jitter ranges asked = %v", asked)
	}
}

func TestLineBreaksInBase64FieldsAreRejected(t *testing.T) {
	pair := newKeyPair(t, testKeyID)
	var envelope Envelope
	if err := json.Unmarshal(signed(t, samplePayload(), pair), &envelope); err != nil {
		t.Fatal(err)
	}
	for _, breaker := range []string{"\n", "\r", "\r\n"} {
		mutated := envelope
		mutated.Signature = envelope.Signature[:10] + breaker + envelope.Signature[10:]
		body, _ := json.Marshal(mutated)
		if _, err := verifierFor(pair).Verify(body, testNow); !errors.Is(err, ErrInvalid) {
			t.Errorf("signature with %q err = %v", breaker, err)
		}
		mutated = envelope
		mutated.Payload = envelope.Payload[:7] + breaker + envelope.Payload[7:]
		body, _ = json.Marshal(mutated)
		if _, err := verifierFor(pair).Verify(body, testNow); !errors.Is(err, ErrInvalid) {
			t.Errorf("payload with %q err = %v", breaker, err)
		}
	}
	entry := FormatKey(pair.id, pair.public)
	if _, err := ParseKeys(entry[:20] + "\n" + entry[20:]); err == nil {
		t.Fatal("a key with an embedded line break was accepted")
	}
}

func TestDownloadURLsWithGlobCharactersAreRejected(t *testing.T) {
	pair := newKeyPair(t, testKeyID)
	for _, bad := range []string{"https://d.example.net/a[1-3]", "https://d.example.net/a]", "https://[::1]/a", "https://d.example.net/a{1,2}"} {
		payload := samplePayload().withStable(func(r *Release) { r.Binaries[0].URL = bad })
		if _, err := verifierFor(pair).Verify(signed(t, payload, pair), testNow); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s err = %v", bad, err)
		}
	}
}

func TestBinaryDownloadOnlyUsesHTTPS(t *testing.T) {
	release := samplePayload().Products[0].Channels["stable"]
	commands := Commands(InstallBinary, release, Platform{OS: "linux", Arch: "amd64"})
	if !strings.Contains(commands[0].Text, "--proto '=https' --proto-redir '=https'") {
		t.Fatalf("download command = %q", commands[0].Text)
	}
}

func TestContainerCommandsPrintThePulledDigestBeforeStarting(t *testing.T) {
	release := samplePayload().Products[0].Channels["stable"]
	got := texts(Commands(InstallContainer, release, Platform{}))
	if len(got) != 5 || got[1] != "docker compose pull" || !strings.HasPrefix(got[2], "docker image inspect") || !strings.Contains(got[2], "RepoDigests") || got[3] != release.Images[0].Ref || got[4] != "docker compose up -d" {
		t.Fatalf("commands = %v", got)
	}
	release.Images = nil
	got = texts(Commands(InstallContainer, release, Platform{}))
	if len(got) != 4 || got[3] != "docker compose up -d" {
		t.Fatalf("commands without an image = %v", got)
	}
}

func TestHostileDownloadURLStaysInsideQuotes(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	hostile := "https://a.example/$(id);x&y*!~?q=%27"
	if err := validateURL(hostile); err != nil {
		t.Fatal(err)
	}
	executable := "/opt/it's here/$(id)/sesame"
	commands := binaryCommands(Release{Binaries: []Binary{{OS: "linux", Arch: "amd64", URL: hostile, SHA256: testSHA}}}, Platform{OS: "linux", Arch: "amd64", Executable: executable})
	arguments := map[int]string{
		0: commands[0].Text[strings.LastIndex(commands[0].Text, " 'https")+1:],
		2: strings.TrimPrefix(commands[2].Text, "sudo install -m 0755 sesame-server.new "),
	}
	for index, want := range map[int]string{0: hostile, 2: executable} {
		out, err := exec.Command(shell, "-c", "set -- "+arguments[index]+"; printf '%s' \"$1\"").CombinedOutput()
		if err != nil || string(out) != want {
			t.Fatalf("command %d: shell sees %q, err %v, want %q", index, out, err, want)
		}
	}
}
