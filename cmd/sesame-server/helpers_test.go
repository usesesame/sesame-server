package main

import (
	"bytes"
	"context"
	"net"
	"sync"
	"testing"

	"usesesame.app/backend/internal/selfhost/config"
)

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func lookupFrom(values map[string]string) config.Lookup {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().String()
}

type recorded struct {
	mu          sync.Mutex
	invocations []Invocation
}

func (r *recorded) runner(err error) Runner {
	return RunnerFunc(func(_ context.Context, invocation Invocation) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.invocations = append(r.invocations, invocation)
		return err
	})
}

func (r *recorded) calls() []Invocation {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Invocation(nil), r.invocations...)
}

func testEnvironment(t *testing.T, values map[string]string, commands Commands) (Environment, *syncBuffer, *syncBuffer) {
	t.Helper()
	merged := map[string]string{config.EnvDataDir: t.TempDir()}
	for key, value := range values {
		merged[key] = value
	}
	stdout, stderr := &syncBuffer{}, &syncBuffer{}
	return Environment{Lookup: lookupFrom(merged), Stdout: stdout, Stderr: stderr, Commands: commands, Version: "9.9.9-test", Commit: "abc1234"}, stdout, stderr
}
