package install

import (
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
)

const (
	profileLocal     = "local"
	profileCaddy     = "caddy"
	profileTailscale = "tailscale"

	fixedProxyRange = "172.31.88.0/24"

	maxVersionLength     = 128
	maxHostLength        = 253
	maxToolVersionLength = 128
	maxDirectoryLength   = 4096

	fileMode = "0600"
)

var internalSuffixes = map[string]bool{
	"alt":         true,
	"arpa":        true,
	"corp":        true,
	"example":     true,
	"home":        true,
	"internal":    true,
	"intranet":    true,
	"invalid":     true,
	"int":         true,
	"lan":         true,
	"local":       true,
	"localdomain": true,
	"localhost":   true,
	"onion":       true,
	"private":     true,
	"test":        true,
}

type specification struct {
	profile        string
	version        string
	host           string
	toolVersion    string
	directory      string
	publicURL      string
	trustedProxies []string
	prerequisites  []Prerequisite
}

func validateOptions(options Options) (specification, error) {
	profile, err := parseProfile(options.Profile)
	if err != nil {
		return specification{}, err
	}
	version, err := parseVersion(options.Version)
	if err != nil {
		return specification{}, err
	}
	host, err := parseHost(profile, options.Host)
	if err != nil {
		return specification{}, err
	}
	toolVersion, err := parseToolVersion(options.ToolVersion)
	if err != nil {
		return specification{}, err
	}
	directory, err := parseDirectory(options.Directory)
	if err != nil {
		return specification{}, err
	}

	spec := specification{
		profile:        profile,
		version:        version,
		host:           host,
		toolVersion:    toolVersion,
		directory:      directory,
		trustedProxies: []string{},
	}
	if profile == profileLocal {
		spec.publicURL = "http://localhost:8787"
	} else {
		spec.publicURL = "https://" + host
		spec.trustedProxies = []string{fixedProxyRange}
	}
	spec.prerequisites = profilePrerequisites(profile)
	return spec, nil
}

func parseProfile(value string) (string, error) {
	switch value {
	case "":
		return "", errors.New("install: profile is required")
	case profileLocal, profileCaddy, profileTailscale:
		return value, nil
	default:
		return "", fmt.Errorf("install: profile %q is not local, caddy or tailscale", value)
	}
}

func parseVersion(value string) (string, error) {
	if value == "" {
		return "", errors.New("install: version is required")
	}
	if len(value) > maxVersionLength {
		return "", fmt.Errorf("install: version is longer than %d bytes", maxVersionLength)
	}
	if strings.Contains(value, "+") {
		return "", errors.New("install: version must not contain build metadata")
	}
	core, prerelease, hasPrerelease := strings.Cut(value, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return "", errors.New("install: version must have three numeric components")
	}
	for _, part := range parts {
		if !decimal(part) {
			return "", errors.New("install: version must have three numeric components")
		}
		if len(part) > 1 && part[0] == '0' {
			return "", errors.New("install: version components must not have leading zeros")
		}
	}
	if hasPrerelease {
		if prerelease == "" {
			return "", errors.New("install: version prerelease is empty")
		}
		for _, identifier := range strings.Split(prerelease, ".") {
			if !prereleaseIdentifier(identifier) {
				return "", errors.New("install: version prerelease is not valid SemVer")
			}
		}
	}
	return value, nil
}

func decimal(value string) bool {
	if value == "" {
		return false
	}
	for index := 0; index < len(value); index++ {
		if value[index] < '0' || value[index] > '9' {
			return false
		}
	}
	return true
}

func prereleaseIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if character >= '0' && character <= '9' || character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character == '-' {
			continue
		}
		return false
	}
	if decimal(value) && len(value) > 1 && value[0] == '0' {
		return false
	}
	return true
}

func parseHost(profile, value string) (string, error) {
	if profile == profileLocal {
		if value != "" {
			return "", errors.New("install: host is not allowed with the local profile")
		}
		return "", nil
	}
	if value == "" {
		return "", fmt.Errorf("install: host is required with the %s profile", profile)
	}
	if len(value) > maxHostLength {
		return "", fmt.Errorf("install: host is longer than %d bytes", maxHostLength)
	}
	labels := strings.Split(value, ".")
	if !validHostLabels(labels) {
		return "", fmt.Errorf("install: host %q is not a lowercase ASCII DNS name", value)
	}
	switch profile {
	case profileCaddy:
		if err := validateCaddyHost(value, labels); err != nil {
			return "", err
		}
	case profileTailscale:
		if err := validateTailscaleHost(value, labels); err != nil {
			return "", err
		}
	}
	return value, nil
}

func validHostLabels(labels []string) bool {
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 {
			return false
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for index := 0; index < len(label); index++ {
			character := label[index]
			if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-' {
				continue
			}
			return false
		}
	}
	return true
}

func validateCaddyHost(value string, labels []string) error {
	if len(labels) < 2 {
		return fmt.Errorf("install: host %q must be a public looking DNS name", value)
	}
	if net.ParseIP(value) != nil {
		return fmt.Errorf("install: host %q must not be an IP address", value)
	}
	suffix := labels[len(labels)-1]
	if len(suffix) < 2 || !letters(suffix) {
		return fmt.Errorf("install: host %q must end in a public looking suffix", value)
	}
	if internalSuffixes[suffix] {
		return fmt.Errorf("install: host %q uses an internal or special use name", value)
	}
	return nil
}

func validateTailscaleHost(value string, labels []string) error {
	if len(labels) < 4 || labels[len(labels)-2] != "ts" || labels[len(labels)-1] != "net" {
		return fmt.Errorf("install: host %q must end in .ts.net with a machine and tailnet name", value)
	}
	return nil
}

func letters(value string) bool {
	for index := 0; index < len(value); index++ {
		if value[index] < 'a' || value[index] > 'z' {
			return false
		}
	}
	return true
}

func parseToolVersion(value string) (string, error) {
	if value == "" {
		return "", errors.New("install: tool version is required")
	}
	if len(value) > maxToolVersionLength {
		return "", fmt.Errorf("install: tool version is longer than %d bytes", maxToolVersionLength)
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if character >= '0' && character <= '9' || character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || strings.ContainsRune(".-+_()", rune(character)) {
			continue
		}
		return "", errors.New("install: tool version contains unsupported characters")
	}
	return value, nil
}

func parseDirectory(value string) (string, error) {
	if value == "" {
		return "", errors.New("install: directory is required")
	}
	if len(value) > maxDirectoryLength {
		return "", fmt.Errorf("install: directory path is longer than %d bytes", maxDirectoryLength)
	}
	for _, character := range value {
		if character == 0 || character < 0x20 || character == 0x7f || character >= 0x80 && character <= 0x9f {
			return "", errors.New("install: directory contains control characters")
		}
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("install: directory cannot be resolved: %w", err)
	}
	base := filepath.Base(absolute)
	if base == "" || base == "." || base == ".." || base == string(filepath.Separator) {
		return "", fmt.Errorf("install: directory %q must name a new child directory", value)
	}
	return absolute, nil
}

func profilePrerequisites(profile string) []Prerequisite {
	items := []Prerequisite{
		{ID: "docker", State: "unknown", Message: "Docker with the Compose plugin has not been checked."},
		{ID: "port_8787", State: "unknown", Message: "Whether 127.0.0.1:8787 is free has not been checked."},
		{ID: "subnet", State: "unknown", Message: "Whether 172.31.88.0/24 is free has not been checked."},
	}
	switch profile {
	case profileCaddy:
		items = append(items,
			Prerequisite{ID: "dns", State: "unknown", Message: "Whether the configured host resolves to this machine has not been checked."},
			Prerequisite{ID: "tls", State: "unknown", Message: "Whether Caddy can obtain a certificate for the configured host has not been checked."},
		)
	case profileTailscale:
		items = append(items,
			Prerequisite{ID: "tailnet", State: "unknown", Message: "Whether Tailscale is signed in with MagicDNS enabled has not been checked."},
			Prerequisite{ID: "https", State: "unknown", Message: "Whether Tailscale HTTPS is enabled for the tailnet has not been checked."},
		)
	}
	return items
}
