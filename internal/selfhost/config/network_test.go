package config

import (
	"strings"
	"testing"
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
		{name: "ipv6 loopback accepted without warning", value: "::1/128", want: []string{"::1/128"}},
		{name: "wide ipv6 range warns", value: "fd00::/32", want: []string{"fd00::/32"}, wantWarnings: []string{"trusted proxy range fd00::/32 is wider than /64; pin the proxy network if possible"}},
		{name: "whole ipv4 refused", value: "0.0.0.0/0", wantErr: true},
		{name: "whole ipv6 refused", value: "::/0", wantErr: true},
		{name: "half of ipv4 refused", value: "128.0.0.0/1", wantErr: true},
		{name: "ipv4-mapped ipv6 refused", value: "::ffff:0.0.0.0/96", wantErr: true},
		{name: "range crossing out of private space refused", value: "10.0.0.0/7", wantErr: true},
		{name: "range ending outside private space refused", value: "192.168.0.0/15", wantErr: true},
		{name: "documentation range refused", value: "2001:db8::/64", wantErr: true},
		{name: "public address refused", value: "8.8.8.0/24", wantErr: true},
		{name: "missing prefix refused", value: "172.30.0.1", wantErr: true},
		{name: "malformed refused", value: "not-a-cidr", wantErr: true},
		{name: "bad prefix length refused", value: "172.30.0.0/33", wantErr: true},
		{name: "empty element refused", value: "172.30.0.0/24,", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, warnings, err := ParseTrustedProxies(EnvTrustedProxies, tc.value)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tc.wantErr)
			}
			if err != nil && !strings.HasPrefix(err.Error(), EnvTrustedProxies) {
				t.Fatalf("error %q does not start with the variable name", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("prefixes = %v, want %v", got, tc.want)
			}
			for index, prefix := range got {
				if prefix.String() != tc.want[index] {
					t.Fatalf("prefix %d = %s, want %s", index, prefix, tc.want[index])
				}
			}
			if len(warnings) != len(tc.wantWarnings) {
				t.Fatalf("warnings = %v, want %v", warnings, tc.wantWarnings)
			}
			for index, warning := range warnings {
				if warning != tc.wantWarnings[index] {
					t.Fatalf("warning %d = %q, want %q", index, warning, tc.wantWarnings[index])
				}
			}
		})
	}
}

func TestReadyURL(t *testing.T) {
	cases := []struct {
		addr string
		want string
	}{
		{"127.0.0.1:8787", "http://127.0.0.1:8787/readyz"},
		{"0.0.0.0:8787", "http://127.0.0.1:8787/readyz"},
		{":9000", "http://127.0.0.1:9000/readyz"},
		{"[::]:9000", "http://[::1]:9000/readyz"},
		{"[::1]:9000", "http://[::1]:9000/readyz"},
		{"localhost:9000", "http://localhost:9000/readyz"},
		{"192.168.1.20:8787", "http://192.168.1.20:8787/readyz"},
	}
	for _, tc := range cases {
		got, err := ReadyURL(tc.addr)
		if err != nil || got != tc.want {
			t.Errorf("ReadyURL(%q) = %q, %v; want %q", tc.addr, got, err, tc.want)
		}
	}
	for _, bad := range []string{"", "8787", "127.0.0.1", "127.0.0.1:0", "127.0.0.1:99999", "example.net:80", "127.0.0.1:http", "[fe80::1%eth0]:80", "http://127.0.0.1:8787"} {
		if got, err := ReadyURL(bad); err == nil {
			t.Errorf("ReadyURL(%q) = %q, want an error", bad, got)
		}
	}
}

func TestHealthAddressPrefersTheSelfHostVariable(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"nothing set", nil, "127.0.0.1:8787"},
		{"hosted variable", map[string]string{EnvHostedAddr: "0.0.0.0:9000"}, "0.0.0.0:9000"},
		{"self-host variable", map[string]string{EnvAddr: "0.0.0.0:9100"}, "0.0.0.0:9100"},
		{"both set", map[string]string{EnvAddr: "0.0.0.0:9100", EnvHostedAddr: "0.0.0.0:9000"}, "0.0.0.0:9100"},
		{"blank self-host variable falls through", map[string]string{EnvAddr: " ", EnvHostedAddr: "0.0.0.0:9000"}, "0.0.0.0:9000"},
	}
	for _, tc := range cases {
		if got := HealthAddress(lookupFrom(tc.env)); got != tc.want {
			t.Errorf("%s: HealthAddress = %q, want %q", tc.name, got, tc.want)
		}
	}
}
