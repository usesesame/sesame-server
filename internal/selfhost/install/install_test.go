package install

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"usesesame.app/backend/deploy/selfhost"
)

func localOptions(t *testing.T) Options {
	t.Helper()
	return Options{
		Profile:     "local",
		Version:     "0.4.1",
		Directory:   filepath.Join(t.TempDir(), "sesame"),
		ToolVersion: "0.4.1-dev",
	}
}

func caddyOptions(t *testing.T) Options {
	t.Helper()
	options := localOptions(t)
	options.Profile = "caddy"
	options.Host = "vault.example.com"
	return options
}

func tailscaleOptions(t *testing.T) Options {
	t.Helper()
	options := localOptions(t)
	options.Profile = "tailscale"
	options.Host = "vault.example-tailnet.ts.net"
	return options
}

func TestGenerateLocalProfile(t *testing.T) {
	options := localOptions(t)
	plan, err := Generate(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if plan.PublicURL != "http://localhost:8787" {
		t.Fatalf("public URL is %q", plan.PublicURL)
	}
	if len(plan.TrustedProxies) != 0 {
		t.Fatalf("trusted proxies are %v", plan.TrustedProxies)
	}
	assertPlan(t, options, plan, []string{composeName, envName})
	assertEntries(t, options.Directory, []string{".env", "compose.yaml", "plan.json"})
	assertFileEquals(t, filepath.Join(options.Directory, composeName), selfhostdeploy.Compose())
	assertFileEquals(t, filepath.Join(options.Directory, envName), "SESAME_IMAGE_TAG=0.4.1\nSESAME_PUBLIC_URL=http://localhost:8787\nSESAME_TRUSTED_PROXIES=\n")
	assertPlanJSON(t, options.Directory)
	assertModes(t, options.Directory)
	assertPrerequisites(t, plan, []string{"docker", "port_8787", "subnet"})
}

func TestGenerateCaddyProfile(t *testing.T) {
	options := caddyOptions(t)
	plan, err := Generate(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if plan.PublicURL != "https://vault.example.com" {
		t.Fatalf("public URL is %q", plan.PublicURL)
	}
	if len(plan.TrustedProxies) != 1 || plan.TrustedProxies[0] != "172.31.88.0/24" {
		t.Fatalf("trusted proxies are %v", plan.TrustedProxies)
	}
	assertPlan(t, options, plan, []string{composeName, envName, caddyfileName, caddyEnvName})
	assertEntries(t, options.Directory, []string{".env", "Caddyfile", "caddy.env", "compose.yaml", "plan.json"})
	assertFileEquals(t, filepath.Join(options.Directory, composeName), selfhostdeploy.Compose())
	assertFileEquals(t, filepath.Join(options.Directory, caddyfileName), selfhostdeploy.Caddy())
	assertFileEquals(t, filepath.Join(options.Directory, envName), "SESAME_IMAGE_TAG=0.4.1\nSESAME_PUBLIC_URL=https://vault.example.com\nSESAME_TRUSTED_PROXIES=172.31.88.0/24\n")
	assertFileEquals(t, filepath.Join(options.Directory, caddyEnvName), "SESAME_HOST=vault.example.com\n")
	assertPlanJSON(t, options.Directory)
	assertModes(t, options.Directory)
	assertPrerequisites(t, plan, []string{"docker", "port_8787", "subnet", "dns", "tls"})
}

func TestGenerateTailscaleProfile(t *testing.T) {
	options := tailscaleOptions(t)
	plan, err := Generate(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if plan.PublicURL != "https://vault.example-tailnet.ts.net" {
		t.Fatalf("public URL is %q", plan.PublicURL)
	}
	if len(plan.TrustedProxies) != 1 || plan.TrustedProxies[0] != "172.31.88.0/24" {
		t.Fatalf("trusted proxies are %v", plan.TrustedProxies)
	}
	assertPlan(t, options, plan, []string{composeName, envName})
	assertEntries(t, options.Directory, []string{".env", "compose.yaml", "plan.json"})
	assertFileEquals(t, filepath.Join(options.Directory, composeName), selfhostdeploy.Compose())
	assertFileEquals(t, filepath.Join(options.Directory, envName), "SESAME_IMAGE_TAG=0.4.1\nSESAME_PUBLIC_URL=https://vault.example-tailnet.ts.net\nSESAME_TRUSTED_PROXIES=172.31.88.0/24\n")
	assertPlanJSON(t, options.Directory)
	assertModes(t, options.Directory)
	assertPrerequisites(t, plan, []string{"docker", "port_8787", "subnet", "tailnet", "https"})
}

func TestPlanDoesNotStorePathsOrEnvironment(t *testing.T) {
	options := caddyOptions(t)
	if _, err := Generate(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	raw := mustRead(t, filepath.Join(options.Directory, planName))
	text := string(raw)
	if strings.Contains(text, options.Directory) {
		t.Fatal("plan contains the absolute target path")
	}
	if strings.Contains(text, "SESAME_") {
		t.Fatal("plan contains an environment key")
	}
	if strings.Contains(text, "generatedAt") {
		t.Fatal("plan contains a generatedAt field")
	}
}

func TestRepeatedGenerationDoesNotOverwrite(t *testing.T) {
	options := localOptions(t)
	first, err := Generate(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, options.Directory)
	second, err := Generate(context.Background(), options)
	if err == nil {
		t.Fatal("second generation succeeded")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second generation error is %v", err)
	}
	if second.SchemaVersion != 0 {
		t.Fatalf("second generation returned a plan: %+v", second)
	}
	if first.SchemaVersion != 1 {
		t.Fatalf("first schema version is %d", first.SchemaVersion)
	}
	after := snapshot(t, options.Directory)
	if len(before) != len(after) {
		t.Fatalf("directory has %d entries before and %d after", len(before), len(after))
	}
	for name, content := range before {
		if after[name] != content {
			t.Fatalf("%s changed after a repeated generation", name)
		}
	}
}

func TestExistingTargetsAreRefused(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, parent string) string
	}{
		{name: "file", setup: func(t *testing.T, parent string) string {
			path := filepath.Join(parent, "sesame")
			if err := os.WriteFile(path, []byte("keep this file\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return path
		}},
		{name: "empty directory", setup: func(t *testing.T, parent string) string {
			path := filepath.Join(parent, "sesame")
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			return path
		}},
		{name: "directory with file", setup: func(t *testing.T, parent string) string {
			path := filepath.Join(parent, "sesame")
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(path, "unrelated.txt"), []byte("unrelated\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return path
		}},
		{name: "symlink", setup: func(t *testing.T, parent string) string {
			victim := filepath.Join(parent, "victim")
			if err := os.Mkdir(victim, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(parent, "sesame")
			if err := os.Symlink(victim, path); err != nil {
				t.Fatal(err)
			}
			return path
		}},
		{name: "broken symlink", setup: func(t *testing.T, parent string) string {
			path := filepath.Join(parent, "sesame")
			if err := os.Symlink(filepath.Join(parent, "missing"), path); err != nil {
				t.Fatal(err)
			}
			return path
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			parent := t.TempDir()
			target := testCase.setup(t, parent)
			before, err := os.Lstat(target)
			if err != nil {
				t.Fatal(err)
			}
			beforeLink, _ := os.Readlink(target)
			beforeEntries, _ := os.ReadDir(target)
			beforeFile := []byte(nil)
			if before.Mode().IsRegular() {
				beforeFile = mustRead(t, target)
			}
			options := Options{Profile: "local", Version: "0.4.1", Directory: target, ToolVersion: "0.4.1"}
			if _, err := Generate(context.Background(), options); err == nil {
				t.Fatal("generation succeeded for an existing target")
			}
			after, err := os.Lstat(target)
			if err != nil {
				t.Fatal(err)
			}
			if before.Mode().Type() != after.Mode().Type() {
				t.Fatalf("target type changed from %v to %v", before.Mode().Type(), after.Mode().Type())
			}
			if beforeLink != "" {
				afterLink, err := os.Readlink(target)
				if err != nil || afterLink != beforeLink {
					t.Fatalf("symlink changed from %q to %q: %v", beforeLink, afterLink, err)
				}
			}
			afterEntries, _ := os.ReadDir(target)
			if len(beforeEntries) != len(afterEntries) {
				t.Fatalf("target entries changed from %d to %d", len(beforeEntries), len(afterEntries))
			}
			if before.Mode().IsRegular() {
				if got := mustRead(t, target); string(got) != string(beforeFile) {
					t.Fatal("existing file changed")
				}
			}
		})
	}
}

func TestMissingParentIsNotCreated(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "missing", "sesame")
	options := Options{Profile: "local", Version: "0.4.1", Directory: target, ToolVersion: "0.4.1"}
	if _, err := Generate(context.Background(), options); err == nil {
		t.Fatal("generation succeeded without a parent")
	}
	if _, err := os.Lstat(filepath.Join(base, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing parent was created: %v", err)
	}
}

func TestParentFileIsRefused(t *testing.T) {
	base := t.TempDir()
	parent := filepath.Join(base, "parent")
	if err := os.WriteFile(parent, []byte("not a directory\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	options := Options{Profile: "local", Version: "0.4.1", Directory: filepath.Join(parent, "sesame"), ToolVersion: "0.4.1"}
	if _, err := Generate(context.Background(), options); err == nil {
		t.Fatal("generation succeeded with a file as parent")
	}
	assertFileEquals(t, parent, "not a directory\n")
}

func TestParentSymlinkIsRefused(t *testing.T) {
	base := t.TempDir()
	realParent := filepath.Join(base, "real")
	if err := os.Mkdir(realParent, 0o700); err != nil {
		t.Fatal(err)
	}
	linkParent := filepath.Join(base, "link")
	if err := os.Symlink(realParent, linkParent); err != nil {
		t.Fatal(err)
	}
	options := Options{Profile: "local", Version: "0.4.1", Directory: filepath.Join(linkParent, "sesame"), ToolVersion: "0.4.1"}
	if _, err := Generate(context.Background(), options); err == nil {
		t.Fatal("generation succeeded through a symlinked parent")
	}
	if _, err := os.Lstat(filepath.Join(realParent, "sesame")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("symlinked parent was written through: %v", err)
	}
}

func TestSymlinkTargetDoesNotWriteIntoVictim(t *testing.T) {
	base := t.TempDir()
	victim := filepath.Join(base, "victim")
	if err := os.Mkdir(victim, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(victim, composeName), []byte("victim compose\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(base, "sesame")
	if err := os.Symlink(victim, target); err != nil {
		t.Fatal(err)
	}
	options := Options{Profile: "local", Version: "0.4.1", Directory: target, ToolVersion: "0.4.1"}
	if _, err := Generate(context.Background(), options); err == nil {
		t.Fatal("generation succeeded for a symlink target")
	}
	assertFileEquals(t, filepath.Join(victim, composeName), "victim compose\n")
	assertEntries(t, victim, []string{composeName})
}

func TestInvalidOptionsCreateNothing(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "sesame")
	cases := []Options{
		{Profile: "", Version: "0.4.1", Directory: target, ToolVersion: "0.4.1"},
		{Profile: "nginx", Version: "0.4.1", Directory: target, ToolVersion: "0.4.1"},
		{Profile: "local", Version: "v0.4.1", Directory: target, ToolVersion: "0.4.1"},
		{Profile: "local", Version: "0.4.1", Host: "vault.example.com", Directory: target, ToolVersion: "0.4.1"},
		{Profile: "caddy", Version: "0.4.1", Directory: target, ToolVersion: "0.4.1"},
		{Profile: "caddy", Version: "0.4.1", Host: "vault.local", Directory: target, ToolVersion: "0.4.1"},
		{Profile: "tailscale", Version: "0.4.1", Host: "vault.example.com", Directory: target, ToolVersion: "0.4.1"},
		{Profile: "local", Version: "0.4.1", Directory: target, ToolVersion: "$(id)"},
		{Profile: "local", Version: "0.4.1", Directory: target + "\n", ToolVersion: "0.4.1"},
	}
	for _, options := range cases {
		if _, err := Generate(context.Background(), options); err == nil {
			t.Fatalf("generation succeeded for %+v", options)
		}
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("invalid options created %v", entries)
	}
}

func TestConcurrentGenerationHasOneWinner(t *testing.T) {
	options := localOptions(t)
	const attempts = 12
	start := make(chan struct{})
	results := make(chan error, attempts)
	var wait sync.WaitGroup
	for index := 0; index < attempts; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, err := Generate(context.Background(), options)
			results <- err
		}()
	}
	close(start)
	wait.Wait()
	close(results)

	winners := 0
	for err := range results {
		if err == nil {
			winners++
			continue
		}
		if !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("concurrent loser error is %v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("concurrent generation had %d winners", winners)
	}
	assertEntries(t, options.Directory, []string{".env", "compose.yaml", "plan.json"})
	assertPlanJSON(t, options.Directory)
}

func TestCancellationBeforeCreation(t *testing.T) {
	options := localOptions(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Generate(ctx, options); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error is %v", err)
	}
	if _, err := os.Lstat(options.Directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("target exists after cancellation: %v", err)
	}
}

func TestCancellationDuringGenerationCleansUp(t *testing.T) {
	options := localOptions(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	generator := generator{hook: func(step string) error {
		if step == "write:.env" {
			cancel()
		}
		return nil
	}}
	_, err := generator.generate(ctx, options)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error is %v", err)
	}
	assertDirectoryAbsent(t, options.Directory)
	if entries, err := os.ReadDir(filepath.Dir(options.Directory)); err != nil || len(entries) != 0 {
		t.Fatalf("parent has entries after cancellation: %v, %v", entries, err)
	}
}

func TestCancellationAtPublicationCleansUp(t *testing.T) {
	options := localOptions(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	generator := generator{hook: func(step string) error {
		if step == "link-plan" {
			cancel()
		}
		return nil
	}}
	_, err := generator.generate(ctx, options)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error is %v", err)
	}
	assertDirectoryAbsent(t, options.Directory)
}

func TestFileWriteFailureCleansUp(t *testing.T) {
	options := localOptions(t)
	simulated := errors.New("simulated I/O failure")
	generator := generator{hook: func(step string) error {
		if step == "sync-file:"+composeName {
			return simulated
		}
		return nil
	}}
	if _, err := generator.generate(context.Background(), options); !errors.Is(err, simulated) {
		t.Fatalf("write failure error is %v", err)
	}
	assertDirectoryAbsent(t, options.Directory)
}

func TestSecondFileWriteFailureCleansUpFirst(t *testing.T) {
	options := localOptions(t)
	simulated := errors.New("simulated I/O failure")
	generator := generator{hook: func(step string) error {
		if step == "sync-file:"+envName {
			return simulated
		}
		return nil
	}}
	if _, err := generator.generate(context.Background(), options); !errors.Is(err, simulated) {
		t.Fatalf("write failure error is %v", err)
	}
	assertDirectoryAbsent(t, options.Directory)
}

func TestPublicationFailureCleansUp(t *testing.T) {
	options := caddyOptions(t)
	simulated := errors.New("simulated publication failure")
	generator := generator{hook: func(step string) error {
		if step == "link-plan" {
			return simulated
		}
		return nil
	}}
	if _, err := generator.generate(context.Background(), options); !errors.Is(err, simulated) {
		t.Fatalf("publication failure error is %v", err)
	}
	assertDirectoryAbsent(t, options.Directory)
}

func TestFinalDirectorySyncFailureLeavesCompletePlan(t *testing.T) {
	options := localOptions(t)
	simulated := errors.New("simulated directory sync failure")
	generator := generator{hook: func(step string) error {
		if step == "sync-directory" {
			return simulated
		}
		return nil
	}}
	plan, err := generator.generate(context.Background(), options)
	if !errors.Is(err, simulated) {
		t.Fatalf("directory sync failure error is %v", err)
	}
	if plan.SchemaVersion != 0 {
		t.Fatalf("failed generation returned a plan: %+v", plan)
	}
	assertEntries(t, options.Directory, []string{".env", "compose.yaml", "plan.json"})
	assertPlanJSON(t, options.Directory)
	assertFileEquals(t, filepath.Join(options.Directory, composeName), selfhostdeploy.Compose())
}

func TestCancellationAfterPublicationLeavesCompletePlan(t *testing.T) {
	options := localOptions(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	generator := generator{hook: func(step string) error {
		if step == "sync-directory" {
			cancel()
		}
		return nil
	}}
	if _, err := generator.generate(ctx, options); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error is %v", err)
	}
	assertEntries(t, options.Directory, []string{".env", "compose.yaml", "plan.json"})
	assertPlanJSON(t, options.Directory)
}

func TestForeignPlanIsNotOverwritten(t *testing.T) {
	options := localOptions(t)
	foreign := []byte("{\"foreign\":true}\n")
	generator := generator{hook: func(step string) error {
		if step == "link-plan" {
			return os.WriteFile(filepath.Join(options.Directory, planName), foreign, 0o600)
		}
		return nil
	}}
	if _, err := generator.generate(context.Background(), options); err == nil {
		t.Fatal("generation succeeded with a foreign plan present")
	}
	assertFileEquals(t, filepath.Join(options.Directory, planName), string(foreign))
	assertEntries(t, options.Directory, []string{"plan.json"})
}

func TestCleanupPreservesSubstitutedAndUnrelatedFiles(t *testing.T) {
	options := localOptions(t)
	simulated := errors.New("simulated I/O failure")
	generator := generator{hook: func(step string) error {
		if step != "sync-file:"+envName {
			return nil
		}
		composePath := filepath.Join(options.Directory, composeName)
		if err := os.Remove(composePath); err != nil {
			return err
		}
		if err := os.WriteFile(composePath, []byte("substituted compose\n"), 0o600); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(options.Directory, "unrelated.txt"), []byte("unrelated\n"), 0o600); err != nil {
			return err
		}
		return simulated
	}}
	if _, err := generator.generate(context.Background(), options); err == nil {
		t.Fatal("injected hook did not fail generation")
	} else if !errors.Is(err, simulated) && !strings.Contains(err.Error(), "simulated") {
		t.Fatalf("unexpected error %v", err)
	}
	assertFileEquals(t, filepath.Join(options.Directory, composeName), "substituted compose\n")
	assertFileEquals(t, filepath.Join(options.Directory, "unrelated.txt"), "unrelated\n")
	if _, err := os.Lstat(filepath.Join(options.Directory, envName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial .env remains: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(options.Directory, planName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plan.json exists after failure: %v", err)
	}
}

func TestCleanupDoesNotRemoveReplacedDirectory(t *testing.T) {
	options := localOptions(t)
	moved := options.Directory + "-moved"
	simulated := errors.New("simulated publication failure")
	generator := generator{hook: func(step string) error {
		if step != "link-plan" {
			return nil
		}
		if err := os.Rename(options.Directory, moved); err != nil {
			return err
		}
		if err := os.Mkdir(options.Directory, 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(options.Directory, "foreign.txt"), []byte("foreign\n"), 0o600); err != nil {
			return err
		}
		return simulated
	}}
	if _, err := generator.generate(context.Background(), options); !errors.Is(err, simulated) {
		t.Fatalf("unexpected error %v", err)
	}
	assertEntries(t, options.Directory, []string{"foreign.txt"})
	assertFileEquals(t, filepath.Join(options.Directory, "foreign.txt"), "foreign\n")
	if _, err := os.Lstat(filepath.Join(moved, planName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plan exists in the moved directory: %v", err)
	}
}

func TestPlanIsPublishedLast(t *testing.T) {
	options := caddyOptions(t)
	planPath := filepath.Join(options.Directory, planName)
	generator := generator{hook: func(step string) error {
		if step == "write:compose.yaml" || step == "write:.env" || step == "write:Caddyfile" || step == "write:caddy.env" || step == "publish" {
			if _, err := os.Lstat(planPath); !errors.Is(err, os.ErrNotExist) {
				return errors.New("plan.json existed before publication")
			}
		}
		return nil
	}}
	if _, err := generator.generate(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	assertPlanJSON(t, options.Directory)
}

func assertPlan(t *testing.T, options Options, plan Plan, names []string) {
	t.Helper()
	if plan.SchemaVersion != 1 {
		t.Fatalf("schema version is %d", plan.SchemaVersion)
	}
	if plan.ToolVersion != options.ToolVersion {
		t.Fatalf("tool version is %q", plan.ToolVersion)
	}
	if plan.Profile != options.Profile {
		t.Fatalf("profile is %q", plan.Profile)
	}
	if plan.ImageVersion != options.Version {
		t.Fatalf("image version is %q", plan.ImageVersion)
	}
	if len(plan.Files) != len(names) {
		t.Fatalf("plan lists %d files, want %d", len(plan.Files), len(names))
	}
	for index, name := range names {
		file := plan.Files[index]
		if file.Path != name {
			t.Fatalf("file %d is %q, want %q", index, file.Path, name)
		}
		if file.Mode != "0600" {
			t.Fatalf("%s mode is %q", name, file.Mode)
		}
		raw := mustRead(t, filepath.Join(options.Directory, name))
		sum := sha256.Sum256(raw)
		if file.SHA256 != hex.EncodeToString(sum[:]) {
			t.Fatalf("%s hash is %q", name, file.SHA256)
		}
		if len(file.SHA256) != 64 || strings.ToLower(file.SHA256) != file.SHA256 {
			t.Fatalf("%s hash is not lowercase SHA-256: %q", name, file.SHA256)
		}
		if name == planName {
			t.Fatal("plan lists itself")
		}
	}
	for _, file := range plan.Files {
		if file.Path == planName {
			t.Fatal("plan lists plan.json")
		}
	}
}

func assertPlanJSON(t *testing.T, directory string) {
	t.Helper()
	raw := mustRead(t, filepath.Join(directory, planName))
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	keys := []string{"schemaVersion", "toolVersion", "profile", "imageVersion", "publicUrl", "trustedProxies", "files", "prerequisites"}
	if len(object) != len(keys) {
		t.Fatalf("plan has %d fields: %s", len(object), string(raw))
	}
	for _, key := range keys {
		if _, ok := object[key]; !ok {
			t.Fatalf("plan has no %s field", key)
		}
	}
	var files []map[string]json.RawMessage
	if err := json.Unmarshal(object["files"], &files); err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if len(file) != 3 {
			t.Fatalf("plan file entry has %d fields", len(file))
		}
		for _, key := range []string{"path", "sha256", "mode"} {
			if _, ok := file[key]; !ok {
				t.Fatalf("plan file entry has no %s field", key)
			}
		}
	}
	var prerequisites []map[string]json.RawMessage
	if err := json.Unmarshal(object["prerequisites"], &prerequisites); err != nil {
		t.Fatal(err)
	}
	for _, prerequisite := range prerequisites {
		if len(prerequisite) != 3 {
			t.Fatalf("plan prerequisite has %d fields", len(prerequisite))
		}
		for _, key := range []string{"id", "state", "message"} {
			if _, ok := prerequisite[key]; !ok {
				t.Fatalf("plan prerequisite has no %s field", key)
			}
		}
	}
	var plan Plan
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&plan); err != nil {
		t.Fatal(err)
	}
	if decoder.More() {
		t.Fatal("plan has trailing JSON")
	}
	if plan.TrustedProxies == nil {
		t.Fatal("plan trustedProxies is null")
	}
}

func assertPrerequisites(t *testing.T, plan Plan, ids []string) {
	t.Helper()
	if len(plan.Prerequisites) != len(ids) {
		t.Fatalf("plan has %d prerequisites, want %d", len(plan.Prerequisites), len(ids))
	}
	for index, id := range ids {
		prerequisite := plan.Prerequisites[index]
		if prerequisite.ID != id {
			t.Fatalf("prerequisite %d is %q, want %q", index, prerequisite.ID, id)
		}
		if prerequisite.State != "unknown" {
			t.Fatalf("%s state is %q", id, prerequisite.State)
		}
		if !strings.HasSuffix(prerequisite.Message, ".") {
			t.Fatalf("%s message is not a complete sentence: %q", id, prerequisite.Message)
		}
		if prerequisite.Message == "" || prerequisite.Message[0] < 'A' || prerequisite.Message[0] > 'Z' {
			t.Fatalf("%s message does not start with a capital letter: %q", id, prerequisite.Message)
		}
	}
}

func assertModes(t *testing.T, directory string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("directory mode is %o, want 700", got)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("%s mode is %o, want 600", entry.Name(), got)
		}
	}
}

func assertEntries(t *testing.T, directory string, want []string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		got = append(got, entry.Name())
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("entries are %v, want %v", got, want)
	}
}

func assertDirectoryAbsent(t *testing.T, directory string) {
	t.Helper()
	if _, err := os.Lstat(directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("directory exists: %v", err)
	}
}

func assertFileEquals(t *testing.T, path, want string) {
	t.Helper()
	if got := string(mustRead(t, path)); got != want {
		t.Fatalf("%s is %q, want %q", path, got, want)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func snapshot(t *testing.T, directory string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	content := map[string]string{}
	for _, entry := range entries {
		content[entry.Name()] = string(mustRead(t, filepath.Join(directory, entry.Name())))
	}
	return content
}
