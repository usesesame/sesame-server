package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"usesesame.app/backend/internal/selfhost/config"
	"usesesame.app/backend/internal/selfhost/ops"
)

func TestBackupStdoutStreamsAVerifiedArchive(t *testing.T) {
	inst := newRecoveryTestInstance(t, "Ada")
	writeStreamConfig(t, inst.dir, `{"logLevel":"info"}`)
	code, stdout, stderr := command(t, map[string]string{config.EnvDataDir: inst.dir}, "backup", "--stdout")
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	data := []byte(stdout)
	if len(data) == 0 {
		t.Fatal("the archive stream was empty")
	}
	if !strings.Contains(stderr, "Backup streamed to standard output") {
		t.Fatalf("stderr = %q", stderr)
	}
	work := streamWork(t)
	report, err := ops.CheckReader(context.Background(), bytes.NewReader(data), ops.WithWorkDir(work))
	if err != nil {
		t.Fatalf("the stream was not a complete archive: %v", err)
	}
	if !report.SecretsVerified || !report.HasConfig || report.AuditRows < 1 {
		t.Fatalf("report = %+v", report)
	}
	assertNoStreamWork(t, inst.dir)
}

func TestBackupStdoutWritesNothingWhenTheSourceFails(t *testing.T) {
	dir := t.TempDir()
	code, stdout, stderr := command(t, map[string]string{config.EnvDataDir: dir}, "backup", "--stdout")
	if code != exitFailure {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if stdout != "" {
		t.Fatalf("a failed backup wrote %d archive bytes", len(stdout))
	}
	if !strings.Contains(stderr, "database") {
		t.Fatalf("stderr = %q", stderr)
	}
	assertNoStreamWork(t, dir)
}

func TestCheckStreamJSONMatchesFileJSON(t *testing.T) {
	inst := newRecoveryTestInstance(t, "Ada")
	writeStreamConfig(t, inst.dir, `{"logLevel":"info"}`)
	code, archive, stderr := command(t, map[string]string{config.EnvDataDir: inst.dir}, "backup", "--stdout")
	if code != exitOK {
		t.Fatalf("backup exit %d: %s", code, stderr)
	}
	file := filepath.Join(t.TempDir(), "backup.tar")
	if err := os.WriteFile(file, []byte(archive), 0o600); err != nil {
		t.Fatal(err)
	}
	code, fileJSON, stderr := command(t, map[string]string{config.EnvDataDir: inst.dir}, "check", file, "--json")
	if code != exitOK {
		t.Fatalf("check file exit %d: %s", code, stderr)
	}
	code, streamJSON, stderr := commandInput(t, map[string]string{config.EnvDataDir: inst.dir}, bytes.NewReader([]byte(archive)), "check", "--stdin", "--json")
	if code != exitOK {
		t.Fatalf("check stream exit %d: %s", code, stderr)
	}
	if fileJSON != streamJSON {
		t.Fatalf("file JSON = %s, stream JSON = %s", fileJSON, streamJSON)
	}
	fields := map[string]any{}
	if err := json.Unmarshal([]byte(streamJSON), &fields); err != nil {
		t.Fatal(err)
	}
	keys := []string{"schemaVersion", "supported", "needsMigration", "auditRows", "auditHeadHash", "ownersVerified", "secretsVerified", "hasConfig"}
	if len(fields) != len(keys) {
		t.Fatalf("JSON fields = %v", fields)
	}
	for _, key := range keys {
		if _, ok := fields[key]; !ok {
			t.Fatalf("JSON is missing %s: %s", key, streamJSON)
		}
	}
	schema := fields["schemaVersion"].(float64)
	supported := fields["supported"].(float64)
	if schema < 1 || supported < schema {
		t.Fatalf("schema = %v, supported = %v", schema, supported)
	}
	if fields["needsMigration"].(bool) {
		t.Fatal("the current archive needs a migration")
	}
	if fields["auditRows"].(float64) < 1 {
		t.Fatalf("audit rows = %v", fields["auditRows"])
	}
	if len(fields["auditHeadHash"].(string)) != 64 {
		t.Fatalf("audit head hash = %v", fields["auditHeadHash"])
	}
	if fields["ownersVerified"].(float64) != 1 || !fields["secretsVerified"].(bool) || !fields["hasConfig"].(bool) {
		t.Fatalf("JSON = %s", streamJSON)
	}
}

func TestCheckStreamHumanOutputAndFailure(t *testing.T) {
	inst := newRecoveryTestInstance(t, "Ada")
	code, archive, stderr := command(t, map[string]string{config.EnvDataDir: inst.dir}, "backup", "--stdout")
	if code != exitOK {
		t.Fatalf("backup exit %d: %s", code, stderr)
	}
	code, stdout, stderr := commandInput(t, map[string]string{config.EnvDataDir: inst.dir}, bytes.NewReader([]byte(archive)), "check", "--stdin")
	if code != exitOK {
		t.Fatalf("check exit %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "The backup from standard input is intact.") {
		t.Fatalf("stdout = %q", stdout)
	}
	code, stdout, stderr = commandInput(t, map[string]string{config.EnvDataDir: inst.dir}, strings.NewReader("not an archive"), "check", "--stdin", "--json")
	if code != exitFailure {
		t.Fatalf("malformed stream exit %d", code)
	}
	if stdout != "" {
		t.Fatalf("a failed check printed %q", stdout)
	}
	if !strings.Contains(stderr, "did not pass") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestCheckStillUsesTheLiveDatabaseWithoutArguments(t *testing.T) {
	inst := newRecoveryTestInstance(t, "Ada")
	code, stdout, stderr := command(t, map[string]string{config.EnvDataDir: inst.dir}, "check")
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "passed its checks at schema") || strings.Contains(stdout, `"schemaVersion"`) {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestCheckStdinNeedsAnInput(t *testing.T) {
	stdout, stderr := &syncBuffer{}, &syncBuffer{}
	env := realEnvironment(map[string]string{config.EnvDataDir: t.TempDir()}, stdout, stderr)
	env.Stdin = nil
	if code := run(context.Background(), []string{"check", "--stdin"}, env); code != exitFailure {
		t.Fatalf("exit %d", code)
	}
	if stdout.String() != "" || !strings.Contains(stderr.String(), "needs standard input") {
		t.Fatalf("stdout = %q, stderr = %q", stdout.String(), stderr.String())
	}
}

func writeStreamConfig(t *testing.T, dir, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ops.ConfigName), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func streamWork(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "work")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func assertNoStreamWork(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".backup-stream-") || strings.HasPrefix(entry.Name(), ".check-stream-") {
			t.Fatalf("%s was left behind", entry.Name())
		}
	}
}
