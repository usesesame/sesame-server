package clientaddr

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

type Address struct {
	Client            string
	Peer              string
	ForwardingIgnored bool
}

func Resolve(r *http.Request, trusted []netip.Prefix) Address {
	peer := Peer(r)
	forwarded := strings.Join(r.Header.Values("X-Forwarded-For"), ",")
	if !trustedProxy(trusted, peer) {
		return Address{Client: peer, Peer: peer, ForwardingIgnored: forwarded != ""}
	}
	return Address{Client: forwardedClient(forwarded, peer, trusted), Peer: peer}
}

func forwardedClient(forwarded, peer string, trusted []netip.Prefix) string {
	if forwarded == "" {
		return peer
	}
	hops := strings.Split(forwarded, ",")
	for index := len(hops) - 1; index >= 0; index-- {
		ip, err := netip.ParseAddr(strings.TrimSpace(hops[index]))
		if err != nil {
			return peer
		}
		ip = ip.Unmap()
		if trustedProxy(trusted, ip.String()) {
			continue
		}
		return ip.String()
	}
	return peer
}

func (a Address) LimitKey() string {
	address, err := netip.ParseAddr(a.Client)
	if err != nil {
		return a.Client
	}
	address = address.Unmap()
	if !address.Is6() {
		return address.String()
	}
	prefix, err := address.Prefix(64)
	if err != nil {
		return a.Client
	}
	return prefix.String()
}

func Peer(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func trustedProxy(trusted []netip.Prefix, value string) bool {
	ip, err := netip.ParseAddr(value)
	if err != nil {
		return false
	}
	ip = ip.Unmap()
	for _, prefix := range trusted {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}
