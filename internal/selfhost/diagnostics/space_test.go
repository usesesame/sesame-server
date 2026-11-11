//go:build linux

package diagnostics

import (
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestSpaceThresholds(t *testing.T) {
	databaseSize := uint64(1 * mebibyte)
	critical := uint64(257 * mebibyte)
	tight := uint64(264 * mebibyte)
	cases := []struct {
		name      string
		available uint64
		state     string
		code      string
	}{
		{name: "below critical", available: critical - 1, state: StateAction, code: "space_critical"},
		{name: "at critical", available: critical, state: StateUnknown, code: "space_tight"},
		{name: "below tight", available: tight - 1, state: StateUnknown, code: "space_tight"},
		{name: "at tight", available: tight, state: StateOK, code: "space_sufficient"},
		{name: "far above tight", available: tight * 2, state: StateOK, code: "space_sufficient"},
		{name: "zero", available: 0, state: StateAction, code: "space_critical"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			check := spaceCheckResult(testCase.available, databaseSize)
			if check.State != testCase.state || check.Code != testCase.code {
				t.Fatalf("state and code = %s/%s, want %s/%s", check.State, check.Code, testCase.state, testCase.code)
			}
		})
	}
}

func TestSpaceZeroDatabaseThresholds(t *testing.T) {
	bound := uint64(256 * mebibyte)
	cases := []struct {
		name      string
		available uint64
		state     string
		code      string
	}{
		{name: "below bound", available: bound - 1, state: StateAction, code: "space_critical"},
		{name: "at bound", available: bound, state: StateOK, code: "space_sufficient"},
		{name: "above bound", available: bound + 1, state: StateOK, code: "space_sufficient"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			check := spaceCheckResult(testCase.available, 0)
			if check.State != testCase.state || check.Code != testCase.code {
				t.Fatalf("state and code = %s/%s, want %s/%s", check.State, check.Code, testCase.state, testCase.code)
			}
		})
	}
}

func TestSaturatingArithmetic(t *testing.T) {
	if got := saturatingMul(math.MaxUint64, 2); got != math.MaxUint64 {
		t.Fatalf("saturatingMul = %d", got)
	}
	if got := saturatingMul(math.MaxUint64, 0); got != 0 {
		t.Fatalf("saturatingMul by zero = %d", got)
	}
	if got := saturatingAdd(math.MaxUint64, 1); got != math.MaxUint64 {
		t.Fatalf("saturatingAdd = %d", got)
	}
	if got := saturatingAdd(0, 7); got != 7 {
		t.Fatalf("saturatingAdd = %d", got)
	}
}

func TestSpaceOverflowBounds(t *testing.T) {
	largeDatabase := uint64(math.MaxInt64)
	if check := spaceCheckResult(math.MaxUint64, largeDatabase); check.State != StateOK {
		t.Fatalf("maximum available state = %s", check.State)
	}
	if check := spaceCheckResult(math.MaxUint64-1, largeDatabase); check.State != StateUnknown {
		t.Fatalf("near maximum available state = %s", check.State)
	}
	if check := spaceCheckResult(largeDatabase, largeDatabase); check.State != StateAction {
		t.Fatalf("available equal to database state = %s", check.State)
	}
	if check := spaceCheckResult(math.MaxUint64-1, math.MaxUint64); check.State != StateAction {
		t.Fatalf("fully saturated state = %s", check.State)
	}
}

func TestDirectoryAvailableSpaceLinux(t *testing.T) {
	dir, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	if _, err := directoryAvailableSpace(dir); err != nil {
		t.Fatalf("directoryAvailableSpace: %v", err)
	}
}

func TestSpaceOpenFailure(t *testing.T) {
	f := newFixture(t)
	files := testFiles(1<<40, nil)
	files.openDir = func(string) (*os.File, error) {
		return nil, fs.ErrPermission
	}
	report := f.inspectWith(files)
	assertCheck(t, report, "space", StateUnknown, "space_unavailable")
}

func TestSpaceDirectoryIdentityChange(t *testing.T) {
	f := newFixture(t)
	other := t.TempDir()
	files := testFiles(1<<40, nil)
	files.openDir = func(string) (*os.File, error) {
		return os.Open(other)
	}
	report := f.inspectWith(files)
	assertCheck(t, report, "space", StateUnknown, "space_unavailable")
}

func TestSpaceDirectoryStatFailure(t *testing.T) {
	f := newFixture(t)
	other := t.TempDir()
	files := testFiles(1<<40, nil)
	files.openDir = func(string) (*os.File, error) {
		file, err := os.Open(other)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		return file, nil
	}
	report := f.inspectWith(files)
	assertCheck(t, report, "space", StateUnknown, "space_unavailable")
}

func TestOpenDirectoryRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := t.TempDir()
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := openDirectory(link); err == nil {
		t.Fatal("openDirectory followed a symlink")
	}
}
