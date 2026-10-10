//go:build unix

package ops

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

type Lock struct {
	once sync.Once
	file *os.File
}

func AcquireInstanceLock(dataDir string) (*Lock, error) {
	path := filepath.Join(dataDir, LockFileName)
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("lock file %s cannot be opened: %w", path, err)
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, fmt.Errorf("lock file %s must be a regular file", path)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrInstanceRunning
		}
		return nil, fmt.Errorf("lock file %s cannot be locked: %w", path, err)
	}
	return &Lock{file: file}, nil
}

func (l *Lock) Release() error {
	var err error
	l.once.Do(func() { err = l.file.Close() })
	return err
}
