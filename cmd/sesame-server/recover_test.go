package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"usesesame.app/backend/internal/authkit"
	"usesesame.app/backend/internal/selfhost"
	"usesesame.app/backend/internal/selfhost/config"
	"usesesame.app/backend/internal/selfhost/ops"
	"usesesame.app/backend/internal/selfhost/secrets"
	"usesesame.app/backend/internal/selfhost/server"
	"usesesame.app/backend/internal/selfhost/sqlitestore"
)

const recoveryTestPassword = "a fictional passphrase 42"

type recoveryTestInstance struct {
	dir   string
	store *sqlitestore.Store
	owner selfhost.Owner
	login selfhost.OwnerLogin
}

func openRecoveryTestStore(t *testing.T, dir string, adjust func(*sqlitestore.Options)) *sqlitestore.Store {
	t.Helper()
	loaded, _, err := secrets.LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	options := sqlitestore.Options{
		Path:     filepath.Join(dir, ops.DatabaseName),
		AdminKey: loaded.AdminEncryptionKey,
		Flags:    server.FlagDefinitions(),
	}
	if adjust != nil {
		adjust(&options)
	}
	store, err := sqlitestore.Open(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func newRecoveryTestInstance(t *testing.T, ownerName string) *recoveryTestInstance {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	store := openRecoveryTestStore(t, dir, nil)
	issue, err := store.StartFirstSetup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	login := completeRecoverySetup(t, store, issue, ownerName, recoveryTestPassword)
	return &recoveryTestInstance{dir: dir, store: store, owner: login.Session.Owner, login: login}
}

func newOwnerlessRecoveryTestInstance(t *testing.T) *recoveryTestInstance {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	store := openRecoveryTestStore(t, dir, nil)
	return &recoveryTestInstance{dir: dir, store: store}
}

func completeRecoverySetup(t *testing.T, store *sqlitestore.Store, issue selfhost.SetupIssue, name, password string) selfhost.OwnerLogin {
	t.Helper()
	ctx := context.Background()
	details, err := store.SetupDetails(ctx, issue.Token)
	if err != nil {
		t.Fatal(err)
	}
	code, ok := authkit.TOTPCode(details.TOTPSecret, time.Now())
	if !ok {
		t.Fatal("totp code")
	}
	login, err := store.CompleteSetup(ctx, selfhost.CompleteSetupInput{Token: issue.Token, Name: name, Password: password, Code: code})
	if err != nil {
		t.Fatal(err)
	}
	return login
}

func (i *recoveryTestInstance) actor() selfhost.Actor {
	return selfhost.Actor{Kind: selfhost.ActorOwner, ID: i.owner.ID}
}

func (i *recoveryTestInstance) addOwner(t *testing.T, name string) selfhost.OwnerLogin {
	t.Helper()
	issue, err := i.store.InviteOwner(context.Background(), i.actor(), name)
	if err != nil {
		t.Fatal(err)
	}
	return completeRecoverySetup(t, i.store, issue, name, "another fictional passphrase 43")
}

func (i *recoveryTestInstance) pairDevice(t *testing.T, name string) selfhost.IssuedDevice {
	t.Helper()
	ctx := context.Background()
	pairing, err := i.store.CreatePairing(ctx, i.actor(), selfhost.PairingInput{
		Holder:         selfhost.Holder{Kind: selfhost.HolderOwner, ID: i.owner.ID},
		DeviceNameHint: name,
	})
	if err != nil {
		t.Fatal(err)
	}
	issued, err := i.store.RedeemPairing(ctx, selfhost.RedeemInput{Code: pairing.Code, DeviceName: name})
	if err != nil {
		t.Fatal(err)
	}
	return issued
}

func (i *recoveryTestInstance) backup(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "backup.tar")
	if _, err := ops.Backup(context.Background(), i.dir, path); err != nil {
		t.Fatal(err)
	}
	return path
}

func restoreRecoveryArchive(t *testing.T, archive string) string {
	t.Helper()
	target := filepath.Join(t.TempDir(), "restored")
	if _, err := ops.Restore(context.Background(), archive, target); err != nil {
		t.Fatal(err)
	}
	return target
}

func recoveryValues(dir string) map[string]string {
	return map[string]string{config.EnvDataDir: dir}
}

func fetchRecoveryStatus(t *testing.T, dir string) map[string]any {
	t.Helper()
	code, out, stderr := command(t, recoveryValues(dir), "recover", "--status", "--json")
	if code != exitOK {
		t.Fatalf("recover status: exit %d, %s %s", code, out, stderr)
	}
	var document map[string]any
	if err := json.Unmarshal([]byte(out), &document); err != nil {
		t.Fatalf("recover status is not JSON: %v: %s", err, out)
	}
	return document
}

func approveRecovery(t *testing.T, dir string, args ...string) (int, string, string) {
	t.Helper()
	all := append([]string{"recover"}, args...)
	return command(t, recoveryValues(dir), all...)
}

func rawSQL(t *testing.T, path string, fn func(*sql.DB)) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	fn(db)
}

func mustExec(t *testing.T, db *sql.DB, statement string, args ...any) {
	t.Helper()
	if _, err := db.Exec(statement, args...); err != nil {
		t.Fatalf("exec %q: %v", statement, err)
	}
}

func mustValue(t *testing.T, db *sql.DB, statement string, args ...any) string {
	t.Helper()
	var value sql.NullString
	if err := db.QueryRow(statement, args...).Scan(&value); err != nil {
		t.Fatalf("query %q: %v", statement, err)
	}
	return value.String
}

func jsonKeys(value map[string]any) string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

func TestParseRecoverCommand(t *testing.T) {
	generation := strings.Repeat("ab", 16)
	cases := []struct {
		name    string
		args    []string
		want    string
		wantErr string
	}{
		{name: "bare recover is status", args: []string{"recover"}, want: "recover"},
		{name: "status", args: []string{"recover", "--status"}, want: "recover"},
		{name: "status json", args: []string{"recover", "--status", "--json"}, want: "recover"},
		{name: "approval", args: []string{"recover", "--confirm", generation, "--retain-owner", "one", "--retain-owner", "two", "--retain-device", "three"}, want: "recover"},
		{name: "retention needs confirm", args: []string{"recover", "--retain-owner", "one"}, wantErr: "need --confirm"},
		{name: "status and confirm conflict", args: []string{"recover", "--status", "--confirm", generation}, wantErr: "cannot be combined"},
		{name: "generation must be lower hex", args: []string{"recover", "--confirm", "ABCDEF"}, wantErr: "32-character lowercase hexadecimal"},
		{name: "empty generation", args: []string{"recover", "--confirm", ""}, wantErr: "32-character lowercase hexadecimal"},
		{name: "status and empty confirm conflict", args: []string{"recover", "--status", "--confirm", ""}, wantErr: "cannot be combined"},
		{name: "duplicate owner", args: []string{"recover", "--confirm", generation, "--retain-owner", "one", "--retain-owner", "one"}, wantErr: "selected twice"},
		{name: "empty owner", args: []string{"recover", "--confirm", generation, "--retain-owner", ""}, wantErr: "ID is empty"},
		{name: "unexpected argument", args: []string{"recover", "extra"}, wantErr: "takes no arguments"},
		{name: "unknown flag", args: []string{"recover", "--bogus"}, wantErr: "recover:"},
		{name: "missing value", args: []string{"recover", "--confirm"}, wantErr: "needs an argument"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseCommand(tc.args)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.name != tc.want {
				t.Fatalf("parsed = %+v, want %s", got, tc.want)
			}
		})
	}
}

func TestRecoverStatusMissingDatabaseDoesNotCreateIt(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	code, out, stderr := command(t, recoveryValues(missing), "recover", "--status", "--json")
	if code != exitOK {
		t.Fatalf("exit %d, %s %s", code, out, stderr)
	}
	var document map[string]any
	if err := json.Unmarshal([]byte(out), &document); err != nil {
		t.Fatal(err)
	}
	if document["pending"] != false || document["generation"] != "" {
		t.Fatalf("document = %v", document)
	}
	if got := jsonKeys(document); got != "completedAt,devices,generation,owners,pending,startedAt" {
		t.Fatalf("fields = %s", got)
	}
	if _, err := os.Lstat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("status created %s", missing)
	}

	empty := t.TempDir()
	code, out, stderr = command(t, recoveryValues(empty), "recover")
	if code != exitOK || !strings.Contains(out, "No recovery approval is pending") {
		t.Fatalf("exit %d, %s %s", code, out, stderr)
	}
	if _, err := os.Lstat(filepath.Join(empty, ops.DatabaseName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("status created a database")
	}
	if _, err := os.Lstat(secrets.Dir(empty)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("status created instance secrets")
	}
	entries, err := os.ReadDir(empty)
	if err != nil || len(entries) != 0 {
		t.Fatalf("status changed an empty directory: %v, %v", entries, err)
	}
}

func TestRestorePrintsInstalledSchemaAndRecoveryHint(t *testing.T) {
	inst := newRecoveryTestInstance(t, "Ada")
	info, err := inst.store.System(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	archive := inst.backup(t)
	target := filepath.Join(t.TempDir(), "restored")
	code, out, stderr := command(t, recoveryValues(target), "restore", archive)
	if code != exitOK {
		t.Fatalf("exit %d, %s %s", code, out, stderr)
	}
	if !strings.Contains(out, fmt.Sprintf("installed schema %d", info.SchemaVersion)) {
		t.Fatalf("output = %q", out)
	}
	if !strings.Contains(out, "waiting for local recovery approval") {
		t.Fatalf("output = %q", out)
	}
}

func TestRecoverStatusRefusesOlderSchemaWithoutMigrating(t *testing.T) {
	inst := newRecoveryTestInstance(t, "Ada")
	if err := inst.store.Close(); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(inst.dir, ops.DatabaseName)
	rawSQL(t, database, func(db *sql.DB) {
		mustExec(t, db, `DROP TABLE recovery`)
		mustExec(t, db, `DELETE FROM schema_migrations WHERE version = 6`)
	})
	code, out, stderr := command(t, recoveryValues(inst.dir), "recover", "--status", "--json")
	if code != exitFailure || (!strings.Contains(stderr, "older than") && !strings.Contains(stderr, "needs a migration")) {
		t.Fatalf("exit %d, %s %s", code, out, stderr)
	}
	code, out, stderr = command(t, recoveryValues(inst.dir), "recover", "--confirm", strings.Repeat("a", 32), "--retain-owner", "someone")
	if code != exitFailure || (!strings.Contains(stderr, "older than") && !strings.Contains(stderr, "needs a migration")) {
		t.Fatalf("approval preflight: exit %d, %s %s", code, out, stderr)
	}
	rawSQL(t, database, func(db *sql.DB) {
		if got := mustValue(t, db, `SELECT MAX(version) FROM schema_migrations`); got != "5" {
			t.Fatalf("schema version = %s", got)
		}
		if got := mustValue(t, db, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'recovery'`); got != "0" {
			t.Fatalf("recovery table count = %s", got)
		}
	})
}

func TestRecoverStatusPrintsPendingStateAndExactJSON(t *testing.T) {
	inst := newRecoveryTestInstance(t, "Ada")
	device := inst.pairDevice(t, "Laptop")
	target := restoreRecoveryArchive(t, inst.backup(t))
	document := fetchRecoveryStatus(t, target)
	if document["pending"] != true {
		t.Fatalf("document = %v", document)
	}
	generation, _ := document["generation"].(string)
	if !validRecoveryGeneration(generation) {
		t.Fatalf("generation = %q", generation)
	}
	if document["startedAt"] == nil || document["completedAt"] != nil {
		t.Fatalf("times = %v", document)
	}
	owners, _ := document["owners"].([]any)
	if len(owners) != 1 {
		t.Fatalf("owners = %v", owners)
	}
	owner, _ := owners[0].(map[string]any)
	if got := jsonKeys(owner); got != "id,lastLoginAt,name,setupPending" {
		t.Fatalf("owner fields = %s", got)
	}
	if owner["id"] != inst.owner.ID || owner["name"] != "Ada" || owner["setupPending"] != false {
		t.Fatalf("owner = %v", owner)
	}
	devices, _ := document["devices"].([]any)
	if len(devices) != 1 {
		t.Fatalf("devices = %v", devices)
	}
	gotDevice, _ := devices[0].(map[string]any)
	if got := jsonKeys(gotDevice); got != "expiresAt,holder,id,lastSeenAt,name,revokedAt" {
		t.Fatalf("device fields = %s", got)
	}
	holder, _ := gotDevice["holder"].(map[string]any)
	if got := jsonKeys(holder); got != "id,kind,name" {
		t.Fatalf("holder fields = %s", got)
	}
	if gotDevice["id"] != device.Device.ID || holder["kind"] != "owner" || holder["id"] != inst.owner.ID || holder["name"] != "Ada" {
		t.Fatalf("device = %v", gotDevice)
	}
	code, out, stderr := command(t, recoveryValues(target), "recover", "--status")
	if code != exitOK {
		t.Fatalf("exit %d, %s %s", code, out, stderr)
	}
	if !strings.Contains(out, "Recovery is pending for generation "+generation) || !strings.Contains(out, `"Laptop"`) {
		t.Fatalf("human output = %q", out)
	}
	combined := out + stderr
	for _, secret := range []string{device.Token, inst.login.Token} {
		if strings.Contains(combined, secret) {
			t.Fatal("status output contains a token")
		}
	}
}

func TestRecoverApprovalRetainsSelectedStateAndRevokesOthers(t *testing.T) {
	ctx := context.Background()
	inst := newRecoveryTestInstance(t, "Ada")
	deviceA := inst.pairDevice(t, "Laptop A")
	deviceB := inst.pairDevice(t, "Laptop B")
	ownerB := inst.addOwner(t, "Bea")
	rawSQL(t, filepath.Join(inst.dir, ops.DatabaseName), func(db *sql.DB) {
		mustExec(t, db, `INSERT INTO sync_vaults (id, owner_key, current_revision, head_index, epoch, created_at, updated_at) VALUES ('vault-a', 'owner-key-a', 2, 1, 1, 1, 1)`)
		mustExec(t, db, `INSERT INTO sync_devices (vault_id, device_id, token_device_id, signing_key, package_key, state, added_index, activated_epoch, created_at) VALUES ('vault-a', ?, ?, 'signing-key', 'package-key', 'approved', 1, 1, 1)`, deviceA.Device.ID, deviceA.Device.ID)
		mustExec(t, db, `INSERT INTO sync_device_list (vault_id, idx, previous_digest, kind, signer_device_id, subject_device_id, subject_signing_key, subject_package_key, sealed_label, epoch_after, snapshot_floor, signature) VALUES ('vault-a', 1, 'previous', 'add', ?, ?, 'signing-key', 'package-key', 'label', 1, 0, 'signature')`, deviceA.Device.ID, deviceA.Device.ID)
		mustExec(t, db, `INSERT INTO sync_headers (vault_id, revision, version, device_id, vault_epoch, device_list_index, device_list_digest, previous_digest, ciphertext_digest, ciphertext_length, nonce, signature, digest, receipt, accepted_at) VALUES ('vault-a', 1, 3, ?, 1, 1, 'device-list-digest', 'previous-digest', 'ciphertext-digest', 2, 'nonce', 'signature', 'digest', 'receipt', 1)`, deviceA.Device.ID)
		mustExec(t, db, `INSERT INTO sync_ciphertexts (vault_id, revision, body) VALUES ('vault-a', 1, X'0102')`)
	})
	archive := inst.backup(t)
	if err := inst.store.RevokeDevice(ctx, inst.actor(), deviceB.Device.ID); err != nil {
		t.Fatal(err)
	}
	if err := inst.store.RemoveOwner(ctx, inst.actor(), ownerB.Session.Owner.ID); err != nil {
		t.Fatal(err)
	}
	target := restoreRecoveryArchive(t, archive)
	status := fetchRecoveryStatus(t, target)
	generation, _ := status["generation"].(string)
	if !validRecoveryGeneration(generation) {
		t.Fatalf("generation = %q", generation)
	}
	code, out, stderr := approveRecovery(t, target, "--confirm", generation, "--retain-owner", inst.owner.ID, "--retain-device", deviceA.Device.ID, "--json")
	if code != exitOK {
		t.Fatalf("approval: exit %d, %s %s", code, out, stderr)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if got := jsonKeys(result); got != "alreadyComplete,generation,pending,removedOwners,retainedDevices,retainedOwners,revokedDevices" {
		t.Fatalf("fields = %s", got)
	}
	if result["pending"] != false || result["alreadyComplete"] != false || result["generation"] != generation ||
		result["retainedOwners"] != float64(1) || result["removedOwners"] != float64(1) ||
		result["retainedDevices"] != float64(1) || result["revokedDevices"] != float64(1) {
		t.Fatalf("result = %v", result)
	}

	loaded, err := secrets.Load(target)
	if err != nil {
		t.Fatal(err)
	}
	store, err := sqlitestore.Open(ctx, sqlitestore.Options{Path: filepath.Join(target, ops.DatabaseName), AdminKey: loaded.AdminEncryptionKey, Preserve: true})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	got, err := store.AuthenticateDevice(ctx, deviceA.Token)
	if err != nil || got.ID != deviceA.Device.ID {
		t.Fatalf("retained device = %+v, %v", got, err)
	}
	if _, err := store.AuthenticateDevice(ctx, deviceB.Token); !errors.Is(err, selfhost.ErrDeviceInvalid) {
		t.Fatalf("revoked device err = %v", err)
	}
	owners, err := store.ListOwners(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(owners) != 1 || owners[0].ID != inst.owner.ID {
		t.Fatalf("owners = %+v", owners)
	}
	recovery, err := store.RecoveryStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if recovery.Pending || recovery.Generation != generation || recovery.CompletedAt == nil {
		t.Fatalf("recovery = %+v", recovery)
	}
	rawSQL(t, filepath.Join(target, ops.DatabaseName), func(db *sql.DB) {
		if got := mustValue(t, db, `SELECT device_id FROM sync_devices WHERE vault_id = 'vault-a'`); got != deviceA.Device.ID {
			t.Fatalf("sync device ID = %s", got)
		}
		if got := mustValue(t, db, `SELECT COUNT(*) FROM sync_device_list WHERE vault_id = 'vault-a'`); got != "1" {
			t.Fatalf("sync device list rows = %s", got)
		}
		if got := mustValue(t, db, `SELECT COUNT(*) FROM sync_headers WHERE vault_id = 'vault-a'`); got != "1" {
			t.Fatalf("sync header rows = %s", got)
		}
		if got := mustValue(t, db, `SELECT COUNT(*) FROM sync_ciphertexts WHERE vault_id = 'vault-a'`); got != "1" {
			t.Fatalf("sync ciphertext rows = %s", got)
		}
	})
	code, out, stderr = approveRecovery(t, target, "--confirm", generation, "--json")
	if code != exitOK {
		t.Fatalf("repeated approval: exit %d, %s %s", code, out, stderr)
	}
	if !strings.Contains(out, `"alreadyComplete": true`) {
		t.Fatalf("repeated approval output = %s", out)
	}

	other := restoreRecoveryArchive(t, archive)
	otherStatus := fetchRecoveryStatus(t, other)
	otherGeneration, _ := otherStatus["generation"].(string)
	if otherGeneration == generation {
		t.Fatal("a second restore reused the generation")
	}
	code, out, stderr = approveRecovery(t, other, "--confirm", generation, "--retain-owner", inst.owner.ID, "--json")
	if code != exitFailure || !strings.Contains(stderr, "does not match") {
		t.Fatalf("stale generation: exit %d, %s %s", code, out, stderr)
	}
	stillPending := fetchRecoveryStatus(t, other)
	if stillPending["pending"] != true || stillPending["generation"] != otherGeneration {
		t.Fatalf("stale approval changed state: %v", stillPending)
	}
}

func TestRecoverApprovalRejectsRevokedDeviceAndOwnerlessSelection(t *testing.T) {
	ctx := context.Background()
	inst := newRecoveryTestInstance(t, "Ada")
	deviceA := inst.pairDevice(t, "Laptop A")
	deviceB := inst.pairDevice(t, "Laptop B")
	if err := inst.store.RevokeDevice(ctx, inst.actor(), deviceB.Device.ID); err != nil {
		t.Fatal(err)
	}
	target := restoreRecoveryArchive(t, inst.backup(t))
	status := fetchRecoveryStatus(t, target)
	generation, _ := status["generation"].(string)
	code, out, stderr := approveRecovery(t, target, "--confirm", generation, "--retain-owner", inst.owner.ID, "--retain-device", deviceB.Device.ID)
	if code != exitFailure || !strings.Contains(stderr, "revoked or expired") {
		t.Fatalf("revoked device: exit %d, %s %s", code, out, stderr)
	}
	code, out, stderr = approveRecovery(t, target, "--confirm", generation, "--json")
	if code != exitFailure || !strings.Contains(stderr, "at least one complete owner") {
		t.Fatalf("ownerless selection: exit %d, %s %s", code, out, stderr)
	}
	status = fetchRecoveryStatus(t, target)
	if status["pending"] != true || status["generation"] != generation {
		t.Fatalf("failed approvals changed state: %v", status)
	}
	code, out, stderr = approveRecovery(t, target, "--confirm", generation, "--retain-owner", inst.owner.ID, "--retain-device", deviceA.Device.ID)
	if code != exitOK {
		t.Fatalf("valid approval: exit %d, %s %s", code, out, stderr)
	}
}

func TestRecoverApprovesAnOwnerlessBackup(t *testing.T) {
	inst := newOwnerlessRecoveryTestInstance(t)
	target := restoreRecoveryArchive(t, inst.backup(t))
	status := fetchRecoveryStatus(t, target)
	generation, _ := status["generation"].(string)
	if len(status["owners"].([]any)) != 0 || len(status["devices"].([]any)) != 0 {
		t.Fatalf("status = %v", status)
	}
	code, out, stderr := approveRecovery(t, target, "--confirm", generation, "--json")
	if code != exitOK {
		t.Fatalf("exit %d, %s %s", code, out, stderr)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if result["retainedOwners"] != float64(0) || result["removedOwners"] != float64(0) || result["retainedDevices"] != float64(0) {
		t.Fatalf("result = %v", result)
	}
	loaded, err := secrets.Load(target)
	if err != nil {
		t.Fatal(err)
	}
	store, err := sqlitestore.Open(context.Background(), sqlitestore.Options{Path: filepath.Join(target, ops.DatabaseName), AdminKey: loaded.AdminEncryptionKey, Preserve: true})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	required, err := store.SetupRequired(context.Background())
	if err != nil || !required {
		t.Fatalf("setupRequired = %v, %v", required, err)
	}
	server := startReal(t, target)
	server.stop(t)
}

func TestStartupAndMutationsRefusePendingRecovery(t *testing.T) {
	inst := newRecoveryTestInstance(t, "Ada")
	device := inst.pairDevice(t, "Laptop")
	target := restoreRecoveryArchive(t, inst.backup(t))
	addr := freeAddr(t)
	code, out, stderr := command(t, map[string]string{config.EnvDataDir: target, config.EnvAddr: addr}, "serve")
	if code != exitFailure || !strings.Contains(stderr, "waiting for local recovery approval") {
		t.Fatalf("serve: exit %d, %s %s", code, out, stderr)
	}
	if strings.Contains(stderr, "/setup#token=") || strings.Contains(stderr, "listening") {
		t.Fatalf("pending startup reached setup or the listener:\n%s", stderr)
	}
	code, out, stderr = command(t, recoveryValues(target), "owner", "reset", "Ada")
	if code != exitFailure || !strings.Contains(stderr, "waiting for local recovery approval") || strings.Contains(out, "/setup#token=") {
		t.Fatalf("owner reset: exit %d, %s %s", code, out, stderr)
	}
	code, out, stderr = command(t, recoveryValues(target), "updates", "reset-sequence")
	if code != exitFailure || !strings.Contains(stderr, "waiting for local recovery approval") {
		t.Fatalf("updates reset: exit %d, %s %s", code, out, stderr)
	}
	code, out, stderr = command(t, recoveryValues(target), "check")
	if code != exitOK || !strings.Contains(out, "passed its checks") {
		t.Fatalf("check: exit %d, %s %s", code, out, stderr)
	}
	code, out, _ = command(t, recoveryValues(target), "export")
	if code != exitOK || !json.Valid([]byte(out)) {
		t.Fatalf("export: exit %d, %s", code, out)
	}
	status := fetchRecoveryStatus(t, target)
	generation, _ := status["generation"].(string)
	code, out, stderr = approveRecovery(t, target, "--confirm", generation, "--retain-owner", inst.owner.ID, "--retain-device", device.Device.ID)
	if code != exitOK {
		t.Fatalf("approval: exit %d, %s %s", code, out, stderr)
	}
	server := startReal(t, target)
	server.stop(t)
}

func TestRecoverRefusesWhileTheServerRuns(t *testing.T) {
	inst := newRecoveryTestInstance(t, "Ada")
	if err := inst.store.Close(); err != nil {
		t.Fatal(err)
	}
	server := startReal(t, inst.dir)
	code, out, stderr := command(t, recoveryValues(inst.dir), "recover", "--status", "--json")
	if code != exitFailure || !strings.Contains(stderr, "stop the running server before recovery") {
		t.Fatalf("exit %d, %s %s", code, out, stderr)
	}
	server.stop(t)
}

func TestRecoverRefusesInvalidRecoveryState(t *testing.T) {
	inst := newRecoveryTestInstance(t, "Ada")
	target := restoreRecoveryArchive(t, inst.backup(t))
	status := fetchRecoveryStatus(t, target)
	generation, _ := status["generation"].(string)
	rawSQL(t, filepath.Join(target, ops.DatabaseName), func(db *sql.DB) {
		mustExec(t, db, `DROP TABLE recovery`)
	})
	code, out, stderr := command(t, recoveryValues(target), "recover", "--status", "--json")
	if code != exitFailure || !strings.Contains(stderr, "recovery state") {
		t.Fatalf("status: exit %d, %s %s", code, out, stderr)
	}
	code, out, stderr = approveRecovery(t, target, "--confirm", generation, "--retain-owner", inst.owner.ID)
	if code != exitFailure || !strings.Contains(stderr, "recovery state") {
		t.Fatalf("approval: exit %d, %s %s", code, out, stderr)
	}
	rawSQL(t, filepath.Join(target, ops.DatabaseName), func(db *sql.DB) {
		if got := mustValue(t, db, `SELECT MAX(version) FROM schema_migrations`); got != "6" {
			t.Fatalf("schema version = %s", got)
		}
	})
}

func TestRecoverRefusesSecretMismatch(t *testing.T) {
	inst := newRecoveryTestInstance(t, "Ada")
	target := restoreRecoveryArchive(t, inst.backup(t))
	status := fetchRecoveryStatus(t, target)
	generation, _ := status["generation"].(string)
	keyPath := filepath.Join(secrets.Dir(target), secrets.AdminKeyFile)
	original, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, []byte(strings.Repeat("9", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := approveRecovery(t, target, "--confirm", generation, "--retain-owner", inst.owner.ID)
	if code != exitFailure || !strings.Contains(stderr, "secrets do not match") {
		t.Fatalf("exit %d, %s %s", code, out, stderr)
	}
	status = fetchRecoveryStatus(t, target)
	if status["pending"] != true || status["generation"] != generation {
		t.Fatalf("failed approval changed state: %v", status)
	}
	if err := os.WriteFile(keyPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, stderr = approveRecovery(t, target, "--confirm", generation, "--retain-owner", inst.owner.ID)
	if code != exitOK {
		t.Fatalf("approval after restoring the key: exit %d, %s %s", code, out, stderr)
	}
}
