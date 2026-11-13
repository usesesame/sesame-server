package diagnostics

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"

	"usesesame.app/backend/internal/selfhost/config"
	"usesesame.app/backend/internal/selfhost/secrets"
)

var errPathChanged = errors.New("path changed during inspection")

type fileSystem struct {
	lstat          func(string) (fs.FileInfo, error)
	openDir        func(string) (*os.File, error)
	availableSpace func(*os.File) (uint64, error)
}

var osFileSystem = fileSystem{
	lstat:          os.Lstat,
	openDir:        openDirectory,
	availableSpace: directoryAvailableSpace,
}

type scanner struct {
	cfg      config.Config
	summary  ConfigSummary
	files    fileSystem
	data     dataDirectoryFacts
	database databaseFacts
}

type dataDirectoryFacts struct {
	info    fs.FileInfo
	realDir bool
	missing bool
	check   Check
}

type databaseFacts struct {
	check     Check
	present   bool
	known     bool
	sizeKnown bool
	size      uint64
}

func newScanner(cfg config.Config, summary ConfigSummary, files fileSystem) *scanner {
	return &scanner{cfg: cfg, summary: summary, files: files}
}

func (s *scanner) readMetadata() {
	s.data = s.readDataDirectory()
	s.database = s.readDatabase()
}

func (s *scanner) readDataDirectory() dataDirectoryFacts {
	info, err := stableLstat(s.files, s.cfg.DataDir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return dataDirectoryFacts{missing: true, check: dataDirectoryMissingCheck()}
	case errors.Is(err, errPathChanged):
		return dataDirectoryFacts{check: dataDirectoryPathChangedCheck()}
	case err != nil:
		return dataDirectoryFacts{check: dataDirectoryMetadataUnavailableCheck()}
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return dataDirectoryFacts{check: dataDirectorySymlinkCheck()}
	}
	if !info.IsDir() {
		return dataDirectoryFacts{check: dataDirectoryWrongTypeCheck()}
	}
	if runtime.GOOS != "linux" {
		return dataDirectoryFacts{info: info, realDir: true, check: dataDirectoryACLUnknownCheck()}
	}
	if info.Mode().Perm()&0o077 != 0 {
		return dataDirectoryFacts{info: info, realDir: true, check: dataDirectoryPermissionsCheck()}
	}
	return dataDirectoryFacts{info: info, realDir: true, check: dataDirectoryPrivateCheck()}
}

func (s *scanner) readDatabase() databaseFacts {
	if s.data.missing {
		return databaseFacts{known: true, sizeKnown: true, check: databaseMissingCheck()}
	}
	if !s.data.realDir {
		return databaseFacts{check: databaseNotInspectedCheck()}
	}
	path := filepath.Join(s.cfg.DataDir, secrets.DatabaseFile)
	info, err := stableLstat(s.files, path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return databaseFacts{known: true, sizeKnown: true, check: databaseMissingCheck()}
	case errors.Is(err, errPathChanged):
		return databaseFacts{check: databasePathChangedCheck()}
	case err != nil:
		return databaseFacts{check: databaseMetadataUnavailableCheck()}
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return databaseFacts{present: true, known: true, check: databaseSymlinkCheck()}
	}
	if !info.Mode().IsRegular() {
		return databaseFacts{present: true, known: true, check: databaseWrongTypeCheck()}
	}
	size := uint64(0)
	if info.Size() > 0 {
		size = uint64(info.Size())
	}
	facts := databaseFacts{present: true, known: true, sizeKnown: info.Size() >= 0, size: size}
	switch {
	case info.Size() == 0:
		facts.check = databaseEmptyCheck()
	case runtime.GOOS == "linux" && info.Mode().Perm()&0o077 != 0:
		facts.check = databasePermissionsCheck()
	default:
		facts.check = databaseNotOpenedCheck()
	}
	return facts
}

func stableLstat(files fileSystem, path string) (fs.FileInfo, error) {
	first, err := files.lstat(path)
	if err != nil {
		return nil, err
	}
	second, err := files.lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, errPathChanged
		}
		return nil, err
	}
	if !sameMetadata(first, second) {
		return nil, errPathChanged
	}
	return first, nil
}

func sameMetadata(first, second fs.FileInfo) bool {
	return os.SameFile(first, second) &&
		first.Mode() == second.Mode() &&
		first.Size() == second.Size() &&
		first.ModTime().Equal(second.ModTime())
}

type rankedCheck struct {
	rank  int
	check Check
}

type issues struct {
	action  *rankedCheck
	unknown *rankedCheck
}

func (i *issues) addAction(rank int, check Check) {
	if i.action == nil || rank < i.action.rank {
		i.action = &rankedCheck{rank: rank, check: check}
	}
}

func (i *issues) addUnknown(rank int, check Check) {
	if i.unknown == nil || rank < i.unknown.rank {
		i.unknown = &rankedCheck{rank: rank, check: check}
	}
}

func (i *issues) result(ok Check) Check {
	if i.action != nil {
		return i.action.check
	}
	if i.unknown != nil {
		return i.unknown.check
	}
	return ok
}
