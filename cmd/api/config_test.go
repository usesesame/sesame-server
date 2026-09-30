package main

import (
	"testing"

	"usesesame.app/backend/internal/httpapi"
)

func TestParseTrustedProxies(t *testing.T) {
	cases := []struct {
		name         string
		value        string
		want         []string
		wantWarnings []string
		wantErr      bool
	}{
		{name: "unset trusts nobody", value: "", want: nil},
		{name: "blank trusts nobody", value: "   ", want: nil},
		{name: "single range", value: "172.30.0.0/24", want: []string{"172.30.0.0/24"}},
		{name: "multiple ranges", value: "172.30.0.0/24, fd00::/64", want: []string{"172.30.0.0/24", "fd00::/64"}},
		{name: "masked to the network", value: "172.30.0.7/24", want: []string{"172.30.0.0/24"}},
		{name: "wide private range warns", value: "172.16.0.0/12", want: []string{"172.16.0.0/12"}, wantWarnings: []string{"trusted proxy range 172.16.0.0/12 is wider than /24; pin the proxy network if possible"}},
		{name: "loopback range accepted without warning", value: "127.0.0.0/8", want: []string{"127.0.0.0/8"}},
		{name: "loopback address accepted without warning", value: "127.0.0.1/32", want: []string{"127.0.0.1/32"}},
		{name: "ipv6 loopback accepted without warning", value: "::1/128", want: []string{"::1/128"}},
		{name: "unique local ipv6 accepted", value: "fd00::/64", want: []string{"fd00::/64"}},
		{name: "wide ipv6 range warns", value: "fd00::/32", want: []string{"fd00::/32"}, wantWarnings: []string{"trusted proxy range fd00::/32 is wider than /64; pin the proxy network if possible"}},
		{name: "link-local ipv6 range warns", value: "fe80::/10", want: []string{"fe80::/10"}, wantWarnings: []string{"trusted proxy range fe80::/10 is wider than /64; pin the proxy network if possible"}},
		{name: "whole ipv4 refused", value: "0.0.0.0/0", wantErr: true},
		{name: "whole ipv6 refused", value: "::/0", wantErr: true},
		{name: "half of ipv4 refused", value: "0.0.0.0/1", wantErr: true},
		{name: "other half of ipv4 refused", value: "128.0.0.0/1", wantErr: true},
		{name: "ipv4-mapped ipv6 refused", value: "::ffff:0.0.0.0/96", wantErr: true},
		{name: "range crossing out of private space refused", value: "10.0.0.0/7", wantErr: true},
		{name: "range ending outside private space refused", value: "192.168.0.0/15", wantErr: true},
		{name: "documentation range refused", value: "2001:db8::/64", wantErr: true},
		{name: "missing prefix refused", value: "172.30.0.1", wantErr: true},
		{name: "malformed refused", value: "not-a-cidr", wantErr: true},
		{name: "bad prefix length refused", value: "172.30.0.0/33", wantErr: true},
		{name: "empty element refused", value: "172.30.0.0/24,", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, warnings, err := parseTrustedProxies(tc.value)
			if (err != nil) != tc.wantErr {
				t.Fatalf("parseTrustedProxies(%q) error = %v, wantErr %v", tc.value, err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if len(got) != len(tc.want) {
				t.Fatalf("parseTrustedProxies(%q) = %v, want %v", tc.value, got, tc.want)
			}
			for index, prefix := range got {
				if prefix.String() != tc.want[index] {
					t.Fatalf("parseTrustedProxies(%q)[%d] = %s, want %s", tc.value, index, prefix, tc.want[index])
				}
			}
			if len(warnings) != len(tc.wantWarnings) {
				t.Fatalf("parseTrustedProxies(%q) warnings = %v, want %v", tc.value, warnings, tc.wantWarnings)
			}
			for index, warning := range warnings {
				if warning != tc.wantWarnings[index] {
					t.Fatalf("parseTrustedProxies(%q) warnings[%d] = %q, want %q", tc.value, index, warning, tc.wantWarnings[index])
				}
			}
		})
	}
}

func TestValidateAdminOriginRefusesACollapsedPortalBoundary(t *testing.T) {
	if err := validateAdminOrigin("https://admin.example.invalid", "https://account.example.invalid"); err != nil {
		t.Fatalf("distinct origins must be accepted, got %v", err)
	}
	if err := validateAdminOrigin("", "https://account.example.invalid"); err != nil {
		t.Fatalf("an absent admin origin must be accepted, got %v", err)
	}
	if err := validateAdminOrigin("https://account.example.invalid", "https://account.example.invalid"); err == nil {
		t.Fatal("an admin origin equal to the account origin must be refused")
	}
}

func TestDeploymentProfileFromEnvironment(t *testing.T) {
	for value, expected := range map[string]httpapi.DeploymentProfile{
		"":         httpapi.DeploymentProfileOperator,
		"operator": httpapi.DeploymentProfileOperator,
		"project":  httpapi.DeploymentProfileProject,
	} {
		t.Setenv("SESAME_DEPLOYMENT_PROFILE", value)
		profile, err := deploymentProfileFromEnvironment()
		if err != nil || profile != expected {
			t.Fatalf("SESAME_DEPLOYMENT_PROFILE=%q = %q, %v; want %q", value, profile, err, expected)
		}
	}
	t.Setenv("SESAME_DEPLOYMENT_PROFILE", "staging")
	if _, err := deploymentProfileFromEnvironment(); err == nil {
		t.Fatal("an unknown deployment profile must stop startup")
	}
}

func TestSupportNotifyAddressValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		value   string
		want    string
		wantErr bool
	}{
		{name: "empty", value: "", want: "", wantErr: false},
		{name: "plain address", value: "support@example.invalid", want: "support@example.invalid", wantErr: false},
		{name: "display name", value: "Sesame Support <support@example.invalid>", want: "Sesame Support <support@example.invalid>", wantErr: false},
		{name: "trimmed", value: "  support@example.invalid  ", want: "support@example.invalid", wantErr: false},
		{name: "not an address", value: "not-an-address", want: "", wantErr: true},
		{name: "header injection", value: "support@example.invalid\r\nBcc: attacker@example.invalid", want: "", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := supportNotifyAddress(tc.value)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("address = %q, want %q", got, tc.want)
			}
		})
	}
}
