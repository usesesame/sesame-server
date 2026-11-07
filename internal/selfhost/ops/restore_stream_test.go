package ops

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"usesesame.app/backend/internal/selfhost/secrets"
)

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

func assertNoRestoreInput(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), restoreInputPrefix) {
			t.Fatalf("restore left %s behind", entry.Name())
		}
	}
}

func TestStageRestoreInputIsPrivate(t *testing.T) {
	dir := t.TempDir()
	path, err := stageRestoreInput(context.Background(), dir, strings.NewReader("archive input"), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	assertMode(t, path, 0o600)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

func TestRecoverRemovesInterruptedRestoreInput(t *testing.T) {
	dir := t.TempDir()
	path, err := stageRestoreInput(context.Background(), dir, strings.NewReader("interrupted archive"), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Recover(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("interrupted input remains: %v", err)
	}
}

func TestRecoverRemovesReservedNumericInputFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, restoreInputPrefix+"123456789")
	writeFile(t, path, []byte("abandoned input"), 0o600)
	if _, err := Recover(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reserved input remains: %v", err)
	}
}

func TestRecoverRestoresJournalAndRemovesStagedInput(t *testing.T) {
	oldInst, _, archive, oldState, _ := restoreFixture(t)
	crash := func(step string) error {
		if step == "old-moved" {
			return errSimulatedCrash
		}
		return nil
	}
	if _, err := Restore(context.Background(), archive, oldInst.dir, withHook(crash)); !errors.Is(err, errSimulatedCrash) {
		t.Fatalf("restore did not leave an interrupted journal: %v", err)
	}
	if _, err := stageRestoreInput(context.Background(), oldInst.dir, strings.NewReader("abandoned archive"), DefaultLimits()); err != nil {
		t.Fatal(err)
	}
	if recovered, err := Recover(oldInst.dir); err != nil || recovered != "rolled back" {
		t.Fatalf("combined recovery: %q, %v", recovered, err)
	}
	assertSnapshot(t, oldInst.dir, oldState, "after journal and input recovery")
	assertCleanDataDir(t, oldInst.dir)
	assertNoRestoreInput(t, oldInst.dir)
}

func TestRecoverLeavesInterruptedInputAloneWhileLocked(t *testing.T) {
	dir := t.TempDir()
	path, err := stageRestoreInput(context.Background(), dir, strings.NewReader("interrupted archive"), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	lock, err := AcquireInstanceLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	if _, err := Recover(dir); !errors.Is(err, ErrInstanceRunning) {
		t.Fatalf("recovery did not respect the active lock: %v", err)
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("recovery changed locked input: %v", err)
	}
}

func TestRecoverPreservesOtherFilesAndDirectories(t *testing.T) {
	dir := t.TempDir()
	if _, err := stageRestoreInput(context.Background(), dir, strings.NewReader("interrupted archive"), DefaultLimits()); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, restoreInputPrefix+"notes")
	writeFile(t, other, []byte("operator notes"), 0o600)
	directory := filepath.Join(dir, restoreInputPrefix+"123")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Recover(dir); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{other, directory} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("recovery removed unrelated entry %s: %v", path, err)
		}
	}
}

func TestRestoreReaderImportsArchiveFromStream(t *testing.T) {
	oldInst, _, archive, oldState, newState := restoreFixture(t)
	result, err := RestoreReader(context.Background(), bytes.NewReader(readFile(t, archive)), oldInst.dir)
	if err != nil {
		t.Fatal(err)
	}
	assertRestored(t, oldInst.dir, newState, "restored from standard input")
	assertSnapshot(t, result.AsideDir, oldState, "previous data")
	assertMode(t, filepath.Join(oldInst.dir, DatabaseName), 0o600)
	assertMode(t, secrets.Dir(oldInst.dir), 0o700)
	assertMode(t, filepath.Join(secrets.Dir(oldInst.dir), secrets.AdminKeyFile), 0o600)
	assertCleanDataDir(t, oldInst.dir)
	assertNoRestoreInput(t, oldInst.dir)
}

func TestRestoreReaderImportsIntoEmptyDirectory(t *testing.T) {
	_, _, archive, _, newState := restoreFixture(t)
	target := filepath.Join(t.TempDir(), "fresh")
	if _, err := RestoreReader(context.Background(), bytes.NewReader(readFile(t, archive)), target); err != nil {
		t.Fatal(err)
	}
	assertRestored(t, target, newState, "fresh restore from standard input")
	assertCleanDataDir(t, target)
	assertNoRestoreInput(t, target)
}

func TestRestoreReaderRejectsTruncatedStream(t *testing.T) {
	oldInst, _, archive, oldState, _ := restoreFixture(t)
	data := readFile(t, archive)
	for _, cut := range []int{0, 10, 700, len(data) / 2, len(data) - 1600} {
		if _, err := RestoreReader(context.Background(), bytes.NewReader(data[:cut]), oldInst.dir); err == nil {
			t.Fatalf("archive cut at %d of %d bytes passed", cut, len(data))
		}
		assertSnapshot(t, oldInst.dir, oldState, "after refused truncated stream")
		assertCleanDataDir(t, oldInst.dir)
		assertNoRestoreInput(t, oldInst.dir)
	}
}

func TestRestoreReaderRejectsMalformedStream(t *testing.T) {
	oldInst, _, archive, oldState, _ := restoreFixture(t)
	data := readFile(t, archive)
	offset := bytes.Index(data, []byte("SQLite format 3"))
	data[offset+30] ^= 0xFF
	_, err := RestoreReader(context.Background(), bytes.NewReader(data), oldInst.dir)
	wantInvalid(t, err, "")
	assertSnapshot(t, oldInst.dir, oldState, "after refused malformed stream")
	assertCleanDataDir(t, oldInst.dir)
	assertNoRestoreInput(t, oldInst.dir)

	if _, err := RestoreReader(context.Background(), strings.NewReader("not an archive"), oldInst.dir); err == nil {
		t.Fatal("non-archive input passed")
	}
	assertSnapshot(t, oldInst.dir, oldState, "after refused non-archive stream")
	assertCleanDataDir(t, oldInst.dir)
	assertNoRestoreInput(t, oldInst.dir)
}

func TestRestoreReaderRejectsTrailingData(t *testing.T) {
	oldInst, _, archive, oldState, _ := restoreFixture(t)
	data := append(readFile(t, archive), []byte("trailing")...)
	_, err := RestoreReader(context.Background(), bytes.NewReader(data), oldInst.dir)
	wantInvalid(t, err, "follows the end")
	assertSnapshot(t, oldInst.dir, oldState, "after refused trailing stream")
	assertCleanDataDir(t, oldInst.dir)
	assertNoRestoreInput(t, oldInst.dir)
}

func TestRestoreReaderRejectsOversizedStream(t *testing.T) {
	oldInst, _, _, oldState, _ := restoreFixture(t)
	limits := Limits{MaxDatabaseBytes: 1, MaxConfigBytes: 1}
	input := bytes.NewReader(make([]byte, limits.maxArchiveBytes()+1))
	_, err := RestoreReader(context.Background(), input, oldInst.dir, WithLimits(limits))
	wantInvalid(t, err, "over the limit")
	assertSnapshot(t, oldInst.dir, oldState, "after refused oversized stream")
	assertCleanDataDir(t, oldInst.dir)
	assertNoRestoreInput(t, oldInst.dir)
}

func TestRestoreReaderAppliesMemberLimits(t *testing.T) {
	oldInst, _, archive, oldState, _ := restoreFixture(t)
	limits := Limits{MaxDatabaseBytes: 1024, MaxConfigBytes: 1 << 20}
	_, err := RestoreReader(context.Background(), bytes.NewReader(readFile(t, archive)), oldInst.dir, WithLimits(limits))
	wantInvalid(t, err, "")
	assertSnapshot(t, oldInst.dir, oldState, "after refused member limit")
	assertCleanDataDir(t, oldInst.dir)
	assertNoRestoreInput(t, oldInst.dir)
}

func TestRestoreReaderReportsReadFailureAndCleansUp(t *testing.T) {
	oldInst, _, _, oldState, _ := restoreFixture(t)
	readFailure := errors.New("input pipe failed")
	data := []byte("partial input")
	reads := 0
	input := readerFunc(func(p []byte) (int, error) {
		if reads > 0 {
			return 0, readFailure
		}
		reads++
		return copy(p, data), nil
	})
	_, err := RestoreReader(context.Background(), input, oldInst.dir)
	if !errors.Is(err, readFailure) {
		t.Fatalf("err = %v", err)
	}
	assertSnapshot(t, oldInst.dir, oldState, "after read failure")
	assertCleanDataDir(t, oldInst.dir)
	assertNoRestoreInput(t, oldInst.dir)
}

func TestRestoreReaderStopsOnCanceledContext(t *testing.T) {
	oldInst, _, archive, oldState, _ := restoreFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := RestoreReader(ctx, bytes.NewReader(readFile(t, archive)), oldInst.dir)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	assertSnapshot(t, oldInst.dir, oldState, "after canceled restore")
	assertCleanDataDir(t, oldInst.dir)
	assertNoRestoreInput(t, oldInst.dir)
}

func TestRestoreReaderStopsWhenContextIsCanceledBetweenReads(t *testing.T) {
	oldInst, _, archive, oldState, _ := restoreFixture(t)
	data := readFile(t, archive)
	ctx, cancel := context.WithCancel(context.Background())
	reads := 0
	input := readerFunc(func(p []byte) (int, error) {
		if reads > 0 {
			return 0, io.EOF
		}
		reads++
		n := copy(p, data)
		cancel()
		return n, nil
	})
	_, err := RestoreReader(ctx, input, oldInst.dir)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	assertSnapshot(t, oldInst.dir, oldState, "after mid-read cancellation")
	assertCleanDataDir(t, oldInst.dir)
	assertNoRestoreInput(t, oldInst.dir)
}

func TestRestoreReaderRefusesBeforeReadingWhileInstanceIsLocked(t *testing.T) {
	oldInst, _, _, oldState, _ := restoreFixture(t)
	lock, err := AcquireInstanceLock(oldInst.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	read := false
	input := readerFunc(func([]byte) (int, error) {
		read = true
		return 0, errors.New("the input must not be read")
	})
	_, err = RestoreReader(context.Background(), input, oldInst.dir)
	if !errors.Is(err, ErrInstanceRunning) {
		t.Fatalf("err = %v", err)
	}
	if read {
		t.Fatal("standard input was read while the instance lock was held")
	}
	assertSnapshot(t, oldInst.dir, oldState, "while locked")
	assertCleanDataDir(t, oldInst.dir)
	assertNoRestoreInput(t, oldInst.dir)
}

func TestRestoreReaderRefusesSecretsMismatch(t *testing.T) {
	oldInst, _, archive, oldState, _ := restoreFixture(t)
	u := unpack(t, archive)
	writeFile(t, u.path("secrets/"+secrets.AdminKeyFile), bytes.Repeat([]byte{9}, 32), 0o600)
	_, err := RestoreReader(context.Background(), bytes.NewReader(readFile(t, u.repack(t, nil))), oldInst.dir)
	if !errors.Is(err, ErrSecretsMismatch) {
		t.Fatalf("err = %v", err)
	}
	assertSnapshot(t, oldInst.dir, oldState, "after refused secrets mismatch")
	assertCleanDataDir(t, oldInst.dir)
	assertNoRestoreInput(t, oldInst.dir)
}

func TestRestoreReaderUsesTheRestoreJournal(t *testing.T) {
	oldInst, _, archive, oldState, newState := restoreFixture(t)
	data := readFile(t, archive)
	crash := func(step string) error {
		if step == "installed:"+DatabaseName {
			return errSimulatedCrash
		}
		return nil
	}
	if _, err := RestoreReader(context.Background(), bytes.NewReader(data), oldInst.dir, withHook(crash)); !errors.Is(err, errSimulatedCrash) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(oldInst.dir, journalName)); err != nil {
		t.Fatalf("journal missing after the crash: %v", err)
	}
	assertNoRestoreInput(t, oldInst.dir)
	recovered, err := Recover(oldInst.dir)
	if err != nil {
		t.Fatal(err)
	}
	if recovered != "rolled back" {
		t.Fatalf("recovered = %q", recovered)
	}
	assertSnapshot(t, oldInst.dir, oldState, "after recovery")
	assertCleanDataDir(t, oldInst.dir)
	if _, err := RestoreReader(context.Background(), bytes.NewReader(data), oldInst.dir); err != nil {
		t.Fatal(err)
	}
	assertRestored(t, oldInst.dir, newState, "after restored retry")
	assertCleanDataDir(t, oldInst.dir)
	assertNoRestoreInput(t, oldInst.dir)
}
