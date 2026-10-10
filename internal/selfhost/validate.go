package selfhost

import (
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

func NormalizeName(value string) (string, bool) {
	name := strings.TrimSpace(value)
	if name == "" || utf8.RuneCountInString(name) > MaxNameLength || !utf8.ValidString(name) {
		return "", false
	}
	for _, character := range name {
		if unicode.IsControl(character) || character == unicode.ReplacementChar {
			return "", false
		}
	}
	return name, true
}

func NameKey(name string) string {
	return strings.ToLower(name)
}

func ValidPassword(value string) bool {
	return len(value) >= MinPasswordLength && len(value) <= MaxPasswordLength
}

func NormalizeDeviceName(value string) (string, bool) {
	name := strings.TrimSpace(value)
	if len(name) == 0 || len(name) > MaxNameLength {
		return "", false
	}
	for _, character := range name {
		if character < 0x20 || character > 0x7e {
			return "", false
		}
	}
	return name, true
}

func ValidPairingCode(value string) bool {
	return len(value) >= PairingCodeMinLength && len(value) <= PairingCodeMaxLength
}

func ValidDeviceMeta(meta DeviceMeta) bool {
	if meta.ProtocolVersion < 0 || meta.ProtocolVersion > 100 {
		return false
	}
	for _, value := range []string{meta.AppVersion, meta.Platform, meta.Architecture, meta.UpdateChannel} {
		if len(value) > MaxMetaLength || strings.ContainsAny(value, "\r\n\t") {
			return false
		}
	}
	return true
}

func NormalizePublicURL(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", true
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return "", false
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return "", false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
	case "http":
		if !LoopbackHost(host) {
			return "", false
		}
	default:
		return "", false
	}
	origin := url.URL{Scheme: strings.ToLower(parsed.Scheme), Host: strings.ToLower(parsed.Host)}
	return origin.String(), true
}

func LoopbackHost(host string) bool {
	host = strings.Trim(strings.ToLower(host), "[]")
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}
