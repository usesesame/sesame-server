package secrets

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func skipWithoutUnixModes(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("file modes are not enforced on windows")
	}
}

func newDataDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "data")
}

func mustCreate(t *testing.T, dataDir string) *Secrets {
	t.Helper()
	loaded, _, err := LoadOrCreate(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	result := map[string]string{}
	for _, entry := range entries {
		result[entry.Name()] = string(readFile(t, filepath.Join(dir, entry.Name())))
	}
	return result
}

func restoreOps(t *testing.T) {
	t.Helper()
	saved := ops
	t.Cleanup(func() { ops = saved })
}

func TestFirstRunCreatesAllSecretsWithOwnerOnlyModes(t *testing.T) {
	skipWithoutUnixModes(t)
	dataDir := newDataDir(t)
	loaded, generated, err := LoadOrCreate(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(generated) != 3 {
		t.Fatalf("generated = %v", generated)
	}
	dirInfo, err := os.Stat(Dir(dataDir))
	if err != nil || dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("secrets dir = %v, %v", dirInfo, err)
	}
	for _, name := range Files() {
		info, err := os.Stat(filepath.Join(Dir(dataDir), name))
		if err != nil || info.Mode().Perm() != 0o600 || info.Size() != KeySize {
			t.Fatalf("%s: info = %v, err = %v", name, info, err)
		}
	}
	entries, _ := os.ReadDir(Dir(dataDir))
	if len(entries) != 3 {
		t.Fatalf("directory holds %d entries, want exactly the three secrets", len(entries))
	}
	if len(loaded.SigningKey) != ed25519.PrivateKeySize || len(loaded.AdminEncryptionKey) != KeySize || len(loaded.IPPepper) != KeySize {
		t.Fatalf("lengths = %d %d %d", len(loaded.SigningKey), len(loaded.AdminEncryptionKey), len(loaded.IPPepper))
	}
	message := []byte("sesame")
	if !ed25519.Verify(loaded.PublicKey(), message, ed25519.Sign(loaded.SigningKey, message)) {
		t.Fatal("signing key does not verify its own signature")
	}
	if bytes.Equal(loaded.AdminEncryptionKey, loaded.IPPepper) || bytes.Equal(loaded.AdminEncryptionKey, loaded.SigningKey.Seed()) {
		t.Fatal("secrets share the same bytes")
	}
}

func TestSecondRunReusesTheSameSecretsAndWritesNothing(t *testing.T) {
	dataDir := newDataDir(t)
	first := mustCreate(t, dataDir)
	before := snapshot(t, Dir(dataDir))
	second, generated, err := LoadOrCreate(dataDir)
	if err != nil || len(generated) != 0 {
		t.Fatalf("generated = %v, err = %v", generated, err)
	}
	if !first.SigningKey.Equal(second.SigningKey) || !bytes.Equal(first.AdminEncryptionKey, second.AdminEncryptionKey) || !bytes.Equal(first.IPPepper, second.IPPepper) {
		t.Fatal("secrets changed between runs")
	}
	after := snapshot(t, Dir(dataDir))
	if fmt.Sprint(before) != fmt.Sprint(after) {
		t.Fatal("files changed between runs")
	}
}

func TestLoadReadsWithoutCreating(t *testing.T) {
	dataDir := newDataDir(t)
	if _, err := Load(dataDir); err == nil {
		t.Fatal("Load succeeded without a secrets directory")
	}
	if _, err := os.Stat(Dir(dataDir)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Load created the secrets directory: %v", err)
	}
	created := mustCreate(t, dataDir)
	loaded, err := Load(dataDir)
	if err != nil || !loaded.SigningKey.Equal(created.SigningKey) {
		t.Fatalf("Load = %v, %v", loaded, err)
	}
	if err := os.Remove(filepath.Join(Dir(dataDir), AdminKeyFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dataDir); err == nil || !strings.Contains(err.Error(), AdminKeyFile) {
		t.Fatalf("Load with a missing secret = %v", err)
	}
}

func TestExistingSecretIsNeverOverwritten(t *testing.T) {
	skipWithoutUnixModes(t)
	dataDir := newDataDir(t)
	mustCreate(t, dataDir)
	path := filepath.Join(Dir(dataDir), SigningKeyFile)
	original := readFile(t, path)
	if err := os.Remove(filepath.Join(Dir(dataDir), IPPepperFile)); err != nil {
		t.Fatal(err)
	}
	mustCreate(t, dataDir)
	if !bytes.Equal(readFile(t, path), original) {
		t.Fatal("an existing secret was rewritten")
	}
}

func TestWriteOnceLosesGracefullyToAFileThatAppearsFirst(t *testing.T) {
	dir := t.TempDir()
	winner := bytes.Repeat([]byte{7}, KeySize)
	if err := os.WriteFile(filepath.Join(dir, "x.key"), winner, 0o600); err != nil {
		t.Fatal(err)
	}
	wrote, err := writeOnce(dir, "x.key", bytes.Repeat([]byte{9}, KeySize))
	if err != nil || wrote {
		t.Fatalf("writeOnce = %v, %v", wrote, err)
	}
	if !bytes.Equal(readFile(t, filepath.Join(dir, "x.key")), winner) {
		t.Fatal("writeOnce overwrote an existing file")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temporary file was left behind: %v", entries)
	}
}

func TestConcurrentFirstRunsAgreeOnTheSameSecrets(t *testing.T) {
	dataDir := newDataDir(t)
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	const runs = 8
	results := make([]*Secrets, runs)
	errs := make([]error, runs)
	done := make(chan int)
	for index := range runs {
		go func() {
			results[index], _, errs[index] = LoadOrCreate(dataDir)
			done <- index
		}()
	}
	for range runs {
		<-done
	}
	for index := range runs {
		if errs[index] != nil {
			t.Fatalf("run %d: %v", index, errs[index])
		}
		if !results[index].SigningKey.Equal(results[0].SigningKey) || !bytes.Equal(results[index].AdminEncryptionKey, results[0].AdminEncryptionKey) || !bytes.Equal(results[index].IPPepper, results[0].IPPepper) {
			t.Fatalf("run %d holds different secrets", index)
		}
	}
	entries, _ := os.ReadDir(Dir(dataDir))
	if len(entries) != 3 {
		t.Fatalf("directory holds %d entries: %v", len(entries), entries)
	}
}

func TestLooseFileModesAreRefusedAndLeftAlone(t *testing.T) {
	skipWithoutUnixModes(t)
	for _, mode := range []os.FileMode{0o644, 0o640, 0o604, 0o660, 0o666, 0o700, 0o601} {
		t.Run(fmt.Sprintf("%04o", mode), func(t *testing.T) {
			dataDir := newDataDir(t)
			mustCreate(t, dataDir)
			path := filepath.Join(Dir(dataDir), AdminKeyFile)
			before := readFile(t, path)
			if err := os.Chmod(path, mode); err != nil {
				t.Fatal(err)
			}
			_, _, err := LoadOrCreate(dataDir)
			if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "chmod 600") {
				t.Fatalf("LoadOrCreate = %v", err)
			}
			if _, err := Load(dataDir); err == nil {
				t.Fatal("Load accepted a loose file")
			}
			info, _ := os.Stat(path)
			if info.Mode().Perm() != mode {
				t.Fatalf("mode was changed to %04o", info.Mode().Perm())
			}
			if !bytes.Equal(readFile(t, path), before) {
				t.Fatal("content was changed")
			}
		})
	}
}

func TestStricterFileModeIsAccepted(t *testing.T) {
	skipWithoutUnixModes(t)
	dataDir := newDataDir(t)
	mustCreate(t, dataDir)
	for _, name := range Files() {
		if err := os.Chmod(filepath.Join(Dir(dataDir), name), 0o400); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := LoadOrCreate(dataDir); err != nil {
		t.Fatal(err)
	}
}

func TestLooseDirectoryModeIsRefused(t *testing.T) {
	skipWithoutUnixModes(t)
	for _, mode := range []os.FileMode{0o755, 0o750, 0o770, 0o701} {
		t.Run(fmt.Sprintf("%04o", mode), func(t *testing.T) {
			dataDir := newDataDir(t)
			mustCreate(t, dataDir)
			if err := os.Chmod(Dir(dataDir), mode); err != nil {
				t.Fatal(err)
			}
			if _, _, err := LoadOrCreate(dataDir); err == nil || !strings.Contains(err.Error(), "chmod 700") {
				t.Fatalf("LoadOrCreate = %v", err)
			}
			if _, err := Load(dataDir); err == nil {
				t.Fatal("Load accepted a loose directory")
			}
		})
	}
}

func TestWrongLengthFilesAreRefusedAndNothingIsRegenerated(t *testing.T) {
	skipWithoutUnixModes(t)
	cases := map[string][]byte{
		"empty":              {},
		"truncated":          bytes.Repeat([]byte{1}, 16),
		"one short":          bytes.Repeat([]byte{1}, KeySize-1),
		"one long":           bytes.Repeat([]byte{1}, KeySize+1),
		"private key length": bytes.Repeat([]byte{1}, ed25519.PrivateKeySize),
		"hex text":           []byte(strings.Repeat("ab", KeySize)),
	}
	for name, content := range cases {
		for _, file := range Files() {
			t.Run(name+"/"+file, func(t *testing.T) {
				dataDir := newDataDir(t)
				mustCreate(t, dataDir)
				path := filepath.Join(Dir(dataDir), file)
				if err := os.WriteFile(path, content, 0o600); err != nil {
					t.Fatal(err)
				}
				before := snapshot(t, Dir(dataDir))
				_, _, err := LoadOrCreate(dataDir)
				if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "exactly 32 bytes") {
					t.Fatalf("LoadOrCreate = %v", err)
				}
				if _, err := Load(dataDir); err == nil {
					t.Fatal("Load accepted a wrong length file")
				}
				if fmt.Sprint(before) != fmt.Sprint(snapshot(t, Dir(dataDir))) {
					t.Fatal("a file was rewritten or added")
				}
			})
		}
	}
}

func TestZeroFilledSecretIsRefused(t *testing.T) {
	dataDir := newDataDir(t)
	mustCreate(t, dataDir)
	path := filepath.Join(Dir(dataDir), IPPepperFile)
	if err := os.WriteFile(path, make([]byte, KeySize), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadOrCreate(dataDir); err == nil || !strings.Contains(err.Error(), "zero") {
		t.Fatalf("LoadOrCreate = %v", err)
	}
}

func TestSymbolicLinkSecretIsRefused(t *testing.T) {
	skipWithoutUnixModes(t)
	dataDir := newDataDir(t)
	mustCreate(t, dataDir)
	target := filepath.Join(t.TempDir(), "elsewhere.key")
	if err := os.WriteFile(target, bytes.Repeat([]byte{3}, KeySize), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(Dir(dataDir), SigningKeyFile)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadOrCreate(dataDir); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("LoadOrCreate = %v", err)
	}
}

func TestSecretsDirectoryThatIsAFileOrLinkIsRefused(t *testing.T) {
	skipWithoutUnixModes(t)
	dataDir := newDataDir(t)
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Dir(dataDir), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadOrCreate(dataDir); err == nil || !strings.Contains(err.Error(), "must be a directory") {
		t.Fatalf("file: %v", err)
	}
	if err := os.Remove(Dir(dataDir)); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	if err := os.Chmod(other, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, Dir(dataDir)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadOrCreate(dataDir); err == nil || !strings.Contains(err.Error(), "not a symbolic link") {
		t.Fatalf("link: %v", err)
	}
	entries, _ := os.ReadDir(other)
	if len(entries) != 0 {
		t.Fatalf("secrets were written through the link: %v", entries)
	}
}

func TestPartialGenerationIsCompletedWithoutChangingExistingFiles(t *testing.T) {
	skipWithoutUnixModes(t)
	dataDir := newDataDir(t)
	mustCreate(t, dataDir)
	signing := readFile(t, filepath.Join(Dir(dataDir), SigningKeyFile))
	for _, name := range []string{AdminKeyFile, IPPepperFile} {
		if err := os.Remove(filepath.Join(Dir(dataDir), name)); err != nil {
			t.Fatal(err)
		}
	}
	loaded, generated, err := LoadOrCreate(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(generated) != 2 || !bytes.Equal(loaded.SigningKey.Seed(), signing) {
		t.Fatalf("generated = %v, seed changed = %v", generated, !bytes.Equal(loaded.SigningKey.Seed(), signing))
	}
}

func TestMissingSecretsWithAnExistingDatabaseAreNotRegenerated(t *testing.T) {
	skipWithoutUnixModes(t)
	dataDir := newDataDir(t)
	mustCreate(t, dataDir)
	if err := os.WriteFile(filepath.Join(dataDir, DatabaseFile), []byte("sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(Dir(dataDir), AdminKeyFile)); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, Dir(dataDir))
	_, _, err := LoadOrCreate(dataDir)
	if err == nil || !strings.Contains(err.Error(), AdminKeyFile) || !strings.Contains(err.Error(), "restore the secrets directory from a backup") {
		t.Fatalf("LoadOrCreate = %v", err)
	}
	if fmt.Sprint(before) != fmt.Sprint(snapshot(t, Dir(dataDir))) {
		t.Fatal("a secret was generated next to an existing database")
	}
}

func TestInterruptedGenerationLeavesNoPartialSecretAndRecovers(t *testing.T) {
	skipWithoutUnixModes(t)
	steps := map[string]func(*fileOps){
		"sync fails":     func(o *fileOps) { o.sync = func(*os.File) error { return errors.New("disk full") } },
		"link fails":     func(o *fileOps) { o.link = func(string, string) error { return errors.New("io error") } },
		"entropy fails":  func(o *fileOps) { o.entropy = strings.NewReader("short") },
		"dir sync fails": func(o *fileOps) { o.syncDir = func(string) error { return errors.New("io error") } },
	}
	for name, inject := range steps {
		t.Run(name, func(t *testing.T) {
			restoreOps(t)
			real := ops
			dataDir := newDataDir(t)
			inject(&ops)
			_, _, err := LoadOrCreate(dataDir)
			if err == nil {
				t.Fatal("expected an error")
			}
			entries, _ := os.ReadDir(Dir(dataDir))
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), tempPrefix) {
					t.Fatalf("temporary file left behind: %s", entry.Name())
				}
				if info, statErr := os.Stat(filepath.Join(Dir(dataDir), entry.Name())); statErr == nil && info.Size() != KeySize {
					t.Fatalf("partial secret %s has %d bytes", entry.Name(), info.Size())
				}
			}
			ops = real
			loaded, _, err := LoadOrCreate(dataDir)
			if err != nil {
				t.Fatalf("recovery run: %v", err)
			}
			if len(loaded.IPPepper) != KeySize {
				t.Fatal("recovery run did not produce every secret")
			}
		})
	}
}

func TestInterruptionAfterTheFirstSecretKeepsItAndCompletesTheRest(t *testing.T) {
	skipWithoutUnixModes(t)
	restoreOps(t)
	dataDir := newDataDir(t)
	calls := 0
	realLink := os.Link
	ops.link = func(oldname, newname string) error {
		calls++
		if calls == 2 {
			return errors.New("power loss")
		}
		return realLink(oldname, newname)
	}
	if _, generated, err := LoadOrCreate(dataDir); err == nil || len(generated) != 1 {
		t.Fatalf("generated = %v, err = %v", generated, err)
	}
	signing := readFile(t, filepath.Join(Dir(dataDir), SigningKeyFile))
	ops.link = realLink
	loaded, generated, err := LoadOrCreate(dataDir)
	if err != nil || len(generated) != 2 {
		t.Fatalf("generated = %v, err = %v", generated, err)
	}
	if !bytes.Equal(loaded.SigningKey.Seed(), signing) {
		t.Fatal("the first secret changed during recovery")
	}
}

func TestLeftoverTemporaryFileNeverBecomesASecret(t *testing.T) {
	skipWithoutUnixModes(t)
	dataDir := newDataDir(t)
	if err := os.MkdirAll(Dir(dataDir), 0o700); err != nil {
		t.Fatal(err)
	}
	partial := filepath.Join(Dir(dataDir), tempPrefix+SigningKeyFile+"-deadbeef")
	if err := os.WriteFile(partial, []byte("half"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded := mustCreate(t, dataDir)
	if len(loaded.SigningKey) != ed25519.PrivateKeySize {
		t.Fatal("signing key missing")
	}
	if _, err := os.Stat(partial); err != nil {
		t.Fatalf("a fresh temporary file was removed, but it may belong to a concurrent start: %v", err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(partial, old, old); err != nil {
		t.Fatal(err)
	}
	mustCreate(t, dataDir)
	if _, err := os.Stat(partial); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a stale temporary file was kept: %v", err)
	}
}

func TestLinkUnsupportedFallsBackWithoutClobbering(t *testing.T) {
	skipWithoutUnixModes(t)
	restoreOps(t)
	ops.link = func(string, string) error { return syscall.ENOTSUP }
	dataDir := newDataDir(t)
	loaded := mustCreate(t, dataDir)
	if len(loaded.IPPepper) != KeySize {
		t.Fatal("fallback did not create every secret")
	}
	dir := t.TempDir()
	existing := bytes.Repeat([]byte{5}, KeySize)
	if err := os.WriteFile(filepath.Join(dir, "x.key"), existing, 0o600); err != nil {
		t.Fatal(err)
	}
	wrote, err := writeOnce(dir, "x.key", bytes.Repeat([]byte{6}, KeySize))
	if err != nil || wrote || !bytes.Equal(readFile(t, filepath.Join(dir, "x.key")), existing) {
		t.Fatalf("fallback clobbered an existing file: wrote = %v, err = %v", wrote, err)
	}
}

func TestSecretsDoNotAppearInFormattedOutput(t *testing.T) {
	loaded := mustCreate(t, newDataDir(t))
	var out bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&out, nil))
	logger.Info("secrets", "value", loaded)
	fmt.Fprintf(&out, "%v %+v %#v %s", loaded, loaded, loaded, loaded)
	text := out.String()
	for _, secret := range [][]byte{loaded.AdminEncryptionKey, loaded.IPPepper, loaded.SigningKey.Seed()} {
		if strings.Contains(text, string(secret)) || strings.Contains(text, fmt.Sprint(secret)) {
			t.Fatal("secret bytes were formatted")
		}
	}
}

func TestSecretsAreIndependentAcrossInstances(t *testing.T) {
	first := mustCreate(t, newDataDir(t))
	second := mustCreate(t, newDataDir(t))
	if first.SigningKey.Equal(second.SigningKey) || bytes.Equal(first.AdminEncryptionKey, second.AdminEncryptionKey) || bytes.Equal(first.IPPepper, second.IPPepper) {
		t.Fatal("two instances generated the same secret")
	}
}
