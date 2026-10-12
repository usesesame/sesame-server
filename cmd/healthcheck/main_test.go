package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"usesesame.app/backend/internal/selfhost/config"
)

func lookupFrom(values map[string]string) config.Lookup {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

func serve(t *testing.T, status int) (port string, hits *atomic.Int32) {
	t.Helper()
	hits = &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/readyz" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		hits.Add(1)
		w.WriteHeader(status)
	}))
	t.Cleanup(server.Close)
	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return port, hits
}

func TestProbeFollowsTheHostedAddress(t *testing.T) {
	port, hits := serve(t, http.StatusOK)
	err := probe(context.Background(), lookupFrom(map[string]string{"SESAME_API_ADDR": "127.0.0.1:" + port}), newClient())
	if err != nil || hits.Load() != 1 {
		t.Fatalf("probe = %v, hits = %d", err, hits.Load())
	}
}

func TestProbeFollowsTheSelfHostAddress(t *testing.T) {
	port, hits := serve(t, http.StatusOK)
	err := probe(context.Background(), lookupFrom(map[string]string{"SESAME_ADDR": "127.0.0.1:" + port}), newClient())
	if err != nil || hits.Load() != 1 {
		t.Fatalf("probe = %v, hits = %d", err, hits.Load())
	}
}

func TestProbeMapsTheWildcardAddressToLoopback(t *testing.T) {
	port, hits := serve(t, http.StatusOK)
	for _, name := range []string{"SESAME_ADDR", "SESAME_API_ADDR"} {
		before := hits.Load()
		if err := probe(context.Background(), lookupFrom(map[string]string{name: "0.0.0.0:" + port}), newClient()); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if hits.Load() != before+1 {
			t.Fatalf("%s: the server was not reached", name)
		}
	}
}

func TestProbeIgnoresTheHostedAddressWhenTheSelfHostOneIsSet(t *testing.T) {
	port, hits := serve(t, http.StatusOK)
	err := probe(context.Background(), lookupFrom(map[string]string{"SESAME_ADDR": "127.0.0.1:" + port, "SESAME_API_ADDR": "127.0.0.1:1"}), newClient())
	if err != nil || hits.Load() != 1 {
		t.Fatalf("probe = %v, hits = %d", err, hits.Load())
	}
}

func TestProbeReportsAnUnhealthyServer(t *testing.T) {
	port, _ := serve(t, http.StatusServiceUnavailable)
	err := probe(context.Background(), lookupFrom(map[string]string{"SESAME_ADDR": "127.0.0.1:" + port}), newClient())
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("probe = %v", err)
	}
}

func TestProbeDoesNotFollowRedirects(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()
	_, port, _ := net.SplitHostPort(redirector.Listener.Addr().String())
	err := probe(context.Background(), lookupFrom(map[string]string{"SESAME_ADDR": "127.0.0.1:" + port}), newClient())
	if err == nil || !strings.Contains(err.Error(), "302") {
		t.Fatalf("probe = %v", err)
	}
}

func TestProbeFailsWhenNothingListens(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	listener.Close()
	if err := probe(context.Background(), lookupFrom(map[string]string{"SESAME_ADDR": "127.0.0.1:" + port}), newClient()); err == nil {
		t.Fatal("probe succeeded against a closed port")
	}
}

func TestProbeRejectsAMalformedAddress(t *testing.T) {
	for _, value := range []string{"8787", "127.0.0.1", "127.0.0.1:0", "example.net:80", "http://127.0.0.1:8787"} {
		err := probe(context.Background(), lookupFrom(map[string]string{"SESAME_ADDR": value}), newClient())
		if err == nil || !strings.Contains(err.Error(), "SESAME_ADDR") {
			t.Errorf("probe(%q) = %v", value, err)
		}
	}
}

func TestProbeDefaultsToTheDocumentedAddress(t *testing.T) {
	got, err := config.ReadyURL(config.HealthAddress(lookupFrom(nil)))
	if err != nil || got != "http://127.0.0.1:8787/readyz" {
		t.Fatalf("default target = %q, %v", got, err)
	}
}
