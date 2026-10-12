package config

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
)

func ParseTrustedProxies(name, value string) ([]netip.Prefix, []string, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil, nil
	}
	prefixes := make([]netip.Prefix, 0)
	warnings := make([]string, 0)
	for _, raw := range strings.Split(value, ",") {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err != nil {
			return nil, nil, errors.New(name + " must contain comma separated CIDR ranges such as 172.30.0.0/24")
		}
		prefix = prefix.Masked()
		if prefix.Addr().Is4In6() {
			return nil, nil, errors.New(name + " must not contain IPv4-mapped IPv6 ranges")
		}
		if !proxyAddress(prefix.Addr()) || !proxyAddress(lastAddress(prefix)) {
			return nil, nil, errors.New(name + " must stay inside loopback, private, or link-local addresses")
		}
		if warning := proxyWidthWarning(prefix); warning != "" {
			warnings = append(warnings, warning)
		}
		prefixes = append(prefixes, prefix)
	}
	return prefixes, warnings, nil
}

func proxyWidthWarning(prefix netip.Prefix) string {
	if prefix.Addr().IsLoopback() {
		return ""
	}
	limit := 64
	if prefix.Addr().Is4() {
		limit = 24
	}
	if prefix.Bits() >= limit {
		return ""
	}
	return fmt.Sprintf("trusted proxy range %s is wider than /%d; pin the proxy network if possible", prefix, limit)
}

func proxyAddress(addr netip.Addr) bool {
	return addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast()
}

func lastAddress(prefix netip.Prefix) netip.Addr {
	addr := prefix.Addr()
	raw := addr.As16()
	start := prefix.Bits()
	if addr.Is4() {
		start += 96
	}
	for bit := start; bit < 128; bit++ {
		raw[bit/8] |= 1 << (7 - bit%8)
	}
	if addr.Is4() {
		var raw4 [4]byte
		copy(raw4[:], raw[12:])
		return netip.AddrFrom4(raw4)
	}
	return netip.AddrFrom16(raw)
}
