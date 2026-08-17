package httpapi

import (
	"net/mail"
	"strings"
)

// Returns nothing on rejection, so a caller cannot put attacker-controlled
// text into a rate-limit key, a database lookup, or an email header.
func normalizedEmail(value string) (string, bool) {
	email := strings.ToLower(strings.TrimSpace(value))
	if len(email) == 0 || len(email) > 254 {
		return "", false
	}
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed.Address != email {
		return "", false
	}
	return email, true
}

func validPassword(value string) bool { return len(value) >= 12 && len(value) <= 1024 }

func validDeviceName(value string) bool {
	name := strings.TrimSpace(value)
	if len(name) == 0 || len(name) > 64 {
		return false
	}
	for _, character := range name {
		if character < 0x20 || character > 0x7e {
			return false
		}
	}
	return true
}

func validDeviceID(value string) bool {
	if len(value) != 32 || strings.Contains(value, "/") {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func validDesktopHeartbeat(input desktopHeartbeatRequest) bool {
	if input.ProtocolVersion < 1 || input.ProtocolVersion > 100 {
		return false
	}
	for _, value := range []string{input.AppVersion, input.Platform, input.Architecture, input.UpdateChannel} {
		if len(value) > 64 || strings.ContainsAny(value, "\r\n\t") {
			return false
		}
	}
	return true
}
