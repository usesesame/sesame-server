package server

import (
	"math"
	"net/netip"
	"testing"
	"time"
)

func TestClassifyBackupBoundaries(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	interval := 24 * time.Hour
	cases := []struct {
		name     string
		last     *time.Time
		interval time.Duration
		want     backupKind
	}{
		{name: "missing", last: nil, interval: interval, want: backupMissing},
		{name: "fresh", last: internalTimePointer(now.Add(-time.Hour)), interval: interval, want: backupRecorded},
		{name: "at twice the interval", last: internalTimePointer(now.Add(-2 * interval)), interval: interval, want: backupRecorded},
		{name: "just stale", last: internalTimePointer(now.Add(-2*interval - time.Nanosecond)), interval: interval, want: backupStale},
		{name: "future", last: internalTimePointer(now.Add(time.Nanosecond)), interval: interval, want: backupFuture},
		{name: "missing with no schedule", last: nil, interval: 0, want: backupMissingScheduleOff},
		{name: "no schedule", last: internalTimePointer(now.Add(-time.Hour)), interval: 0, want: backupRecordedScheduleOff},
		{name: "future with no schedule", last: internalTimePointer(now.Add(time.Nanosecond)), interval: 0, want: backupFuture},
		{name: "largest interval does not overflow", last: internalTimePointer(now.Add(-time.Hour)), interval: time.Duration(math.MaxInt64), want: backupRecorded},
		{name: "oldest time does not overflow", last: internalTimePointer(time.Time{}), interval: time.Duration(math.MaxInt64), want: backupRecorded},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := classifyBackup(testCase.last, now, testCase.interval); got != testCase.want {
				t.Fatalf("classifyBackup = %d, want %d", got, testCase.want)
			}
		})
	}
}

func TestClassifyProxyRanges(t *testing.T) {
	cases := []struct {
		name     string
		prefixes []string
		want     proxyTrustKind
	}{
		{name: "none", want: proxyNotConfigured},
		{name: "narrow ipv4", prefixes: []string{"10.0.0.0/24"}, want: proxyConfigured},
		{name: "wide ipv4", prefixes: []string{"10.0.0.0/8"}, want: proxyWide},
		{name: "narrow ipv6", prefixes: []string{"fd00::/64"}, want: proxyConfigured},
		{name: "wide ipv6", prefixes: []string{"fd00::/32"}, want: proxyWide},
		{name: "loopback wide", prefixes: []string{"127.0.0.0/8"}, want: proxyConfigured},
		{name: "invalid prefix is wide", prefixes: []string{"invalid"}, want: proxyWide},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			prefixes := make([]netip.Prefix, 0, len(testCase.prefixes))
			for _, raw := range testCase.prefixes {
				prefix, err := netip.ParsePrefix(raw)
				if err != nil {
					prefixes = append(prefixes, netip.Prefix{})
					continue
				}
				prefixes = append(prefixes, prefix)
			}
			if got := classifyProxy(prefixes); got != testCase.want {
				t.Fatalf("classifyProxy = %d, want %d", got, testCase.want)
			}
		})
	}
}

func internalTimePointer(value time.Time) *time.Time { return &value }

func TestAggregateDiagnosticsPrecedence(t *testing.T) {
	check := func(state string) diagnosticsCheck {
		return diagnosticsCheck{ID: "test", State: state, Code: "test", Summary: "Test."}
	}
	cases := []struct {
		name   string
		states []string
		want   string
	}{
		{name: "empty", want: diagnosticsOK},
		{name: "ok", states: []string{diagnosticsOK, diagnosticsOK}, want: diagnosticsOK},
		{name: "unknown", states: []string{diagnosticsOK, diagnosticsUnknown}, want: diagnosticsUnknown},
		{name: "action after unknown", states: []string{diagnosticsUnknown, diagnosticsAction}, want: diagnosticsAction},
		{name: "action before unknown", states: []string{diagnosticsAction, diagnosticsUnknown}, want: diagnosticsAction},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			checks := make([]diagnosticsCheck, 0, len(testCase.states))
			for _, state := range testCase.states {
				checks = append(checks, check(state))
			}
			if got := aggregateDiagnostics(checks); got != testCase.want {
				t.Fatalf("aggregateDiagnostics = %q, want %q", got, testCase.want)
			}
		})
	}
}
