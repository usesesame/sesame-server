package ops

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func restoreTemporaryFiles(dataDir string) ([]string, error) {
	entries, err := os.ReadDir(dataDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if entry.Type().IsRegular() && restoreInputName(entry.Name()) || entry.IsDir() && restoreStagingName(entry.Name()) {
			names = append(names, entry.Name())
		}
	}
	return names, nil
}

func restoreInputName(name string) bool {
	suffix, found := strings.CutPrefix(name, restoreInputPrefix)
	if !found || suffix == "" {
		return false
	}
	for _, character := range suffix {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func restoreStagingName(name string) bool {
	suffix, found := strings.CutPrefix(name, stagingPrefix)
	if !found || len(suffix) != 12 {
		return false
	}
	for _, character := range suffix {
		if character >= '0' && character <= '9' || character >= 'a' && character <= 'f' {
			continue
		}
		return false
	}
	return true
}

func cleanupRestoreTemporaryFiles(dataDir string) error {
	names, err := restoreTemporaryFiles(dataDir)
	if err != nil {
		return err
	}
	for _, name := range names {
		if err := os.RemoveAll(filepath.Join(dataDir, name)); err != nil {
			return fmt.Errorf("interrupted restore files cannot be removed: %w", err)
		}
	}
	if len(names) > 0 {
		return syncDir(dataDir)
	}
	return nil
}
