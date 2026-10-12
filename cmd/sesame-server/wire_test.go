package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"usesesame.app/backend/internal/selfhost/config"
	"usesesame.app/backend/internal/selfhost/secrets"
)

type realRun struct {
	cancel context.CancelFunc
	exit   chan int
	addr   string
	stderr *syncBuffer
}

func realEnvironment(values map[string]string, stdout, stderr *syncBuffer) Environment {
	return defaultEnvironment(lookupFrom(values), stdout, stderr)
}

func startReal(t *testing.T, dataDir string) *realRun {
	t.Helper()
	addr := freeAddr(t)
	stderr := &syncBuffer{}
	env := realEnvironment(map[string]string{config.EnvDataDir: dataDir, config.EnvAddr: addr}, &syncBuffer{}, stderr)
	ctx, cancel := context.WithCancel(context.Background())
	run := &realRun{cancel: cancel, exit: make(chan int, 1), addr: addr, stderr: stderr}
	go func() { run.exit <- runWith(ctx, nil, env) }()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case code := <-run.exit:
			t.Fatalf("server exited with %d: %s", code, stderr.String())
		default:
		}
		response, err := plainClient.Get("http://" + addr + "/readyz")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return run
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("server never became ready: %s", stderr.String())
	return nil
}

func runWith(ctx context.Context, args []string, env Environment) int { return run(ctx, args, env) }

func (r *realRun) stop(t *testing.T) {
	t.Helper()
	r.cancel()
	select {
	case code := <-r.exit:
		if code != exitOK {
			t.Fatalf("exit %d: %s", code, r.stderr.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("server did not stop")
	}
}

func (r *realRun) getJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	response, err := plainClient.Get("http://" + r.addr + path)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("%s: %v: %s", path, err, body)
	}
	return decoded
}

func command(t *testing.T, values map[string]string, args ...string) (int, string, string) {
	t.Helper()
	stdout, stderr := &syncBuffer{}, &syncBuffer{}
	code := run(context.Background(), args, realEnvironment(values, stdout, stderr))
	return code, stdout.String(), stderr.String()
}

func TestRealServerLifecycle(t *testing.T) {
	dataDir := t.TempDir()
	first := startReal(t, dataDir)

	instance := first.getJSON(t, "/v1/instance")
	if instance["profile"] != "selfhost" || instance["setupRequired"] != true || instance["fingerprint"] == "" {
		t.Fatalf("instance = %v", instance)
	}
	if !strings.Contains(first.stderr.String(), "/setup#token=") || !strings.Contains(first.stderr.String(), "to create the first owner") {
		t.Fatalf("the first run did not print a setup link:\n%s", first.stderr.String())
	}

	response, err := plainClient.Get("http://" + first.addr + "/")
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(strings.ToLower(string(page)), "<!doctype html>") {
		t.Fatalf("console = %d %q", response.StatusCode, page)
	}

	values := map[string]string{config.EnvDataDir: dataDir, config.EnvAddr: freeAddr(t)}
	code, _, stderr := command(t, values, "serve")
	if code != exitFailure || !strings.Contains(stderr, "already using") {
		t.Fatalf("a second server on the same data directory: exit %d, stderr %q", code, stderr)
	}
	code, _, stderr = command(t, values, "restore", filepath.Join(dataDir, "nothing.tar"))
	if code != exitFailure || !strings.Contains(stderr, "stop the running server") {
		t.Fatalf("restore while running: exit %d, stderr %q", code, stderr)
	}

	code, out, stderr := command(t, map[string]string{config.EnvDataDir: dataDir, config.EnvAddr: first.addr}, "healthcheck")
	if code != exitOK {
		t.Fatalf("healthcheck: exit %d, %s %s", code, out, stderr)
	}

	first.stop(t)
	fingerprint := instance["fingerprint"]

	code, _, stderr = command(t, map[string]string{config.EnvDataDir: dataDir, config.EnvAddr: first.addr}, "healthcheck")
	if code != exitFailure {
		t.Fatalf("healthcheck after shutdown: exit %d, %s", code, stderr)
	}

	second := startReal(t, dataDir)
	if got := second.getJSON(t, "/v1/instance")["fingerprint"]; got != fingerprint {
		t.Fatalf("the instance identity changed across restarts: %v then %v", fingerprint, got)
	}
	second.stop(t)

	operate(t, dataDir)
}

func operate(t *testing.T, dataDir string) {
	t.Helper()
	values := map[string]string{config.EnvDataDir: dataDir}

	code, out, stderr := command(t, values, "backup")
	if code != exitOK || !strings.Contains(out, "Backup written to") {
		t.Fatalf("backup: exit %d, %s %s", code, out, stderr)
	}
	matches, _ := filepath.Glob(filepath.Join(dataDir, "backups", "sesame-manual-*.tar"))
	if len(matches) != 1 {
		t.Fatalf("manual backups = %v", matches)
	}
	explicit := filepath.Join(t.TempDir(), "named.tar")
	if code, out, stderr = command(t, values, "backup", explicit); code != exitOK {
		t.Fatalf("backup to a path: exit %d, %s %s", code, out, stderr)
	}
	if code, _, stderr = command(t, values, "backup", explicit); code != exitFailure || !strings.Contains(stderr, "already exists") {
		t.Fatalf("backup over an existing file: exit %d, %s", code, stderr)
	}

	if code, out, stderr = command(t, values, "check", explicit); code != exitOK || !strings.Contains(out, "is intact") {
		t.Fatalf("check backup: exit %d, %s %s", code, out, stderr)
	}
	garbage := filepath.Join(t.TempDir(), "garbage.tar")
	if err := os.WriteFile(garbage, []byte("not an archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr = command(t, values, "check", garbage); code != exitFailure || !strings.Contains(stderr, "did not pass") {
		t.Fatalf("check garbage: exit %d, %s", code, stderr)
	}
	if code, out, stderr = command(t, values, "check"); code != exitOK || !strings.Contains(out, "passed its checks") {
		t.Fatalf("check live: exit %d, %s %s", code, out, stderr)
	}

	exported := filepath.Join(t.TempDir(), "export.json")
	if code, out, stderr = command(t, values, "export", exported); code != exitOK {
		t.Fatalf("export: exit %d, %s %s", code, out, stderr)
	}
	info, err := os.Stat(exported)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("export file = %v, %v", info, err)
	}
	content, _ := os.ReadFile(exported)
	var decoded map[string]any
	if err := json.Unmarshal(content, &decoded); err != nil {
		t.Fatalf("export is not JSON: %v", err)
	}
	if code, _, stderr = command(t, values, "export", exported); code != exitFailure || !strings.Contains(stderr, "never overwrites") {
		t.Fatalf("export over an existing file: exit %d, %s", code, stderr)
	}
	if code, out, _ = command(t, values, "export"); code != exitOK || !json.Valid([]byte(out)) {
		t.Fatalf("export to stdout: exit %d, %q", code, out)
	}

	if code, _, stderr = command(t, values, "owner", "reset", "Nobody Here"); code != exitFailure || !strings.Contains(stderr, `no active owner is named "Nobody Here"`) {
		t.Fatalf("owner reset: exit %d, %s", code, stderr)
	}

	restored := t.TempDir()
	if code, out, stderr = command(t, map[string]string{config.EnvDataDir: restored}, "restore", explicit); code != exitOK || !strings.Contains(out, "Restored") {
		t.Fatalf("restore: exit %d, %s %s", code, out, stderr)
	}
	original, err := secrets.Load(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	copied, err := secrets.Load(restored)
	if err != nil || !copied.SigningKey.Equal(original.SigningKey) {
		t.Fatalf("restored secrets = %v, %v", copied, err)
	}
	if code, out, stderr = command(t, map[string]string{config.EnvDataDir: restored}, "check"); code != exitOK {
		t.Fatalf("check restored: exit %d, %s %s", code, out, stderr)
	}
}

func TestOperationalCommandsRefuseAnEmptyDataDirectory(t *testing.T) {
	values := map[string]string{config.EnvDataDir: t.TempDir()}
	for _, args := range [][]string{{"check"}, {"export"}, {"owner", "reset", "Ada"}} {
		if code, _, stderr := command(t, values, args...); code != exitFailure || !strings.Contains(stderr, "secret") {
			t.Fatalf("%v: exit %d, %s", args, code, stderr)
		}
	}
	if code, _, stderr := command(t, values, "backup"); code != exitFailure || stderr == "" {
		t.Fatalf("backup of nothing: exit %d, %s", code, stderr)
	}
	entries, _ := os.ReadDir(values[config.EnvDataDir])
	for _, entry := range entries {
		if entry.Name() == secrets.DirName || entry.Name() == "sesame.db" {
			t.Fatalf("a read-only command created %s", entry.Name())
		}
	}
}
