package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"usesesame.app/backend/internal/selfhost/config"
)

func TestParseCommand(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		want     string
		wantArgs []string
		wantErr  string
	}{
		{name: "no arguments serve", args: nil, want: "serve"},
		{name: "serve", args: []string{"serve"}, want: "serve"},
		{name: "backup without a path", args: []string{"backup"}, want: "backup"},
		{name: "backup with a path", args: []string{"backup", "/tmp/out.bak"}, want: "backup", wantArgs: []string{"/tmp/out.bak"}},
		{name: "restore", args: []string{"restore", "in.bak"}, want: "restore", wantArgs: []string{"in.bak"}},
		{name: "check without a file", args: []string{"check"}, want: "check"},
		{name: "check with a file", args: []string{"check", "in.bak"}, want: "check", wantArgs: []string{"in.bak"}},
		{name: "owner reset", args: []string{"owner", "reset", "Ada Quill"}, want: "owner reset", wantArgs: []string{"Ada Quill"}},
		{name: "export", args: []string{"export", "out.json"}, want: "export", wantArgs: []string{"out.json"}},
		{name: "healthcheck", args: []string{"healthcheck"}, want: "healthcheck"},
		{name: "version", args: []string{"version"}, want: "version"},
		{name: "version flag", args: []string{"--version"}, want: "version"},
		{name: "double dash keeps a dashed file name", args: []string{"backup", "--", "-odd"}, want: "backup", wantArgs: []string{"-odd"}},
		{name: "unknown command", args: []string{"launch"}, wantErr: `unknown command "launch"`},
		{name: "unknown flag", args: []string{"serve", "--bogus"}, wantErr: "serve:"},
		{name: "leading unknown flag", args: []string{"--bogus"}, wantErr: "unknown command"},
		{name: "serve with an argument", args: []string{"serve", "extra"}, wantErr: "takes no arguments"},
		{name: "healthcheck with an argument", args: []string{"healthcheck", "extra"}, wantErr: "takes no arguments"},
		{name: "backup with two paths", args: []string{"backup", "a", "b"}, wantErr: "at most 1"},
		{name: "restore without a file", args: []string{"restore"}, wantErr: "exactly 1"},
		{name: "restore with two files", args: []string{"restore", "a", "b"}, wantErr: "exactly 1"},
		{name: "owner without a subcommand", args: []string{"owner"}, wantErr: "owner needs the subcommand reset"},
		{name: "owner with another subcommand", args: []string{"owner", "delete", "x"}, wantErr: "owner needs the subcommand reset"},
		{name: "owner reset without a name", args: []string{"owner", "reset"}, wantErr: "exactly 1"},
		{name: "owner reset with two names", args: []string{"owner", "reset", "a", "b"}, wantErr: "exactly 1"},
		{name: "version with an argument", args: []string{"version", "x"}, wantErr: "no arguments"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseCommand(tc.args)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.name != tc.want || strings.Join(got.args, "|") != strings.Join(tc.wantArgs, "|") {
				t.Fatalf("parsed = %+v, want %s %v", got, tc.want, tc.wantArgs)
			}
		})
	}
}

func TestHelpIsAnExplicitRequest(t *testing.T) {
	for _, args := range [][]string{{"-h"}, {"--help"}, {"help"}, {"backup", "-h"}} {
		env, stdout, stderr := testEnvironment(t, nil, Commands{})
		if code := run(context.Background(), args, env); code != exitOK {
			t.Fatalf("%v: exit %d, stderr %q", args, code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "Usage: sesame-server") {
			t.Fatalf("%v: stdout %q", args, stdout.String())
		}
	}
}

func TestVersionNeedsNoConfiguration(t *testing.T) {
	env, stdout, _ := testEnvironment(t, map[string]string{config.EnvAddr: "not an address"}, Commands{})
	if code := run(context.Background(), []string{"version"}, env); code != exitOK {
		t.Fatalf("exit %d", code)
	}
	if stdout.String() != "sesame-server 9.9.9-test (abc1234)\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestUsageErrorsExitWithTwo(t *testing.T) {
	env, stdout, stderr := testEnvironment(t, nil, Commands{})
	if code := run(context.Background(), []string{"restore"}, env); code != exitUsage {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(stderr.String(), "restore: needs exactly 1 argument") || !strings.Contains(stderr.String(), "Usage:") || stdout.String() != "" {
		t.Fatalf("stderr = %q, stdout = %q", stderr.String(), stdout.String())
	}
}

func TestBadConfigurationStopsEveryCommandAndNamesTheVariable(t *testing.T) {
	for _, command := range [][]string{{"serve"}, {"backup"}, {"restore", "x"}, {"check"}, {"owner", "reset", "Ada"}, {"export"}, {"healthcheck"}} {
		var calls recorded
		commands := Commands{
			Serve:      ApplicationBuilderFunc(func(context.Context, BuildInput) (*Application, error) { t.Fatal("serve built"); return nil, nil }),
			Backup:     calls.runner(nil),
			Restore:    calls.runner(nil),
			Check:      calls.runner(nil),
			OwnerReset: calls.runner(nil),
			Export:     calls.runner(nil),
		}
		env, _, stderr := testEnvironment(t, map[string]string{config.EnvPublicURL: "http://sesame.example.net"}, commands)
		if code := run(context.Background(), command, env); code != exitFailure {
			t.Fatalf("%v: exit %d", command, code)
		}
		if !strings.Contains(stderr.String(), "SESAME_PUBLIC_URL") || !strings.Contains(stderr.String(), "configuration is invalid") {
			t.Fatalf("%v: stderr = %q", command, stderr.String())
		}
		if len(calls.calls()) != 0 {
			t.Fatalf("%v: a runner ran with an invalid configuration", command)
		}
	}
}

func TestEachSubcommandReachesItsRunnerWithConfigAndArguments(t *testing.T) {
	cases := []struct {
		args []string
		name string
	}{
		{[]string{"backup", "/tmp/a.bak"}, "backup"},
		{[]string{"restore", "/tmp/a.bak"}, "restore"},
		{[]string{"check", "/tmp/a.bak"}, "check"},
		{[]string{"owner", "reset", "Ada Quill"}, "owner reset"},
		{[]string{"export", "/tmp/a.json"}, "export"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			records := map[string]*recorded{}
			for _, name := range []string{"backup", "restore", "check", "owner reset", "export"} {
				records[name] = &recorded{}
			}
			commands := Commands{
				Backup:     records["backup"].runner(nil),
				Restore:    records["restore"].runner(nil),
				Check:      records["check"].runner(nil),
				OwnerReset: records["owner reset"].runner(nil),
				Export:     records["export"].runner(nil),
			}
			env, _, stderr := testEnvironment(t, map[string]string{config.EnvLogLevel: "debug"}, commands)
			if code := run(context.Background(), tc.args, env); code != exitOK {
				t.Fatalf("exit %d: %s", code, stderr.String())
			}
			for name, record := range records {
				want := 0
				if name == tc.name {
					want = 1
				}
				if len(record.calls()) != want {
					t.Fatalf("%s ran %d times, want %d", name, len(record.calls()), want)
				}
			}
			got := records[tc.name].calls()[0]
			if strings.Join(got.Args, "|") != strings.Join(tc.args[len(tc.args)-1:], "|") {
				t.Fatalf("args = %v", got.Args)
			}
			if got.Config.LogLevel != "debug" || got.Config.DataDir == "" || got.Logger == nil || got.Stdout == nil || got.Stderr == nil {
				t.Fatalf("invocation = %+v", got)
			}
		})
	}
}

func TestRunnerFailureExitsWithOneAndShowsTheMessage(t *testing.T) {
	var calls recorded
	env, _, stderr := testEnvironment(t, nil, Commands{Backup: calls.runner(errors.New("disk is full"))})
	if code := run(context.Background(), []string{"backup"}, env); code != exitFailure {
		t.Fatalf("exit %d", code)
	}
	if stderr.String() != "sesame-server: disk is full\n" {
		t.Fatalf("stderr = %q", stderr.String())
	}
}
