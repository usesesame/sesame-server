//go:build unix

package main

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"usesesame.app/backend/internal/selfhost/config"
)

func TestBackupStreamSignalHelper(t *testing.T) {
	if os.Getenv("SESAME_BACKUP_STREAM_HELPER") != "1" {
		return
	}
	os.Args = []string{"sesame-server", "backup", "--stdout"}
	main()
}

func TestCheckStreamSignalHelper(t *testing.T) {
	if os.Getenv("SESAME_CHECK_STREAM_HELPER") != "1" {
		return
	}
	os.Args = []string{"sesame-server", "check", "--stdin"}
	main()
}

func TestBackupStreamStopsOnSignalWithBlockedOutput(t *testing.T) {
	inst := newRecoveryTestInstance(t, "Ada")
	padding := strings.Repeat(" ", 512<<10)
	writeStreamConfig(t, inst.dir, `{"logLevel":"info"}`+padding)
	cases := []struct {
		name   string
		signal os.Signal
	}{
		{name: "interrupt", signal: os.Interrupt},
		{name: "terminate", signal: syscall.SIGTERM},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			command, stdout, stdin, stderr, finished := startStreamProcess(t, "TestBackupStreamSignalHelper", map[string]string{
				"SESAME_BACKUP_STREAM_HELPER": "1",
				config.EnvDataDir:             inst.dir,
			})
			defer stdin.Close()
			one := make([]byte, 1)
			if _, err := io.ReadFull(stdout, one); err != nil {
				t.Fatalf("the backup never started streaming: %v, stderr %s", err, stderr.String())
			}
			select {
			case err := <-finished:
				t.Fatalf("the backup exited before the signal: %v, stderr %s", err, stderr.String())
			case <-time.After(200 * time.Millisecond):
			}
			if err := command.Process.Signal(tc.signal); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-finished:
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != exitFailure {
					t.Fatalf("interrupted backup exit: %v, stderr %s", err, stderr.String())
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("the signal did not stop the backup: stderr %s", stderr.String())
			}
		})
	}
}

func TestBackupStreamReportsBrokenPipe(t *testing.T) {
	inst := newRecoveryTestInstance(t, "Ada")
	_, stdout, stdin, stderr, finished := startStreamProcess(t, "TestBackupStreamSignalHelper", map[string]string{
		"SESAME_BACKUP_STREAM_HELPER": "1",
		config.EnvDataDir:             inst.dir,
	})
	defer stdin.Close()
	if err := stdout.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != exitFailure {
			t.Fatalf("broken pipe exit: %v, stderr %s", err, stderr.String())
		}
		if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			t.Fatalf("the process died from %v instead of reporting the failure", status.Signal())
		}
		if strings.Contains(stderr.String(), "Backup streamed") {
			t.Fatalf("the broken stream reported success: %s", stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("the backup did not notice the broken pipe: stderr %s", stderr.String())
	}
}

func TestCheckStreamStopsOnSignalWithBlockedInput(t *testing.T) {
	cases := []struct {
		name   string
		signal os.Signal
	}{
		{name: "interrupt", signal: os.Interrupt},
		{name: "terminate", signal: syscall.SIGTERM},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			command, _, stdin, stderr, finished := startStreamProcess(t, "TestCheckStreamSignalHelper", map[string]string{
				"SESAME_CHECK_STREAM_HELPER": "1",
				config.EnvDataDir:            dir,
			})
			defer stdin.Close()
			waitForStreamWork(t, dir)
			select {
			case err := <-finished:
				t.Fatalf("the check exited before the signal: %v, stderr %s", err, stderr.String())
			default:
			}
			if err := command.Process.Signal(tc.signal); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-finished:
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != exitFailure {
					t.Fatalf("interrupted check exit: %v, stderr %s", err, stderr.String())
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("the signal did not stop the check: stderr %s", stderr.String())
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".check-stream-") {
					t.Fatalf("%s was left behind", entry.Name())
				}
			}
		})
	}
}

func startStreamProcess(t *testing.T, testName string, values map[string]string) (*exec.Cmd, io.ReadCloser, io.WriteCloser, *syncBuffer, <-chan error) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^"+testName+"$")
	command.Env = replaceEnvironment(os.Environ(), values)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := &syncBuffer{}
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() {
		finished <- command.Wait()
		close(finished)
	}()
	t.Cleanup(func() {
		_ = command.Process.Kill()
		<-finished
		_ = stdout.Close()
		_ = stdin.Close()
	})
	return command, stdout, stdin, stderr, finished
}

func replaceEnvironment(environment []string, values map[string]string) []string {
	kept := make([]string, 0, len(environment)+len(values))
	for _, entry := range environment {
		replace := false
		for key := range values {
			if strings.HasPrefix(entry, key+"=") {
				replace = true
				break
			}
		}
		if !replace {
			kept = append(kept, entry)
		}
	}
	for key, value := range values {
		kept = append(kept, key+"="+value)
	}
	return kept
}

func waitForStreamWork(t *testing.T, dir string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".check-stream-") {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the check never started staging its input")
}
