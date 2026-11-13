package install

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestParseVersion(t *testing.T) {
	cases := []struct {
		name  string
		value string
		valid bool
	}{
		{name: "release", value: "0.4.1", valid: true},
		{name: "zeros", value: "0.0.0", valid: true},
		{name: "large components", value: "10.20.30", valid: true},
		{name: "prerelease", value: "1.2.3-rc.1", valid: true},
		{name: "numeric prerelease", value: "1.2.3-0", valid: true},
		{name: "hyphen prerelease", value: "1.2.3-alpha-1", valid: true},
		{name: "alphanumeric leading zero", value: "1.2.3-01a", valid: true},
		{name: "long prerelease", value: "1.2.3-" + strings.Repeat("a", 100), valid: true},
		{name: "maximum length", value: strings.Repeat("1", 124) + ".2.3", valid: true},
		{name: "empty", value: "", valid: false},
		{name: "leading v", value: "v0.4.1", valid: false},
		{name: "leading upper v", value: "V0.4.1", valid: false},
		{name: "two components", value: "0.4", valid: false},
		{name: "four components", value: "0.4.1.2", valid: false},
		{name: "leading zero major", value: "01.2.3", valid: false},
		{name: "leading zero minor", value: "1.02.3", valid: false},
		{name: "leading zero patch", value: "1.2.03", valid: false},
		{name: "build metadata", value: "1.2.3+meta", valid: false},
		{name: "empty prerelease", value: "1.2.3-", valid: false},
		{name: "empty prerelease identifier", value: "1.2.3-alpha..1", valid: false},
		{name: "leading zero numeric prerelease", value: "1.2.3-01", valid: false},
		{name: "dollar injection", value: "1.2.3-$x", valid: false},
		{name: "space prefix", value: " 1.2.3", valid: false},
		{name: "space suffix", value: "1.2.3 ", valid: false},
		{name: "underscore", value: "1.2.3-rc_1", valid: false},
		{name: "slash", value: "1.2.3-rc/1", valid: false},
		{name: "newline", value: "1.2.3\n", valid: false},
		{name: "unicode", value: "1.2.3-α", valid: false},
		{name: "too long", value: strings.Repeat("1", 126) + ".2.3", valid: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := parseVersion(testCase.value)
			if testCase.valid {
				if err != nil {
					t.Fatalf("parseVersion(%q) returned %v", testCase.value, err)
				}
				if got != testCase.value {
					t.Fatalf("parseVersion(%q) returned %q", testCase.value, got)
				}
				return
			}
			if err == nil {
				t.Fatalf("parseVersion(%q) succeeded", testCase.value)
			}
		})
	}
}

func TestParseHost(t *testing.T) {
	longValid := strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + ".com"
	longInvalid := strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 63) + ".com"
	cases := []struct {
		name    string
		profile string
		host    string
		valid   bool
	}{
		{name: "caddy", profile: profileCaddy, host: "vault.example.com", valid: true},
		{name: "caddy country suffix", profile: profileCaddy, host: "sesame.example.co.uk", valid: true},
		{name: "caddy internationalized suffix uses manual setup", profile: profileCaddy, host: "server.example.xn--p1ai", valid: false},
		{name: "caddy long valid", profile: profileCaddy, host: longValid, valid: true},
		{name: "caddy empty", profile: profileCaddy, host: "", valid: false},
		{name: "caddy uppercase", profile: profileCaddy, host: "Vault.Example.com", valid: false},
		{name: "caddy localhost", profile: profileCaddy, host: "localhost", valid: false},
		{name: "caddy localhost suffix", profile: profileCaddy, host: "vault.localhost", valid: false},
		{name: "caddy local suffix", profile: profileCaddy, host: "vault.local", valid: false},
		{name: "caddy internal suffix", profile: profileCaddy, host: "vault.internal", valid: false},
		{name: "caddy private suffix", profile: profileCaddy, host: "vault.private", valid: false},
		{name: "caddy int suffix", profile: profileCaddy, host: "vault.int", valid: false},
		{name: "caddy test suffix", profile: profileCaddy, host: "vault.test", valid: false},
		{name: "caddy example suffix", profile: profileCaddy, host: "vault.example", valid: false},
		{name: "caddy invalid suffix", profile: profileCaddy, host: "vault.invalid", valid: false},
		{name: "caddy ipv4", profile: profileCaddy, host: "192.0.2.10", valid: false},
		{name: "caddy numeric suffix", profile: profileCaddy, host: "vault.123", valid: false},
		{name: "caddy single label", profile: profileCaddy, host: "vault", valid: false},
		{name: "caddy leading hyphen", profile: profileCaddy, host: "-vault.example.com", valid: false},
		{name: "caddy trailing hyphen", profile: profileCaddy, host: "vault-.example.com", valid: false},
		{name: "caddy empty label", profile: profileCaddy, host: "vault..example.com", valid: false},
		{name: "caddy trailing dot", profile: profileCaddy, host: "vault.example.com.", valid: false},
		{name: "caddy label too long", profile: profileCaddy, host: strings.Repeat("a", 64) + ".example.com", valid: false},
		{name: "caddy too long", profile: profileCaddy, host: longInvalid, valid: false},
		{name: "caddy underscore", profile: profileCaddy, host: "vault_name.example.com", valid: false},
		{name: "caddy path", profile: profileCaddy, host: "vault.example.com/path", valid: false},
		{name: "caddy port", profile: profileCaddy, host: "vault.example.com:443", valid: false},
		{name: "caddy dollar", profile: profileCaddy, host: "$HOST.example.com", valid: false},
		{name: "caddy newline", profile: profileCaddy, host: "vault.example.com\n", valid: false},
		{name: "caddy unicode", profile: profileCaddy, host: "vault.example.cöm", valid: false},
		{name: "tailscale", profile: profileTailscale, host: "vault.example-tailnet.ts.net", valid: true},
		{name: "tailscale dotted tailnet", profile: profileTailscale, host: "my-laptop.example.com.ts.net", valid: true},
		{name: "tailscale short", profile: profileTailscale, host: "a.b.ts.net", valid: true},
		{name: "tailscale no tailnet", profile: profileTailscale, host: "vault.ts.net", valid: false},
		{name: "tailscale suffix only", profile: profileTailscale, host: "ts.net", valid: false},
		{name: "tailscale uppercase", profile: profileTailscale, host: "vault.tailnet.ts.NET", valid: false},
		{name: "tailscale trailing dot", profile: profileTailscale, host: "vault.tailnet.ts.net.", valid: false},
		{name: "tailscale wrong suffix", profile: profileTailscale, host: "vault.tailnet.example.com", valid: false},
		{name: "tailscale ipv4", profile: profileTailscale, host: "192.0.2.10", valid: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := parseHost(testCase.profile, testCase.host)
			if testCase.valid {
				if err != nil {
					t.Fatalf("parseHost(%q, %q) returned %v", testCase.profile, testCase.host, err)
				}
				if got != testCase.host {
					t.Fatalf("parseHost(%q, %q) returned %q", testCase.profile, testCase.host, got)
				}
				return
			}
			if err == nil {
				t.Fatalf("parseHost(%q, %q) succeeded", testCase.profile, testCase.host)
			}
		})
	}
}

func TestParseHostLocalRejectsHost(t *testing.T) {
	if _, err := parseHost(profileLocal, "vault.example.com"); err == nil {
		t.Fatal("local profile accepted a host")
	}
	got, err := parseHost(profileLocal, "")
	if err != nil || got != "" {
		t.Fatalf("local profile host returned %q, %v", got, err)
	}
}

func TestParseToolVersion(t *testing.T) {
	cases := []struct {
		name  string
		value string
		valid bool
	}{
		{name: "release", value: "0.4.1", valid: true},
		{name: "development", value: "0.1.0-dev", valid: true},
		{name: "metadata", value: "0.4.1+abcdef", valid: true},
		{name: "parentheses", value: "0.4.1(abcdef)", valid: true},
		{name: "empty", value: "", valid: false},
		{name: "command substitution", value: "$(id)", valid: false},
		{name: "backtick", value: "0.4.1`id`", valid: false},
		{name: "space", value: "0.4.1 build", valid: false},
		{name: "newline", value: "0.4.1\n", valid: false},
		{name: "long", value: strings.Repeat("1", 129), valid: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := parseToolVersion(testCase.value)
			if testCase.valid && err != nil {
				t.Fatalf("parseToolVersion(%q) returned %v", testCase.value, err)
			}
			if !testCase.valid && err == nil {
				t.Fatalf("parseToolVersion(%q) succeeded", testCase.value)
			}
		})
	}
}

func TestParseDirectory(t *testing.T) {
	cases := []struct {
		name  string
		value string
		valid bool
	}{
		{name: "relative", value: "sesame", valid: true},
		{name: "nested", value: "one/two/sesame", valid: true},
		{name: "dollar", value: "$(id)", valid: true},
		{name: "empty", value: "", valid: false},
		{name: "nul", value: "one\x00two", valid: false},
		{name: "newline", value: "one\ntwo", valid: false},
		{name: "delete", value: "one\x7ftwo", valid: false},
		{name: "too long", value: strings.Repeat("a", 4097), valid: false},
		{name: "root", value: string(filepath.Separator), valid: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := parseDirectory(testCase.value)
			if testCase.valid {
				if err != nil {
					t.Fatalf("parseDirectory(%q) returned %v", testCase.value, err)
				}
				if !filepath.IsAbs(got) {
					t.Fatalf("parseDirectory(%q) returned non-absolute %q", testCase.value, got)
				}
				return
			}
			if err == nil {
				t.Fatalf("parseDirectory(%q) succeeded with %q", testCase.value, got)
			}
		})
	}
}

func TestValidateOptionsProfilesAndRequiredValues(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "sesame")
	cases := []struct {
		name    string
		options Options
		valid   bool
	}{
		{name: "local", options: Options{Profile: "local", Version: "0.4.1", Directory: directory, ToolVersion: "0.4.1"}, valid: true},
		{name: "caddy", options: Options{Profile: "caddy", Version: "0.4.1", Host: "vault.example.com", Directory: directory, ToolVersion: "0.4.1"}, valid: true},
		{name: "tailscale", options: Options{Profile: "tailscale", Version: "0.4.1", Host: "vault.tailnet.ts.net", Directory: directory, ToolVersion: "0.4.1"}, valid: true},
		{name: "empty profile", options: Options{Version: "0.4.1", Directory: directory, ToolVersion: "0.4.1"}, valid: false},
		{name: "unknown profile", options: Options{Profile: "nginx", Version: "0.4.1", Directory: directory, ToolVersion: "0.4.1"}, valid: false},
		{name: "local host", options: Options{Profile: "local", Version: "0.4.1", Host: "vault.example.com", Directory: directory, ToolVersion: "0.4.1"}, valid: false},
		{name: "caddy no host", options: Options{Profile: "caddy", Version: "0.4.1", Directory: directory, ToolVersion: "0.4.1"}, valid: false},
		{name: "tailscale no host", options: Options{Profile: "tailscale", Version: "0.4.1", Directory: directory, ToolVersion: "0.4.1"}, valid: false},
		{name: "bad version", options: Options{Profile: "local", Version: "0.4.1+meta", Directory: directory, ToolVersion: "0.4.1"}, valid: false},
		{name: "no directory", options: Options{Profile: "local", Version: "0.4.1", ToolVersion: "0.4.1"}, valid: false},
		{name: "no tool version", options: Options{Profile: "local", Version: "0.4.1", Directory: directory}, valid: false},
		{name: "injected tool version", options: Options{Profile: "local", Version: "0.4.1", Directory: directory, ToolVersion: "$(id)"}, valid: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := validateOptions(testCase.options)
			if testCase.valid && err != nil {
				t.Fatalf("validateOptions returned %v", err)
			}
			if !testCase.valid && err == nil {
				t.Fatal("validateOptions succeeded")
			}
		})
	}
}
