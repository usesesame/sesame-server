package main

import (
	"testing"

	"usesesame.app/backend/internal/httpapi"
)

func TestParseTrustedProxies(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		want    []string
		wantErr bool
	}{
		{name: "unset trusts nobody", value: "", want: nil},
		{name: "blank trusts nobody", value: "   ", want: nil},
		{name: "single range", value: "172.30.0.0/24", want: []string{"172.30.0.0/24"}},
		{name: "multiple ranges", value: "172.30.0.0/24, 2001:db8::/64", want: []string{"172.30.0.0/24", "2001:db8::/64"}},
		{name: "masked to the network", value: "172.30.0.7/24", want: []string{"172.30.0.0/24"}},
		{name: "whole ipv4 refused", value: "0.0.0.0/0", wantErr: true},
		{name: "whole ipv6 refused", value: "::/0", wantErr: true},
		{name: "missing prefix refused", value: "172.30.0.1", wantErr: true},
		{name: "malformed refused", value: "not-a-cidr", wantErr: true},
		{name: "bad prefix length refused", value: "172.30.0.0/33", wantErr: true},
		{name: "empty element refused", value: "172.30.0.0/24,", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseTrustedProxies(tc.value)
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
