package ops

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
)

func syncDir(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func randomSuffix() (string, error) {
	var raw [6]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func makePrivateDir(parent, prefix string) (string, error) {
	suffix, err := randomSuffix()
	if err != nil {
		return "", err
	}
	path := filepath.Join(parent, prefix+suffix)
	if err := os.Mkdir(path, 0o700); err != nil {
		return "", err
	}
	return path, nil
}

func openRegular(path string) (*os.File, os.FileInfo, error) {
	linkInfo, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if !linkInfo.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("%s must be a regular file and not a link or device", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, nil, err
	}
	if !os.SameFile(linkInfo, info) || !info.Mode().IsRegular() {
		file.Close()
		return nil, nil, fmt.Errorf("%s changed while it was being opened", path)
	}
	return file, info, nil
}

func hashFile(path string) (string, int64, error) {
	file, _, err := openRegular(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	digest := sha256.New()
	size, err := io.Copy(digest, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(digest.Sum(nil)), size, nil
}

func copyFileLimited(src, dst string, limit int64) error {
	in, info, err := openRegular(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if info.Size() > limit {
		return fmt.Errorf("%s is %d bytes, which is over the limit of %d", src, info.Size(), limit)
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	written, err := io.Copy(out, io.LimitReader(in, limit+1))
	if err == nil && written > limit {
		err = fmt.Errorf("%s grew past the limit of %d while it was being copied", src, limit)
	}
	if err == nil {
		err = out.Sync()
	}
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(dst)
	}
	return err
}

func exists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func publishNoReplace(temp, final string) error {
	err := os.Link(temp, final)
	switch {
	case err == nil:
		return os.Remove(temp)
	case errors.Is(err, fs.ErrExist):
		return ErrBackupExists
	case errors.Is(err, syscall.EPERM), errors.Is(err, syscall.ENOTSUP), errors.Is(err, syscall.EOPNOTSUPP), errors.Is(err, errors.ErrUnsupported):
		if present, statErr := exists(final); statErr != nil {
			return statErr
		} else if present {
			return ErrBackupExists
		}
		return os.Rename(temp, final)
	default:
		return err
	}
}
