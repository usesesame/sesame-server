package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func probeServer(t *testing.T, status int) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/readyz" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(server.Close)
	return server.Listener.Addr().String()
}

func TestProbeReady(t *testing.T) {
	good := probeServer(t, http.StatusOK)
	if err := probeReady(context.Background(), good, newTestClient()); err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(good)
	if err := probeReady(context.Background(), "0.0.0.0:"+port, newTestClient()); err != nil {
		t.Fatalf("wildcard address: %v", err)
	}
	if err := probeReady(context.Background(), ":"+port, newTestClient()); err != nil {
		t.Fatalf("empty host: %v", err)
	}
	if err := probeReady(context.Background(), probeServer(t, http.StatusServiceUnavailable), newTestClient()); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("unhealthy server: %v", err)
	}
	if err := probeReady(context.Background(), "not-an-address", newTestClient()); err == nil || !strings.Contains(err.Error(), "SESAME_ADDR") {
		t.Fatalf("bad address: %v", err)
	}
	closed := freeAddr(t)
	if err := probeReady(context.Background(), closed, newTestClient()); err == nil {
		t.Fatal("probe succeeded against a closed port")
	}
}

func newTestClient() *http.Client { return &http.Client{Transport: &http.Transport{}} }
