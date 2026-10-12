package config

import (
	"errors"
	"net"
	"net/netip"
	"strconv"
)

const DefaultAddr = "127.0.0.1:8787"

var errAddressShape = errors.New("must be host:port with an IP address or localhost as the host (or no host) and a port from 1 to 65535")

func splitListenAddress(value string) (host string, port int, err error) {
	rawHost, rawPort, splitErr := net.SplitHostPort(value)
	if splitErr != nil {
		return "", 0, errAddressShape
	}
	port, convErr := strconv.Atoi(rawPort)
	if convErr != nil || port < 1 || port > 65535 || rawPort != strconv.Itoa(port) {
		return "", 0, errAddressShape
	}
	if rawHost != "" && rawHost != "localhost" {
		parsed, parseErr := netip.ParseAddr(rawHost)
		if parseErr != nil || parsed.Zone() != "" {
			return "", 0, errAddressShape
		}
	}
	return rawHost, port, nil
}

func ValidateListenAddress(value string) error {
	_, _, err := splitListenAddress(value)
	return err
}

func ReadyURL(addr string) (string, error) {
	host, port, err := splitListenAddress(addr)
	if err != nil {
		return "", err
	}
	switch {
	case host == "":
		host = "127.0.0.1"
	case host != "localhost":
		parsed := netip.MustParseAddr(host)
		if parsed.IsUnspecified() {
			if parsed.Is4() {
				host = "127.0.0.1"
			} else {
				host = "::1"
			}
		}
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(port)) + "/readyz", nil
}

type Lookup func(string) (string, bool)

func HealthAddress(lookup Lookup) string {
	for _, name := range []string{EnvAddr, EnvHostedAddr} {
		if value, ok := lookup(name); ok && trimmed(value) != "" {
			return trimmed(value)
		}
	}
	return DefaultAddr
}
