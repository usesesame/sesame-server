package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"usesesame.app/backend/internal/selfhost/config"
	"usesesame.app/backend/internal/selfhost/install"
)

func TestParseInstallArgs(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		want     InstallRequest
		wantJSON bool
		wantErr  string
		wantHelp bool
	}{
		{
			name: "local",
			args: []string{"--profile", "local", "--version", "0.4.1", "--dir", "./sesame"},
			want: InstallRequest{Profile: "local", Version: "0.4.1", Directory: "./sesame"},
		},
		{
			name: "caddy",
			args: []string{"--profile", "caddy", "--host", "vault.example.com", "--version", "0.4.1", "--dir", "./sesame"},
			want: InstallRequest{Profile: "caddy", Version: "0.4.1", Host: "vault.example.com", Directory: "./sesame"},
		},
		{
			name:     "tailscale with json",
			args:     []string{"--json", "--dir", "./sesame", "--version", "0.4.1", "--host", "vault.example-tailnet.ts.net", "--profile", "tailscale"},
			want:     InstallRequest{Profile: "tailscale", Version: "0.4.1", Host: "vault.example-tailnet.ts.net", Directory: "./sesame"},
			wantJSON: true,
		},
		{
			name: "equals form",
			args: []string{"--profile=local", "--version=0.4.1", "--dir=./sesame"},
			want: InstallRequest{Profile: "local", Version: "0.4.1", Directory: "./sesame"},
		},
		{
			name:     "help",
			args:     []string{"--help"},
			wantHelp: true,
		},
		{
			name:     "short help",
			args:     []string{"-h"},
			wantHelp: true,
		},
		{
			name:    "repeated profile",
			args:    []string{"--profile", "local", "--profile", "caddy", "--version", "0.4.1", "--dir", "./sesame"},
			wantErr: "--profile was given more than once",
		},
		{
			name:    "repeated version",
			args:    []string{"--profile", "local", "--version", "0.4.1", "--version", "0.4.2", "--dir", "./sesame"},
			wantErr: "--version was given more than once",
		},
		{
			name:    "repeated directory",
			args:    []string{"--profile", "local", "--version", "0.4.1", "--dir", "./sesame", "--dir", "./other"},
			wantErr: "--dir was given more than once",
		},
		{
			name:    "repeated host",
			args:    []string{"--profile", "caddy", "--host", "vault.example.com", "--host", "vault.example.net", "--version", "0.4.1", "--dir", "./sesame"},
			wantErr: "--host was given more than once",
		},
		{
			name:    "repeated json",
			args:    []string{"--profile", "local", "--version", "0.4.1", "--dir", "./sesame", "--json", "--json"},
			wantErr: "--json was given more than once",
		},
		{
			name:    "unknown long flag",
			args:    []string{"--profile", "local", "--version", "0.4.1", "--dir", "./sesame", "--bogus"},
			wantErr: "flag provided but not defined: --bogus",
		},
		{
			name:    "unknown short flag",
			args:    []string{"-profile", "local", "--version", "0.4.1", "--dir", "./sesame"},
			wantErr: "flag provided but not defined: -profile",
		},
		{
			name:    "missing profile value",
			args:    []string{"--profile"},
			wantErr: "--profile needs a value",
		},
		{
			name:    "missing version value before another flag",
			args:    []string{"--profile", "local", "--version", "--dir", "./sesame"},
			wantErr: "--version needs a value",
		},
		{
			name:    "empty directory equals",
			args:    []string{"--profile", "local", "--version", "0.4.1", "--dir="},
			wantErr: "--dir needs a value",
		},
		{
			name:    "empty host separate",
			args:    []string{"--profile", "caddy", "--host", "", "--version", "0.4.1", "--dir", "./sesame"},
			wantErr: "--host needs a value",
		},
		{
			name:    "positional argument",
			args:    []string{"--profile", "local", "--version", "0.4.1", "--dir", "./sesame", "extra"},
			wantErr: "takes no arguments",
		},
		{
			name:    "positional after double dash",
			args:    []string{"--profile", "local", "--version", "0.4.1", "--dir", "./sesame", "--", "--json"},
			wantErr: "takes no arguments",
		},
		{
			name:    "json with a value",
			args:    []string{"--profile", "local", "--version", "0.4.1", "--dir", "./sesame", "--json=true"},
			wantErr: "--json takes no value",
		},
		{
			name:    "missing profile",
			args:    []string{"--version", "0.4.1", "--dir", "./sesame"},
			wantErr: "--profile is required",
		},
		{
			name:    "missing version",
			args:    []string{"--profile", "local", "--dir", "./sesame"},
			wantErr: "--version is required",
		},
		{
			name:    "missing directory",
			args:    []string{"--profile", "local", "--version", "0.4.1"},
			wantErr: "--dir is required",
		},
		{
			name:    "dash value",
			args:    []string{"--profile", "local", "--version", "0.4.1", "--dir", "-"},
			wantErr: "--dir needs a value",
		},
		{
			name:    "no arguments",
			args:    nil,
			wantErr: "--profile is required",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, flags, err := parseInstallArgs(tc.args)
			if tc.wantHelp {
				if !errors.Is(err, flag.ErrHelp) {
					t.Fatalf("error = %v, want help", err)
				}
				return
			}
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want %q", err, tc.wantErr)
				}
				if !strings.HasPrefix(err.Error(), "install:") {
					t.Fatalf("error = %q, want the install prefix", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("request = %+v, want %+v", got, tc.want)
			}
			if flags.JSON != tc.wantJSON {
				t.Fatalf("json = %t, want %t", flags.JSON, tc.wantJSON)
			}
		})
	}
}

func TestParseCommandRoutesInstall(t *testing.T) {
	command, err := parseCommand([]string{"install", "--profile", "local", "--version", "0.4.1", "--dir", "./sesame", "--json"})
	if err != nil {
		t.Fatal(err)
	}
	if command.name != "install" || !command.flags.JSON {
		t.Fatalf("command = %+v", command)
	}
	want := InstallRequest{Profile: "local", Version: "0.4.1", Directory: "./sesame"}
	if command.install != want {
		t.Fatalf("install = %+v, want %+v", command.install, want)
	}
}

func TestInstallHelpNamesTheExactCommand(t *testing.T) {
	env, stdout, stderr := testEnvironment(t, nil, Commands{})
	if code := run(context.Background(), []string{"install", "--help"}, env); code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "install --profile local|caddy|tailscale --version <tag> --dir <path> [--host <name>] [--json]") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestInstallUsageErrorsExitWithTwoAndLeaveStandardOutputEmpty(t *testing.T) {
	cases := [][]string{
		{"install", "--profile", "local", "--profile", "caddy", "--version", "0.4.1", "--dir", "./sesame"},
		{"install", "--profile", "local", "--version", "0.4.1", "--dir", "./sesame", "--bogus"},
		{"install", "--profile", "local", "--version", "0.4.1"},
		{"install", "extra"},
	}
	for _, args := range cases {
		env, stdout, stderr := testEnvironment(t, nil, Commands{})
		if code := run(context.Background(), args, env); code != exitUsage {
			t.Fatalf("%v: exit %d", args, code)
		}
		if stdout.String() != "" {
			t.Fatalf("%v: stdout = %q", args, stdout.String())
		}
		if !strings.Contains(stderr.String(), "install:") || !strings.Contains(stderr.String(), "Usage: sesame-server") {
			t.Fatalf("%v: stderr = %q", args, stderr.String())
		}
	}
}

func TestInstallRunsBeforeConfigurationLoad(t *testing.T) {
	var calls recorded
	dataDir := filepath.Join(t.TempDir(), "not-created")
	env, stdout, stderr := testEnvironment(t, map[string]string{
		config.EnvPublicURL: "http://sesame.example.net",
		config.EnvDataDir:   dataDir,
	}, Commands{Install: calls.runner(nil)})
	args := []string{"install", "--profile", "tailscale", "--host", "vault.example-tailnet.ts.net", "--version", "0.4.1", "--dir", "./sesame", "--json"}
	if code := run(context.Background(), args, env); code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	callsSeen := calls.calls()
	if len(callsSeen) != 1 {
		t.Fatalf("install ran %d times", len(callsSeen))
	}
	got := callsSeen[0]
	if got.Config.DataDir != "" || got.Logger != nil {
		t.Fatalf("install loaded configuration: %+v", got.Config)
	}
	want := InstallRequest{Profile: "tailscale", Version: "0.4.1", Host: "vault.example-tailnet.ts.net", Directory: "./sesame"}
	if got.Install != want {
		t.Fatalf("install = %+v, want %+v", got.Install, want)
	}
	if !got.Flags.JSON || got.Version != "9.9.9-test" {
		t.Fatalf("invocation = %+v", got)
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if _, err := os.Stat(dataDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the data directory was read or created: %v", err)
	}
}

func TestInstallFailureLeavesStandardOutputEmpty(t *testing.T) {
	var calls recorded
	env, stdout, stderr := testEnvironment(t, nil, Commands{Install: calls.runner(errors.New("the target already exists"))})
	if code := run(context.Background(), []string{"install", "--profile", "local", "--version", "0.4.1", "--dir", "./sesame"}, env); code != exitFailure {
		t.Fatalf("exit %d", code)
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if stderr.String() != "sesame-server: the target already exists\n" {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestWriteInstallSummaryQuotesTheDirectoryAndDirectsInspection(t *testing.T) {
	plan := install.Plan{
		Profile:      "caddy",
		ImageVersion: "0.4.1",
		PublicURL:    "https://vault.example.com",
		Files: []install.File{
			{Path: "compose.yaml", SHA256: strings.Repeat("a", 64), Mode: "0600"},
			{Path: ".env", SHA256: strings.Repeat("b", 64), Mode: "0600"},
			{Path: "Caddyfile", SHA256: strings.Repeat("c", 64), Mode: "0600"},
			{Path: "caddy.env", SHA256: strings.Repeat("d", 64), Mode: "0600"},
		},
		Prerequisites: []install.Prerequisite{
			{ID: "docker", State: "unknown", Message: "Docker is installed and can run this Compose file."},
		},
	}
	var summary strings.Builder
	if err := writeInstallSummary(&summary, "/tmp/sesame install", plan); err != nil {
		t.Fatal(err)
	}
	got := summary.String()
	if !strings.Contains(got, `"/tmp/sesame install"`) {
		t.Fatalf("summary does not quote the directory: %q", got)
	}
	if !strings.Contains(got, "docker compose config") || !strings.Contains(got, "Inspect the files") {
		t.Fatalf("summary does not direct inspection: %q", got)
	}
	if !strings.Contains(got, "docker: Docker is installed and can run this Compose file.") {
		t.Fatalf("summary does not show the prerequisite: %q", got)
	}
	if strings.Contains(got, `"schemaVersion"`) || strings.HasPrefix(strings.TrimSpace(got), "{") {
		t.Fatalf("summary looks like JSON: %q", got)
	}
}

func TestWriteInstallSummaryQuotesTheRelativeDirectoryAsGiven(t *testing.T) {
	plan := install.Plan{Profile: "local", ImageVersion: "0.4.1", PublicURL: "http://localhost:8787"}
	var summary strings.Builder
	if err := writeInstallSummary(&summary, "./sesame-local", plan); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary.String(), `"./sesame-local"`) {
		t.Fatalf("summary does not quote the relative target as given: %q", summary.String())
	}
}

func TestInstallThroughTheRunner(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "sesame")
	env, stdout, stderr := testEnvironment(t, nil, Commands{Install: RunnerFunc(runInstall)})
	if code := run(context.Background(), []string{"install", "--profile", "local", "--version", "0.4.1", "--dir", target}, env); code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	plan := readInstallPlan(t, target)
	if plan.SchemaVersion != 1 || plan.ToolVersion != "9.9.9-test" || plan.Profile != "local" || plan.ImageVersion != "0.4.1" {
		t.Fatalf("plan = %+v", plan)
	}
	if plan.PublicURL != "http://localhost:8787" || len(plan.TrustedProxies) != 0 {
		t.Fatalf("plan = %+v", plan)
	}
	if len(plan.Files) != 2 {
		t.Fatalf("files = %+v", plan.Files)
	}
	for _, file := range plan.Files {
		if file.Mode != "0600" || len(file.SHA256) != 64 {
			t.Fatalf("file = %+v", file)
		}
	}
	if len(plan.Prerequisites) == 0 {
		t.Fatal("the plan has no prerequisites")
	}
	for _, prerequisite := range plan.Prerequisites {
		if prerequisite.State != "unknown" || prerequisite.Message == "" {
			t.Fatalf("prerequisite = %+v", prerequisite)
		}
	}
	if !strings.Contains(stdout.String(), `"`+target+`"`) || !strings.Contains(stdout.String(), "docker compose config") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	body, err := os.ReadFile(filepath.Join(target, "plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	wantFields := []string{"schemaVersion", "toolVersion", "profile", "imageVersion", "publicUrl", "trustedProxies", "files", "prerequisites"}
	if len(fields) != len(wantFields) {
		t.Fatalf("plan fields = %v", fields)
	}
	for _, name := range wantFields {
		if _, ok := fields[name]; !ok {
			t.Fatalf("plan is missing %s", name)
		}
	}
}

func TestInstallJSONPrintsOnlyThePlan(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "sesame")
	env, stdout, stderr := testEnvironment(t, nil, Commands{Install: RunnerFunc(runInstall)})
	args := []string{"install", "--profile", "caddy", "--host", "vault.example.com", "--version", "0.4.1", "--dir", target, "--json"}
	if code := run(context.Background(), args, env); code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	var printed install.Plan
	decoder := json.NewDecoder(strings.NewReader(stdout.String()))
	if err := decoder.Decode(&printed); err != nil {
		t.Fatalf("stdout is not a JSON plan: %v in %q", err, stdout.String())
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		t.Fatalf("stdout holds more than one JSON value: %q", stdout.String())
	}
	onDisk := readInstallPlan(t, target)
	if !reflect.DeepEqual(printed, onDisk) {
		t.Fatalf("printed plan = %+v, on disk = %+v", printed, onDisk)
	}
}

func TestInstallRefusesAnExistingTargetAndLeavesStandardOutputEmpty(t *testing.T) {
	target := t.TempDir()
	env, stdout, stderr := testEnvironment(t, nil, Commands{Install: RunnerFunc(runInstall)})
	args := []string{"install", "--profile", "tailscale", "--host", "vault.example-tailnet.ts.net", "--version", "0.4.1", "--dir", target, "--json"}
	if code := run(context.Background(), args, env); code != exitFailure {
		t.Fatalf("exit %d", code)
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "sesame-server:") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestInstallGeneratesWithoutLoadingConfiguration(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "sesame")
	dataDir := filepath.Join(parent, "data")
	env, _, stderr := testEnvironment(t, map[string]string{
		config.EnvPublicURL: "http://sesame.example.net",
		config.EnvDataDir:   dataDir,
	}, Commands{Install: RunnerFunc(runInstall)})
	if code := run(context.Background(), []string{"install", "--profile", "local", "--version", "0.4.1", "--dir", target}, env); code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if _, err := os.Stat(dataDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the data directory was read or created: %v", err)
	}
	readInstallPlan(t, target)
}

func TestInstallRejectsInvalidValuesThroughTheRunner(t *testing.T) {
	cases := []struct {
		name string
		args func(parent string) []string
	}{
		{
			name: "local with a host",
			args: func(parent string) []string {
				return []string{"--profile", "local", "--host", "vault.example.com", "--version", "0.4.1", "--dir", filepath.Join(parent, "sesame")}
			},
		},
		{
			name: "caddy without a host",
			args: func(parent string) []string {
				return []string{"--profile", "caddy", "--version", "0.4.1", "--dir", filepath.Join(parent, "sesame")}
			},
		},
		{
			name: "tailscale without a host",
			args: func(parent string) []string {
				return []string{"--profile", "tailscale", "--version", "0.4.1", "--dir", filepath.Join(parent, "sesame")}
			},
		},
		{
			name: "version with a leading v",
			args: func(parent string) []string {
				return []string{"--profile", "local", "--version", "v0.4.1", "--dir", filepath.Join(parent, "sesame")}
			},
		},
		{
			name: "version with build metadata",
			args: func(parent string) []string {
				return []string{"--profile", "local", "--version", "0.4.1+build", "--dir", filepath.Join(parent, "sesame")}
			},
		},
		{
			name: "host with a command injection",
			args: func(parent string) []string {
				return []string{"--profile", "caddy", "--host", "vault.example.com;touch /tmp/pwned", "--version", "0.4.1", "--dir", filepath.Join(parent, "sesame")}
			},
		},
		{
			name: "unknown profile",
			args: func(parent string) []string {
				return []string{"--profile", "nginx", "--host", "vault.example.com", "--version", "0.4.1", "--dir", filepath.Join(parent, "sesame")}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parent := t.TempDir()
			target := filepath.Join(parent, "sesame")
			env, stdout, stderr := testEnvironment(t, nil, Commands{Install: RunnerFunc(runInstall)})
			args := append([]string{"install"}, tc.args(parent)...)
			if code := run(context.Background(), args, env); code != exitFailure {
				t.Fatalf("exit %d: %s", code, stderr.String())
			}
			if stdout.String() != "" {
				t.Fatalf("stdout = %q", stdout.String())
			}
			if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("the target was created: %v", err)
			}
		})
	}
}

func TestInstallRefusesAMissingParent(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "missing", "sesame")
	env, stdout, stderr := testEnvironment(t, nil, Commands{Install: RunnerFunc(runInstall)})
	args := []string{"install", "--profile", "local", "--version", "0.4.1", "--dir", target}
	if code := run(context.Background(), args, env); code != exitFailure {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(parent, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a missing ancestor was created: %v", err)
	}
}

func readInstallPlan(t *testing.T, target string) install.Plan {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(target, "plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	var plan install.Plan
	if err := json.Unmarshal(body, &plan); err != nil {
		t.Fatal(err)
	}
	return plan
}
