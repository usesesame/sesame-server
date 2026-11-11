//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"usesesame.app/backend/internal/selfhost/config"
	"usesesame.app/backend/internal/selfhost/diagnostics"
)

func TestDoctorSignalHelper(t *testing.T) {
	if os.Getenv("SESAME_DOCTOR_SIGNAL_HELPER") != "1" {
		return
	}
	os.Args = []string{"sesame-server", "doctor", "--json"}
	main()
}

func TestDoctorStopsOnSignalWithBlockedOutput(t *testing.T) {
	cases := []struct {
		name   string
		signal os.Signal
		merged bool
	}{
		{name: "interrupt stdout", signal: os.Interrupt},
		{name: "terminate stdout", signal: syscall.SIGTERM},
		{name: "interrupt merged", signal: os.Interrupt, merged: true},
		{name: "terminate merged", signal: syscall.SIGTERM, merged: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newDoctorFixture(t, 0o600)
			settings := fixture.environment()
			prefixes := make([]string, 64)
			for index := range prefixes {
				prefixes[index] = fmt.Sprintf("fd00:1111:2222:3333:4444:5555:6666:%x/128", index+1)
			}
			settings[config.EnvTrustedProxies] = strings.Join(prefixes, ",")
			report := diagnostics.Inspect(context.Background(), diagnostics.Options{
				Lookup:      func(name string) (string, bool) { value, ok := settings[name]; return value, ok },
				ToolVersion: "signal-fixture",
			})
			body, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			if len(body) <= 4096+512 {
				t.Fatalf("the report is too small to block the fixture pipe: %d", len(body))
			}
			before := snapshotDoctorTree(t, fixture.root)
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			defer writer.Close()
			capacity, _, errno := syscall.Syscall(syscall.SYS_FCNTL, writer.Fd(), syscall.F_SETPIPE_SZ, 4096)
			if errno != 0 {
				t.Fatal(errno)
			}
			if capacity != 4096 {
				t.Fatalf("the signal fixture requires a 4096-byte pipe, got %d", capacity)
			}
			binary := os.Getenv("SESAME_DOCTOR_JOURNEY_BINARY")
			args := []string{"doctor", "--json"}
			if binary == "" {
				binary = os.Args[0]
				args = []string{"-test.run=^TestDoctorSignalHelper$"}
				settings["SESAME_DOCTOR_SIGNAL_HELPER"] = "1"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, binary, args...)
			for _, entry := range os.Environ() {
				if !strings.HasPrefix(entry, "SESAME_") {
					command.Env = append(command.Env, entry)
				}
			}
			for name, value := range settings {
				command.Env = append(command.Env, name+"="+value)
			}
			var stderr bytes.Buffer
			command.Stdout = writer
			command.Stderr = &stderr
			if tc.merged {
				command.Stderr = writer
			}
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			finished := make(chan error, 1)
			go func() { finished <- command.Wait() }()
			var first [1]byte
			_, readErr := io.ReadFull(reader, first[:])
			if readErr == nil {
				if err := command.Process.Signal(tc.signal); err != nil {
					t.Fatal(err)
				}
			}
			waitErr := <-finished
			if ctx.Err() != nil {
				t.Fatalf("the doctor did not stop while output was blocked: %v", ctx.Err())
			}
			if readErr != nil || first[0] != '{' {
				t.Fatalf("the report did not reach its blocked output: %v, stderr %q", readErr, stderr.String())
			}
			if waitErr == nil || command.ProcessState.ExitCode() != exitFailure {
				t.Fatalf("exit %d, error %v, stderr %q", command.ProcessState.ExitCode(), waitErr, stderr.String())
			}
			if !tc.merged && stderr.String() != doctorWriteFailure {
				t.Fatalf("stderr = %q", stderr.String())
			}
			if !reflect.DeepEqual(before, snapshotDoctorTree(t, fixture.root)) {
				t.Fatal("the interrupted doctor changed its data tree")
			}
		})
	}
}
