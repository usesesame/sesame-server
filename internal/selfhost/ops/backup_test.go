package ops

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"usesesame.app/backend/internal/selfhost"
	"usesesame.app/backend/internal/selfhost/secrets"
	"usesesame.app/backend/internal/selfhost/sqlitestore"
)

func TestBackupWritesPrivateArchiveThatChecks(t *testing.T) {
	inst := newInstance(t, "Ada", true)
	out := filepath.Join(t.TempDir(), "nested", "backup.tar")
	manifest, err := Backup(context.Background(), inst.dir, out, WithVersion("9.9.9"))
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode is %o", info.Mode().Perm())
	}
	entries, err := os.ReadDir(filepath.Dir(out))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("backup directory holds %d entries, want only the archive", len(entries))
	}
	if manifest.ServerVersion != "9.9.9" || manifest.SchemaVersion < 1 || manifest.Format != FormatVersion {
		t.Fatalf("manifest = %+v", manifest)
	}
	if len(manifest.Members) != 5 {
		t.Fatalf("members = %d, want database, three secrets and config", len(manifest.Members))
	}
	report, err := Check(context.Background(), out)
	if err != nil {
		t.Fatal(err)
	}
	if !report.SecretsVerified || report.OwnersVerified != 1 || !report.HasConfig || report.AuditRows < 2 || report.NeedsMigration {
		t.Fatalf("report = %+v", report)
	}
}

func TestBackupRefusesToOverwrite(t *testing.T) {
	inst := newInstance(t, "Ada", false)
	out := inst.backup(t)
	before := fileDigest(t, out)
	_, err := Backup(context.Background(), inst.dir, out)
	if !errors.Is(err, ErrBackupExists) {
		t.Fatalf("err = %v", err)
	}
	if fileDigest(t, out) != before {
		t.Fatal("existing backup changed")
	}
	link := filepath.Join(filepath.Dir(out), "link.tar")
	if err := os.Symlink(out, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Backup(context.Background(), inst.dir, link); !errors.Is(err, ErrBackupExists) {
		t.Fatalf("symlink target err = %v", err)
	}
}

func TestBackupWithoutConfigOmitsMember(t *testing.T) {
	inst := newInstance(t, "Ada", false)
	report, err := Check(context.Background(), inst.backup(t))
	if err != nil {
		t.Fatal(err)
	}
	if report.HasConfig || len(report.Manifest.Members) != 4 {
		t.Fatalf("report = %+v", report)
	}
}

func TestBackupFailsWithoutSecretsAndLeavesNothing(t *testing.T) {
	inst := newInstance(t, "Ada", false)
	if err := os.Remove(filepath.Join(secrets.Dir(inst.dir), secrets.AdminKeyFile)); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "backup.tar")
	if _, err := Backup(context.Background(), inst.dir, out); err == nil {
		t.Fatal("backup without the encryption key succeeded")
	}
	entries, _ := os.ReadDir(filepath.Dir(out))
	if len(entries) != 0 {
		t.Fatalf("leftover entries: %v", entries)
	}
}

func TestBackupFailsOnMissingDatabase(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := secrets.LoadOrCreate(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := Backup(context.Background(), dir, filepath.Join(t.TempDir(), "b.tar")); err == nil {
		t.Fatal("backup of a missing database succeeded")
	}
}

func TestBackupRefusesSymlinkedSecret(t *testing.T) {
	inst := newInstance(t, "Ada", false)
	target := filepath.Join(t.TempDir(), "elsewhere")
	writeFile(t, target, bytes.Repeat([]byte{7}, 32), 0o600)
	name := filepath.Join(secrets.Dir(inst.dir), secrets.IPPepperFile)
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, name); err != nil {
		t.Fatal(err)
	}
	if _, err := Backup(context.Background(), inst.dir, filepath.Join(t.TempDir(), "b.tar")); err == nil {
		t.Fatal("backup followed a symlinked secret")
	}
}

func TestBackupOfDatabaseBeingWritten(t *testing.T) {
	for name, withStore := range map[string]bool{"standalone snapshot": false, "store snapshot": true} {
		t.Run(name, func(t *testing.T) {
			inst := newInstance(t, "Ada", false)
			ctx, cancel := context.WithCancel(context.Background())
			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; ctx.Err() == nil; i++ {
					_, _ = inst.store.AppendAudit(ctx, selfhost.AuditInput{Actor: inst.by(), Action: "test.write", Target: "load", Detail: map[string]string{"n": "x"}})
					if i%5 == 0 {
						_, _ = inst.store.SetFlag(ctx, inst.by(), "sync.preview", i%2 == 0)
					}
				}
			}()
			var options []Option
			if withStore {
				options = append(options, WithSnapshotter(inst.store))
			}
			var last Report
			for round := 0; round < 4; round++ {
				out := filepath.Join(t.TempDir(), "b.tar")
				if _, err := Backup(context.Background(), inst.dir, out, options...); err != nil {
					cancel()
					wg.Wait()
					t.Fatal(err)
				}
				report, err := Check(context.Background(), out)
				if err != nil {
					cancel()
					wg.Wait()
					t.Fatalf("round %d: %v", round, err)
				}
				if report.AuditRows < last.AuditRows {
					t.Fatalf("audit rows went from %d to %d", last.AuditRows, report.AuditRows)
				}
				last = report
			}
			cancel()
			wg.Wait()
			if last.AuditRows < 3 {
				t.Fatalf("writer never made progress, audit rows = %d", last.AuditRows)
			}
		})
	}
}

func TestRoundTripPreservesState(t *testing.T) {
	ctx := context.Background()
	inst := newInstance(t, "Ada", true)
	pairing, err := inst.store.CreatePairing(ctx, inst.by(), selfhost.PairingInput{Holder: selfhost.Holder{Kind: selfhost.HolderOwner, ID: inst.owner.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inst.store.RedeemPairing(ctx, selfhost.RedeemInput{Code: pairing.Code, DeviceName: "Laptop"}); err != nil {
		t.Fatal(err)
	}
	originalAudit, err := inst.store.VerifyAudit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	originalInstance, _ := inst.store.Instance(ctx)
	originalMembers, _ := inst.store.ListMembers(ctx)
	originalDevices, _ := inst.store.ListDevices(ctx, selfhost.DeviceFilter{IncludeInactive: true})

	archive := inst.backup(t)
	target := filepath.Join(t.TempDir(), "restored")
	result, err := Restore(ctx, archive, target)
	if err != nil {
		t.Fatal(err)
	}
	if result.AsideDir == "" {
		t.Fatal("aside directory not reported")
	}
	for _, name := range secrets.Files() {
		if !bytes.Equal(readFile(t, filepath.Join(secrets.Dir(inst.dir), name)), readFile(t, filepath.Join(secrets.Dir(target), name))) {
			t.Fatalf("secret %s differs", name)
		}
	}
	if !bytes.Equal(readFile(t, filepath.Join(inst.dir, ConfigName)), readFile(t, filepath.Join(target, ConfigName))) {
		t.Fatal("config differs")
	}
	assertMode(t, filepath.Join(target, DatabaseName), 0o600)
	assertMode(t, secrets.Dir(target), 0o700)
	assertMode(t, filepath.Join(secrets.Dir(target), secrets.AdminKeyFile), 0o600)

	loaded, err := secrets.Load(target)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := sqlitestore.Open(ctx, sqlitestore.Options{Path: filepath.Join(target, DatabaseName), AdminKey: loaded.AdminEncryptionKey})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	audit, err := restored.VerifyAudit(ctx)
	if err != nil || !audit.OK {
		t.Fatalf("audit = %+v, err = %v", audit, err)
	}
	if audit.Rows < originalAudit.Rows || audit.HeadSeq < originalAudit.HeadSeq {
		t.Fatalf("restored audit %+v lost rows from %+v", audit, originalAudit)
	}
	gotInstance, _ := restored.Instance(ctx)
	if gotInstance.ID != originalInstance.ID || gotInstance.Name != originalInstance.Name {
		t.Fatalf("instance = %+v, want %+v", gotInstance, originalInstance)
	}
	gotMembers, _ := restored.ListMembers(ctx)
	if len(gotMembers) != len(originalMembers) || gotMembers[0].ID != originalMembers[0].ID || gotMembers[0].Name != originalMembers[0].Name {
		t.Fatalf("members = %+v, want %+v", gotMembers, originalMembers)
	}
	gotDevices, _ := restored.ListDevices(ctx, selfhost.DeviceFilter{IncludeInactive: true})
	if len(gotDevices) != 1 || gotDevices[0].ID != originalDevices[0].ID || gotDevices[0].Name != "Laptop" {
		t.Fatalf("devices = %+v", gotDevices)
	}
	owners, _ := restored.ListOwners(ctx)
	if len(owners) != 1 || owners[0].ID != inst.owner.ID {
		t.Fatalf("owners = %+v", owners)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != want {
		t.Fatalf("%s mode = %o, want %o", path, info.Mode().Perm(), want)
	}
}

func TestInstanceLockIsExclusive(t *testing.T) {
	dir := t.TempDir()
	first, err := AcquireInstanceLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireInstanceLock(dir); !errors.Is(err, ErrInstanceRunning) {
		t.Fatalf("second lock err = %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	second, err := AcquireInstanceLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = second.Release()
}
