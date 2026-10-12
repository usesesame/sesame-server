package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"usesesame.app/backend/internal/selfhost"
	"usesesame.app/backend/internal/selfhost/config"
	"usesesame.app/backend/internal/selfhost/secrets"
)

type fakeUpdateResetStore struct {
	cleared int
	err     error
	by      selfhost.Actor
	closed  bool
}

func (f *fakeUpdateResetStore) ResetUpdateSequences(_ context.Context, by selfhost.Actor) (int, error) {
	f.by = by
	return f.cleared, f.err
}

func (f *fakeUpdateResetStore) Close() error { f.closed = true; return nil }

func TestUpdateSequenceResetReportsAndClosesTheStore(t *testing.T) {
	store := &fakeUpdateResetStore{cleared: 2}
	invocation, stdout := ownerResetInvocation(t, "unused", true)
	invocation.Args = nil
	runner := newUpdateSequenceReset(func(context.Context, config.Config, *secrets.Secrets) (updateResetStore, error) { return store, nil })
	if err := runner.Run(context.Background(), invocation); err != nil {
		t.Fatal(err)
	}
	if store.by != hostActor || !store.closed || !strings.Contains(stdout.String(), "for 2 signing keys") {
		t.Fatalf("store %+v stdout %q", store, stdout.String())
	}
}

func TestUpdateSequenceResetFailures(t *testing.T) {
	invocation, stdout := ownerResetInvocation(t, "unused", true)
	runner := newUpdateSequenceReset(func(context.Context, config.Config, *secrets.Secrets) (updateResetStore, error) {
		return &fakeUpdateResetStore{err: errors.New("disk I/O error")}, nil
	})
	if err := runner.Run(context.Background(), invocation); err == nil || !strings.Contains(err.Error(), "disk I/O error") || stdout.String() != "" {
		t.Fatalf("error = %v, stdout %q", err, stdout.String())
	}
	missing, _ := ownerResetInvocation(t, "unused", false)
	opened := false
	runner = newUpdateSequenceReset(func(context.Context, config.Config, *secrets.Secrets) (updateResetStore, error) {
		opened = true
		return nil, nil
	})
	if err := runner.Run(context.Background(), missing); err == nil || opened {
		t.Fatalf("error = %v, opened = %v", err, opened)
	}
}

func TestUpdateSequenceResetAgainstARealDatabase(t *testing.T) {
	dataDir := t.TempDir()
	startReal(t, dataDir).stop(t)
	values := map[string]string{config.EnvDataDir: dataDir}
	cfg, err := config.Load(lookupFrom(values))
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := secrets.Load(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := openStore(t.Context(), cfg, loaded, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordUpdateFeed(t.Context(), selfhost.UpdateFeed{KeyID: "key-a", Raw: []byte("{}"), Sequence: 900, CheckedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := command(t, values, "updates", "reset-sequence")
	if code != exitOK || !strings.Contains(out, "for 1 signing keys") {
		t.Fatalf("exit %d: %s %s", code, out, stderr)
	}
	store, err = openStore(t.Context(), cfg, loaded, false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	settings, err := store.UpdateSettings(t.Context())
	if err != nil || len(settings.Sequences) != 0 {
		t.Fatalf("settings = %+v, %v", settings, err)
	}
	page, err := store.ListAudit(t.Context(), 0, 5)
	if err != nil || len(page.Entries) == 0 || page.Entries[0].Action != "updates.sequences_reset" || page.Entries[0].Actor != hostActor.String() {
		t.Fatalf("audit = %+v, %v", page.Entries, err)
	}
}

func TestUpdatesCommandNeedsItsSubcommand(t *testing.T) {
	for _, args := range [][]string{{"updates"}, {"updates", "other"}, {"updates", "reset-sequence", "extra"}} {
		code, _, stderr := command(t, map[string]string{config.EnvDataDir: t.TempDir()}, args...)
		if code != exitUsage || !strings.Contains(stderr, "updates") {
			t.Errorf("%v: exit %d %s", args, code, stderr)
		}
	}
}
