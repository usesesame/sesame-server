package ops

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"usesesame.app/backend/internal/selfhost/secrets"
)

type snapshot struct {
	database string
	secrets  map[string]string
	config   string
}

func takeSnapshot(t *testing.T, dir string) snapshot {
	t.Helper()
	state := snapshot{database: fileDigest(t, filepath.Join(dir, DatabaseName)), secrets: map[string]string{}}
	for _, name := range secrets.Files() {
		state.secrets[name] = fileDigest(t, filepath.Join(secrets.Dir(dir), name))
	}
	if _, err := os.Lstat(filepath.Join(dir, ConfigName)); err == nil {
		state.config = fileDigest(t, filepath.Join(dir, ConfigName))
	}
	return state
}

func assertSnapshot(t *testing.T, dir string, want snapshot, label string) {
	t.Helper()
	got := takeSnapshot(t, dir)
	if got.database != want.database || got.config != want.config {
		t.Fatalf("%s: database or config differ from the expected state", label)
	}
	for name, digest := range want.secrets {
		if got.secrets[name] != digest {
			t.Fatalf("%s: secret %s differs", label, name)
		}
	}
}

func assertCleanDataDir(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, stagingPrefix) || strings.HasPrefix(name, journalName) {
			t.Fatalf("restore left %s behind", name)
		}
	}
}

func restoreFixture(t *testing.T) (oldInst, newInst *instance, archive string, oldState, newState snapshot) {
	t.Helper()
	oldInst = newInstance(t, "Ada", true)
	newInst = newInstance(t, "Bea", false)
	if err := oldInst.store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := newInst.store.Close(); err != nil {
		t.Fatal(err)
	}
	archive = newInst.backup(t)
	oldState = takeSnapshot(t, oldInst.dir)
	u := unpack(t, archive)
	newState = snapshot{database: fileDigest(t, u.path(DatabaseName)), secrets: map[string]string{}}
	for _, name := range secrets.Files() {
		newState.secrets[name] = fileDigest(t, u.path("secrets/"+name))
	}
	return
}

func TestRestoreReplacesDataAndKeepsPreviousAside(t *testing.T) {
	oldInst, _, archive, oldState, newState := restoreFixture(t)
	writeFile(t, filepath.Join(oldInst.dir, DatabaseName+"-wal"), []byte("stale wal"), 0o600)
	writeFile(t, filepath.Join(oldInst.dir, DatabaseName+"-shm"), []byte("stale shm"), 0o600)
	if err := os.MkdirAll(filepath.Join(oldInst.dir, BackupDirName), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(oldInst.dir, BackupDirName, "keep.txt"), []byte("mine"), 0o600)

	result, err := Restore(context.Background(), archive, oldInst.dir)
	if err != nil {
		t.Fatal(err)
	}
	assertSnapshot(t, oldInst.dir, newState, "restored")
	if _, err := os.Lstat(filepath.Join(oldInst.dir, ConfigName)); err == nil {
		t.Fatal("config.json from the old data remained although the backup has none")
	}
	for _, stale := range []string{DatabaseName + "-wal", DatabaseName + "-shm"} {
		if _, err := os.Lstat(filepath.Join(oldInst.dir, stale)); err == nil {
			t.Fatalf("%s from the old data remained next to the restored database", stale)
		}
	}
	assertSnapshot(t, result.AsideDir, oldState, "previous data")
	if string(readFile(t, filepath.Join(oldInst.dir, BackupDirName, "keep.txt"))) != "mine" {
		t.Fatal("backups directory was touched")
	}
	assertCleanDataDir(t, oldInst.dir)
	assertMode(t, result.AsideDir, 0o700)
	if _, err := Check(context.Background(), archive); err != nil {
		t.Fatal(err)
	}

	again, err := Restore(context.Background(), archive, oldInst.dir)
	if err != nil {
		t.Fatal(err)
	}
	if again.AsideDir == result.AsideDir {
		t.Fatal("second restore reused the aside directory")
	}
	assertSnapshot(t, again.AsideDir, newState, "second aside")
}

func TestRestoreIntoEmptyDirectory(t *testing.T) {
	_, _, archive, _, newState := restoreFixture(t)
	target := filepath.Join(t.TempDir(), "fresh")
	result, err := Restore(context.Background(), archive, target)
	if err != nil {
		t.Fatal(err)
	}
	assertSnapshot(t, target, newState, "fresh restore")
	entries, _ := os.ReadDir(result.AsideDir)
	if len(entries) != 0 {
		t.Fatalf("aside directory of an empty target holds %v", entries)
	}
	assertCleanDataDir(t, target)
}

func TestRestoreRefusesInvalidArchiveAndChangesNothing(t *testing.T) {
	oldInst, _, archive, oldState, _ := restoreFixture(t)
	data := readFile(t, archive)
	offset := bytes.Index(data, []byte("SQLite format 3"))
	data[offset+30] ^= 0xFF
	bad := filepath.Join(t.TempDir(), "bad.tar")
	writeFile(t, bad, data, 0o600)
	_, err := Restore(context.Background(), bad, oldInst.dir)
	wantInvalid(t, err, "")
	assertSnapshot(t, oldInst.dir, oldState, "after refused restore")
	assertCleanDataDir(t, oldInst.dir)
	entries, _ := os.ReadDir(oldInst.dir)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), asidePrefix) {
			t.Fatalf("refused restore created %s", entry.Name())
		}
	}
}

func TestRestoreRefusesSecretsMismatch(t *testing.T) {
	oldInst, _, archive, oldState, _ := restoreFixture(t)
	u := unpack(t, archive)
	writeFile(t, u.path("secrets/"+secrets.AdminKeyFile), bytes.Repeat([]byte{9}, 32), 0o600)
	_, err := Restore(context.Background(), u.repack(t, nil), oldInst.dir)
	if !errors.Is(err, ErrSecretsMismatch) {
		t.Fatalf("err = %v", err)
	}
	assertSnapshot(t, oldInst.dir, oldState, "after refused restore")
}

func TestRestoreRefusesWhileInstanceLockIsHeld(t *testing.T) {
	oldInst, _, archive, oldState, _ := restoreFixture(t)
	lock, err := AcquireInstanceLock(oldInst.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	_, err = Restore(context.Background(), archive, oldInst.dir)
	if !errors.Is(err, ErrInstanceRunning) {
		t.Fatalf("err = %v", err)
	}
	assertSnapshot(t, oldInst.dir, oldState, "while locked")
	assertCleanDataDir(t, oldInst.dir)
}

func TestRestoreRefusesWhileDatabaseWriteLockIsHeld(t *testing.T) {
	oldInst, _, archive, oldState, _ := restoreFixture(t)
	db, err := sql.Open("sqlite", "file:"+filepath.Join(oldInst.dir, DatabaseName)+"?_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	_, err = Restore(context.Background(), archive, oldInst.dir)
	if !errors.Is(err, ErrInstanceRunning) {
		t.Fatalf("err = %v", err)
	}
	assertCleanDataDir(t, oldInst.dir)
	if _, err := conn.ExecContext(context.Background(), `ROLLBACK`); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	db.Close()
	assertSnapshot(t, oldInst.dir, oldState, "while write locked")
	if _, err := Restore(context.Background(), archive, oldInst.dir); err != nil {
		t.Fatalf("restore after the lock was released: %v", err)
	}
}

func TestInterruptedRestoreLeavesOldOrNewData(t *testing.T) {
	steps := []struct {
		step      string
		wantState string
	}{
		{"journal-written", "old"},
		{"moved-old:" + DatabaseName, "old"},
		{"moved-old:" + secrets.DirName, "old"},
		{"moved-old:" + ConfigName, "old"},
		{"old-moved", "old"},
		{"installed:" + DatabaseName, "old"},
		{"installed:" + secrets.DirName, "old"},
		{"committed", "new"},
	}
	for _, tc := range steps {
		t.Run(tc.step, func(t *testing.T) {
			oldInst, _, archive, oldState, newState := restoreFixture(t)
			hook := func(step string) error {
				if step == tc.step {
					return errSimulatedCrash
				}
				return nil
			}
			_, err := Restore(context.Background(), archive, oldInst.dir, withHook(hook))
			if !errors.Is(err, errSimulatedCrash) {
				t.Fatalf("err = %v, step %q never ran", err, tc.step)
			}
			if _, statErr := os.Lstat(filepath.Join(oldInst.dir, journalName)); statErr != nil {
				t.Fatalf("journal missing after the crash: %v", statErr)
			}
			if _, err := Restore(context.Background(), archive, t.TempDir()); err != nil {
				t.Fatalf("unrelated restore failed: %v", err)
			}

			recovered, err := Recover(oldInst.dir)
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantState == "old" {
				if recovered != "rolled back" {
					t.Fatalf("recovered = %q", recovered)
				}
				assertSnapshot(t, oldInst.dir, oldState, "after recovery")
			} else {
				if recovered != "completed" {
					t.Fatalf("recovered = %q", recovered)
				}
				assertSnapshot(t, oldInst.dir, newState, "after recovery")
			}
			assertCleanDataDir(t, oldInst.dir)

			again, err := Recover(oldInst.dir)
			if err != nil || again != "" {
				t.Fatalf("second recovery = %q, %v", again, err)
			}
			if _, err := Restore(context.Background(), archive, oldInst.dir); err != nil {
				t.Fatalf("restore after recovery: %v", err)
			}
			assertSnapshot(t, oldInst.dir, newState, "final restore")
		})
	}
}

func TestInterruptedRestoreIsRecoveredByNextRestore(t *testing.T) {
	oldInst, _, archive, oldState, newState := restoreFixture(t)
	crash := func(step string) error {
		if step == "installed:"+DatabaseName {
			return errSimulatedCrash
		}
		return nil
	}
	if _, err := Restore(context.Background(), archive, oldInst.dir, withHook(crash)); !errors.Is(err, errSimulatedCrash) {
		t.Fatal(err)
	}
	for _, name := range []string{DatabaseName, secrets.DirName} {
		if _, err := os.Lstat(filepath.Join(oldInst.dir, name)); err != nil && name == DatabaseName {
			t.Fatalf("mixed state expected to hold the new database: %v", err)
		}
	}
	result, err := Restore(context.Background(), archive, oldInst.dir)
	if err != nil {
		t.Fatal(err)
	}
	if result.Recovered != "rolled back" {
		t.Fatalf("recovered = %q", result.Recovered)
	}
	assertSnapshot(t, oldInst.dir, newState, "after second restore")
	assertSnapshot(t, result.AsideDir, oldState, "previous data after recovered restore")
}

func TestFailedRestoreRollsBackWithoutHelp(t *testing.T) {
	failing := errors.New("disk full")
	for _, step := range []string{"journal-written", "moved-old:" + DatabaseName, "old-moved", "installed:" + DatabaseName, "installed:" + secrets.DirName} {
		t.Run(step, func(t *testing.T) {
			oldInst, _, archive, oldState, _ := restoreFixture(t)
			hook := func(name string) error {
				if name == step {
					return failing
				}
				return nil
			}
			_, err := Restore(context.Background(), archive, oldInst.dir, withHook(hook))
			if !errors.Is(err, failing) {
				t.Fatalf("err = %v", err)
			}
			assertSnapshot(t, oldInst.dir, oldState, "after failed restore")
			assertCleanDataDir(t, oldInst.dir)
			entries, _ := os.ReadDir(oldInst.dir)
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), asidePrefix) {
					t.Fatalf("failed restore left %s behind", entry.Name())
				}
			}
		})
	}
}

func TestRecoverRefusesDamagedJournal(t *testing.T) {
	oldInst, _, _, oldState, _ := restoreFixture(t)
	for name, content := range map[string]string{
		"garbage":   "{not json",
		"escaping":  `{"version":1,"phase":"moving-old","staging":"../x","aside":"a","old":[],"new":[]}`,
		"unmanaged": `{"version":1,"phase":"moving-old","staging":"s","aside":"a","old":["backups"],"new":[]}`,
		"version":   `{"version":7,"phase":"moving-old","staging":"s","aside":"a","old":[],"new":[]}`,
	} {
		writeFile(t, filepath.Join(oldInst.dir, journalName), []byte(content), 0o600)
		if _, err := Recover(oldInst.dir); err == nil {
			t.Fatalf("%s journal was accepted", name)
		}
		assertSnapshot(t, oldInst.dir, oldState, name)
	}
}

func TestRecoverRefusesWhileInstanceRuns(t *testing.T) {
	oldInst, _, archive, oldState, _ := restoreFixture(t)
	crash := func(step string) error {
		if step == "old-moved" {
			return errSimulatedCrash
		}
		return nil
	}
	_, _ = Restore(context.Background(), archive, oldInst.dir, withHook(crash))
	lock, err := AcquireInstanceLock(oldInst.dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Recover(oldInst.dir); !errors.Is(err, ErrInstanceRunning) {
		t.Fatalf("err = %v", err)
	}
	_ = lock.Release()
	if _, err := Recover(oldInst.dir); err != nil {
		t.Fatal(err)
	}
	assertSnapshot(t, oldInst.dir, oldState, "recovered")
}
