//go:build unix

package main

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"usesesame.app/backend/internal/selfhost/config"
)

func TestRestoreInputSignalHelper(t *testing.T) {
	if os.Getenv("SESAME_RESTORE_SIGNAL_HELPER") != "1" {
		return
	}
	os.Args = []string{"sesame-server", "restore", "-"}
	main()
}

func TestRestoreInputStopsOnInterrupt(t *testing.T) {
	command, dir, finished := startBlockedRestore(t)
	if err := command.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != exitFailure {
			t.Fatalf("interrupted restore exit: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("interrupt did not stop the restore while standard input was blocked")
	}
	assertNoStagedInput(t, dir)
}

func TestServerRemovesRestoreInputAfterHardKill(t *testing.T) {
	process, dir, finished := startBlockedRestore(t)
	if err := process.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := <-finished; err == nil {
		t.Fatal("the killed restore exited successfully")
	}
	entries, err := filepath.Glob(filepath.Join(dir, ".restore-input-*"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("the killed restore did not leave its staged input: %v, %v", entries, err)
	}
	running := startReal(t, dir)
	running.stop(t)
	assertNoStagedInput(t, dir)
	if code, _, stderr := command(t, map[string]string{config.EnvDataDir: dir}, "check"); code != exitOK {
		t.Fatalf("database after startup recovery: exit %d, %s", code, stderr)
	}
}

func TestServerReportsActiveRestoreAsBusy(t *testing.T) {
	_, dir, _ := startBlockedRestore(t)
	code, _, stderr := command(t, map[string]string{config.EnvDataDir: dir}, "serve")
	if code != exitFailure || !strings.Contains(stderr, "another Sesame server is already using") {
		t.Fatalf("active restore diagnostic: exit %d, %s", code, stderr)
	}
}

func assertNoStagedInput(t *testing.T, dir string) {
	t.Helper()
	entries, err := filepath.Glob(filepath.Join(dir, ".restore-input-*"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("interrupted restore left input files: %v, %v", entries, err)
	}
}

func startBlockedRestore(t *testing.T) (*exec.Cmd, string, <-chan error) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	command := exec.Command(executable, "-test.run=^TestRestoreInputSignalHelper$")
	command.Env = append(os.Environ(), "SESAME_RESTORE_SIGNAL_HELPER=1", "SESAME_DATA_DIR="+dir)
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
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
	})
	t.Cleanup(func() { _ = input.Close() })
	if _, err := io.WriteString(input, "unfinished archive"); err != nil {
		t.Fatal(err)
	}
	staging := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		entries, readErr := os.ReadDir(dir)
		if readErr != nil {
			t.Fatal(readErr)
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".restore-input-") {
				staging = true
			}
		}
		if staging {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !staging {
		t.Fatal("the restore never started reading standard input")
	}
	return command, dir, finished
}
