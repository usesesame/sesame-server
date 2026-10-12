package config

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
)

type PublicURL struct {
	Origin string
	Host   string
	Secure bool
}

func loopbackName(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func ParsePublicURL(name, value string) (PublicURL, error) {
	value = strings.TrimSpace(value)
	shape := name + " must be an absolute origin such as https://sesame.example.net with no credentials, path, query, or fragment"
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.Opaque != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") || strings.Contains(parsed.Host, "%") {
		return PublicURL{}, errors.New(shape)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return PublicURL{}, errors.New(name + " must start with https:// (http:// is accepted only for localhost, 127.0.0.1 or ::1)")
	}
	hostname := strings.ToLower(parsed.Hostname())
	if hostname == "" {
		return PublicURL{}, errors.New(shape)
	}
	if scheme == "http" && !loopbackName(hostname) {
		return PublicURL{}, errors.New(name + " must use https:// unless the host is localhost, 127.0.0.1 or ::1, because sign-in tokens would cross the network unencrypted")
	}
	port := parsed.Port()
	if port != "" {
		number, convErr := strconv.Atoi(port)
		if convErr != nil || number < 1 || number > 65535 || port != strconv.Itoa(number) {
			return PublicURL{}, errors.New(name + " has a port outside 1 to 65535")
		}
	}
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	host := hostname
	if strings.Contains(hostname, ":") {
		host = "[" + hostname + "]"
	}
	if port != "" {
		host = net.JoinHostPort(hostname, port)
	}
	return PublicURL{Origin: scheme + "://" + host, Host: host, Secure: scheme == "https"}, nil
}
