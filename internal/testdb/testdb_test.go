package testdb

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"testing"
)

const fictionalURL = "postgres://fictional@db.example.invalid/sesame"

type recordingT struct {
	testing.TB
	skipped  bool
	failure  string
	cleanups []func()
}

func (r *recordingT) Helper() {}

func (r *recordingT) Skip(...any) {
	r.skipped = true
	runtime.Goexit()
}

func (r *recordingT) Fatalf(format string, args ...any) {
	r.failure = fmt.Sprintf(format, args...)
	runtime.Goexit()
}

func (r *recordingT) Cleanup(cleanup func()) {
	r.cleanups = append(r.cleanups, cleanup)
}

func run(body func(*recordingT)) *recordingT {
	recorder := &recordingT{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		body(recorder)
	}()
	<-done
	return recorder
}

type fakeStore struct{ closed bool }

func (s *fakeStore) Close() error {
	s.closed = true
	return nil
}

func TestRunsUnderCIReadsTheCIVariable(t *testing.T) {
	for value, want := range map[string]bool{
		"":      false,
		" ":     false,
		"0":     false,
		"false": false,
		"FALSE": false,
		"true":  true,
		"True":  true,
		"1":     true,
		"yes":   true,
	} {
		if got := runsUnderCI(value); got != want {
			t.Errorf("runsUnderCI(%q) = %v, want %v", value, got, want)
		}
	}
}

func TestURLFailsUnderCIWithoutADatabase(t *testing.T) {
	t.Setenv(urlVariable, "")
	t.Setenv(ciVariable, "true")
	returned := false
	recorder := run(func(r *recordingT) {
		URL(r)
		returned = true
	})
	if recorder.skipped || returned || recorder.failure == "" {
		t.Fatalf("a CI run without a database skipped=%v returned=%v failure=%q", recorder.skipped, returned, recorder.failure)
	}
}

func TestURLReturnsTheDatabaseUnderCI(t *testing.T) {
	t.Setenv(urlVariable, fictionalURL)
	t.Setenv(ciVariable, "true")
	var got string
	recorder := run(func(r *recordingT) { got = URL(r) })
	if recorder.skipped || recorder.failure != "" || got != fictionalURL {
		t.Fatalf("URL under CI skipped=%v failure=%q url=%q", recorder.skipped, recorder.failure, got)
	}
}

func TestURLSkipsWithoutADatabase(t *testing.T) {
	t.Setenv(urlVariable, "")
	t.Setenv(ciVariable, "")
	returned := false
	recorder := run(func(r *recordingT) {
		URL(r)
		returned = true
	})
	if !recorder.skipped || returned || recorder.failure != "" {
		t.Fatal("a test without a database did not skip")
	}
}

func TestOpenPassesTheURLAndClosesTheStoreAtCleanup(t *testing.T) {
	t.Setenv(urlVariable, fictionalURL)
	store := &fakeStore{}
	var passed string
	var opened *fakeStore
	recorder := run(func(r *recordingT) {
		opened = Open(r, func(_ context.Context, databaseURL string) (*fakeStore, error) {
			passed = databaseURL
			return store, nil
		})
	})
	if recorder.skipped || recorder.failure != "" || passed != fictionalURL || opened != store || store.closed {
		t.Fatalf("Open skipped=%v failure=%q url=%q", recorder.skipped, recorder.failure, passed)
	}
	for _, cleanup := range recorder.cleanups {
		cleanup()
	}
	if !store.closed {
		t.Fatal("the store was not closed at cleanup")
	}
}

func TestOpenFailsWhenTheStoreCannotOpen(t *testing.T) {
	t.Setenv(urlVariable, fictionalURL)
	recorder := run(func(r *recordingT) {
		Open(r, func(context.Context, string) (*fakeStore, error) { return nil, errors.New("fictional outage") })
	})
	if recorder.failure == "" || recorder.skipped || len(recorder.cleanups) != 0 {
		t.Fatalf("a store that cannot open gave failure=%q skipped=%v", recorder.failure, recorder.skipped)
	}
}

func TestOpenSkipsWithoutADatabase(t *testing.T) {
	t.Setenv(urlVariable, "")
	t.Setenv(ciVariable, "")
	called := false
	recorder := run(func(r *recordingT) {
		Open(r, func(context.Context, string) (*fakeStore, error) {
			called = true
			return &fakeStore{}, nil
		})
	})
	if !recorder.skipped || called {
		t.Fatal("Open tried to reach a database that is not configured")
	}
}

func TestOpenFailsUnderCIWithoutADatabase(t *testing.T) {
	t.Setenv(urlVariable, "")
	t.Setenv(ciVariable, "true")
	called := false
	recorder := run(func(r *recordingT) {
		Open(r, func(context.Context, string) (*fakeStore, error) {
			called = true
			return &fakeStore{}, nil
		})
	})
	if recorder.skipped || recorder.failure == "" || called {
		t.Fatalf("Open under CI without a database skipped=%v failure=%q opened=%v", recorder.skipped, recorder.failure, called)
	}
}
