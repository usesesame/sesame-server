package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"usesesame.app/backend/internal/selfhost/config"
	"usesesame.app/backend/internal/selfhost/diagnostics"
	"usesesame.app/backend/internal/selfhost/secrets"
)

type doctorDocument struct {
	SchemaVersion int                   `json:"schemaVersion"`
	ToolVersion   string                `json:"toolVersion"`
	GeneratedAt   string                `json:"generatedAt"`
	Mode          string                `json:"mode"`
	Overall       string                `json:"overall"`
	Config        *doctorConfigDocument `json:"config"`
	Checks        []doctorCheckDocument `json:"checks"`
	Limitations   []string              `json:"limitations"`
}

type doctorConfigDocument struct {
	PublicURL           string   `json:"publicUrl"`
	PublicURLSource     string   `json:"publicUrlSource"`
	ListenScope         string   `json:"listenScope"`
	ListenPort          int      `json:"listenPort"`
	TrustedProxies      []string `json:"trustedProxies"`
	DataDirectorySource string   `json:"dataDirectorySource"`
	ConfigFile          string   `json:"configFile"`
}

type doctorCheckDocument struct {
	ID      string  `json:"id"`
	State   string  `json:"state"`
	Code    string  `json:"code"`
	Summary string  `json:"summary"`
	Next    *string `json:"next"`
}

type doctorFixture struct {
	root            string
	dataDir         string
	configPath      string
	configPassword  string
	environmentPass string
	databaseMarker  string
	secretMarkers   []string
}

type doctorTreeEntry struct {
	Mode    fs.FileMode
	Size    int64
	ModTime time.Time
	Digest  string
	Target  string
}

func TestParseDoctorArgs(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantJSON bool
		wantHelp bool
		wantErr  string
	}{
		{name: "no options"},
		{name: "json", args: []string{"--json"}, wantJSON: true},
		{name: "help long", args: []string{"--help"}, wantHelp: true},
		{name: "help short", args: []string{"-h"}, wantHelp: true},
		{name: "help single dash", args: []string{"-help"}, wantHelp: true},
		{name: "help with unknown option", args: []string{"--help", "--bogus"}, wantErr: "flag provided but not defined: --bogus"},
		{name: "help with positional", args: []string{"--help", "extra"}, wantErr: "takes no arguments"},
		{name: "help with network", args: []string{"--help", "--network"}, wantErr: "--network is not supported"},
		{name: "help with repeated json", args: []string{"--help", "--json", "--json"}, wantErr: "--json was given more than once"},
		{name: "repeated help", args: []string{"--help", "-h"}, wantErr: "help was given more than once"},
		{name: "repeated json", args: []string{"--json", "--json"}, wantErr: "--json was given more than once"},
		{name: "json value", args: []string{"--json=true"}, wantErr: "--json takes no value"},
		{name: "network", args: []string{"--network"}, wantErr: "--network is not supported"},
		{name: "network value", args: []string{"--network=true"}, wantErr: "--network is not supported"},
		{name: "unknown long", args: []string{"--bogus"}, wantErr: "flag provided but not defined: --bogus"},
		{name: "unknown short", args: []string{"-bogus"}, wantErr: "flag provided but not defined: -bogus"},
		{name: "positional", args: []string{"extra"}, wantErr: "takes no arguments"},
		{name: "dash positional", args: []string{"-"}, wantErr: "takes no arguments"},
		{name: "double dash positional", args: []string{"--", "--json"}, wantErr: "takes no arguments"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			flags, err := parseDoctorArgs(tc.args)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if tc.wantHelp {
				if !errors.Is(err, flag.ErrHelp) {
					t.Fatalf("error = %v, want flag.ErrHelp", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if flags.JSON != tc.wantJSON {
				t.Fatalf("JSON = %v, want %v", flags.JSON, tc.wantJSON)
			}
		})
	}
}

func TestParseCommandRecognizesDoctor(t *testing.T) {
	command, err := parseCommand([]string{"doctor", "--json"})
	if err != nil {
		t.Fatal(err)
	}
	if command.name != "doctor" || !command.flags.JSON {
		t.Fatalf("parsed = %+v", command)
	}
	if _, err := parseCommand([]string{"doctor", "--network"}); err == nil {
		t.Fatal("doctor --network was accepted")
	}
	if _, err := parseCommand([]string{"doctor", "extra"}); err == nil {
		t.Fatal("doctor with a positional argument was accepted")
	}
}

func TestDoctorUsageAndHelp(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantExit   int
		wantOut    string
		wantErr    string
		absentFrom string
	}{
		{name: "help", args: []string{"doctor", "--help"}, wantExit: exitOK, wantOut: "Usage: sesame-server"},
		{name: "short help", args: []string{"doctor", "-h"}, wantExit: exitOK, wantOut: "Usage: sesame-server"},
		{name: "network", args: []string{"doctor", "--network", "--json"}, wantExit: exitUsage, wantErr: "doctor: --network is not supported"},
		{name: "network alone", args: []string{"doctor", "--network"}, wantExit: exitUsage, wantErr: "doctor: --network is not supported"},
		{name: "repeated json", args: []string{"doctor", "--json", "--json"}, wantExit: exitUsage, wantErr: "--json was given more than once"},
		{name: "json value", args: []string{"doctor", "--json=yes"}, wantExit: exitUsage, wantErr: "--json takes no value"},
		{name: "unknown option", args: []string{"doctor", "--bogus"}, wantExit: exitUsage, wantErr: "flag provided but not defined: --bogus"},
		{name: "unknown option value", args: []string{"doctor", "--bogus=doctor-echo-marker"}, wantExit: exitUsage, wantErr: "flag provided but not defined: --bogus", absentFrom: "doctor-echo-marker"},
		{name: "positional", args: []string{"doctor", "doctor-positional-marker"}, wantExit: exitUsage, wantErr: "takes no arguments", absentFrom: "doctor-positional-marker"},
		{name: "dash positional", args: []string{"doctor", "-"}, wantExit: exitUsage, wantErr: "takes no arguments"},
		{name: "double dash positional", args: []string{"doctor", "--", "--json"}, wantExit: exitUsage, wantErr: "takes no arguments"},
		{name: "json then positional", args: []string{"doctor", "--json", "extra"}, wantExit: exitUsage, wantErr: "takes no arguments"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			missingDataDir := filepath.Join(t.TempDir(), "never-created")
			env, stdout, stderr := testEnvironment(t, map[string]string{config.EnvDataDir: missingDataDir}, Commands{})
			code := run(context.Background(), tc.args, env)
			if code != tc.wantExit {
				t.Fatalf("exit = %d, want %d: stderr %q", code, tc.wantExit, stderr.String())
			}
			if tc.wantOut != "" && !strings.Contains(stdout.String(), tc.wantOut) {
				t.Fatalf("stdout = %q, want %q", stdout.String(), tc.wantOut)
			}
			if tc.wantErr != "" && !strings.Contains(stderr.String(), tc.wantErr) {
				t.Fatalf("stderr = %q, want %q", stderr.String(), tc.wantErr)
			}
			if tc.wantExit == exitOK {
				if !strings.Contains(stdout.String(), "doctor [--json]") || strings.Contains(stdout.String(), "--network") {
					t.Fatalf("help output does not name only the supported option: %q", stdout.String())
				}
			}
			if tc.wantExit == exitUsage {
				if stdout.String() != "" {
					t.Fatalf("usage error wrote stdout %q", stdout.String())
				}
				if !strings.Contains(stderr.String(), "Usage:") {
					t.Fatalf("usage error did not show usage: %q", stderr.String())
				}
				if strings.Contains(stdout.String()+stderr.String(), "{") {
					t.Fatalf("usage error wrote a JSON document: %q", stdout.String()+stderr.String())
				}
			}
			if tc.absentFrom != "" && strings.Contains(stdout.String()+stderr.String(), tc.absentFrom) {
				t.Fatalf("usage error echoed private input %q", tc.absentFrom)
			}
			if _, err := os.Lstat(missingDataDir); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("a usage or help path created the data directory: %v", err)
			}
		})
	}
}

func TestDoctorJSONUsesTheActualOfflineReportAndChangesNothing(t *testing.T) {
	fixture := newDoctorFixture(t, 0o600)
	before := snapshotDoctorTree(t, fixture.root)
	env, stdout, stderr := testEnvironment(t, fixture.environment(), doctorFailingCommands(t))
	env.Stdin = doctorFailingReader{t: t}
	code := run(context.Background(), []string{"doctor", "--json"}, env)
	if code != exitUnknown {
		t.Fatalf("exit = %d, want %d: stdout %q stderr %q", code, exitUnknown, stdout.String(), stderr.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q", stderr.String())
	}
	after := snapshotDoctorTree(t, fixture.root)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("the doctor changed the data tree:\nbefore %+v\nafter %+v", before, after)
	}
	assertNoDoctorArtifacts(t, after)
	assertNoDoctorLeak(t, stdout.String()+stderr.String(), fixture)

	document, top := decodeDoctorDocument(t, stdout.String())
	if document.SchemaVersion != 1 || document.Mode != "offline" {
		t.Fatalf("schema or mode = %d %q", document.SchemaVersion, document.Mode)
	}
	if document.ToolVersion != "9.9.9-test" {
		t.Fatalf("toolVersion = %q", document.ToolVersion)
	}
	if document.Overall != "unknown" {
		t.Fatalf("overall = %q", document.Overall)
	}
	generated, err := time.Parse(time.RFC3339, document.GeneratedAt)
	if err != nil {
		t.Fatalf("generatedAt = %q: %v", document.GeneratedAt, err)
	}
	if generated.Format(time.RFC3339) != document.GeneratedAt || !strings.HasSuffix(document.GeneratedAt, "Z") {
		t.Fatalf("generatedAt is not UTC at seconds precision: %q", document.GeneratedAt)
	}
	if document.Config == nil {
		t.Fatal("config is null for valid configuration")
	}
	if document.Config.PublicURL != "http://localhost:8787" || document.Config.PublicURLSource != "default" {
		t.Fatalf("public URL = %+v", document.Config)
	}
	if document.Config.ListenScope != "loopback" || document.Config.ListenPort != 8787 {
		t.Fatalf("listen = %+v", document.Config)
	}
	if len(document.Config.TrustedProxies) != 0 {
		t.Fatalf("trustedProxies = %v", document.Config.TrustedProxies)
	}
	if document.Config.DataDirectorySource != "environment" || document.Config.ConfigFile != "loaded" {
		t.Fatalf("sources = %+v", document.Config)
	}
	if len(document.Limitations) == 0 {
		t.Fatal("limitations are empty")
	}
	for _, limitation := range document.Limitations {
		if limitation == "" {
			t.Fatal("a limitation code is empty")
		}
	}

	configRaw, ok := top["config"]
	if !ok || string(configRaw) == "null" {
		t.Fatalf("config raw = %s", configRaw)
	}
	var configObject map[string]json.RawMessage
	if err := json.Unmarshal(configRaw, &configObject); err != nil {
		t.Fatal(err)
	}
	assertDoctorKeys(t, configObject, "publicUrl", "publicUrlSource", "listenScope", "listenPort", "trustedProxies", "dataDirectorySource", "configFile")
	if string(configObject["trustedProxies"]) == "null" {
		t.Fatal("trustedProxies is null")
	}

	wantChecks := []struct {
		id    string
		state string
		code  string
	}{
		{"configuration", "ok", "configuration_valid"},
		{"origin", "ok", "origin_local"},
		{"listen", "ok", "listen_loopback"},
		{"proxy_trust", "ok", "proxy_trust_empty"},
		{"config_file", "ok", "config_file_private"},
		{"data_directory", "ok", "data_directory_private"},
		{"secrets", "ok", "secrets_metadata_ok"},
		{"database", "unknown", "database_not_opened"},
		{"space", "ok", "space_sufficient"},
	}
	if len(document.Checks) != len(wantChecks) {
		t.Fatalf("checks = %d, want %d", len(document.Checks), len(wantChecks))
	}
	for index, want := range wantChecks {
		check := document.Checks[index]
		if check.ID != want.id || check.State != want.state || check.Code != want.code {
			t.Fatalf("check %d = %+v, want %+v", index, check, want)
		}
		if check.Summary == "" {
			t.Fatalf("check %s has an empty summary", check.ID)
		}
	}
	var checks []json.RawMessage
	if err := json.Unmarshal(top["checks"], &checks); err != nil {
		t.Fatal(err)
	}
	for _, raw := range checks {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil {
			t.Fatal(err)
		}
		assertDoctorKeys(t, object, "id", "state", "code", "summary", "next")
		if next, ok := object["next"]; !ok || (string(next) != "null" && !strings.HasPrefix(string(next), `"`)) {
			t.Fatalf("next = %s", next)
		}
	}
}

func TestDoctorInvalidConfigurationIsASafeActionReport(t *testing.T) {
	cases := []struct {
		name    string
		content string
		marker  string
	}{
		{
			name:    "invalid origin",
			content: `{"publicUrl":"http://doctor-private-invalid.invalid"}` + "\n",
			marker:  "doctor-private-invalid",
		},
		{
			name:    "oversized file",
			content: strings.Repeat("doctor-oversized-marker ", 1<<16),
			marker:  "doctor-oversized-marker",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dataDir := filepath.Join(root, "data")
			if err := os.Mkdir(dataDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(dataDir, 0o700); err != nil {
				t.Fatal(err)
			}
			configPath := filepath.Join(dataDir, config.DefaultConfigName)
			writeDoctorFile(t, configPath, []byte(tc.content), 0o600)
			before := snapshotDoctorTree(t, root)

			env, stdout, stderr := testEnvironment(t, map[string]string{config.EnvDataDir: dataDir}, doctorFailingCommands(t))
			code := run(context.Background(), []string{"doctor", "--json"}, env)
			if code != exitFailure {
				t.Fatalf("exit = %d, want %d: stdout %q stderr %q", code, exitFailure, stdout.String(), stderr.String())
			}
			if stderr.String() != "" {
				t.Fatalf("stderr = %q", stderr.String())
			}
			if strings.Contains(stdout.String(), tc.marker) || strings.Contains(stdout.String(), dataDir) || strings.Contains(stdout.String(), configPath) {
				t.Fatal("the invalid configuration report leaked a private value")
			}
			after := snapshotDoctorTree(t, root)
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("the doctor changed the data tree:\nbefore %+v\nafter %+v", before, after)
			}
			assertNoDoctorArtifacts(t, after)

			document, top := decodeDoctorDocument(t, stdout.String())
			if document.Overall != "action" {
				t.Fatalf("overall = %q", document.Overall)
			}
			if string(top["config"]) != "null" {
				t.Fatalf("config = %s", top["config"])
			}
			if len(document.Checks) != 1 {
				t.Fatalf("checks = %+v", document.Checks)
			}
			check := document.Checks[0]
			if check.ID != "configuration" || check.State != "action" || check.Code != "configuration_invalid" {
				t.Fatalf("check = %+v", check)
			}
			if check.Summary == "" || check.Next == nil || *check.Next == "" {
				t.Fatalf("check messages = %+v", check)
			}
		})
	}
}

func TestDoctorBroadConfigPermissionsNeedAction(t *testing.T) {
	fixture := newDoctorFixture(t, 0o644)
	before := snapshotDoctorTree(t, fixture.root)
	env, stdout, stderr := testEnvironment(t, fixture.environment(), doctorFailingCommands(t))
	code := run(context.Background(), []string{"doctor", "--json"}, env)
	if code != exitFailure {
		t.Fatalf("exit = %d, want %d: stdout %q stderr %q", code, exitFailure, stdout.String(), stderr.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q", stderr.String())
	}
	after := snapshotDoctorTree(t, fixture.root)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("the doctor changed the data tree:\nbefore %+v\nafter %+v", before, after)
	}
	assertNoDoctorLeak(t, stdout.String(), fixture)
	document, _ := decodeDoctorDocument(t, stdout.String())
	if document.Overall != "action" {
		t.Fatalf("overall = %q", document.Overall)
	}
	check := doctorCheckByID(t, document, "config_file")
	if check.State != "action" || check.Code != "config_file_permissions" {
		t.Fatalf("config_file check = %+v", check)
	}
}

func TestDoctorMissingDataDirectoryStaysMissing(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "never-created")
	before := snapshotDoctorTree(t, root)
	env, stdout, stderr := testEnvironment(t, map[string]string{config.EnvDataDir: dataDir}, doctorFailingCommands(t))
	code := run(context.Background(), []string{"doctor", "--json"}, env)
	if code != exitUnknown {
		t.Fatalf("exit = %d, want %d: stdout %q stderr %q", code, exitUnknown, stdout.String(), stderr.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if _, err := os.Lstat(dataDir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the doctor created the data directory: %v", err)
	}
	after := snapshotDoctorTree(t, root)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("the doctor changed the parent tree:\nbefore %+v\nafter %+v", before, after)
	}
	document, _ := decodeDoctorDocument(t, stdout.String())
	if document.Overall != "unknown" {
		t.Fatalf("overall = %q", document.Overall)
	}
	for _, id := range []string{"data_directory", "secrets", "database", "space"} {
		check := doctorCheckByID(t, document, id)
		if check.State != "unknown" {
			t.Fatalf("%s state = %q, want unknown", id, check.State)
		}
	}
}

func TestDoctorHumanOutputUsesFixedMessages(t *testing.T) {
	t.Run("unknown", func(t *testing.T) {
		fixture := newDoctorFixture(t, 0o600)
		env, stdout, stderr := testEnvironment(t, fixture.environment(), doctorFailingCommands(t))
		code := run(context.Background(), []string{"doctor"}, env)
		if code != exitUnknown {
			t.Fatalf("exit = %d, want %d: stdout %q stderr %q", code, exitUnknown, stdout.String(), stderr.String())
		}
		if stderr.String() != "" {
			t.Fatalf("stderr = %q", stderr.String())
		}
		text := stdout.String()
		if !strings.Contains(text, "Overall: unknown") || !strings.Contains(text, "database: unknown") {
			t.Fatalf("stdout = %q", text)
		}
		if !strings.Contains(text, "not a passing result") {
			t.Fatalf("human unknown output did not state the limit: %q", text)
		}
		if strings.Contains(text, "{") || strings.Contains(text, "http://localhost:8787") {
			t.Fatalf("human output dumped JSON or configuration values: %q", text)
		}
		assertNoDoctorLeak(t, text, fixture)
	})

	t.Run("action", func(t *testing.T) {
		root := t.TempDir()
		dataDir := filepath.Join(root, "data")
		if err := os.Mkdir(dataDir, 0o700); err != nil {
			t.Fatal(err)
		}
		configPath := filepath.Join(dataDir, config.DefaultConfigName)
		writeDoctorFile(t, configPath, []byte(`{"publicUrl":"http://doctor-private-human.invalid"}`), 0o600)
		env, stdout, stderr := testEnvironment(t, map[string]string{config.EnvDataDir: dataDir}, doctorFailingCommands(t))
		code := run(context.Background(), []string{"doctor"}, env)
		if code != exitFailure {
			t.Fatalf("exit = %d, want %d: stdout %q stderr %q", code, exitFailure, stdout.String(), stderr.String())
		}
		if stderr.String() != "" {
			t.Fatalf("stderr = %q", stderr.String())
		}
		text := stdout.String()
		if !strings.Contains(text, "Overall: action") || !strings.Contains(text, "configuration: action") {
			t.Fatalf("stdout = %q", text)
		}
		if !strings.Contains(text, "At least one check needs action.") {
			t.Fatalf("human action output did not state the outcome: %q", text)
		}
		if strings.Contains(text, "doctor-private-human") || strings.Contains(text, dataDir) {
			t.Fatalf("human output leaked private input: %q", text)
		}
	})
}

func TestDoctorReportWriteFailuresExitOneWithFixedStderr(t *testing.T) {
	cases := []struct {
		name   string
		json   bool
		writer io.Writer
		marker string
	}{
		{
			name: "json error",
			json: true,
			writer: doctorWriterFunc(func([]byte) (int, error) {
				return 0, errors.New("doctor-private-writer-marker")
			}),
			marker: "doctor-private-writer-marker",
		},
		{
			name: "json short write",
			json: true,
			writer: doctorWriterFunc(func(p []byte) (int, error) {
				return len(p) - 1, nil
			}),
		},
		{
			name: "human error",
			writer: doctorWriterFunc(func([]byte) (int, error) {
				return 0, errors.New("doctor-private-writer-marker")
			}),
			marker: "doctor-private-writer-marker",
		},
		{
			name: "human short write",
			writer: doctorWriterFunc(func(p []byte) (int, error) {
				return len(p) - 1, nil
			}),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dataDir := filepath.Join(t.TempDir(), "never-created")
			env, _, stderr := testEnvironment(t, map[string]string{config.EnvDataDir: dataDir}, doctorFailingCommands(t))
			env.Stdout = tc.writer
			args := []string{"doctor"}
			if tc.json {
				args = append(args, "--json")
			}
			code := run(context.Background(), args, env)
			if code != exitFailure {
				t.Fatalf("exit = %d, want %d", code, exitFailure)
			}
			if stderr.String() != doctorWriteFailure {
				t.Fatalf("stderr = %q, want %q", stderr.String(), doctorWriteFailure)
			}
			if tc.marker != "" && strings.Contains(stderr.String(), tc.marker) {
				t.Fatal("the writer error leaked to stderr")
			}
			if _, err := os.Lstat(dataDir); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("a failed report write created the data directory: %v", err)
			}
		})
	}
}

func TestDoctorMissingStdoutIsAFixedFailure(t *testing.T) {
	env, _, stderr := testEnvironment(t, map[string]string{config.EnvDataDir: filepath.Join(t.TempDir(), "never-created")}, doctorFailingCommands(t))
	env.Stdout = nil
	if code := run(context.Background(), []string{"doctor", "--json"}, env); code != exitFailure {
		t.Fatalf("exit = %d, want %d", code, exitFailure)
	}
	if stderr.String() != doctorWriteFailure {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestDoctorExitCodeMapsReportStates(t *testing.T) {
	cases := map[string]int{
		"ok":      exitOK,
		"action":  exitFailure,
		"unknown": exitUnknown,
		"":        exitUnknown,
		"blocked": exitUnknown,
	}
	for overall, want := range cases {
		if got := doctorExitCode(overall); got != want {
			t.Fatalf("doctorExitCode(%q) = %d, want %d", overall, got, want)
		}
	}
}

func TestDoctorHumanOKUsesTheCoreMessage(t *testing.T) {
	var output syncBuffer
	report := diagnostics.Report{
		Overall: "ok",
		Checks: []diagnostics.Check{{
			ID:      "configuration",
			State:   "ok",
			Code:    "configuration_valid",
			Summary: "The service configuration loaded from the process environment and the optional config file.",
		}},
		Limitations: []string{"network_not_probed"},
	}
	if err := writeDoctorHuman(&output, report); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	if !strings.Contains(text, "Overall: ok") || !strings.Contains(text, "Every performed check is ok.") {
		t.Fatalf("stdout = %q", text)
	}
	if strings.Contains(text, "not a passing result") {
		t.Fatalf("a clean report was described as incomplete: %q", text)
	}
}

func TestDoctorSanitizesTheToolVersionThroughCore(t *testing.T) {
	fixture := newDoctorFixture(t, 0o600)
	env, stdout, stderr := testEnvironment(t, fixture.environment(), doctorFailingCommands(t))
	env.Version = "doctor-version-marker\n"
	code := run(context.Background(), []string{"doctor", "--json"}, env)
	if code != exitUnknown {
		t.Fatalf("exit = %d, want %d: stdout %q stderr %q", code, exitUnknown, stdout.String(), stderr.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if strings.Contains(stdout.String(), "doctor-version-marker") {
		t.Fatal("the report leaked the unsupported tool version")
	}
	document, _ := decodeDoctorDocument(t, stdout.String())
	if document.ToolVersion != "unknown" {
		t.Fatalf("toolVersion = %q", document.ToolVersion)
	}
}

func TestDoctorContextCancellationDoesNotStartTheServer(t *testing.T) {
	fixture := newDoctorFixture(t, 0o600)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	env, stdout, stderr := testEnvironment(t, fixture.environment(), doctorFailingCommands(t))
	code := run(ctx, []string{"doctor", "--json"}, env)
	if code != exitUnknown {
		t.Fatalf("exit = %d, want %d: stdout %q stderr %q", code, exitUnknown, stdout.String(), stderr.String())
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q", stderr.String())
	}
	document, _ := decodeDoctorDocument(t, stdout.String())
	if document.Overall != "unknown" {
		t.Fatalf("overall = %q", document.Overall)
	}
	assertNoDoctorLeak(t, stdout.String(), fixture)
}

func newDoctorFixture(t *testing.T, configMode fs.FileMode) doctorFixture {
	t.Helper()
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := os.Mkdir(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	secretsDir := secrets.Dir(dataDir)
	if err := os.Mkdir(secretsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(secretsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	markers := []string{
		"doctor-signing-key-marker",
		"doctor-admin-key-marker",
		"doctor-ip-pepper-marker",
	}
	for index, name := range secrets.Files() {
		writeDoctorFile(t, filepath.Join(secretsDir, name), doctorSecretBytes(markers[index]), 0o600)
	}
	databaseMarker := "doctor-database-marker"
	writeDoctorFile(t, filepath.Join(dataDir, secrets.DatabaseFile), []byte(databaseMarker+"\n"), 0o600)

	configPassword := "doctor-config-password-marker"
	environmentPassword := "doctor-environment-password-marker"
	configPath := filepath.Join(dataDir, config.DefaultConfigName)
	content := fmt.Sprintf("{\"smtp\":{\"addr\":\"smtp.example.com:587\",\"username\":\"fictional-operator\",\"password\":%q,\"from\":\"fixture@example.com\"}}\n", configPassword)
	writeDoctorFile(t, configPath, []byte(content), configMode)
	return doctorFixture{
		root:            root,
		dataDir:         dataDir,
		configPath:      configPath,
		configPassword:  configPassword,
		environmentPass: environmentPassword,
		databaseMarker:  databaseMarker,
		secretMarkers:   markers,
	}
}

func (f doctorFixture) environment() map[string]string {
	return map[string]string{
		config.EnvDataDir:  f.dataDir,
		config.EnvSMTPAuth: f.environmentPass,
	}
}

func writeDoctorFile(t *testing.T, path string, content []byte, mode fs.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, content, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func doctorSecretBytes(marker string) []byte {
	content := []byte(marker)
	for len(content) < secrets.KeySize {
		content = append(content, '.')
	}
	return content[:secrets.KeySize]
}

func doctorFailingCommands(t *testing.T) Commands {
	t.Helper()
	failRunner := RunnerFunc(func(context.Context, Invocation) error {
		t.Fatal("a doctor run reached a service command")
		return nil
	})
	return Commands{
		Serve: ApplicationBuilderFunc(func(context.Context, BuildInput) (*Application, error) {
			t.Fatal("a doctor run reached the server builder")
			return nil, nil
		}),
		Install:      failRunner,
		Backup:       failRunner,
		Restore:      failRunner,
		Check:        failRunner,
		OwnerReset:   failRunner,
		UpdatesReset: failRunner,
		Export:       failRunner,
		Recover:      failRunner,
	}
}

type doctorFailingReader struct {
	t *testing.T
}

func (r doctorFailingReader) Read([]byte) (int, error) {
	r.t.Fatal("a doctor run read standard input")
	return 0, io.EOF
}

type doctorWriterFunc func([]byte) (int, error)

func (f doctorWriterFunc) Write(p []byte) (int, error) { return f(p) }

func decodeDoctorDocument(t *testing.T, output string) (doctorDocument, map[string]json.RawMessage) {
	t.Helper()
	if !strings.HasSuffix(output, "\n") {
		t.Fatalf("JSON output does not end in a newline: %q", output)
	}
	if strings.Count(output, "\n") != 1 {
		t.Fatalf("JSON output is not one line plus a newline: %q", output)
	}
	body := strings.TrimSuffix(output, "\n")
	if !strings.HasPrefix(body, "{") {
		t.Fatalf("JSON output does not start with an object: %q", output)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &top); err != nil {
		t.Fatalf("JSON output is invalid: %v: %q", err, output)
	}
	assertDoctorKeys(t, top, "schemaVersion", "toolVersion", "generatedAt", "mode", "overall", "config", "checks", "limitations")
	var document doctorDocument
	if err := json.Unmarshal([]byte(body), &document); err != nil {
		t.Fatal(err)
	}
	if document.Checks == nil {
		t.Fatal("checks is null")
	}
	if document.Limitations == nil {
		t.Fatal("limitations is null")
	}
	return document, top
}

func assertDoctorKeys(t *testing.T, object map[string]json.RawMessage, keys ...string) {
	t.Helper()
	if len(object) != len(keys) {
		t.Fatalf("object has %d keys, want %d: %v", len(object), len(keys), object)
	}
	for _, key := range keys {
		if _, ok := object[key]; !ok {
			t.Fatalf("object is missing key %q: %v", key, object)
		}
	}
}

func doctorCheckByID(t *testing.T, document doctorDocument, id string) doctorCheckDocument {
	t.Helper()
	for _, check := range document.Checks {
		if check.ID == id {
			return check
		}
	}
	t.Fatalf("check %q is missing", id)
	return doctorCheckDocument{}
}

func snapshotDoctorTree(t *testing.T, root string) map[string]doctorTreeEntry {
	t.Helper()
	entries := make(map[string]doctorTreeEntry)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		record := doctorTreeEntry{Mode: info.Mode(), Size: info.Size(), ModTime: info.ModTime()}
		if info.Mode().IsRegular() {
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(content)
			record.Digest = fmt.Sprintf("%x", sum)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			record.Target = target
		}
		entries[relative] = record
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func assertNoDoctorArtifacts(t *testing.T, entries map[string]doctorTreeEntry) {
	t.Helper()
	for path := range entries {
		name := filepath.Base(path)
		if name == "sesame.lock" || strings.HasSuffix(name, ".db-shm") || strings.HasSuffix(name, ".db-wal") || strings.HasPrefix(name, ".tmp-") {
			t.Fatalf("the doctor left a runtime artifact: %q", path)
		}
	}
}

func assertNoDoctorLeak(t *testing.T, text string, fixture doctorFixture) {
	t.Helper()
	values := []string{
		fixture.dataDir,
		fixture.configPath,
		secrets.Dir(fixture.dataDir),
		fixture.configPassword,
		fixture.environmentPass,
		fixture.databaseMarker,
	}
	values = append(values, fixture.secretMarkers...)
	for _, value := range values {
		if value != "" && strings.Contains(text, value) {
			t.Fatalf("the report leaked a private fixture value %q", value)
		}
	}
}
