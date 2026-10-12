package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"usesesame.app/backend/internal/selfhost/secrets"
)

const (
	journalName     = ".restore-journal"
	stagingPrefix   = ".restore-new-"
	asidePrefix     = "restore-previous-"
	journalVersion  = 1
	phaseMovingOld  = "moving-old"
	phaseInstalling = "installing-new"
	phaseCommitted  = "committed"
	asideTimeFormat = "20060102T150405Z"
	journalMode     = 0o600
)

var errSimulatedCrash = errors.New("ops: simulated crash")

var managedItems = []string{DatabaseName, DatabaseName + "-wal", DatabaseName + "-shm", secrets.DirName, ConfigName}

type journal struct {
	Version int      `json:"version"`
	Phase   string   `json:"phase"`
	Staging string   `json:"staging"`
	Aside   string   `json:"aside"`
	Old     []string `json:"old"`
	New     []string `json:"new"`
}

type RestoreResult struct {
	Report    Report
	AsideDir  string
	Recovered string
}

func Restore(ctx context.Context, file, dataDir string, options ...Option) (RestoreResult, error) {
	cfg, err := newConfig(options)
	if err != nil {
		return RestoreResult{}, err
	}
	dataDir, err = filepath.Abs(dataDir)
	if err != nil {
		return RestoreResult{}, err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return RestoreResult{}, fmt.Errorf("data directory %s cannot be created: %w", dataDir, err)
	}
	lock, err := AcquireInstanceLock(dataDir)
	if err != nil {
		return RestoreResult{}, err
	}
	defer releaseLock(lock, cfg.logger)

	var result RestoreResult
	if result.Recovered, err = recoverLocked(dataDir); err != nil {
		return result, err
	}
	busy, err := writeLockBusy(ctx, filepath.Join(dataDir, DatabaseName))
	if err != nil {
		return result, err
	}
	if busy {
		return result, fmt.Errorf("%w: the database has an open write lock", ErrInstanceRunning)
	}

	suffix, err := randomSuffix()
	if err != nil {
		return result, err
	}
	staging := filepath.Join(dataDir, stagingPrefix+suffix)
	if err := os.Mkdir(staging, 0o700); err != nil {
		return result, err
	}
	keepStaging := false
	defer func() {
		if !keepStaging {
			_ = os.RemoveAll(staging)
		}
	}()

	_, report, err := verifyArchive(ctx, file, staging, cfg)
	result.Report = report
	if err != nil {
		return result, err
	}
	for _, leftover := range []string{DatabaseName + "-wal", DatabaseName + "-shm"} {
		_ = os.Remove(filepath.Join(staging, leftover))
	}
	if err := syncTree(staging); err != nil {
		return result, err
	}
	if err := ownLike(dataDir, staging); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}

	keepStaging = true
	aside, err := swap(dataDir, staging, cfg)
	result.AsideDir = aside
	if errors.Is(err, errSimulatedCrash) {
		return result, err
	}
	keepStaging = false
	if err != nil {
		return result, err
	}
	return result, nil
}

func swap(dataDir, staging string, cfg config) (string, error) {
	aside, err := uniqueAside(dataDir, cfg.now())
	if err != nil {
		return "", err
	}
	var old, added []string
	for _, name := range managedItems {
		present, err := exists(filepath.Join(dataDir, name))
		if err != nil {
			return "", err
		}
		if present {
			old = append(old, name)
		}
		if staged, err := exists(filepath.Join(staging, name)); err != nil {
			return "", err
		} else if staged {
			added = append(added, name)
		}
	}
	state := journal{Version: journalVersion, Phase: phaseMovingOld, Staging: filepath.Base(staging), Aside: filepath.Base(aside), Old: old, New: added}
	if err := writeJournal(dataDir, state); err != nil {
		_ = os.Remove(aside)
		return "", err
	}
	if err := cfg.step("journal-written"); err != nil {
		return aside, finish(dataDir, err)
	}
	for _, name := range old {
		if err := os.Rename(filepath.Join(dataDir, name), filepath.Join(aside, name)); err != nil {
			return aside, finish(dataDir, err)
		}
		if err := cfg.step("moved-old:" + name); err != nil {
			return aside, finish(dataDir, err)
		}
	}
	if err := syncDir(dataDir); err != nil {
		return aside, finish(dataDir, err)
	}
	state.Phase = phaseInstalling
	if err := writeJournal(dataDir, state); err != nil {
		return aside, finish(dataDir, err)
	}
	if err := cfg.step("old-moved"); err != nil {
		return aside, finish(dataDir, err)
	}
	for _, name := range added {
		if err := os.Rename(filepath.Join(staging, name), filepath.Join(dataDir, name)); err != nil {
			return aside, finish(dataDir, err)
		}
		if err := cfg.step("installed:" + name); err != nil {
			return aside, finish(dataDir, err)
		}
	}
	if err := syncDir(dataDir); err != nil {
		return aside, finish(dataDir, err)
	}
	state.Phase = phaseCommitted
	if err := writeJournal(dataDir, state); err != nil {
		return aside, finish(dataDir, err)
	}
	if err := cfg.step("committed"); err != nil {
		return aside, err
	}
	if err := cleanupCommitted(dataDir, state); err != nil {
		return aside, err
	}
	return aside, nil
}

func finish(dataDir string, cause error) error {
	if errors.Is(cause, errSimulatedCrash) {
		return cause
	}
	if _, err := recoverLocked(dataDir); err != nil {
		return fmt.Errorf("%w; rolling back also failed: %w", cause, err)
	}
	return cause
}

func uniqueAside(dataDir string, now time.Time) (string, error) {
	base := filepath.Join(dataDir, asidePrefix+now.UTC().Format(asideTimeFormat))
	for attempt := 0; attempt < 100; attempt++ {
		candidate := base
		if attempt > 0 {
			candidate = fmt.Sprintf("%s-%d", base, attempt)
		}
		err := os.Mkdir(candidate, 0o700)
		if err == nil {
			return candidate, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", err
		}
	}
	return "", errors.New("a directory for the previous data cannot be created")
}

func writeJournal(dataDir string, state journal) error {
	encoded, err := json.Marshal(state)
	if err != nil {
		return err
	}
	temp := filepath.Join(dataDir, journalName+".tmp")
	file, err := os.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, journalMode)
	if err != nil {
		return err
	}
	if _, err := file.Write(encoded); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(temp, filepath.Join(dataDir, journalName)); err != nil {
		return err
	}
	return syncDir(dataDir)
}

func readJournal(dataDir string) (journal, bool, error) {
	raw, err := os.ReadFile(filepath.Join(dataDir, journalName))
	if errors.Is(err, fs.ErrNotExist) {
		return journal{}, false, nil
	}
	if err != nil {
		return journal{}, false, err
	}
	var state journal
	if err := json.Unmarshal(raw, &state); err != nil {
		return journal{}, false, fmt.Errorf("restore journal %s is not readable, so the data directory needs manual attention: %w", journalName, err)
	}
	if state.Version != journalVersion || !safeName(state.Staging) || !safeName(state.Aside) {
		return journal{}, false, fmt.Errorf("restore journal %s is not valid, so the data directory needs manual attention", journalName)
	}
	for _, name := range append(append([]string{}, state.Old...), state.New...) {
		if !managedName(name) {
			return journal{}, false, fmt.Errorf("restore journal %s names an unexpected item, so the data directory needs manual attention", journalName)
		}
	}
	return state, true, nil
}

func safeName(name string) bool {
	return name != "" && name == filepath.Base(name) && name != "." && name != ".."
}

func managedName(name string) bool {
	for _, item := range managedItems {
		if item == name {
			return true
		}
	}
	return false
}

func releaseLock(lock *Lock, logger *slog.Logger) {
	if err := lock.Release(); err != nil {
		logger.Warn("the instance lock could not be released cleanly", "error", err)
	}
}

func Recover(dataDir string) (string, error) {
	dataDir, err := filepath.Abs(dataDir)
	if err != nil {
		return "", err
	}
	present, err := exists(filepath.Join(dataDir, journalName))
	if err != nil || !present {
		return "", err
	}
	lock, err := AcquireInstanceLock(dataDir)
	if err != nil {
		return "", err
	}
	defer releaseLock(lock, slog.Default())
	return recoverLocked(dataDir)
}

func recoverLocked(dataDir string) (string, error) {
	state, found, err := readJournal(dataDir)
	if err != nil || !found {
		return "", err
	}
	if state.Phase == phaseCommitted {
		return "completed", cleanupCommitted(dataDir, state)
	}
	aside := filepath.Join(dataDir, state.Aside)
	staging := filepath.Join(dataDir, state.Staging)
	if state.Phase == phaseInstalling {
		for _, name := range state.New {
			stagedHere, err := exists(filepath.Join(staging, name))
			if err != nil {
				return "", err
			}
			if stagedHere {
				continue
			}
			if err := os.RemoveAll(filepath.Join(dataDir, name)); err != nil {
				return "", err
			}
		}
	}
	for _, name := range state.Old {
		inAside, err := exists(filepath.Join(aside, name))
		if err != nil {
			return "", err
		}
		inPlace, err := exists(filepath.Join(dataDir, name))
		if err != nil {
			return "", err
		}
		if inAside && !inPlace {
			if err := os.Rename(filepath.Join(aside, name), filepath.Join(dataDir, name)); err != nil {
				return "", err
			}
		}
	}
	if err := syncDir(dataDir); err != nil {
		return "", err
	}
	_ = os.RemoveAll(staging)
	_ = os.Remove(aside)
	if err := os.Remove(filepath.Join(dataDir, journalName)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	_ = os.Remove(filepath.Join(dataDir, journalName+".tmp"))
	if err := syncDir(dataDir); err != nil {
		return "", err
	}
	return "rolled back", nil
}

func cleanupCommitted(dataDir string, state journal) error {
	_ = os.RemoveAll(filepath.Join(dataDir, state.Staging))
	if err := os.Remove(filepath.Join(dataDir, journalName)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	_ = os.Remove(filepath.Join(dataDir, journalName+".tmp"))
	return syncDir(dataDir)
}

func syncTree(root string) error {
	rooted, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer rooted.Close()
	return fs.WalkDir(rooted.FS(), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && runtime.GOOS == "windows" {
			return nil
		}
		handle, err := rooted.Open(path)
		if err != nil {
			return err
		}
		defer handle.Close()
		return handle.Sync()
	})
}
