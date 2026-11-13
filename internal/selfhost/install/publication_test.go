package install

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGenerationRefusesSymlinkedAncestor(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(filepath.Join(real, "parent"), 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlinks are unavailable: %v", err)
		}
		t.Fatal(err)
	}
	options := localOptions(t)
	options.Directory = filepath.Join(link, "parent", "sesame")
	if _, err := Generate(context.Background(), options); err == nil {
		t.Fatal("generation traversed a symlinked ancestor")
	}
	assertDirectoryAbsent(t, filepath.Join(real, "parent", "sesame"))
}

func TestTargetSubstitutionBeforePublicationRefusesSuccess(t *testing.T) {
	options := localOptions(t)
	previous := options.Directory + "-moved"
	marker := "fictional directory owned by another operation"
	g := generator{hook: func(step string) error {
		if step != "link-plan" {
			return nil
		}
		if err := os.Rename(options.Directory, previous); err != nil {
			return err
		}
		if err := os.Mkdir(options.Directory, 0o700); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(options.Directory, "keep"), []byte(marker), 0o600)
	}}
	if _, err := g.generate(context.Background(), options); err == nil {
		t.Fatal("generation reported success after its target was substituted")
	}
	assertFileEquals(t, filepath.Join(options.Directory, "keep"), marker)
	assertEntries(t, options.Directory, []string{"keep"})
	if _, err := os.Lstat(filepath.Join(previous, planName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a plan was published at the moved target: %v", err)
	}
}

func TestParentSyncFailurePreventsPlanPublication(t *testing.T) {
	options := localOptions(t)
	simulated := errors.New("simulated parent directory sync failure")
	g := generator{hook: func(step string) error {
		if step == "sync-parent" {
			return simulated
		}
		return nil
	}}
	if _, err := g.generate(context.Background(), options); !errors.Is(err, simulated) {
		t.Fatalf("generation did not propagate the parent sync failure: %v", err)
	}
	assertDirectoryAbsent(t, options.Directory)
}

func TestParentSubstitutionBeforePublicationRefusesSuccess(t *testing.T) {
	base := t.TempDir()
	parent := filepath.Join(base, "parent")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	options := localOptions(t)
	options.Directory = filepath.Join(parent, "sesame")
	previous := filepath.Join(base, "moved")
	marker := "fictional replacement parent"
	g := generator{hook: func(step string) error {
		if step != "link-plan" {
			return nil
		}
		if err := os.Rename(parent, previous); err != nil {
			return err
		}
		if err := os.Mkdir(parent, 0o700); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(parent, "keep"), []byte(marker), 0o600)
	}}
	if _, err := g.generate(context.Background(), options); err == nil {
		t.Fatal("generation reported success after its parent path was substituted")
	}
	assertFileEquals(t, filepath.Join(parent, "keep"), marker)
	assertEntries(t, parent, []string{"keep"})
	if _, err := os.Lstat(filepath.Join(previous, "sesame", planName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a plan was published under the moved parent: %v", err)
	}
}

func TestSubstitutedPlanInputIsNotPublished(t *testing.T) {
	options := localOptions(t)
	foreign := "{\"fictionalForeignPlan\":true}\n"
	var substituted string
	g := generator{hook: func(step string) error {
		if step != "link-plan" {
			return nil
		}
		entries, err := os.ReadDir(options.Directory)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if !strings.HasPrefix(entry.Name(), planTempPrefix) {
				continue
			}
			substituted = filepath.Join(options.Directory, entry.Name())
			if err := os.Rename(substituted, substituted+".saved"); err != nil {
				return err
			}
			return os.WriteFile(substituted, []byte(foreign), 0o600)
		}
		return errors.New("no staged plan was found")
	}}
	if _, err := g.generate(context.Background(), options); err == nil {
		t.Fatal("generation published a substituted temporary plan")
	}
	if substituted == "" {
		t.Fatal("the substitution was not exercised")
	}
	assertFileEquals(t, substituted, foreign)
	if _, err := os.Lstat(filepath.Join(options.Directory, planName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a substituted plan was published: %v", err)
	}
}
