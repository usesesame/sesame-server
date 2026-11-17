package clientaddrtest

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
)

type Case struct {
	Name       string
	Trusted    []string
	RemoteAddr string
	Forwarded  []string
	Headers    map[string]string
	Client     string
	LimitKey   string
}

func (c Case) Prefixes() []netip.Prefix {
	prefixes := make([]netip.Prefix, 0, len(c.Trusted))
	for _, raw := range c.Trusted {
		prefixes = append(prefixes, netip.MustParsePrefix(raw))
	}
	return prefixes
}

func (c Case) Request() *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/v1/test", nil)
	request.RemoteAddr = c.RemoteAddr
	for _, line := range c.Forwarded {
		request.Header.Add("x-forwarded-for", line)
	}
	for name, value := range c.Headers {
		request.Header.Set(name, value)
	}
	return request
}

const (
	proxyNetwork = "198.51.100.0/24"
	proxyPeer    = "198.51.100.2:41000"
	proxyAddress = "198.51.100.2"
	client       = "203.0.113.10"
)

func Cases() []Case {
	trusted := []string{proxyNetwork}
	longForged := strings.Repeat("192.0.2.66, ", 4000) + client
	longTrusted := strings.TrimSuffix(strings.Repeat("198.51.100.3, ", 4000), ", ")
	manyLines := make([]string, 0, 200)
	for range 199 {
		manyLines = append(manyLines, "192.0.2.66")
	}
	manyLines = append(manyLines, client)
	return []Case{
		{Name: "no header from an untrusted peer", Trusted: trusted, RemoteAddr: "203.0.113.10:41000", Client: client, LimitKey: client},
		{Name: "no header from a trusted proxy", Trusted: trusted, RemoteAddr: proxyPeer, Client: proxyAddress, LimitKey: proxyAddress},
		{Name: "empty header from a trusted proxy", Trusted: trusted, RemoteAddr: proxyPeer, Forwarded: []string{""}, Client: proxyAddress, LimitKey: proxyAddress},
		{Name: "one address from a trusted proxy", Trusted: trusted, RemoteAddr: proxyPeer, Forwarded: []string{client}, Client: client, LimitKey: client},
		{Name: "client supplied leading entries are ignored", Trusted: trusted, RemoteAddr: proxyPeer, Forwarded: []string{"192.0.2.66, 192.0.2.67, " + client}, Client: client, LimitKey: client},
		{Name: "a forged trusted entry on the left is ignored", Trusted: trusted, RemoteAddr: proxyPeer, Forwarded: []string{"198.51.100.9, " + client}, Client: client, LimitKey: client},
		{Name: "trusted proxy chain is skipped from the right", Trusted: trusted, RemoteAddr: proxyPeer, Forwarded: []string{client + ", 198.51.100.3, 198.51.100.4"}, Client: client, LimitKey: client},
		{Name: "every hop trusted falls back to the peer", Trusted: trusted, RemoteAddr: proxyPeer, Forwarded: []string{"198.51.100.3, 198.51.100.4"}, Client: proxyAddress, LimitKey: proxyAddress},
		{Name: "untrusted peer sending the header", Trusted: trusted, RemoteAddr: "203.0.113.10:41000", Forwarded: []string{"192.0.2.1"}, Client: client, LimitKey: client},
		{Name: "no trusted list ignores the header from a private peer", RemoteAddr: "10.0.0.8:41000", Forwarded: []string{client}, Client: "10.0.0.8", LimitKey: "10.0.0.8"},
		{Name: "malformed only entry falls back to the peer", Trusted: trusted, RemoteAddr: proxyPeer, Forwarded: []string{"not-an-address"}, Client: proxyAddress, LimitKey: proxyAddress},
		{Name: "malformed rightmost entry falls back to the peer", Trusted: trusted, RemoteAddr: proxyPeer, Forwarded: []string{client + ", unknown"}, Client: proxyAddress, LimitKey: proxyAddress},
		{Name: "malformed entry left of the client is ignored", Trusted: trusted, RemoteAddr: proxyPeer, Forwarded: []string{"garbage, " + client}, Client: client, LimitKey: client},
		{Name: "malformed entry behind a trusted hop falls back to the peer", Trusted: trusted, RemoteAddr: proxyPeer, Forwarded: []string{"junk, 198.51.100.3"}, Client: proxyAddress, LimitKey: proxyAddress},
		{Name: "space inside an address is malformed", Trusted: trusted, RemoteAddr: proxyPeer, Forwarded: []string{"203.0.113. 10"}, Client: proxyAddress, LimitKey: proxyAddress},
		{Name: "leading zeros are malformed", Trusted: trusted, RemoteAddr: proxyPeer, Forwarded: []string{"203.0.113.010"}, Client: proxyAddress, LimitKey: proxyAddress},
		{Name: "only separators fall back to the peer", Trusted: trusted, RemoteAddr: proxyPeer, Forwarded: []string{" , "}, Client: proxyAddress, LimitKey: proxyAddress},
		{Name: "trailing empty entry falls back to the peer", Trusted: trusted, RemoteAddr: proxyPeer, Forwarded: []string{client + ","}, Client: proxyAddress, LimitKey: proxyAddress},
		{Name: "leading empty entry is ignored", Trusted: trusted, RemoteAddr: proxyPeer, Forwarded: []string{"," + client}, Client: client, LimitKey: client},
		{Name: "surrounding whitespace is trimmed", Trusted: trusted, RemoteAddr: proxyPeer, Forwarded: []string{" \t" + client + " \t"}, Client: client, LimitKey: client},
		{Name: "IPv4 with a port is malformed", Trusted: trusted, RemoteAddr: proxyPeer, Forwarded: []string{client + ":5555"}, Client: proxyAddress, LimitKey: proxyAddress},
		{Name: "bracketed IPv6 with a port is malformed", Trusted: trusted, RemoteAddr: proxyPeer, Forwarded: []string{"[2001:db8::10]:5555"}, Client: proxyAddress, LimitKey: proxyAddress},
		{Name: "bracketed IPv6 without a port is malformed", Trusted: trusted, RemoteAddr: proxyPeer, Forwarded: []string{"[2001:db8::10]"}, Client: proxyAddress, LimitKey: proxyAddress},
		{Name: "RFC 7239 Forwarded and X-Real-IP are not read", Trusted: trusted, RemoteAddr: proxyPeer, Headers: map[string]string{"Forwarded": "for=192.0.2.1", "X-Real-IP": "192.0.2.2"}, Client: proxyAddress, LimitKey: proxyAddress},
		{Name: "IPv4-mapped trusted peer is trusted", Trusted: trusted, RemoteAddr: "[::ffff:198.51.100.2]:41000", Forwarded: []string{client}, Client: client, LimitKey: client},
		{Name: "IPv4-mapped header address is unmapped", Trusted: trusted, RemoteAddr: proxyPeer, Forwarded: []string{"::ffff:" + client}, Client: client, LimitKey: client},
		{Name: "IPv4-mapped trusted hop is skipped", Trusted: trusted, RemoteAddr: proxyPeer, Forwarded: []string{client + ", ::ffff:198.51.100.3"}, Client: client, LimitKey: client},
		{Name: "IPv4-mapped untrusted peer keeps its form and keys as IPv4", Trusted: trusted, RemoteAddr: "[::ffff:203.0.113.10]:41000", Client: "::ffff:203.0.113.10", LimitKey: client},
		{Name: "IPv4-mapped trusted range does not match", Trusted: []string{"::ffff:198.51.100.0/120"}, RemoteAddr: proxyPeer, Forwarded: []string{client}, Client: proxyAddress, LimitKey: proxyAddress},
		{Name: "IPv6 peer keys by its /64", Trusted: trusted, RemoteAddr: "[2001:db8:1:2::5]:41000", Client: "2001:db8:1:2::5", LimitKey: "2001:db8:1:2::/64"},
		{Name: "IPv6 client behind an IPv6 proxy", Trusted: []string{"2001:db8:ffff::/48"}, RemoteAddr: "[2001:db8:ffff::2]:41000", Forwarded: []string{"2001:db8:1:2::99"}, Client: "2001:db8:1:2::99", LimitKey: "2001:db8:1:2::/64"},
		{Name: "zoned peer is never trusted", Trusted: []string{"fe80::/64"}, RemoteAddr: "[fe80::1%eth0]:41000", Forwarded: []string{client}, Client: "fe80::1%eth0", LimitKey: "fe80::/64"},
		{Name: "zoned header address keeps its zone and keys without it", Trusted: trusted, RemoteAddr: proxyPeer, Forwarded: []string{"fe80::1%eth0"}, Client: "fe80::1%eth0", LimitKey: "fe80::/64"},
		{Name: "zoned header hop inside the trusted range is not skipped", Trusted: []string{proxyNetwork, "fe80::/64"}, RemoteAddr: proxyPeer, Forwarded: []string{client + ", fe80::1%eth0"}, Client: "fe80::1%eth0", LimitKey: "fe80::/64"},
		{Name: "very long forged header keeps the rightmost client", Trusted: trusted, RemoteAddr: proxyPeer, Forwarded: []string{longForged}, Client: client, LimitKey: client},
		{Name: "very long trusted header falls back to the peer", Trusted: trusted, RemoteAddr: proxyPeer, Forwarded: []string{longTrusted}, Client: proxyAddress, LimitKey: proxyAddress},
		{Name: "many header lines read only the first", Trusted: trusted, RemoteAddr: proxyPeer, Forwarded: manyLines, Client: "192.0.2.66", LimitKey: "192.0.2.66"},
		{Name: "a proxy appending its own line is ignored", Trusted: trusted, RemoteAddr: proxyPeer, Forwarded: []string{"192.0.2.66", client}, Client: "192.0.2.66", LimitKey: "192.0.2.66"},
		{Name: "a trusted hop on a later line is not consulted", Trusted: trusted, RemoteAddr: proxyPeer, Forwarded: []string{client, "198.51.100.3"}, Client: client, LimitKey: client},
		{Name: "uppercase IPv6 is normalised", Trusted: trusted, RemoteAddr: proxyPeer, Forwarded: []string{"2001:DB8:0:A::B"}, Client: "2001:db8:0:a::b", LimitKey: "2001:db8:0:a::/64"},
		{Name: "expanded IPv6 is normalised", Trusted: trusted, RemoteAddr: proxyPeer, Forwarded: []string{"2001:0db8:0000:0000:0000:0000:0000:000a"}, Client: "2001:db8::a", LimitKey: "2001:db8::/64"},
		{Name: "uppercase IPv6 peer keeps its form and keys normalised", Trusted: trusted, RemoteAddr: "[2001:DB8::A]:41000", Client: "2001:DB8::A", LimitKey: "2001:db8::/64"},
		{Name: "private header address is used", Trusted: trusted, RemoteAddr: proxyPeer, Forwarded: []string{"10.0.0.5"}, Client: "10.0.0.5", LimitKey: "10.0.0.5"},
		{Name: "loopback header address is used", Trusted: trusted, RemoteAddr: proxyPeer, Forwarded: []string{"127.0.0.1"}, Client: "127.0.0.1", LimitKey: "127.0.0.1"},
		{Name: "IPv6 loopback peer", RemoteAddr: "[::1]:41000", Client: "::1", LimitKey: "::/64"},
		{Name: "trusted loopback proxy", Trusted: []string{"127.0.0.1/32"}, RemoteAddr: "127.0.0.1:41000", Forwarded: []string{client}, Client: client, LimitKey: client},
		{Name: "wide trusted range skips every hop inside it", Trusted: []string{"10.0.0.0/8"}, RemoteAddr: "10.1.2.3:41000", Forwarded: []string{client + ", 10.200.0.1"}, Client: client, LimitKey: client},
		{Name: "wide trusted range swallows an internal client", Trusted: []string{"10.0.0.0/8"}, RemoteAddr: "10.1.2.3:41000", Forwarded: []string{"10.9.9.9"}, Client: "10.1.2.3", LimitKey: "10.1.2.3"},
		{Name: "a range covering every IPv4 address trusts every hop", Trusted: []string{"0.0.0.0/0"}, RemoteAddr: "203.0.113.200:41000", Forwarded: []string{"192.0.2.66, " + client}, Client: "203.0.113.200", LimitKey: "203.0.113.200"},
		{Name: "peer without a port is used as given", Trusted: trusted, RemoteAddr: client, Client: client, LimitKey: client},
		{Name: "unparseable peer is used as given", Trusted: trusted, RemoteAddr: "pipe", Forwarded: []string{"192.0.2.1"}, Client: "pipe", LimitKey: "pipe"},
		{Name: "empty peer is used as given", Trusted: trusted, RemoteAddr: "", Forwarded: []string{"192.0.2.1"}, Client: "", LimitKey: ""},
	}
}
