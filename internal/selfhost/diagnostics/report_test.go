//go:build linux

package diagnostics

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"usesesame.app/backend/internal/selfhost/config"
)

func TestInspectValidOfflineReport(t *testing.T) {
	f := newFixture(t)
	report := f.inspect()

	if report.SchemaVersion != 1 {
		t.Fatalf("schemaVersion = %d", report.SchemaVersion)
	}
	if report.ToolVersion != "0.4.1-test" {
		t.Fatalf("toolVersion = %q", report.ToolVersion)
	}
	if report.GeneratedAt != "2026-11-07T10:34:56Z" {
		t.Fatalf("generatedAt = %q", report.GeneratedAt)
	}
	if report.Mode != ModeOffline {
		t.Fatalf("mode = %q", report.Mode)
	}
	if report.Overall != StateUnknown {
		t.Fatalf("overall = %q", report.Overall)
	}
	if report.Config == nil {
		t.Fatal("config is null")
	}
	if report.Config.PublicURL != "https://vault.example.test" {
		t.Fatalf("publicUrl = %q", report.Config.PublicURL)
	}
	if report.Config.PublicURLSource != "config_file" {
		t.Fatalf("publicUrlSource = %q", report.Config.PublicURLSource)
	}
	if report.Config.ListenScope != "loopback" {
		t.Fatalf("listenScope = %q", report.Config.ListenScope)
	}
	if report.Config.ListenPort != 8787 {
		t.Fatalf("listenPort = %d", report.Config.ListenPort)
	}
	if report.Config.TrustedProxies == nil || len(report.Config.TrustedProxies) != 0 {
		t.Fatalf("trustedProxies = %#v", report.Config.TrustedProxies)
	}
	if report.Config.DataDirectorySource != "environment" {
		t.Fatalf("dataDirectorySource = %q", report.Config.DataDirectorySource)
	}
	if report.Config.ConfigFile != "loaded" {
		t.Fatalf("configFile = %q", report.Config.ConfigFile)
	}
	if report.Checks == nil {
		t.Fatal("checks are null")
	}
	if got, want := checkIDs(report), []string{
		"configuration",
		"origin",
		"listen",
		"proxy_trust",
		"config_file",
		"data_directory",
		"secrets",
		"database",
		"space",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("check order = %v, want %v", got, want)
	}
	assertCheck(t, report, "configuration", StateOK, "configuration_valid")
	assertCheck(t, report, "origin", StateOK, "origin_https")
	assertCheck(t, report, "listen", StateOK, "listen_loopback")
	assertCheck(t, report, "proxy_trust", StateUnknown, "proxy_trust_empty")
	assertCheck(t, report, "config_file", StateOK, "config_file_private")
	assertCheck(t, report, "data_directory", StateOK, "data_directory_private")
	assertCheck(t, report, "secrets", StateOK, "secrets_metadata_ok")
	assertCheck(t, report, "database", StateUnknown, "database_not_opened")
	assertCheck(t, report, "space", StateOK, "space_sufficient")
	if got := checkByID(t, report, "database").Next; got == nil {
		t.Fatal("database next is null")
	}
	wantLimitations := []string{
		"clock_accuracy_not_verified",
		"database_not_opened",
		"network_not_probed",
		"port_availability_not_tested",
		"proxy_path_not_observed",
		"host_mapping_not_visible",
		"secrets_contents_not_read",
		"service_context_not_proven",
		"write_access_not_tested",
	}
	if !reflect.DeepEqual(report.Limitations, wantLimitations) {
		t.Fatalf("limitations = %v", report.Limitations)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	assertNoPrivateMarkers(t, string(encoded))
	for _, private := range []string{f.root, f.dataDir, f.configPath, f.secretsDir, f.databasePath, "SESAME_"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("output contains private value %q", private)
		}
	}
}

func TestReportJSONExactSchema(t *testing.T) {
	report := newFixture(t).inspect()
	assertTags(t, Report{}, []string{
		`json:"schemaVersion"`,
		`json:"toolVersion"`,
		`json:"generatedAt"`,
		`json:"mode"`,
		`json:"overall"`,
		`json:"config"`,
		`json:"checks"`,
		`json:"limitations"`,
	})
	assertTags(t, ConfigSummary{}, []string{
		`json:"publicUrl"`,
		`json:"publicUrlSource"`,
		`json:"listenScope"`,
		`json:"listenPort"`,
		`json:"trustedProxies"`,
		`json:"dataDirectorySource"`,
		`json:"configFile"`,
	})
	assertTags(t, Check{}, []string{
		`json:"id"`,
		`json:"state"`,
		`json:"code"`,
		`json:"summary"`,
		`json:"next"`,
	})
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	object := jsonObject(t, encoded)
	if len(object) != 8 {
		t.Fatalf("top-level keys = %v", object)
	}
	for _, key := range []string{"schemaVersion", "toolVersion", "generatedAt", "mode", "overall", "config", "checks", "limitations"} {
		if _, ok := object[key]; !ok {
			t.Fatalf("top-level key %q is missing", key)
		}
	}
	if string(object["config"]) == "null" {
		t.Fatal("config is null")
	}
	configObject := jsonObject(t, object["config"])
	if len(configObject) != 7 {
		t.Fatalf("config keys = %v", configObject)
	}
	if got := string(configObject["trustedProxies"]); got != "[]" {
		t.Fatalf("trustedProxies = %s", got)
	}
	if got := string(object["limitations"]); !strings.HasPrefix(got, "[") {
		t.Fatalf("limitations = %s", got)
	}
	checkData, err := json.Marshal(checkByID(t, report, "configuration"))
	if err != nil {
		t.Fatal(err)
	}
	checkObject := jsonObject(t, checkData)
	if len(checkObject) != 5 {
		t.Fatalf("check keys = %v", checkObject)
	}
	if got := string(checkObject["next"]); got != "null" {
		t.Fatalf("configuration next = %s", got)
	}
	proxyData, err := json.Marshal(checkByID(t, report, "proxy_trust"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(jsonObject(t, proxyData)["next"]); got == "null" {
		t.Fatal("proxy_trust next is null")
	}
	invalid := invalidConfigurationReport(Report{
		SchemaVersion: SchemaVersion,
		ToolVersion:   "test",
		GeneratedAt:   "2026-11-07T10:34:56Z",
		Mode:          ModeOffline,
		Limitations:   append([]string{}, limitationCodes...),
	})
	invalidData, err := json.Marshal(invalid)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(jsonObject(t, invalidData)["config"]); got != "null" {
		t.Fatalf("invalid config = %s", got)
	}
}

func assertTags(t *testing.T, value any, want []string) {
	t.Helper()
	typ := reflect.TypeOf(value)
	if typ.NumField() != len(want) {
		t.Fatalf("%s has %d fields, want %d", typ, typ.NumField(), len(want))
	}
	for index, tag := range want {
		if got := string(typ.Field(index).Tag); got != tag {
			t.Fatalf("%s field %d tag = %s, want %s", typ, index, got, tag)
		}
	}
}

func jsonObject(t *testing.T, data []byte) map[string]json.RawMessage {
	t.Helper()
	value := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestToolVersionSanitization(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  string
	}{
		{name: "empty", value: "", want: "unknown"},
		{name: "ordinary", value: "0.4.1", want: "0.4.1"},
		{name: "maximum", value: strings.Repeat("v", 128), want: strings.Repeat("v", 128)},
		{name: "too long", value: strings.Repeat("v", 129), want: "unknown"},
		{name: "newline", value: "v1\n2", want: "unknown"},
		{name: "tab", value: "v1\t2", want: "unknown"},
		{name: "non ascii", value: "v1é", want: "unknown"},
		{name: "printable space", value: "v1 2", want: "v1 2"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := safeToolVersion(testCase.value); got != testCase.want {
				t.Fatalf("safeToolVersion = %q, want %q", got, testCase.want)
			}
		})
	}
	f := newFixture(t)
	f.env[config.EnvAddr] = "127.0.0.1:8787"
	report := inspect(context.Background(), Options{
		Lookup:      f.lookup(),
		ToolVersion: strings.Repeat("v", 129),
		Now:         fixedNow,
	}, testFiles(1<<40, nil))
	if report.ToolVersion != "unknown" {
		t.Fatalf("report toolVersion = %q", report.ToolVersion)
	}
}

func TestClockIsCalledOnceAtSecondPrecision(t *testing.T) {
	f := newFixture(t)
	calls := 0
	report := inspect(context.Background(), Options{
		Lookup:      f.lookup(),
		ToolVersion: "test",
		Now: func() time.Time {
			calls++
			return time.Date(2026, 11, 7, 23, 59, 59, 999_999_999, time.UTC)
		},
	}, testFiles(1<<40, nil))
	if calls != 1 {
		t.Fatalf("clock calls = %d", calls)
	}
	if report.GeneratedAt != "2026-11-07T23:59:59Z" {
		t.Fatalf("generatedAt = %q", report.GeneratedAt)
	}
	if strings.Contains(report.GeneratedAt, ".") {
		t.Fatalf("generatedAt has fractional seconds: %q", report.GeneratedAt)
	}
}

func TestNilLookupIsEmpty(t *testing.T) {
	t.Setenv(config.EnvDataDir, "/fictional/ambient/path")
	lookup := effectiveLookup(nil)
	if value, ok := lookup(config.EnvDataDir); ok {
		t.Fatalf("nil lookup returned %q", value)
	}
	if value, ok := lookup(config.EnvSMTPAuth); ok {
		t.Fatalf("nil lookup returned %q", value)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	report := Inspect(ctx, Options{})
	if report.Overall != StateUnknown {
		t.Fatalf("overall = %q", report.Overall)
	}
	assertCheck(t, report, "inspection", StateUnknown, "interrupted")
}

func TestInvalidConfigurationIsRedacted(t *testing.T) {
	cases := []struct {
		name     string
		document []byte
	}{
		{
			name:     "invalid json",
			document: []byte(`{"publicUrl":"https://vault.example.test","smtp":{"password":"` + configMarker + `"},"broken":`),
		},
		{
			name:     "oversized file",
			document: bytes.Repeat([]byte(configMarker+"-"), 100_001),
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			f := newFixture(t)
			mustWrite(t, f.configPath, testCase.document, 0o644)
			report := Inspect(context.Background(), Options{
				Lookup:      f.lookup(),
				ToolVersion: "0.4.1-test",
				Now:         fixedNow,
			})
			if report.Overall != StateAction {
				t.Fatalf("overall = %q", report.Overall)
			}
			if report.Config != nil {
				t.Fatalf("config = %#v", report.Config)
			}
			if len(report.Checks) != 1 {
				t.Fatalf("checks = %v", checkIDs(report))
			}
			check := assertCheck(t, report, "configuration", StateAction, "configuration_invalid")
			if check.Next == nil {
				t.Fatal("next is null")
			}
			if !strings.Contains(check.Summary, "private path or credential") {
				t.Fatalf("summary = %q", check.Summary)
			}
			encoded, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			assertNoPrivateMarkers(t, string(encoded))
			if strings.Contains(string(encoded), f.root) {
				t.Fatalf("output contains the fixture path")
			}
			if strings.Contains(string(encoded), "readable by other users") {
				t.Fatalf("output contains a raw warning")
			}
		})
	}
}

func TestGroupReadableConfigIsActionWithEnvironmentPassword(t *testing.T) {
	f := newFixture(t)
	if err := os.Chmod(f.configPath, 0o644); err != nil {
		t.Fatal(err)
	}
	f.env[config.EnvSMTPAuth] = environmentMarker
	report := f.inspect()
	if report.Overall != StateAction {
		t.Fatalf("overall = %q", report.Overall)
	}
	assertCheck(t, report, "configuration", StateOK, "configuration_valid")
	assertCheck(t, report, "config_file", StateAction, "config_file_permissions")
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	assertNoPrivateMarkers(t, string(encoded))
	if strings.Contains(string(encoded), "readable by other users") {
		t.Fatalf("output contains a raw warning")
	}
}

func TestConfigurationReportLimits(t *testing.T) {
	longOrigin := "https://" + strings.Repeat("a", 2041)
	cases := []struct {
		name     string
		document string
	}{
		{
			name:     "public origin over bound",
			document: mustJSON(t, map[string]any{"publicUrl": longOrigin}),
		},
		{
			name: "too many proxy entries",
			document: mustJSON(t, map[string]any{
				"publicUrl":      "https://vault.example.test",
				"trustedProxies": repeatString("127.0.0.1/32", 65),
			}),
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			f := newFixture(t)
			f.writeConfig(testCase.document)
			report := Inspect(context.Background(), Options{
				Lookup:      f.lookup(),
				ToolVersion: "0.4.1-test",
				Now:         fixedNow,
			})
			if report.Overall != StateAction {
				t.Fatalf("overall = %q", report.Overall)
			}
			if report.Config != nil {
				t.Fatalf("config = %#v", report.Config)
			}
			if len(report.Checks) != 1 {
				t.Fatalf("checks = %v", checkIDs(report))
			}
			assertCheck(t, report, "configuration", StateAction, "configuration_report_limit")
			encoded, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), longOrigin) {
				t.Fatal("output contains the overlong origin")
			}
			if strings.Contains(string(encoded), f.root) {
				t.Fatal("output contains the fixture path")
			}
		})
	}
}

func TestConfigurationReportBoundsAccepted(t *testing.T) {
	f := newFixture(t)
	f.writeConfig(mustJSON(t, map[string]any{
		"publicUrl":      "https://" + strings.Repeat("a", 2040),
		"trustedProxies": repeatString("127.0.0.1/32", 64),
	}))
	report := f.inspect()
	if report.Config == nil {
		t.Fatalf("config is null: %v", checkIDs(report))
	}
	if len(report.Config.PublicURL) != 2048 {
		t.Fatalf("publicUrl length = %d", len(report.Config.PublicURL))
	}
	if len(report.Config.TrustedProxies) != 64 {
		t.Fatalf("trustedProxies length = %d", len(report.Config.TrustedProxies))
	}
	assertCheck(t, report, "configuration", StateOK, "configuration_valid")
	assertCheck(t, report, "proxy_trust", StateOK, "proxy_trust_configured")
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func repeatString(value string, count int) []string {
	values := make([]string, count)
	for index := range values {
		values[index] = value
	}
	return values
}

func TestCheckMessageStyle(t *testing.T) {
	constructors := []func() Check{
		configurationValidCheck,
		originHTTPSCheck,
		originLocalCheck,
		listenLoopbackCheck,
		listenAllInterfacesCheck,
		listenNonLoopbackCheck,
		proxyTrustEmptyHTTPSCheck,
		proxyTrustEmptyLocalCheck,
		proxyTrustConfiguredCheck,
		proxyTrustWideCheck,
		configFileAbsentCheck,
		configFilePrivateCheck,
		configFilePermissionsCheck,
		configFileSymlinkCheck,
		configFilePathChangedCheck,
		configFileMetadataUnavailableCheck,
		configFileACLUnknownCheck,
		dataDirectoryPrivateCheck,
		dataDirectoryMissingCheck,
		dataDirectoryWrongTypeCheck,
		dataDirectorySymlinkCheck,
		dataDirectoryPermissionsCheck,
		dataDirectoryPathChangedCheck,
		dataDirectoryMetadataUnavailableCheck,
		dataDirectoryACLUnknownCheck,
		secretsMetadataOKCheck,
		secretsMissingCheck,
		secretsUninitializedCheck,
		secretsNotInspectedCheck,
		secretsDatabaseUnknownCheck,
		secretsDirectoryUnsafeCheck,
		secretsDirectoryPermissionsCheck,
		secretsSymlinkCheck,
		secretsWrongTypeCheck,
		secretsSizeCheck,
		secretsPermissionsCheck,
		secretsPathChangedCheck,
		secretsMetadataUnavailableCheck,
		secretsACLUnknownCheck,
		databaseMissingCheck,
		databaseNotOpenedCheck,
		databaseWrongTypeCheck,
		databaseSymlinkCheck,
		databaseEmptyCheck,
		databasePermissionsCheck,
		databasePathChangedCheck,
		databaseMetadataUnavailableCheck,
		databaseNotInspectedCheck,
		spaceCriticalCheck,
		spaceTightCheck,
		spaceSufficientCheck,
		spaceUnavailableCheck,
		spaceDatabaseUnknownCheck,
	}
	empty := Report{
		SchemaVersion: SchemaVersion,
		ToolVersion:   "test",
		GeneratedAt:   "2026-11-07T10:34:56Z",
		Mode:          ModeOffline,
		Limitations:   append([]string{}, limitationCodes...),
	}
	special := []Check{
		interruptedReport(empty).Checks[0],
		invalidConfigurationReport(empty).Checks[0],
		reportLimitReport(empty).Checks[0],
	}
	all := make([]Check, 0, len(constructors)+len(special))
	for _, constructor := range constructors {
		all = append(all, constructor())
	}
	all = append(all, special...)
	for _, check := range all {
		if check.ID == "" || check.Code == "" || check.Summary == "" {
			t.Fatalf("incomplete check %#v", check)
		}
		if check.State != StateOK && check.State != StateAction && check.State != StateUnknown {
			t.Fatalf("check %s state = %q", check.ID, check.State)
		}
		if strings.ToLower(check.Code) != check.Code || strings.ContainsAny(check.Code, " \t\n") {
			t.Fatalf("check %s code = %q", check.ID, check.Code)
		}
		validateMessage(t, check.ID, check.Summary)
		if check.Next != nil {
			validateMessage(t, check.ID+" next", *check.Next)
		}
	}
}

func validateMessage(t *testing.T, name, value string) {
	t.Helper()
	if !strings.HasSuffix(value, ".") {
		t.Fatalf("%s does not end with a full stop: %q", name, value)
	}
	for _, forbidden := range []string{"(", ")", "—", "–", "  "} {
		if strings.Contains(value, forbidden) {
			t.Fatalf("%s contains %q: %q", name, forbidden, value)
		}
	}
	if strings.Contains(value, "\n") {
		t.Fatalf("%s contains a line break", name)
	}
}

func TestAggregate(t *testing.T) {
	cases := []struct {
		name   string
		states []string
		want   string
	}{
		{name: "empty", want: StateOK},
		{name: "ok", states: []string{StateOK, StateOK}, want: StateOK},
		{name: "unknown", states: []string{StateOK, StateUnknown}, want: StateUnknown},
		{name: "action", states: []string{StateOK, StateUnknown, StateAction}, want: StateAction},
		{name: "action first", states: []string{StateAction, StateUnknown}, want: StateAction},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			checks := make([]Check, len(testCase.states))
			for index, state := range testCase.states {
				checks[index] = Check{ID: fmt.Sprintf("check-%d", index), State: state, Code: "code", Summary: "Summary."}
			}
			if got := aggregate(checks); got != testCase.want {
				t.Fatalf("aggregate = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestSourceAndConfigFileEnums(t *testing.T) {
	if got := sourceName(config.SourceDefault); got != "default" {
		t.Fatalf("default source = %q", got)
	}
	if got := sourceName(config.SourceEnvironment); got != "environment" {
		t.Fatalf("environment source = %q", got)
	}
	if got := sourceName(config.SourceConfigFile); got != "config_file" {
		t.Fatalf("config file source = %q", got)
	}
	if got := configFileState(true); got != "loaded" {
		t.Fatalf("loaded config file = %q", got)
	}
	if got := configFileState(false); got != "absent" {
		t.Fatalf("absent config file = %q", got)
	}
}

func TestListenFactsAndScopeEnums(t *testing.T) {
	cases := []struct {
		host  string
		scope string
	}{
		{host: "", scope: "all"},
		{host: "localhost", scope: "loopback"},
		{host: "127.0.0.1", scope: "loopback"},
		{host: "::1", scope: "loopback"},
		{host: "0.0.0.0", scope: "all"},
		{host: "::", scope: "all"},
		{host: "10.0.0.1", scope: "private"},
		{host: "192.0.2.10", scope: "public"},
		{host: "169.254.1.1", scope: "other"},
		{host: "not-an-address", scope: "other"},
	}
	for _, testCase := range cases {
		if got := scopeName(testCase.host); got != testCase.scope {
			t.Fatalf("scopeName(%q) = %q, want %q", testCase.host, got, testCase.scope)
		}
	}
	scope, port := listenFacts("[::1]:8787")
	if scope != "loopback" || port != 8787 {
		t.Fatalf("listenFacts = %q %d", scope, port)
	}
	scope, port = listenFacts("not an address")
	if scope != "other" || port != 0 {
		t.Fatalf("listenFacts fallback = %q %d", scope, port)
	}
}
