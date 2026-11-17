package server

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
)

type warningCounter struct {
	mu    *sync.Mutex
	count *int
}

func (h warningCounter) Enabled(context.Context, slog.Level) bool { return true }
func (h warningCounter) Handle(_ context.Context, record slog.Record) error {
	if record.Level == slog.LevelWarn && strings.Contains(record.Message, "SESAME_TRUSTED_PROXIES") {
		h.mu.Lock()
		*h.count++
		h.mu.Unlock()
	}
	return nil
}
func (h warningCounter) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h warningCounter) WithGroup(string) slog.Handler      { return h }

func clientAddressRequest(remoteAddr, forwardedFor string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/v1/owner/login", nil)
	request.RemoteAddr = remoteAddr
	if forwardedFor != "" {
		request.Header.Set("X-Forwarded-For", forwardedFor)
	}
	return request
}

func TestClientLimitKeyUsesTheConfiguredTrustedProxies(t *testing.T) {
	request := clientAddressRequest("172.30.0.2:5000", "2001:db8:1:2::99")
	trusted := &server{cfg: Config{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("172.30.0.0/24")}}}
	if got := trusted.clientLimitKey(request); got != "2001:db8:1:2::/64" {
		t.Fatalf("clientLimitKey = %q, want the forwarded client network", got)
	}
	if got := (&server{}).clientLimitKey(request); got != "172.30.0.2" {
		t.Fatalf("clientLimitKey = %q, want the peer address", got)
	}
}

func TestIgnoredForwardingFromAPrivatePeerWarnsOnce(t *testing.T) {
	var mu sync.Mutex
	warnings := 0
	previous := slog.Default()
	slog.SetDefault(slog.New(warningCounter{mu: &mu, count: &warnings}))
	defer slog.SetDefault(previous)

	s := &server{cfg: Config{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("172.30.0.0/24")}}}
	s.clientLimitKey(clientAddressRequest("172.30.0.2:5000", "203.0.113.4"))
	s.clientLimitKey(clientAddressRequest("203.0.113.9:5000", "203.0.113.4"))
	s.clientLimitKey(clientAddressRequest("10.0.0.8:5000", ""))
	if s.forwardedFromUntrusted() {
		t.Fatal("a trusted, public or headerless peer was reported as untrusted forwarding")
	}
	s.clientLimitKey(clientAddressRequest("10.0.0.8:5000", "203.0.113.4"))
	s.clientLimitKey(clientAddressRequest("10.0.0.9:5000", "203.0.113.5"))
	if !s.forwardedFromUntrusted() {
		t.Fatal("forwarding from an untrusted private peer was not recorded")
	}
	mu.Lock()
	defer mu.Unlock()
	if warnings != 1 {
		t.Fatalf("logged %d forwarding warnings, want 1", warnings)
	}
}
