package secrets

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

const (
	DirName        = "secrets"
	SigningKeyFile = "signing.key"
	AdminKeyFile   = "admin-encryption.key"
	IPPepperFile   = "ip-pepper.key"
	DatabaseFile   = "sesame.db"
	KeySize        = 32

	staleTempAge = time.Minute
	tempPrefix   = ".tmp-"
)

func Files() []string {
	return []string{SigningKeyFile, AdminKeyFile, IPPepperFile}
}

type Secrets struct {
	SigningKey         ed25519.PrivateKey
	AdminEncryptionKey []byte
	IPPepper           []byte
}

func (s *Secrets) PublicKey() ed25519.PublicKey {
	return s.SigningKey.Public().(ed25519.PublicKey)
}

func (s *Secrets) String() string { return "secrets.Secrets{redacted}" }

func (s *Secrets) GoString() string { return s.String() }

func (s *Secrets) LogValue() slog.Value { return slog.StringValue("[redacted]") }

func Dir(dataDir string) string { return filepath.Join(dataDir, DirName) }

type fileOps struct {
	entropy io.Reader
	sync    func(*os.File) error
	link    func(oldname, newname string) error
	rename  func(oldname, newname string) error
	syncDir func(path string) error
	now     func() time.Time
}

var ops = fileOps{
	entropy: rand.Reader,
	sync:    func(file *os.File) error { return file.Sync() },
	link:    os.Link,
	rename:  os.Rename,
	syncDir: syncDirectory,
	now:     time.Now,
}

func Load(dataDir string) (*Secrets, error) {
	dir := Dir(dataDir)
	if err := checkDirectory(dir); err != nil {
		return nil, err
	}
	return readAll(dir)
}

func LoadOrCreate(dataDir string) (*Secrets, []string, error) {
	dir := Dir(dataDir)
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, nil, fmt.Errorf("data directory %s cannot be created: %w", dataDir, err)
	}
	if err := ensureDirectory(dataDir, dir); err != nil {
		return nil, nil, err
	}
	missing, err := validateExisting(dir)
	if err != nil {
		return nil, nil, err
	}
	if len(missing) > 0 {
		if _, statErr := os.Lstat(filepath.Join(dataDir, DatabaseFile)); statErr == nil {
			return nil, nil, fmt.Errorf("secrets %s are missing but %s already exists in %s, so new keys would make its owner sign-in data unreadable or change the instance identity; restore the secrets directory from a backup", strings.Join(missing, ", "), DatabaseFile, dataDir)
		} else if !errors.Is(statErr, fs.ErrNotExist) {
			return nil, nil, fmt.Errorf("%s cannot be inspected: %w", filepath.Join(dataDir, DatabaseFile), statErr)
		}
	}
	removeStaleTemps(dir)
	var generated []string
	for _, name := range missing {
		buffer := make([]byte, KeySize)
		if _, err := io.ReadFull(ops.entropy, buffer); err != nil {
			return nil, generated, fmt.Errorf("secret %s could not be generated: %w", name, err)
		}
		wrote, err := writeOnce(dir, name, buffer)
		clear(buffer)
		if err != nil {
			return nil, generated, fmt.Errorf("secret %s could not be written: %w", name, err)
		}
		if wrote {
			generated = append(generated, name)
		}
	}
	loaded, err := readAll(dir)
	if err != nil {
		return nil, generated, err
	}
	return loaded, generated, nil
}

func ensureDirectory(dataDir, dir string) error {
	_, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("secrets directory %s cannot be created: %w", dir, err)
		}
		if err := restrictDirectory(dir); err != nil {
			return fmt.Errorf("secrets directory %s cannot be restricted: %w", dir, err)
		}
		if err := ops.syncDir(dataDir); err != nil {
			return fmt.Errorf("data directory %s could not be flushed: %w", dataDir, err)
		}
	} else if err != nil {
		return fmt.Errorf("secrets directory %s cannot be inspected: %w", dir, err)
	}
	return checkDirectory(dir)
}

func restrictDirectory(dir string) error {
	handle, err := os.Open(dir)
	if err != nil {
		return err
	}
	if err := handle.Chmod(0o700); err != nil {
		_ = handle.Close()
		return err
	}
	return handle.Close()
}

func checkDirectory(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("secrets directory %s cannot be inspected: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("secrets path %s must be a directory and not a symbolic link", dir)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("secrets directory %s has mode %04o, but only its owner may access it; run chmod 700 %s", dir, info.Mode().Perm(), dir)
	}
	return nil
}

func validateExisting(dir string) ([]string, error) {
	var missing []string
	for _, name := range Files() {
		_, err := readSecret(filepath.Join(dir, name))
		switch {
		case err == nil:
		case errors.Is(err, fs.ErrNotExist):
			missing = append(missing, name)
		default:
			return nil, err
		}
	}
	return missing, nil
}

func readAll(dir string) (*Secrets, error) {
	contents := make(map[string][]byte, 3)
	for _, name := range Files() {
		value, err := readSecret(filepath.Join(dir, name))
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("secret %s is missing", filepath.Join(dir, name))
		}
		if err != nil {
			return nil, err
		}
		contents[name] = value
	}
	return &Secrets{
		SigningKey:         ed25519.NewKeyFromSeed(contents[SigningKeyFile]),
		AdminEncryptionKey: contents[AdminKeyFile],
		IPPepper:           contents[IPPepperFile],
	}, nil
}

func readSecret(path string) ([]byte, error) {
	linkInfo, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		return nil, fmt.Errorf("secret %s cannot be inspected: %w", path, err)
	}
	if !linkInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("secret %s must be a regular file and not a link or device", path)
	}
	if err := checkMode(path, linkInfo); err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("secret %s cannot be opened: %w", path, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("secret %s cannot be inspected: %w", path, err)
	}
	if !os.SameFile(linkInfo, info) || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("secret %s changed while it was being opened", path)
	}
	if err := checkMode(path, info); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, KeySize+1))
	if err != nil {
		return nil, fmt.Errorf("secret %s cannot be read: %w", path, err)
	}
	if len(data) != KeySize {
		clear(data)
		return nil, fmt.Errorf("secret %s must hold exactly %d bytes; restore it from a backup instead of deleting it, because a replacement key cannot read data written with the original", path, KeySize)
	}
	if bytes.Equal(data, make([]byte, KeySize)) {
		return nil, fmt.Errorf("secret %s holds only zero bytes; restore it from a backup", path)
	}
	return data, nil
}

func checkMode(path string, info fs.FileInfo) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	if info.Mode().Perm()&^0o600 != 0 {
		return fmt.Errorf("secret %s has mode %04o, but only its owner may read it; run chmod 600 %s", path, info.Mode().Perm(), path)
	}
	return nil
}

func writeOnce(dir, name string, data []byte) (bool, error) {
	var suffix [8]byte
	if _, err := io.ReadFull(rand.Reader, suffix[:]); err != nil {
		return false, err
	}
	temp := filepath.Join(dir, tempPrefix+name+"-"+hex.EncodeToString(suffix[:]))
	file, err := os.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return false, err
	}
	cleanup := func() { _ = os.Remove(temp) }
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		cleanup()
		return false, err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		cleanup()
		return false, err
	}
	if err := ops.sync(file); err != nil {
		file.Close()
		cleanup()
		return false, err
	}
	if err := file.Close(); err != nil {
		cleanup()
		return false, err
	}
	final := filepath.Join(dir, name)
	wrote := true
	if err := ops.link(temp, final); err != nil {
		switch {
		case errors.Is(err, fs.ErrExist):
			wrote = false
		case errors.Is(err, syscall.EPERM), errors.Is(err, syscall.ENOTSUP), errors.Is(err, syscall.EOPNOTSUPP), errors.Is(err, errors.ErrUnsupported):
			if _, statErr := os.Lstat(final); statErr == nil {
				wrote = false
			} else if renameErr := ops.rename(temp, final); renameErr != nil {
				cleanup()
				return false, renameErr
			}
		default:
			cleanup()
			return false, err
		}
	}
	cleanup()
	if err := ops.syncDir(dir); err != nil {
		return wrote, err
	}
	return wrote, nil
}

func removeStaleTemps(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), tempPrefix) || !entry.Type().IsRegular() {
			continue
		}
		info, err := entry.Info()
		if err != nil || ops.now().Sub(info.ModTime()) < staleTempAge {
			continue
		}
		_ = os.Remove(filepath.Join(dir, entry.Name()))
	}
}
