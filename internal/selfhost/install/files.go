package install

import (
	"context"
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
	"strings"

	"usesesame.app/backend/deploy/selfhost"
)

const (
	composeName    = "compose.yaml"
	envName        = ".env"
	caddyfileName  = "Caddyfile"
	caddyEnvName   = "caddy.env"
	planName       = "plan.json"
	planTempPrefix = ".plan-"

	privateDirMode  fs.FileMode = 0o700
	privateFileMode fs.FileMode = 0o600
)

type renderedFile struct {
	name string
	data []byte
}

type createdFile struct {
	name string
	info fs.FileInfo
}

type target struct {
	path   string
	name   string
	parent *os.Root
	root   *os.Root
	info   fs.FileInfo
}

func renderFiles(spec specification) ([]renderedFile, error) {
	compose := selfhostdeploy.Compose()
	caddy := selfhostdeploy.Caddy()
	if strings.TrimSpace(compose) == "" {
		return nil, errors.New("install: embedded compose template is empty")
	}
	if strings.TrimSpace(caddy) == "" {
		return nil, errors.New("install: embedded Caddy template is empty")
	}
	env := fmt.Sprintf("SESAME_IMAGE_TAG=%s\nSESAME_PUBLIC_URL=%s\nSESAME_TRUSTED_PROXIES=%s\n", spec.version, spec.publicURL, strings.Join(spec.trustedProxies, ","))
	files := []renderedFile{
		{name: composeName, data: []byte(compose)},
		{name: envName, data: []byte(env)},
	}
	if spec.profile == profileCaddy {
		files = append(files,
			renderedFile{name: caddyfileName, data: []byte(caddy)},
			renderedFile{name: caddyEnvName, data: []byte("SESAME_HOST=" + spec.host + "\n")},
		)
	}
	return files, nil
}

func buildPlan(spec specification, files []renderedFile) Plan {
	plan := Plan{
		SchemaVersion:  1,
		ToolVersion:    spec.toolVersion,
		Profile:        spec.profile,
		ImageVersion:   spec.version,
		PublicURL:      spec.publicURL,
		TrustedProxies: append([]string{}, spec.trustedProxies...),
	}
	if plan.TrustedProxies == nil {
		plan.TrustedProxies = []string{}
	}
	for _, file := range files {
		digest := sha256.Sum256(file.data)
		plan.Files = append(plan.Files, File{
			Path:   file.name,
			SHA256: hex.EncodeToString(digest[:]),
			Mode:   fileMode,
		})
	}
	plan.Prerequisites = append([]Prerequisite{}, spec.prerequisites...)
	return plan
}

func planTempName() (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return planTempPrefix + hex.EncodeToString(raw[:]) + ".tmp", nil
}

func createTarget(directory string) (result *target, err error) {
	parentPath := filepath.Dir(directory)
	name := filepath.Base(directory)
	parent, err := openParent(parentPath)
	if err != nil {
		return nil, fmt.Errorf("install: parent directory %q cannot be opened: %w", parentPath, err)
	}
	defer func() {
		if err != nil {
			_ = parent.Close()
		}
	}()
	if err := parent.Mkdir(name, privateDirMode); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("install: target directory %q already exists; choose a new directory", directory)
		}
		return nil, fmt.Errorf("install: target directory %q cannot be created: %w", directory, err)
	}
	createdInfo, err := parent.Lstat(name)
	if err != nil {
		return nil, fmt.Errorf("install: target directory %q cannot be inspected after creation: %w", directory, err)
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		_ = removeIfSame(parent, name, createdInfo)
		return nil, fmt.Errorf("install: target directory %q cannot be opened: %w", directory, err)
	}
	openedInfo, err := root.Stat(".")
	if err != nil {
		_ = root.Close()
		_ = removeIfSame(parent, name, createdInfo)
		return nil, fmt.Errorf("install: target directory %q cannot be inspected: %w", directory, err)
	}
	if !os.SameFile(createdInfo, openedInfo) {
		_ = root.Close()
		_ = removeIfSame(parent, name, createdInfo)
		return nil, fmt.Errorf("install: target directory %q changed while it was being created", directory)
	}
	directoryFile, err := root.Open(".")
	if err != nil {
		_ = root.Close()
		_ = removeIfSame(parent, name, createdInfo)
		return nil, fmt.Errorf("install: target directory %q cannot be opened: %w", directory, err)
	}
	chmodErr := directoryFile.Chmod(privateDirMode)
	closeErr := directoryFile.Close()
	if chmodErr != nil {
		_ = root.Close()
		_ = removeIfSame(parent, name, createdInfo)
		return nil, fmt.Errorf("install: target directory %q cannot be restricted: %w", directory, chmodErr)
	}
	if closeErr != nil {
		_ = root.Close()
		_ = removeIfSame(parent, name, createdInfo)
		return nil, fmt.Errorf("install: target directory %q cannot be closed: %w", directory, closeErr)
	}
	return &target{path: directory, name: name, parent: parent, root: root, info: createdInfo}, nil
}

func openParent(path string) (*os.Root, error) {
	anchor := filepath.VolumeName(path) + string(filepath.Separator)
	root, err := os.OpenRoot(anchor)
	if err != nil {
		return nil, err
	}
	complete := false
	defer func() {
		if !complete {
			_ = root.Close()
		}
	}()
	for _, name := range strings.Split(strings.TrimPrefix(path, anchor), string(filepath.Separator)) {
		if name == "" {
			continue
		}
		info, err := root.Lstat(name)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, fmt.Errorf("parent component %q must be a directory and cannot be a symbolic link", name)
		}
		next, err := root.OpenRoot(name)
		if err != nil {
			return nil, err
		}
		opened, err := next.Stat(".")
		if err != nil || !os.SameFile(info, opened) {
			_ = next.Close()
			if err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("parent component %q changed while it was being opened", name)
		}
		closeErr := root.Close()
		root = next
		if closeErr != nil {
			return nil, closeErr
		}
	}
	complete = true
	return root, nil
}

func (t *target) checkIdentity() error {
	currentParent, err := openParent(filepath.Dir(t.path))
	if err != nil {
		return fmt.Errorf("install: parent path changed during generation: %w", err)
	}
	defer currentParent.Close()
	currentParentInfo, err := currentParent.Stat(".")
	if err != nil {
		return fmt.Errorf("install: parent directory cannot be inspected before completion: %w", err)
	}
	originalParentInfo, err := t.parent.Stat(".")
	if err != nil {
		return fmt.Errorf("install: original parent directory cannot be inspected: %w", err)
	}
	if !os.SameFile(currentParentInfo, originalParentInfo) {
		return errors.New("install: parent path changed during generation")
	}
	info, err := t.parent.Lstat(t.name)
	if err != nil {
		return fmt.Errorf("install: target directory cannot be inspected before completion: %w", err)
	}
	if !os.SameFile(t.info, info) {
		return errors.New("install: target directory changed during generation")
	}
	return nil
}

func (t *target) checkFileIdentity(file createdFile) error {
	current, err := t.root.Lstat(file.name)
	if err != nil {
		return fmt.Errorf("install: generated file %q cannot be inspected: %w", file.name, err)
	}
	if !current.Mode().IsRegular() || !os.SameFile(file.info, current) {
		return fmt.Errorf("install: generated file %q changed during publication", file.name)
	}
	return nil
}

func (t *target) close() {
	if t.root != nil {
		_ = t.root.Close()
	}
	if t.parent != nil {
		_ = t.parent.Close()
	}
}

func (t *target) cleanup(created []createdFile) error {
	var problems []error
	for index := len(created) - 1; index >= 0; index-- {
		if err := removeIfSame(t.root, created[index].name, created[index].info); err != nil {
			problems = append(problems, fmt.Errorf("install: %s cannot be removed: %w", created[index].name, err))
		}
	}
	if err := syncDirectory(t.root); err != nil {
		problems = append(problems, fmt.Errorf("install: generated directory cannot be synced after cleanup: %w", err))
	}
	_ = t.root.Close()
	current, err := t.parent.Lstat(t.name)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		problems = append(problems, fmt.Errorf("install: target directory cannot be inspected during cleanup: %w", err))
	case !os.SameFile(t.info, current):
	default:
		empty, emptyErr := directoryEmpty(t.parent, t.name)
		switch {
		case emptyErr != nil:
			problems = append(problems, fmt.Errorf("install: target directory cannot be inspected during cleanup: %w", emptyErr))
		case empty:
			if err := t.parent.Remove(t.name); err != nil && !errors.Is(err, fs.ErrNotExist) {
				problems = append(problems, fmt.Errorf("install: target directory cannot be removed during cleanup: %w", err))
			} else if err == nil {
				if err := syncDirectory(t.parent); err != nil {
					problems = append(problems, fmt.Errorf("install: parent directory cannot be synced after cleanup: %w", err))
				}
			}
		}
	}
	_ = t.parent.Close()
	return errors.Join(problems...)
}

func (g generator) writeNewFile(ctx context.Context, root *os.Root, name string, data []byte, created *[]createdFile) error {
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, privateFileMode)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		_ = root.Remove(name)
		return err
	}
	*created = append(*created, createdFile{name: name, info: info})
	failed := func(cause error) error {
		_ = file.Close()
		if removeErr := removeIfSame(root, name, info); removeErr != nil {
			return errors.Join(cause, removeErr)
		}
		return cause
	}
	if err := file.Chmod(privateFileMode); err != nil {
		return failed(err)
	}
	if _, err := file.Write(data); err != nil {
		return failed(err)
	}
	if err := g.step(ctx, "sync-file:"+name); err != nil {
		return failed(err)
	}
	if err := file.Sync(); err != nil {
		return failed(err)
	}
	if err := file.Close(); err != nil {
		_ = removeIfSame(root, name, info)
		return err
	}
	return nil
}

func removeIfSame(root *os.Root, name string, expected fs.FileInfo) error {
	if expected == nil {
		return nil
	}
	current, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !os.SameFile(expected, current) {
		return nil
	}
	if err := root.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func directoryEmpty(root *os.Root, name string) (bool, error) {
	directory, err := root.Open(name)
	if err != nil {
		return false, err
	}
	defer directory.Close()
	names, err := directory.Readdirnames(1)
	if errors.Is(err, io.EOF) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return len(names) == 0, nil
}

func syncDirectory(root *os.Root) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}
