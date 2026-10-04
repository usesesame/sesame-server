package httpapi

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func clientIPTestAPI(trusted ...string) *api {
	prefixes := make([]netip.Prefix, 0, len(trusted))
	for _, raw := range trusted {
		prefixes = append(prefixes, netip.MustParsePrefix(raw))
	}
	return &api{config: Config{TrustedProxies: prefixes}}
}

func clientIPRequest(remoteAddr, forwardedFor string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "/livez", nil)
	request.RemoteAddr = remoteAddr
	if forwardedFor != "" {
		request.Header.Set("X-Forwarded-For", forwardedFor)
	}
	return request
}

func TestClientIPIgnoresForwardedAddressesWithoutTrustedProxies(t *testing.T) {
	service := clientIPTestAPI()
	request := clientIPRequest("10.10.0.5:12345", "203.0.113.9")
	if got := service.clientIP(request); got != "10.10.0.5" {
		t.Fatalf("clientIP = %q, want the peer address", got)
	}
}

func TestClientIPIgnoresForwardedAddressesFromUntrustedPeers(t *testing.T) {
	service := clientIPTestAPI("172.30.0.0/24")
	request := clientIPRequest("10.10.0.5:12345", "203.0.113.9")
	if got := service.clientIP(request); got != "10.10.0.5" {
		t.Fatalf("clientIP = %q, want the peer address", got)
	}
}

func TestClientIPUsesForwardedAddressesFromTrustedPeers(t *testing.T) {
	service := clientIPTestAPI("172.30.0.0/24")
	request := clientIPRequest("172.30.0.2:12345", "203.0.113.9")
	if got := service.clientIP(request); got != "203.0.113.9" {
		t.Fatalf("clientIP = %q, want the forwarded client address", got)
	}
}

func TestClientIPSkipsTrustedHopsFromTheRight(t *testing.T) {
	service := clientIPTestAPI("172.30.0.0/24")
	request := clientIPRequest("172.30.0.2:12345", "203.0.113.9, 172.30.0.3, 172.30.0.4")
	if got := service.clientIP(request); got != "203.0.113.9" {
		t.Fatalf("clientIP = %q, want the first untrusted hop", got)
	}
}

func TestClientIPFallsBackToThePeerOnMalformedForwarding(t *testing.T) {
	service := clientIPTestAPI("172.30.0.0/24")
	request := clientIPRequest("172.30.0.2:12345", "not-an-address")
	if got := service.clientIP(request); got != "172.30.0.2" {
		t.Fatalf("clientIP = %q, want the peer address", got)
	}
}

func TestClientIPFallsBackToThePeerWhenEveryForwardedHopIsTrusted(t *testing.T) {
	service := clientIPTestAPI("172.30.0.0/24")
	request := clientIPRequest("172.30.0.2:12345", "172.30.0.3, 172.30.0.4")
	if got := service.clientIP(request); got != "172.30.0.2" {
		t.Fatalf("clientIP = %q, want the peer address", got)
	}
}
