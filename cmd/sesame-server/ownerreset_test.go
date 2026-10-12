package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"usesesame.app/backend/internal/selfhost"
	"usesesame.app/backend/internal/selfhost/config"
	"usesesame.app/backend/internal/selfhost/secrets"
)

type fakeResetStore struct {
	issue  selfhost.SetupIssue
	err    error
	name   string
	by     selfhost.Actor
	closed bool
}

func (f *fakeResetStore) ResetOwner(_ context.Context, by selfhost.Actor, name string) (selfhost.SetupIssue, error) {
	f.name, f.by = name, by
	return f.issue, f.err
}

func (f *fakeResetStore) Close() error { f.closed = true; return nil }

func ownerResetInvocation(t *testing.T, name string, createSecrets bool) (Invocation, *syncBuffer) {
	t.Helper()
	dataDir := t.TempDir()
	if createSecrets {
		if _, _, err := secrets.LoadOrCreate(dataDir); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.Load(lookupFrom(map[string]string{config.EnvDataDir: dataDir, config.EnvPublicURL: "https://sesame.example.net"}))
	if err != nil {
		t.Fatal(err)
	}
	stdout := &syncBuffer{}
	return Invocation{Config: cfg, Args: []string{name}, Stdout: stdout, Stderr: &syncBuffer{}, Logger: quietLogger()}, stdout
}

func TestOwnerResetPrintsASetupLink(t *testing.T) {
	store := &fakeResetStore{issue: selfhost.SetupIssue{Owner: selfhost.Owner{Name: "Ada Quill"}, Token: "token-value", ExpiresAt: time.Date(2026, 10, 11, 9, 30, 0, 0, time.UTC)}}
	invocation, stdout := ownerResetInvocation(t, "  Ada Quill ", true)
	runner := newOwnerReset(func(context.Context, config.Config, *secrets.Secrets) (ownerResetStore, error) { return store, nil })
	if err := runner.Run(context.Background(), invocation); err != nil {
		t.Fatal(err)
	}
	if store.name != "Ada Quill" || store.by.Kind != selfhost.ActorSystem || !store.closed {
		t.Fatalf("store = %+v", store)
	}
	want := "Open https://sesame.example.net/setup#token=token-value to set a new password and authenticator for Ada Quill. The link works once, expires 2026-10-11 09:30 UTC, and has ended every session for this owner.\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestOwnerResetFailures(t *testing.T) {
	cases := []struct {
		name  string
		err   error
		want  string
		setup bool
		open  error
	}{
		{name: "unknown owner", err: selfhost.ErrNotFound, want: `no active owner is named "Ada"`, setup: true},
		{name: "invalid name", err: selfhost.ErrInvalidInput, want: `"Ada" is not a valid owner name`, setup: true},
		{name: "store failure", err: errors.New("disk I/O error"), want: "disk I/O error", setup: true},
		{name: "open failure", open: errors.New("database is locked"), want: "database is locked", setup: true},
		{name: "secrets missing", want: "admin encryption key", setup: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeResetStore{err: tc.err}
			invocation, stdout := ownerResetInvocation(t, "Ada", tc.setup)
			opened := false
			runner := newOwnerReset(func(context.Context, config.Config, *secrets.Secrets) (ownerResetStore, error) {
				opened = true
				if tc.open != nil {
					return nil, tc.open
				}
				return store, nil
			})
			err := runner.Run(context.Background(), invocation)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			if stdout.String() != "" {
				t.Fatalf("a link was printed on failure: %q", stdout.String())
			}
			if !tc.setup && opened {
				t.Fatal("the store was opened without the instance secrets")
			}
			if tc.setup && tc.open == nil && !store.closed {
				t.Fatal("the store was not closed")
			}
		})
	}
}

func TestOwnerResetRefusesLooseSecrets(t *testing.T) {
	invocation, _ := ownerResetInvocation(t, "Ada", true)
	path := filepath.Join(secrets.Dir(invocation.Config.DataDir), secrets.AdminKeyFile)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	opened := false
	runner := newOwnerReset(func(context.Context, config.Config, *secrets.Secrets) (ownerResetStore, error) {
		opened = true
		return nil, nil
	})
	if err := runner.Run(context.Background(), invocation); err == nil || opened {
		t.Fatalf("error = %v, opened = %v", err, opened)
	}
}
