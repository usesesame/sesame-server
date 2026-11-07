package ops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"usesesame.app/backend/internal/authkit"
	"usesesame.app/backend/internal/selfhost"
	"usesesame.app/backend/internal/selfhost/secrets"
	"usesesame.app/backend/internal/selfhost/sqlitestore"
)

func TestRestorePreparationUsesConfiguredClock(t *testing.T) {
	oldInst, _, archive, _, _ := restoreFixture(t)
	when := time.Date(2030, 4, 15, 12, 0, 0, 0, time.UTC)
	if _, err := Restore(context.Background(), archive, oldInst.dir, WithClock(func() time.Time { return when })); err != nil {
		t.Fatal(err)
	}
	loaded, err := secrets.Load(oldInst.dir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := sqlitestore.Open(context.Background(), sqlitestore.Options{
		Path: filepath.Join(oldInst.dir, DatabaseName), AdminKey: loaded.AdminEncryptionKey, ReadOnly: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	status, err := store.RecoveryStatus(context.Background())
	if err != nil || !status.StartedAt.Equal(when) {
		t.Fatalf("restore ignored its configured clock: %+v, %v", status, err)
	}
}

func TestRestoreInvalidatesSessions(t *testing.T) {
	ctx := context.Background()
	inst := newInstance(t, "Ada", false)
	issue, err := inst.store.ResetOwner(ctx, selfhost.SystemActor, "Ada")
	if err != nil {
		t.Fatal(err)
	}
	details, err := inst.store.SetupDetails(ctx, issue.Token)
	if err != nil {
		t.Fatal(err)
	}
	code, ok := authkit.TOTPCode(details.TOTPSecret, time.Now())
	if !ok {
		t.Fatal("totp code")
	}
	login, err := inst.store.CompleteSetup(ctx, selfhost.CompleteSetupInput{
		Token:    issue.Token,
		Name:     "Ada",
		Password: "a different fictional passphrase 43",
		Code:     code,
	})
	if err != nil {
		t.Fatal(err)
	}
	archive := inst.backup(t)
	target := filepath.Join(t.TempDir(), "restored")
	if _, err := Restore(ctx, archive, target); err != nil {
		t.Fatal(err)
	}
	loaded, err := secrets.Load(target)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := sqlitestore.Open(ctx, sqlitestore.Options{Path: filepath.Join(target, DatabaseName), AdminKey: loaded.AdminEncryptionKey})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if _, err := restored.Session(ctx, login.Token); !errors.Is(err, selfhost.ErrSessionInvalid) {
		t.Fatalf("a session from before the restore is still valid: %v", err)
	}
}

func TestRestoreWritesPendingRecoveryAndInstalledSchema(t *testing.T) {
	oldInst, _, archive, oldState, newState := restoreFixture(t)
	result, err := Restore(context.Background(), archive, oldInst.dir)
	if err != nil {
		t.Fatal(err)
	}
	if result.Report.SchemaVersion != currentSchema(t) || result.InstalledSchema != currentSchema(t) {
		t.Fatalf("schema report = %d, installed = %d, want %d", result.Report.SchemaVersion, result.InstalledSchema, currentSchema(t))
	}
	assertRestored(t, oldInst.dir, newState, "restored")
	assertSnapshot(t, result.AsideDir, oldState, "previous data")
	loaded, err := secrets.Load(oldInst.dir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := sqlitestore.Open(context.Background(), sqlitestore.Options{
		Path:     filepath.Join(oldInst.dir, DatabaseName),
		AdminKey: loaded.AdminEncryptionKey,
		ReadOnly: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	status, err := store.RecoveryStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.Pending || len(status.Generation) != 32 || status.StartedAt.IsZero() || status.CompletedAt != nil {
		t.Fatalf("recovery status = %+v", status)
	}
	owners, err := store.ListOwners(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(owners) != 1 || owners[0].ID == "" {
		t.Fatalf("owners = %+v", owners)
	}
}

func TestRestoreFromOlderSchemaInstallsCurrentSchema(t *testing.T) {
	ctx := context.Background()
	inst := newInstance(t, "Ada", false)
	if err := inst.store.Close(); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(inst.dir, DatabaseName)
	mustSQL(t, database, `DROP TABLE recovery`)
	mustSQL(t, database, `DELETE FROM schema_migrations WHERE version = ?`, currentSchema(t))
	archive := inst.backup(t)
	target := filepath.Join(t.TempDir(), "restored")
	result, err := Restore(ctx, archive, target)
	if err != nil {
		t.Fatal(err)
	}
	if result.Report.SchemaVersion != currentSchema(t)-1 {
		t.Fatalf("archive schema = %d, want %d", result.Report.SchemaVersion, currentSchema(t)-1)
	}
	if result.InstalledSchema != currentSchema(t) {
		t.Fatalf("installed schema = %d, want %d", result.InstalledSchema, currentSchema(t))
	}
	loaded, err := secrets.Load(target)
	if err != nil {
		t.Fatal(err)
	}
	store, err := sqlitestore.Open(ctx, sqlitestore.Options{
		Path:     filepath.Join(target, DatabaseName),
		AdminKey: loaded.AdminEncryptionKey,
		ReadOnly: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	status, err := store.RecoveryStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Pending {
		t.Fatalf("recovery status = %+v", status)
	}
}

func TestRestorePreparationFailureLeavesLiveDataUntouched(t *testing.T) {
	oldInst, newInst, _, oldState, _ := restoreFixture(t)
	archive := newInst.backup(t)
	u := unpack(t, archive)
	mustSQL(t, u.path(DatabaseName), `DELETE FROM instance`)
	broken := u.repack(t, nil)
	_, err := Restore(context.Background(), broken, oldInst.dir)
	if !errors.Is(err, selfhost.ErrRecoveryStateInvalid) {
		t.Fatalf("err = %v, want invalid recovery state", err)
	}
	assertSnapshot(t, oldInst.dir, oldState, "after refused preparation")
	assertCleanDataDir(t, oldInst.dir)
	entries, _ := os.ReadDir(oldInst.dir)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), asidePrefix) {
			t.Fatalf("refused restore created %s", entry.Name())
		}
	}
}

func TestRestoreInvalidatesEnrollment(t *testing.T) {
	ctx := context.Background()
	inst := newInstance(t, "Ada", false)
	invite, err := inst.store.InviteOwner(ctx, inst.by(), "Bea")
	if err != nil {
		t.Fatal(err)
	}
	pairing, err := inst.store.CreatePairing(ctx, inst.by(), selfhost.PairingInput{
		Holder:         selfhost.Holder{Kind: selfhost.HolderOwner, ID: inst.owner.ID},
		DeviceNameHint: "Laptop",
	})
	if err != nil {
		t.Fatal(err)
	}
	archive := inst.backup(t)
	target := filepath.Join(t.TempDir(), "restored")
	if _, err := Restore(ctx, archive, target); err != nil {
		t.Fatal(err)
	}
	loaded, err := secrets.Load(target)
	if err != nil {
		t.Fatal(err)
	}
	store, err := sqlitestore.Open(ctx, sqlitestore.Options{
		Path:     filepath.Join(target, DatabaseName),
		AdminKey: loaded.AdminEncryptionKey,
		Preserve: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.SetupDetails(ctx, invite.Token); !errors.Is(err, selfhost.ErrSetupTokenInvalid) {
		t.Fatalf("restored setup token err = %v", err)
	}
	if _, err := store.RedeemPairing(ctx, selfhost.RedeemInput{Code: pairing.Code, DeviceName: "Laptop"}); !errors.Is(err, selfhost.ErrPairingInvalid) {
		t.Fatalf("restored pairing err = %v", err)
	}
	pairings, err := store.ListPairings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pairings) != 0 {
		t.Fatalf("pairings = %+v", pairings)
	}
}
