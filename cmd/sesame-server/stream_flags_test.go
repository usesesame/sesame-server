package main

import (
	"context"
	"strings"
	"testing"
)

func TestStreamFlagsAreAccepted(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{args: []string{"backup", "--stdout"}, want: "backup"},
		{args: []string{"check", "--stdin"}, want: "check"},
		{args: []string{"check", "--stdin", "--json"}, want: "check"},
		{args: []string{"check", "archive.tar", "--json"}, want: "check"},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			got, err := parseCommand(tc.args)
			if err != nil {
				t.Fatalf("%v was rejected: %v", tc.args, err)
			}
			if got.name != tc.want {
				t.Fatalf("parsed = %+v, want %s", got, tc.want)
			}
		})
	}
}

func TestStreamFlagsReachTheirRunner(t *testing.T) {
	var calls recorded
	env, _, stderr := testEnvironment(t, nil, Commands{
		Backup: calls.runner(nil),
		Check:  calls.runner(nil),
	})
	if code := run(context.Background(), []string{"backup", "--stdout"}, env); code != exitOK {
		t.Fatalf("backup --stdout exit %d: %s", code, stderr.String())
	}
	if code := run(context.Background(), []string{"check", "--stdin", "--json"}, env); code != exitOK {
		t.Fatalf("check --stdin --json exit %d: %s", code, stderr.String())
	}
	got := calls.calls()
	if len(got) != 2 {
		t.Fatalf("runner calls = %d, want 2", len(got))
	}
	if !got[0].Flags.Stdout || got[0].Flags.Stdin || got[0].Flags.JSON {
		t.Fatalf("backup flags = %+v", got[0].Flags)
	}
	if !got[1].Flags.Stdin || !got[1].Flags.JSON || got[1].Flags.Stdout {
		t.Fatalf("check flags = %+v", got[1].Flags)
	}
}

func TestStreamFlagsRejectIncompatibleArguments(t *testing.T) {
	cases := []struct {
		args []string
		text string
	}{
		{args: []string{"backup", "--stdout", "backup.tar"}, text: "no output file"},
		{args: []string{"check", "--stdin", "backup.tar"}, text: "does not take a backup file"},
		{args: []string{"check", "--json"}, text: "needs a backup file or --stdin"},
		{args: []string{"check", "--bogus"}, text: "flag provided but not defined"},
		{args: []string{"check", "one.tar", "two.tar"}, text: "at most 1"},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			_, err := parseCommand(tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.text) {
				t.Fatalf("%v error = %v, want %q", tc.args, err, tc.text)
			}
		})
	}
}

func TestCheckHelpStillWorks(t *testing.T) {
	for _, args := range [][]string{{"check", "-h"}, {"check", "--help"}} {
		env, stdout, stderr := testEnvironment(t, nil, Commands{})
		if code := run(context.Background(), args, env); code != exitOK {
			t.Fatalf("%v: exit %d, stderr %q", args, code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "Usage: sesame-server") {
			t.Fatalf("%v: stdout %q", args, stdout.String())
		}
	}
}
