package server_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"usesesame.app/backend/internal/selfhost"
	"usesesame.app/backend/internal/selfhost/server"
)

const diagnosticsWarningMarker = "fictional configuration warning /private/path"

func TestSystemDiagnosticsReport(t *testing.T) {
	e := newEnv(t, func(cfg *server.Config) { cfg.Warnings = []string{diagnosticsWarningMarker} })
	o := e.createFirstOwner("Dana")
	recorder := o.do(req{path: "/v1/owner/system/diagnostics"})
	expectStatus(t, recorder, http.StatusOK)
	report := decodeBody(t, recorder)
	if len(report) != 5 {
		t.Fatalf("top-level keys = %v", report)
	}
	for _, key := range []string{"schemaVersion", "generatedAt", "overall", "checks", "limitations"} {
		if _, ok := report[key]; !ok {
			t.Fatalf("top-level key %q is missing", key)
		}
	}

	if report["schemaVersion"] != float64(1) {
		t.Fatalf("schemaVersion = %v", report["schemaVersion"])
	}
	generatedAt, ok := report["generatedAt"].(string)
	if !ok || !strings.HasSuffix(generatedAt, "Z") || strings.Contains(generatedAt, ".") {
		t.Fatalf("generatedAt = %v", report["generatedAt"])
	}
	if _, err := time.Parse(time.RFC3339, generatedAt); err != nil {
		t.Fatalf("generatedAt is not RFC 3339: %v", err)
	}
	if report["overall"] != "action" {
		t.Fatalf("overall = %v", report["overall"])
	}
	if got, want := diagnosticsCheckIDs(t, report), []string{"origin", "proxy", "audit", "backup", "forwarding", "external"}; !equalStrings(got, want) {
		t.Fatalf("check order = %v, want %v", got, want)
	}
	expectDiagnosticsCheck(t, report, "origin", "ok", "origin_local")
	expectDiagnosticsCheck(t, report, "proxy", "ok", "proxy_not_configured")
	expectDiagnosticsCheck(t, report, "audit", "ok", "audit_incremental_ok")
	expectDiagnosticsCheck(t, report, "backup", "action", "backup_missing")
	expectDiagnosticsCheck(t, report, "forwarding", "ok", "forwarding_not_observed")
	expectDiagnosticsCheck(t, report, "external", "unknown", "external_not_inspected")

	limitations := anyStrings(report["limitations"])
	wantLimitations := []string{
		"certificate_validity_not_checked",
		"network_reachability_not_probed",
		"host_configuration_not_inspected",
		"audit_uses_incremental_verification",
		"backup_off_host_copy_not_verified",
		"recovery_drill_not_verified",
		"unseen_traffic_not_evaluated",
	}
	if !equalStrings(limitations, wantLimitations) {
		t.Fatalf("limitations = %v, want %v", limitations, wantLimitations)
	}
	for _, limitation := range limitations {
		if limitation == "" || strings.ContainsAny(limitation, " \t\r\n") || strings.ToLower(limitation) != limitation {
			t.Fatalf("limitation = %q", limitation)
		}
	}
	expectDiagnosticsNext(t, report, "origin", true)
	expectDiagnosticsNext(t, report, "proxy", false)
	expectDiagnosticsNext(t, report, "audit", false)
	expectDiagnosticsNext(t, report, "backup", true)
	expectDiagnosticsNext(t, report, "forwarding", false)
	expectDiagnosticsNext(t, report, "external", true)

	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{diagnosticsWarningMarker, o.id, "Dana", "/private/path", "SESAME_"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("diagnostics output contains %q", private)
		}
	}
	for _, check := range report["checks"].([]any) {
		object := check.(map[string]any)
		if len(object) != 5 {
			t.Fatalf("check keys = %v", object)
		}
		summary := object["summary"].(string)
		if !strings.HasSuffix(summary, ".") || len(summary) > 400 || strings.ContainsAny(summary, "\r\n()") {
			t.Fatalf("check %v summary = %q", object["id"], summary)
		}
		if next, ok := object["next"].(string); ok {
			if !strings.HasSuffix(next, ".") || len(next) > 400 || strings.ContainsAny(next, "\r\n()") {
				t.Fatalf("check %v next = %q", object["id"], next)
			}
		}
	}
}

func TestSystemDiagnosticsRefusesQueryAndBody(t *testing.T) {
	e := newEnv(t)
	o := e.createFirstOwner("Dana")
	baseFull, baseIncremental := verifyCounts(e)
	for _, path := range []string{
		"/v1/owner/system/diagnostics?full=true",
		"/v1/owner/system/diagnostics?full=",
		"/v1/owner/system/diagnostics?anything=1",
	} {
		expectError(t, o.do(req{path: path}), http.StatusBadRequest, "invalid_request")
	}
	expectError(t, o.do(req{method: http.MethodGet, path: "/v1/owner/system/diagnostics", body: map[string]string{"full": "true"}}), http.StatusBadRequest, "invalid_request")
	expectError(t, o.do(req{method: http.MethodPost, path: "/v1/owner/system/diagnostics"}), http.StatusMethodNotAllowed, "method_not_allowed")
	full, incremental := verifyCounts(e)
	if full != baseFull || incremental != baseIncremental {
		t.Fatalf("refused requests ran %d full and %d incremental audit checks", full-baseFull, incremental-baseIncremental)
	}
}

func TestSystemDiagnosticsUsesOwnerGuardAndIncrementalAudit(t *testing.T) {
	e := newEnv(t)
	o := e.createFirstOwner("Dana")
	baseFull, baseIncremental := verifyCounts(e)
	expectStatus(t, o.do(req{path: "/v1/owner/system/diagnostics"}), http.StatusOK)
	full, incremental := verifyCounts(e)
	if full != baseFull || incremental != baseIncremental+1 {
		t.Fatalf("diagnostics ran %d full and %d incremental audit checks", full-baseFull, incremental-baseIncremental)
	}
	expectError(t, o.do(req{path: "/v1/owner/system/diagnostics", host: "evil.example"}), http.StatusMisdirectedRequest, "host_mismatch")
	expectError(t, o.do(req{path: "/v1/owner/system/diagnostics", header: map[string]string{"Origin": "https://evil.example"}}), http.StatusForbidden, "origin_not_allowed")
	expectError(t, e.do(req{path: "/v1/owner/system/diagnostics"}), http.StatusUnauthorized, "not_authenticated")
}

func TestSystemDiagnosticsOriginAndProxyStates(t *testing.T) {
	originCases := []struct {
		name   string
		secure bool
		state  string
		code   string
	}{
		{name: "loopback http", secure: false, state: "ok", code: "origin_local"},
		{name: "https", secure: true, state: "ok", code: "origin_https"},
	}
	for _, testCase := range originCases {
		t.Run("origin "+testCase.name, func(t *testing.T) {
			options := []option{}
			if testCase.secure {
				options = append(options, withPublicURL("https://vault.example.test"))
			}
			e := newEnv(t, options...)
			o := e.createFirstOwner("Dana")
			report := decodeBody(t, o.do(req{path: "/v1/owner/system/diagnostics"}))
			expectDiagnosticsCheck(t, report, "origin", testCase.state, testCase.code)
		})
	}

	proxyCases := []struct {
		name    string
		secure  bool
		proxies []string
		state   string
		code    string
	}{
		{name: "none local", secure: false, state: "ok", code: "proxy_not_configured"},
		{name: "none https", secure: true, state: "unknown", code: "proxy_not_configured"},
		{name: "configured", secure: true, proxies: []string{"10.0.0.0/24"}, state: "ok", code: "proxy_configured"},
		{name: "wide ipv4", secure: true, proxies: []string{"10.0.0.0/8"}, state: "action", code: "proxy_wide"},
		{name: "wide ipv6", secure: true, proxies: []string{"fd00::/32"}, state: "action", code: "proxy_wide"},
		{name: "loopback wide accepted", secure: true, proxies: []string{"127.0.0.0/8"}, state: "ok", code: "proxy_configured"},
	}
	for _, testCase := range proxyCases {
		t.Run("proxy "+testCase.name, func(t *testing.T) {
			options := []option{}
			if testCase.secure {
				options = append(options, withPublicURL("https://vault.example.test"))
			}
			if len(testCase.proxies) > 0 {
				options = append(options, withProxies(testCase.proxies...))
			}
			e := newEnv(t, options...)
			o := e.createFirstOwner("Dana")
			report := decodeBody(t, o.do(req{path: "/v1/owner/system/diagnostics"}))
			expectDiagnosticsCheck(t, report, "proxy", testCase.state, testCase.code)
		})
	}
}

func TestSystemDiagnosticsBackupTransitions(t *testing.T) {
	e := newEnv(t)
	o := e.createFirstOwner("Dana")
	now := e.clock.Now()
	offset := func(duration time.Duration) *time.Time {
		at := now.Add(duration)
		return &at
	}
	cases := []struct {
		name     string
		backupAt *time.Time
		interval time.Duration
		state    string
		code     string
	}{
		{name: "missing with schedule", backupAt: nil, interval: 24 * time.Hour, state: "action", code: "backup_missing"},
		{name: "missing without schedule", backupAt: nil, interval: 0, state: "action", code: "backup_missing_schedule_off"},
		{name: "fresh", backupAt: offset(-time.Hour), interval: 24 * time.Hour, state: "ok", code: "backup_recorded"},
		{name: "at twice the interval", backupAt: offset(-48 * time.Hour), interval: 24 * time.Hour, state: "ok", code: "backup_recorded"},
		{name: "stale", backupAt: offset(-48*time.Hour - time.Second), interval: 24 * time.Hour, state: "action", code: "backup_stale"},
		{name: "future", backupAt: offset(time.Hour), interval: 24 * time.Hour, state: "unknown", code: "backup_future"},
		{name: "recorded without schedule", backupAt: offset(-time.Hour), interval: 0, state: "unknown", code: "backup_recorded_schedule_off"},
		{name: "future without schedule", backupAt: offset(time.Hour), interval: 0, state: "unknown", code: "backup_future"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			e.store.backupAt = testCase.backupAt
			e.cfg.BackupInterval = testCase.interval
			e.handler = server.New(e.cfg)
			report := decodeBody(t, o.do(req{path: "/v1/owner/system/diagnostics"}))
			expectDiagnosticsCheck(t, report, "backup", testCase.state, testCase.code)
		})
	}
}

func TestSystemDiagnosticsOverallState(t *testing.T) {
	e := newEnv(t, withPublicURL("https://vault.example.test"), withProxies("10.0.0.0/24"))
	o := e.createFirstOwner("Dana")
	at := e.clock.Now()
	e.store.backupAt = &at
	report := decodeBody(t, o.do(req{path: "/v1/owner/system/diagnostics"}))
	if report["overall"] != "unknown" {
		t.Fatalf("overall with only external unknown = %v", report["overall"])
	}
	e.store.auditBreak = &selfhost.AuditBreak{Seq: 7, Reason: "fictional"}
	report = decodeBody(t, o.do(req{path: "/v1/owner/system/diagnostics"}))
	if report["overall"] != "action" {
		t.Fatalf("overall with an action = %v", report["overall"])
	}
}

func TestSystemDiagnosticsAuditBreakAndForwarding(t *testing.T) {
	e := newEnv(t)
	o := e.createFirstOwner("Dana")
	e.store.auditBreak = &selfhost.AuditBreak{Seq: 41, Reason: "fictional raw audit error /private/path"}
	report := decodeBody(t, o.do(req{path: "/v1/owner/system/diagnostics"}))
	expectDiagnosticsCheck(t, report, "audit", "action", "audit_incremental_break")
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"fictional raw audit error", "/private/path", "entry 41"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("diagnostics contains %q", private)
		}
	}

	expectDiagnosticsCheck(t, report, "forwarding", "ok", "forwarding_not_observed")
	e.do(req{method: http.MethodPost, path: "/v1/owner/login", remote: "10.0.0.8:1234", header: map[string]string{"X-Forwarded-For": "203.0.113.4"}, body: map[string]string{"name": "Nobody", "password": "wrong password here", "code": "000000"}})
	report = decodeBody(t, o.do(req{path: "/v1/owner/system/diagnostics"}))
	expectDiagnosticsCheck(t, report, "forwarding", "action", "forwarding_untrusted_observed")
	encoded, err = json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"203.0.113.4", "10.0.0.8"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("diagnostics contains the forwarding address %q", private)
		}
	}
}

func TestSystemWarningCodesStayParallelAndOrdered(t *testing.T) {
	markers := []string{"Configuration warning one.", "Configuration warning two."}
	e := newEnv(t, func(cfg *server.Config) { cfg.Warnings = markers })
	o := e.createFirstOwner("Dana")
	e.store.auditBreak = &selfhost.AuditBreak{Seq: 41, Reason: "the stored hash does not match the recomputed hash"}
	e.do(req{method: http.MethodPost, path: "/v1/owner/login", remote: "10.0.0.8:1234", header: map[string]string{"X-Forwarded-For": "203.0.113.4"}, body: map[string]string{"name": "Nobody", "password": "wrong password here", "code": "000000"}})
	system := decodeBody(t, o.do(req{path: "/v1/owner/system"}))

	warnings := anyStrings(system["warnings"])
	codes := anyStrings(system["warningCodes"])
	wantWarnings := []string{
		markers[0],
		markers[1],
		"The audit log chain breaks at entry 41: the stored hash does not match the recomputed hash.",
		"No backup has been made yet.",
		"Requests from a private address carry X-Forwarded-For, but that address is not a trusted proxy. Every visitor shares one rate limit until SESAME_TRUSTED_PROXIES is set.",
		"The public address is plain http on this machine, so pairing from another machine needs an https SESAME_PUBLIC_URL.",
	}
	wantCodes := []string{"configuration", "configuration", "audit", "backup", "forwarding", "origin"}
	if !equalStrings(warnings, wantWarnings) {
		t.Fatalf("warnings = %v", warnings)
	}
	if !equalStrings(codes, wantCodes) {
		t.Fatalf("warningCodes = %v", codes)
	}
	if len(warnings) != len(codes) {
		t.Fatalf("warning columns differ: %d warnings and %d codes", len(warnings), len(codes))
	}
}

func TestSystemWarningsKeepLegacyVariants(t *testing.T) {
	cases := []struct {
		name     string
		backupAt *time.Time
		interval time.Duration
		want     string
	}{
		{name: "stale", backupAt: timePointer(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)), interval: 24 * time.Hour, want: "No backup has been made in over twice the configured interval."},
		{name: "schedule off", backupAt: nil, interval: 0, want: "Scheduled backups are off and no backup has been made yet."},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			e := newEnv(t)
			o := e.createFirstOwner("Dana")
			e.store.backupAt = testCase.backupAt
			e.cfg.BackupInterval = testCase.interval
			e.handler = server.New(e.cfg)
			system := decodeBody(t, o.do(req{path: "/v1/owner/system"}))
			if !containsString(anyStrings(system["warnings"]), testCase.want) {
				t.Fatalf("warnings = %v", system["warnings"])
			}
			if !containsString(anyStrings(system["warningCodes"]), "backup") {
				t.Fatalf("warningCodes = %v", system["warningCodes"])
			}
		})
	}
}

func TestSystemWarningColumnsAreArraysWhenEmpty(t *testing.T) {
	e := newEnv(t, withPublicURL("https://vault.example.test"))
	o := e.createFirstOwner("Dana")
	at := e.clock.Now()
	e.store.backupAt = &at
	system := decodeBody(t, o.do(req{path: "/v1/owner/system"}))
	if warnings, ok := system["warnings"].([]any); !ok || len(warnings) != 0 {
		t.Fatalf("warnings = %v", system["warnings"])
	}
	if codes, ok := system["warningCodes"].([]any); !ok || len(codes) != 0 {
		t.Fatalf("warningCodes = %v", system["warningCodes"])
	}
}

func TestSystemDiagnosticsFailuresAreRedacted(t *testing.T) {
	e := newEnv(t)
	o := e.createFirstOwner("Dana")
	e.store.systemErr = errors.New("fictional private path /private/system.db")
	expectError(t, o.do(req{path: "/v1/owner/system/diagnostics"}), http.StatusServiceUnavailable, "unavailable")
	e.store.systemErr = nil
	e.store.auditErr = errors.New("fictional private path /private/audit.db")
	recorder := o.do(req{path: "/v1/owner/system/diagnostics"})
	expectError(t, recorder, http.StatusServiceUnavailable, "unavailable")
	if strings.Contains(recorder.Body.String(), "/private/") {
		t.Fatalf("failure leaked a path: %s", recorder.Body.String())
	}
}

func diagnosticsCheckIDs(t *testing.T, report map[string]any) []string {
	t.Helper()
	checks, ok := report["checks"].([]any)
	if !ok {
		t.Fatalf("checks = %v", report["checks"])
	}
	ids := make([]string, 0, len(checks))
	for _, check := range checks {
		object, ok := check.(map[string]any)
		if !ok {
			t.Fatalf("check = %v", check)
		}
		id, ok := object["id"].(string)
		if !ok {
			t.Fatalf("check id = %v", object["id"])
		}
		ids = append(ids, id)
	}
	return ids
}

func expectDiagnosticsCheck(t *testing.T, report map[string]any, id, state, code string) {
	t.Helper()
	checks, ok := report["checks"].([]any)
	if !ok {
		t.Fatalf("checks = %v", report["checks"])
	}
	for _, check := range checks {
		object := check.(map[string]any)
		if object["id"] != id {
			continue
		}
		if object["state"] != state || object["code"] != code {
			t.Fatalf("%s = %v", id, object)
		}
		return
	}
	t.Fatalf("no %s check in %v", id, report["checks"])
}

func expectDiagnosticsNext(t *testing.T, report map[string]any, id string, present bool) {
	t.Helper()
	checks := report["checks"].([]any)
	for _, check := range checks {
		object := check.(map[string]any)
		if object["id"] != id {
			continue
		}
		_, hasNext := object["next"].(string)
		if hasNext != present {
			t.Fatalf("%s next present = %t, want %t: %v", id, hasNext, present, object)
		}
		return
	}
	t.Fatalf("no %s check in %v", id, report["checks"])
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func timePointer(value time.Time) *time.Time { return &value }
