//go:build linux

package diagnostics

import (
	"os"
	"syscall"
)

func openDirectory(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
}

func directoryAvailableSpace(dir *os.File) (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Fstatfs(int(dir.Fd()), &stat); err != nil {
		return 0, err
	}
	if int64(stat.Bsize) <= 0 {
		return 0, errSpaceUnavailable
	}
	return saturatingMul(uint64(stat.Bavail), uint64(stat.Bsize)), nil
}
