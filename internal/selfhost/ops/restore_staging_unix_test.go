//go:build unix

package ops

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestRestoreStagingCrashHelper(t *testing.T) {
	if os.Getenv("SESAME_RESTORE_STAGING_HELPER") != "1" {
		return
	}
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	hook := func(step string) error {
		if step == "restore-prepared" {
			fmt.Fprintln(os.Stdout, "restore prepared")
			<-time.After(time.Hour)
		}
		return nil
	}
	if _, err := Restore(context.Background(), filepath.Join(dir, "restore-crash-test.tar"), dir, withHook(hook)); err != nil {
		t.Fatal(err)
	}
}

func TestRecoverRemovesPreparedStagingAfterHardKill(t *testing.T) {
	oldInst, _, archive, oldState, _ := restoreFixture(t)
	if err := copyFileLimited(archive, filepath.Join(oldInst.dir, "restore-crash-test.tar"), DefaultLimits().maxArchiveBytes()); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(executable, "-test.run=^TestRestoreStagingCrashHelper$")
	child.Dir = oldInst.dir
	child.Env = append(os.Environ(), "SESAME_RESTORE_STAGING_HELPER=1")
	output, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() {
		finished <- child.Wait()
		close(finished)
	}()
	t.Cleanup(func() {
		_ = child.Process.Kill()
		<-finished
	})
	ready := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(output).ReadString('\n')
		ready <- line
	}()
	select {
	case line := <-ready:
		if line != "restore prepared\n" {
			t.Fatalf("restore did not reach private preparation: %q", line)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("restore did not reach private preparation")
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := <-finished; err == nil {
		t.Fatal("killed restore exited successfully")
	}
	staging, err := filepath.Glob(filepath.Join(oldInst.dir, stagingPrefix+"*"))
	if err != nil || len(staging) != 1 {
		t.Fatalf("killed restore did not leave prepared staging: %v, %v", staging, err)
	}
	if _, err := os.Stat(filepath.Join(staging[0], "secrets")); err != nil {
		t.Fatalf("staging did not contain restored secrets: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(oldInst.dir, journalName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the pre-swap crash wrote a journal: %v", err)
	}
	if _, err := Recover(oldInst.dir); err != nil {
		t.Fatal(err)
	}
	assertSnapshot(t, oldInst.dir, oldState, "after pre-swap crash recovery")
	assertCleanDataDir(t, oldInst.dir)
}

func TestRecoverDoesNotFollowStagingLinks(t *testing.T) {
	dir := t.TempDir()
	staging := abandonedRestoreStaging(t, dir)
	outside := t.TempDir()
	marker := filepath.Join(outside, "operator-file")
	writeFile(t, marker, []byte("operator data"), 0o600)
	before := fileDigest(t, marker)
	link := filepath.Join(dir, stagingPrefix+"abcdef012345")
	for _, path := range []string{link, filepath.Join(staging, "nested-link")} {
		if err := os.Symlink(outside, path); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Recover(dir); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("recovery changed the external link: %v, %v", info, err)
	}
	if fileDigest(t, marker) != before {
		t.Fatal("staging cleanup followed a link outside the data directory")
	}
}
