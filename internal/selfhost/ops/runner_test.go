package ops

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"usesesame.app/backend/internal/authkit"
	"usesesame.app/backend/internal/selfhost"
)

type fakeClock struct{ now time.Time }

func (c *fakeClock) read() time.Time { return c.now }
func (c *fakeClock) advance(d time.Duration) {
	c.now = c.now.Add(d)
}

func listNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func contains(names []string, name string) bool {
	for _, candidate := range names {
		if candidate == name {
			return true
		}
	}
	return false
}

func TestRotationKeepsNewestAndNeverDeletesForeignFiles(t *testing.T) {
	inst := newInstance(t, "Ada", false)
	clock := &fakeClock{now: time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)}
	var recorded []time.Time
	runner := &Runner{
		DataDir:  inst.dir,
		Interval: time.Hour,
		Keep:     3,
		Options:  []Option{WithClock(clock.read)},
		Recorder: func(_ context.Context, at time.Time) error { recorded = append(recorded, at); return nil },
	}
	dir := BackupDir(inst.dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	foreign := map[string][]byte{
		"notes.txt":                           []byte("keep me"),
		"sesame-backup-20200101T000000Z.tar":  []byte("looks like ours but is not an archive"),
		"sesame-backup-20200102T000000Z.tar2": []byte("wrong suffix"),
		"pre-migration-v1-1700000000.db":      []byte("written by the store"),
		"other-backup-20200101T000000Z.tar":   []byte("other tool"),
	}
	for name, content := range foreign {
		writeFile(t, filepath.Join(dir, name), content, 0o600)
	}
	outside := filepath.Join(t.TempDir(), "outside.tar")
	other := inst.backup(t)
	if err := os.Rename(other, outside); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "sesame-backup-20200103T000000Z.tar")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sesame-backup-20200104T000000Z.tar"), 0o700); err != nil {
		t.Fatal(err)
	}
	validForeignName := filepath.Join(dir, "sesame-backup-20190101T000000Z.tar")
	data := readFile(t, outside)
	writeFile(t, validForeignName, data, 0o600)

	var paths []string
	for i := 0; i < 6; i++ {
		path, err := runner.RunOnce(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
		clock.advance(time.Hour)
	}
	if len(recorded) != 6 || !recorded[5].Equal(time.Date(2026, 10, 1, 13, 0, 0, 0, time.UTC)) {
		t.Fatalf("recorded = %v", recorded)
	}
	names := listNames(t, dir)
	for _, newest := range paths[3:] {
		if !contains(names, filepath.Base(newest)) {
			t.Fatalf("newest backup %s was removed; have %v", newest, names)
		}
	}
	for _, old := range paths[:3] {
		if contains(names, filepath.Base(old)) {
			t.Fatalf("old backup %s was kept; have %v", old, names)
		}
	}
	if contains(names, filepath.Base(validForeignName)) {
		t.Fatal("an older genuine backup beyond the keep count survived")
	}
	for name, content := range foreign {
		if !bytes.Equal(readFile(t, filepath.Join(dir, name)), content) {
			t.Fatalf("foreign file %s was removed or changed", name)
		}
	}
	if info, err := os.Lstat(filepath.Join(dir, "sesame-backup-20200103T000000Z.tar")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink named like a backup was removed")
	}
	if info, err := os.Lstat(filepath.Join(dir, "sesame-backup-20200104T000000Z.tar")); err != nil || !info.IsDir() {
		t.Fatal("directory named like a backup was removed")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatal("symlink target was removed")
	}
}

func TestRotateRejectsInvalidKeepAndMissingDirectory(t *testing.T) {
	if _, err := Rotate(t.TempDir(), 0); err == nil {
		t.Fatal("keep 0 accepted")
	}
	removed, err := Rotate(filepath.Join(t.TempDir(), "missing"), 3)
	if err != nil || len(removed) != 0 {
		t.Fatalf("removed = %v, err = %v", removed, err)
	}
	if _, err := newConfig([]Option{WithKeep(-1)}); err == nil {
		t.Fatal("negative keep accepted")
	}
}

func TestRunnerSameSecondBackupsDoNotCollide(t *testing.T) {
	inst := newInstance(t, "Ada", false)
	clock := &fakeClock{now: time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)}
	runner := &Runner{DataDir: inst.dir, Interval: time.Hour, Keep: 5, Options: []Option{WithClock(clock.read)}}
	first, err := runner.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := runner.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("second backup reused the first name")
	}
	if _, err := Check(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if _, err := Check(context.Background(), second); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerRecorderFailureDoesNotLoseBackup(t *testing.T) {
	inst := newInstance(t, "Ada", false)
	failing := errors.New("record failed")
	runner := &Runner{DataDir: inst.dir, Interval: time.Hour, Recorder: func(context.Context, time.Time) error { return failing }}
	path, err := runner.RunOnce(context.Background())
	if !errors.Is(err, failing) {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatal("backup missing")
	}
}

func TestRunnerWaitsUntilDue(t *testing.T) {
	inst := newInstance(t, "Ada", false)
	clock := &fakeClock{now: time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)}
	runner := &Runner{DataDir: inst.dir, Interval: 24 * time.Hour, Options: []Option{WithClock(clock.read)}}
	cfg, err := newConfig(runner.options())
	if err != nil {
		t.Fatal(err)
	}
	if wait := runner.untilDue(cfg); wait != 0 {
		t.Fatalf("wait with no backups = %v", wait)
	}
	if _, err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	clock.advance(6 * time.Hour)
	if wait := runner.untilDue(cfg); wait != 18*time.Hour {
		t.Fatalf("wait = %v, want 18h", wait)
	}
	clock.advance(30 * time.Hour)
	if wait := runner.untilDue(cfg); wait != 0 {
		t.Fatalf("overdue wait = %v", wait)
	}
}

func TestRunnerRunsOnScheduleAndStopsOnCancel(t *testing.T) {
	inst := newInstance(t, "Ada", false)
	var recorded atomic.Int32
	runner := &Runner{
		DataDir:  inst.dir,
		Interval: 100 * time.Millisecond,
		Keep:     2,
		Recorder: func(context.Context, time.Time) error { recorded.Add(1); return nil },
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	deadline := time.After(20 * time.Second)
	for recorded.Load() < 3 {
		select {
		case <-deadline:
			cancel()
			t.Fatal("scheduled backups did not run")
		case <-time.After(50 * time.Millisecond):
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := len(listNames(t, BackupDir(inst.dir))); got > 2 {
		t.Fatalf("retention left %d backups", got)
	}
}

func TestRunnerDisabledWithZeroInterval(t *testing.T) {
	inst := newInstance(t, "Ada", false)
	runner := &Runner{DataDir: inst.dir, Interval: 0}
	if err := runner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(BackupDir(inst.dir)); err == nil {
		t.Fatal("disabled runner created the backup directory")
	}
}

func TestPreMigrationBacksUpOnlyWhenSchemaIsOlder(t *testing.T) {
	inst := newInstance(t, "Ada", true)
	if err := inst.store.Close(); err != nil {
		t.Fatal(err)
	}
	path, err := PreMigration(context.Background(), inst.dir)
	if err != nil || path != "" {
		t.Fatalf("current schema: path = %q, err = %v", path, err)
	}
	if _, err := os.Stat(BackupDir(inst.dir)); err == nil {
		t.Fatal("backup directory created without a migration")
	}
	empty := t.TempDir()
	if path, err := PreMigration(context.Background(), empty); err != nil || path != "" {
		t.Fatalf("fresh directory: path = %q, err = %v", path, err)
	}

	current := currentSchema(t)
	setSupportedForTest(t, current+1)
	clock := &fakeClock{now: time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)}
	path, err = PreMigration(context.Background(), inst.dir, WithClock(clock.read), WithVersion("1.2.3"), WithKeep(2))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != fmt.Sprintf("sesame-premigration-v%d-20261001T080000Z.tar", current) {
		t.Fatalf("path = %s", path)
	}
	report, err := Check(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if report.Manifest.ServerVersion != "1.2.3" || report.SchemaVersion != current || !report.HasConfig {
		t.Fatalf("report = %+v", report)
	}
	for i := 0; i < 3; i++ {
		clock.advance(time.Hour)
		if _, err := PreMigration(context.Background(), inst.dir, WithClock(clock.read), WithKeep(2)); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(listNames(t, BackupDir(inst.dir))); got != 2 {
		t.Fatalf("pre-migration retention kept %d", got)
	}
}

func TestPreMigrationFailsWhenSecretsAreMissing(t *testing.T) {
	inst := newInstance(t, "Ada", false)
	if err := inst.store.Close(); err != nil {
		t.Fatal(err)
	}
	setSupportedForTest(t, currentSchema(t)+1)
	if err := os.RemoveAll(filepath.Join(inst.dir, "secrets")); err != nil {
		t.Fatal(err)
	}
	if path, err := PreMigration(context.Background(), inst.dir); err == nil || path != "" {
		t.Fatalf("path = %q, err = %v", path, err)
	}
	if names := listNames(t, BackupDir(inst.dir)); len(names) != 0 {
		t.Fatalf("partial backup left behind: %v", names)
	}
}

func TestExportListsMembersDevicesAndAuditWithoutTokens(t *testing.T) {
	ctx := context.Background()
	inst := newInstance(t, "Ada", false)
	pairing, err := inst.store.CreatePairing(ctx, inst.by(), selfhost.PairingInput{Holder: selfhost.Holder{Kind: selfhost.HolderOwner, ID: inst.owner.ID}})
	if err != nil {
		t.Fatal(err)
	}
	issued, err := inst.store.RedeemPairing(ctx, selfhost.RedeemInput{Code: pairing.Code, DeviceName: "Laptop", Meta: selfhost.DeviceMeta{AppVersion: "1.0.0", Platform: "linux"}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := inst.store.CreatePairing(ctx, inst.by(), selfhost.PairingInput{Holder: selfhost.Holder{Kind: selfhost.HolderOwner, ID: inst.owner.ID}})
	if err != nil {
		t.Fatal(err)
	}
	revoked, err := inst.store.RedeemPairing(ctx, selfhost.RedeemInput{Code: second.Code, DeviceName: "Old phone"})
	if err != nil {
		t.Fatal(err)
	}
	if err := inst.store.RevokeDevice(ctx, inst.by(), revoked.Device.ID); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := Export(ctx, inst.store, &out, WithClock(func() time.Time { return time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC) })); err != nil {
		t.Fatal(err)
	}
	var document struct {
		Format     string `json:"format"`
		ExportedAt string `json:"exportedAt"`
		Members    []struct {
			Name string `json:"name"`
		} `json:"members"`
		Devices []struct {
			ID        string  `json:"id"`
			Name      string  `json:"name"`
			RevokedAt *string `json:"revokedAt"`
			Holder    struct {
				Name string `json:"name"`
			} `json:"holder"`
		} `json:"devices"`
		AuditChain struct {
			OK   bool  `json:"ok"`
			Rows int64 `json:"rows"`
		} `json:"auditChain"`
		Audit []struct {
			Seq    int64  `json:"seq"`
			Action string `json:"action"`
			Hash   string `json:"hash"`
		} `json:"audit"`
	}
	if err := json.Unmarshal(out.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if document.Format != ExportFormat || document.ExportedAt != "2026-10-01T08:00:00Z" {
		t.Fatalf("header = %+v", document)
	}
	if len(document.Members) != 1 || document.Members[0].Name != "Grace" {
		t.Fatalf("members = %+v", document.Members)
	}
	if len(document.Devices) != 2 {
		t.Fatalf("devices = %+v", document.Devices)
	}
	revokedSeen := false
	for _, device := range document.Devices {
		if device.Name == "Old phone" && device.RevokedAt != nil {
			revokedSeen = true
		}
		if device.Holder.Name != "Ada" {
			t.Fatalf("holder = %+v", device.Holder)
		}
	}
	if !revokedSeen {
		t.Fatal("revoked device missing from export")
	}
	if !document.AuditChain.OK || int64(len(document.Audit)) != document.AuditChain.Rows {
		t.Fatalf("audit chain = %+v with %d entries", document.AuditChain, len(document.Audit))
	}
	for index, entry := range document.Audit {
		if entry.Seq != int64(index+1) {
			t.Fatalf("audit entry %d has seq %d", index, entry.Seq)
		}
	}
	text := out.String()
	for _, token := range []string{issued.Token, revoked.Token, pairing.Code, second.Code} {
		hash := authkit.HashToken(token)
		for _, secret := range []string{token, hex.EncodeToString(hash), base64.StdEncoding.EncodeToString(hash), base64.RawURLEncoding.EncodeToString(hash), base64.URLEncoding.EncodeToString(hash)} {
			if bytes.Contains([]byte(text), []byte(secret)) {
				t.Fatalf("export leaks token material %q", secret[:8])
			}
		}
	}
	for _, forbidden := range []string{"tokenHash", "token_hash", "passwordHash", "totp"} {
		if bytes.Contains([]byte(text), []byte(forbidden)) {
			t.Fatalf("export mentions %s", forbidden)
		}
	}
}

func TestExportPagesThroughLongAuditLog(t *testing.T) {
	ctx := context.Background()
	inst := newInstance(t, "Ada", false)
	for i := 0; i < 450; i++ {
		if _, err := inst.store.AppendAudit(ctx, selfhost.AuditInput{Actor: inst.by(), Action: "test.bulk", Target: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	if err := Export(ctx, inst.store, &out); err != nil {
		t.Fatal(err)
	}
	var document struct {
		Audit []struct {
			Seq int64 `json:"seq"`
		} `json:"audit"`
		AuditChain struct {
			Rows int64 `json:"rows"`
		} `json:"auditChain"`
	}
	if err := json.Unmarshal(out.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if int64(len(document.Audit)) != document.AuditChain.Rows || len(document.Audit) < 450 {
		t.Fatalf("exported %d of %d rows", len(document.Audit), document.AuditChain.Rows)
	}
	for index, entry := range document.Audit {
		if entry.Seq != int64(index+1) {
			t.Fatalf("entry %d has seq %d", index, entry.Seq)
		}
	}
}

func TestExportFailsOnClosedStore(t *testing.T) {
	inst := newInstance(t, "Ada", false)
	_ = inst.store.Close()
	if err := Export(context.Background(), inst.store, &bytes.Buffer{}); !errors.Is(err, selfhost.ErrClosed) {
		t.Fatalf("err = %v", err)
	}
}
