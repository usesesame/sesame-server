package sqlitestore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"usesesame.app/backend/internal/authkit"
	"usesesame.app/backend/internal/selfhost"
	"usesesame.app/backend/internal/selfhost/storetest"
)

const (
	recoveryOwnerA           = "owner-a"
	recoveryOwnerB           = "owner-b"
	recoveryOwnerPending     = "owner-pending"
	recoveryOwnerRemoved     = "owner-removed"
	recoveryMemberActive     = "member-active"
	recoveryMemberRemoved    = "member-removed"
	recoveryDeviceA          = "device-a"
	recoveryDeviceAOther     = "device-a-other"
	recoveryDeviceB          = "device-b"
	recoveryDeviceMember     = "device-member"
	recoveryDeviceMemberGone = "device-member-removed"
	recoveryDeviceExpired    = "device-expired"
	recoveryDeviceRevoked    = "device-revoked"
	recoveryDevicePending    = "device-pending"
	recoverySessionA         = "session-a"
	recoverySessionB         = "session-b"
	recoverySetupToken       = "setup-token-a"
	recoveryPairingUsed      = "pairing-used"
	recoveryPairingCancelled = "pairing-cancelled"
	recoveryPairingUnused    = "pairing-unused"
)

var syncPersistentTables = []string{
	"sync_vaults",
	"sync_devices",
	"sync_device_list",
	"sync_key_packages",
	"sync_headers",
	"sync_ciphertexts",
	"sync_checkpoints",
}

type recoveryTestEnv struct {
	t     *testing.T
	ctx   context.Context
	path  string
	key   []byte
	clock *storetest.Clock
}

type recoverySource struct {
	instance     selfhost.Instance
	syncSnapshot map[string][]string
}

func newRecoveryTestEnv(t *testing.T) *recoveryTestEnv {
	t.Helper()
	return &recoveryTestEnv{
		t:     t,
		ctx:   context.Background(),
		path:  filepath.Join(t.TempDir(), "sesame.db"),
		key:   bytes.Repeat([]byte{0x42}, 32),
		clock: storetest.NewClock(),
	}
}

func (e *recoveryTestEnv) options() Options {
	return Options{Path: e.path, AdminKey: e.key, Now: e.clock.Now}
}

func (e *recoveryTestEnv) open(adjust func(*Options)) *Store {
	e.t.Helper()
	options := e.options()
	if adjust != nil {
		adjust(&options)
	}
	store, err := Open(e.ctx, options)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _ = store.Close() })
	return store
}

func (e *recoveryTestEnv) prepare() (int, error) {
	e.t.Helper()
	return PrepareRestore(e.ctx, e.options())
}

func (e *recoveryTestEnv) mustClose(store *Store) {
	e.t.Helper()
	if err := store.Close(); err != nil {
		e.t.Fatal(err)
	}
}

func (e *recoveryTestEnv) withDB(fn func(*sql.DB)) {
	e.t.Helper()
	db, err := sql.Open("sqlite", "file:"+e.path+"?_busy_timeout=5000&_foreign_keys=1")
	if err != nil {
		e.t.Fatal(err)
	}
	defer db.Close()
	fn(db)
}

func (e *recoveryTestEnv) seedRichSource() recoverySource {
	e.t.Helper()
	store := e.open(nil)
	instance, err := store.Instance(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	now := e.clock.Now()
	insertRecoveryOwner(e.t, store.db, recoveryOwnerA, true, false, now)
	insertRecoveryOwner(e.t, store.db, recoveryOwnerB, true, false, now)
	insertRecoveryOwner(e.t, store.db, recoveryOwnerPending, false, false, now)
	insertRecoveryOwner(e.t, store.db, recoveryOwnerRemoved, false, true, now)
	insertRecoveryMember(e.t, store.db, recoveryMemberActive, false, now)
	insertRecoveryMember(e.t, store.db, recoveryMemberRemoved, true, now)
	insertRecoveryDevice(e.t, store.db, recoveryDeviceA, recoveryOwnerA, "", now.Add(24*time.Hour), nil, now)
	insertRecoveryDevice(e.t, store.db, recoveryDeviceAOther, recoveryOwnerA, "", now.Add(24*time.Hour), nil, now)
	insertRecoveryDevice(e.t, store.db, recoveryDeviceB, recoveryOwnerB, "", now.Add(24*time.Hour), nil, now)
	insertRecoveryDevice(e.t, store.db, recoveryDeviceMember, "", recoveryMemberActive, now.Add(24*time.Hour), nil, now)
	insertRecoveryDevice(e.t, store.db, recoveryDeviceMemberGone, "", recoveryMemberRemoved, now.Add(24*time.Hour), nil, now)
	insertRecoveryDevice(e.t, store.db, recoveryDeviceExpired, recoveryOwnerA, "", now.Add(-time.Hour), nil, now)
	insertRecoveryDevice(e.t, store.db, recoveryDeviceRevoked, recoveryOwnerA, "", now.Add(24*time.Hour), timePtr(now.Add(-2*time.Hour)), now)
	insertRecoverySession(e.t, store.db, recoverySessionA, recoveryOwnerA, now)
	insertRecoverySession(e.t, store.db, recoverySessionB, recoveryOwnerB, now)
	insertRecoverySetupToken(e.t, store.db, recoverySetupToken, recoveryOwnerPending, now)
	insertRecoveryPairing(e.t, store.db, recoveryPairingUnused, recoveryOwnerA, now, false, false)
	insertRecoveryPairing(e.t, store.db, recoveryPairingUsed, recoveryOwnerB, now, true, false)
	insertRecoveryPairing(e.t, store.db, recoveryPairingCancelled, recoveryOwnerA, now, false, true)
	seedRecoverySync(e.t, store.db, now)
	snapshot := snapshotTables(e.t, store.db, syncPersistentTables...)
	e.mustClose(store)
	return recoverySource{instance: instance, syncSnapshot: snapshot}
}

func (e *recoveryTestEnv) seedPendingOnlySource() recoverySource {
	e.t.Helper()
	store := e.open(nil)
	instance, err := store.Instance(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	now := e.clock.Now()
	insertRecoveryOwner(e.t, store.db, recoveryOwnerPending, false, false, now)
	insertRecoveryDevice(e.t, store.db, recoveryDevicePending, recoveryOwnerPending, "", now.Add(24*time.Hour), nil, now)
	e.mustClose(store)
	return recoverySource{instance: instance}
}

func insertRecoveryOwner(t *testing.T, db *sql.DB, id string, complete, removed bool, now time.Time) {
	t.Helper()
	var hash, secret, lastLogin, removedAt any
	counter := 0
	if complete {
		hash = "hash-" + id
		secret = []byte{1, 2, 3}
		counter = 7
		lastLogin = now.Add(-30 * time.Minute).Unix()
	}
	if removed {
		removedAt = now.Add(-time.Hour).Unix()
	}
	mustExec(t, db, `
		INSERT INTO owners (id, name, name_key, password_hash, totp_secret, totp_last_counter, created_at, last_login_at, removed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, id, "Owner "+id, strings.ToLower(id), hash, secret, counter, now.Add(-24*time.Hour).Unix(), lastLogin, removedAt)
}

func insertRecoveryMember(t *testing.T, db *sql.DB, id string, removed bool, now time.Time) {
	t.Helper()
	var removedAt any
	if removed {
		removedAt = now.Add(-time.Hour).Unix()
	}
	mustExec(t, db, `
		INSERT INTO members (id, name, name_key, created_at, removed_at) VALUES (?, ?, ?, ?, ?)
	`, id, "Member "+id, strings.ToLower(id), now.Add(-24*time.Hour).Unix(), removedAt)
}

func insertRecoveryDevice(t *testing.T, db *sql.DB, id, ownerID, memberID string, expires time.Time, revokedAt *time.Time, now time.Time) {
	t.Helper()
	var owner, member, revoked any
	if ownerID != "" {
		owner = ownerID
	}
	if memberID != "" {
		member = memberID
	}
	if revokedAt != nil {
		revoked = revokedAt.Unix()
	}
	mustExec(t, db, `
		INSERT INTO devices (id, token_hash, name, owner_id, member_id, created_at, expires_at, last_seen_at, revoked_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, id, authkit.HashToken("token-"+id), "Device "+id, owner, member, now.Add(-time.Hour).Unix(), expires.Unix(), now.Unix(), revoked)
}

func insertRecoverySession(t *testing.T, db *sql.DB, id, ownerID string, now time.Time) {
	t.Helper()
	mustExec(t, db, `
		INSERT INTO sessions (id, token_hash, owner_id, created_at, expires_at, recent_auth_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, id, authkit.HashToken("session-"+id), ownerID, now.Unix(), now.Add(time.Hour).Unix(), now.Unix())
}

func insertRecoverySetupToken(t *testing.T, db *sql.DB, id, ownerID string, now time.Time) {
	t.Helper()
	mustExec(t, db, `
		INSERT INTO setup_tokens (id, token_hash, kind, owner_id, created_by, created_at, expires_at)
		VALUES (?, ?, 'invite', ?, 'system', ?, ?)
	`, id, authkit.HashToken("setup-"+id), ownerID, now.Unix(), now.Add(time.Hour).Unix())
}

func insertRecoveryPairing(t *testing.T, db *sql.DB, id, ownerID string, now time.Time, used, cancelled bool) {
	t.Helper()
	var usedAt, cancelledAt any
	if used {
		usedAt = now.Unix()
	}
	if cancelled {
		cancelledAt = now.Unix()
	}
	mustExec(t, db, `
		INSERT INTO pairings (id, code_hash, owner_id, device_name_hint, created_by, created_at, expires_at, used_at, cancelled_at)
		VALUES (?, ?, ?, '', 'system', ?, ?, ?, ?)
	`, id, authkit.HashToken("pairing-"+id), ownerID, now.Unix(), now.Add(time.Hour).Unix(), usedAt, cancelledAt)
}

func seedRecoverySync(t *testing.T, db *sql.DB, now time.Time) {
	t.Helper()
	mustExec(t, db, `
		INSERT INTO sync_vaults (id, owner_key, current_revision, head_index, epoch, created_at, updated_at)
		VALUES ('vault-a', 'owner-key-a', 2, 1, 1, ?, ?)
	`, now.Unix(), now.Unix())
	mustExec(t, db, `
		INSERT INTO sync_devices (vault_id, device_id, token_device_id, signing_key, package_key, state, added_index, activated_epoch, created_at)
		VALUES ('vault-a', 'sync-device-a', 'token-sync-a', 'signing-key', 'package-key', 'approved', 1, 1, ?)
	`, now.Unix())
	mustExec(t, db, `
		INSERT INTO sync_device_list (vault_id, idx, previous_digest, kind, signer_device_id, subject_device_id,
			subject_signing_key, subject_package_key, sealed_label, epoch_after, snapshot_floor, signature)
		VALUES ('vault-a', 1, 'previous', 'add', 'sync-device-a', 'sync-device-a', 'signing-key', 'package-key', 'label', 1, 0, 'signature')
	`)
	mustExec(t, db, `
		INSERT INTO sync_key_packages (vault_id, recipient_device_id, sender_device_id, vault_epoch, entry_digest, ciphertext, signature, created_at)
		VALUES ('vault-a', 'sync-device-a', 'sync-device-a', 1, 'entry-digest', 'ciphertext', 'signature', ?)
	`, now.Unix())
	mustExec(t, db, `
		INSERT INTO sync_headers (vault_id, revision, version, device_id, vault_epoch, device_list_index, device_list_digest,
			previous_digest, ciphertext_digest, ciphertext_length, nonce, signature, digest, receipt, accepted_at)
		VALUES ('vault-a', 1, 3, 'sync-device-a', 1, 1, 'device-list-digest', 'previous-digest', 'ciphertext-digest',
			2, 'nonce', 'signature', 'digest', 'receipt', ?)
	`, now.Unix())
	mustExec(t, db, `INSERT INTO sync_ciphertexts (vault_id, revision, body) VALUES ('vault-a', 1, X'0102')`)
	mustExec(t, db, `
		INSERT INTO sync_checkpoints (vault_id, device_id, version, device_list_index, device_list_digest,
			header_revision, header_digest, observed_at, signature, accepted_at)
		VALUES ('vault-a', 'sync-device-a', 3, 1, 'device-list-digest', 1, 'digest', ?, 'signature', ?)
	`, now.Unix(), now.Unix())
	mustExec(t, db, `
		INSERT INTO sync_pairings (vault_id, device_id, token_device_id, signing_key, package_key, offer, answer, confirm, state, expires_at, created_at)
		VALUES ('vault-a', 'sync-pairing', 'token-pairing', 'signing-key', 'package-key', NULL, NULL, NULL, 'offered', ?, ?)
	`, now.Add(time.Hour).Unix(), now.Unix())
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func mustCount(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var count int
	if err := db.QueryRow(query, args...).Scan(&count); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return count
}

func mustHex(t *testing.T, db *sql.DB, query string, args ...any) string {
	t.Helper()
	var value sql.NullString
	if err := db.QueryRow(query, args...).Scan(&value); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return strings.ToLower(value.String)
}

func snapshotTables(t *testing.T, db *sql.DB, tables ...string) map[string][]string {
	t.Helper()
	snapshot := map[string][]string{}
	for _, table := range tables {
		snapshot[table] = snapshotTable(t, db, table)
	}
	return snapshot
}

func snapshotTable(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query(`SELECT * FROM ` + table)
	if err != nil {
		t.Fatalf("snapshot %s: %v", table, err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for index := range values {
			pointers[index] = &values[index]
		}
		if err := rows.Scan(pointers...); err != nil {
			t.Fatal(err)
		}
		parts := make([]string, len(values))
		for index, value := range values {
			if raw, ok := value.([]byte); ok {
				parts[index] = hex.EncodeToString(raw)
				continue
			}
			parts[index] = fmt.Sprint(value)
		}
		lines = append(lines, strings.Join(parts, "|"))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(lines)
	return lines
}

func requireSameSnapshot(t *testing.T, before, after map[string][]string) {
	t.Helper()
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("preserved rows changed\nbefore: %v\nafter: %v", before, after)
	}
}

func requireCheckpointed(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path + "-wal")
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 0 {
		t.Fatalf("the restored database still has %d bytes in its WAL", info.Size())
	}
}

func timePtr(value time.Time) *time.Time {
	return &value
}

func TestNormalUpgradeCreatesNonPendingRecoveryState(t *testing.T) {
	env := newRecoveryTestEnv(t)
	store := env.open(nil)
	status, err := store.RecoveryStatus(env.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Pending || status.Generation != "" || !status.StartedAt.IsZero() || status.CompletedAt != nil {
		t.Fatalf("normal upgrade recovery state = %+v", status)
	}
	env.mustClose(store)

	store = env.open(func(options *Options) {
		options.RequireRecovered = true
		options.Flags = []selfhost.FlagDefinition{{Key: "sync", Description: "Sync"}}
	})
	status, err = store.RecoveryStatus(env.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Pending {
		t.Fatal("a normal upgrade requires recovery approval")
	}
	flags, err := store.Flags(env.ctx)
	if err != nil || len(flags) != 1 || flags[0].Key != "sync" {
		t.Fatalf("flags after a normal upgrade = %+v, %v", flags, err)
	}
}

func TestOldSchemaUpgradeAndCompatibility(t *testing.T) {
	t.Run("normal upgrade needs no approval", func(t *testing.T) {
		env := newRecoveryTestEnv(t)
		old, err := openWith(env.ctx, env.options(), defaultMigrations[:len(defaultMigrations)-1])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := old.CreateMember(env.ctx, selfhost.SystemActor, "Sam"); err != nil {
			t.Fatal(err)
		}
		env.mustClose(old)

		store := env.open(func(options *Options) {
			options.RequireRecovered = true
			options.Flags = []selfhost.FlagDefinition{{Key: "metrics", Description: "Metrics"}}
		})
		status, err := store.RecoveryStatus(env.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if status.Pending {
			t.Fatal("the old schema upgrade left recovery pending")
		}
	})

	t.Run("prepare upgrades an old archive schema", func(t *testing.T) {
		env := newRecoveryTestEnv(t)
		old, err := openWith(env.ctx, env.options(), defaultMigrations[:len(defaultMigrations)-1])
		if err != nil {
			t.Fatal(err)
		}
		instance, err := old.Instance(env.ctx)
		if err != nil {
			t.Fatal(err)
		}
		env.mustClose(old)

		installed, err := env.prepare()
		if err != nil {
			t.Fatal(err)
		}
		if installed != len(defaultMigrations) {
			t.Fatalf("installed schema = %d, want %d", installed, len(defaultMigrations))
		}
		env.withDB(func(db *sql.DB) {
			status, err := readRecoveryStatus(env.ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			if !status.Pending || !validRecoveryGeneration(status.Generation) {
				t.Fatalf("prepared recovery state = %+v", status)
			}
			var id string
			if err := db.QueryRow(`SELECT id FROM instance WHERE singleton = 1`).Scan(&id); err != nil {
				t.Fatal(err)
			}
			if id != instance.ID {
				t.Fatalf("instance = %q, want %q", id, instance.ID)
			}
		})
		if _, err := openWith(env.ctx, env.options(), defaultMigrations[:len(defaultMigrations)-1]); !errors.Is(err, selfhost.ErrSchemaTooNew) {
			t.Fatalf("an older binary must refuse the restored schema, got %v", err)
		}
	})
}

func TestPrepareRestoreInvalidatesAccessAndPreservesIdentity(t *testing.T) {
	env := newRecoveryTestEnv(t)
	source := env.seedRichSource()
	installed, err := env.prepare()
	if err != nil {
		t.Fatal(err)
	}
	if installed != len(defaultMigrations) {
		t.Fatalf("installed schema = %d, want %d", installed, len(defaultMigrations))
	}
	requireCheckpointed(t, env.path)

	var firstGeneration string
	var firstStarted time.Time
	env.withDB(func(db *sql.DB) {
		status, err := readRecoveryStatus(env.ctx, db)
		if err != nil {
			t.Fatal(err)
		}
		if !status.Pending || !validRecoveryGeneration(status.Generation) || !status.StartedAt.Equal(env.clock.Now()) || status.CompletedAt != nil {
			t.Fatalf("prepared recovery state = %+v", status)
		}
		firstGeneration, firstStarted = status.Generation, status.StartedAt
		var id, name string
		var createdAt int64
		if err := db.QueryRow(`SELECT id, name, created_at FROM instance WHERE singleton = 1`).Scan(&id, &name, &createdAt); err != nil {
			t.Fatal(err)
		}
		if id != source.instance.ID || name != source.instance.Name || createdAt != source.instance.CreatedAt.Unix() {
			t.Fatalf("instance identity changed: %q %q %d", id, name, createdAt)
		}
		if count := mustCount(t, db, `SELECT COUNT(*) FROM sessions`); count != 0 {
			t.Fatalf("%d sessions survived the restore preparation", count)
		}
		if count := mustCount(t, db, `SELECT COUNT(*) FROM setup_tokens`); count != 0 {
			t.Fatalf("%d setup tokens survived the restore preparation", count)
		}
		if count := mustCount(t, db, `SELECT COUNT(*) FROM pairings WHERE used_at IS NULL AND cancelled_at IS NULL`); count != 0 {
			t.Fatalf("%d desktop pairings survived the restore preparation", count)
		}
		if count := mustCount(t, db, `SELECT COUNT(*) FROM sync_pairings`); count != 0 {
			t.Fatalf("%d Sync pairings survived the restore preparation", count)
		}
		if count := mustCount(t, db, `
			SELECT COUNT(*) FROM owners WHERE id = ? AND removed_at IS NULL AND password_hash = ? AND totp_secret = X'010203' AND totp_last_counter = 7
		`, recoveryOwnerA, "hash-"+recoveryOwnerA); count != 1 {
			t.Fatal("a complete owner lost credentials during preparation")
		}
		if count := mustCount(t, db, `SELECT COUNT(*) FROM devices WHERE id = ? AND revoked_at IS NULL`, recoveryDeviceA); count != 1 {
			t.Fatal("a device was revoked during preparation")
		}
		if got, want := mustHex(t, db, `SELECT hex(token_hash) FROM devices WHERE id = ?`, recoveryDeviceA), hex.EncodeToString(authkit.HashToken("token-"+recoveryDeviceA)); got != want {
			t.Fatalf("device token hash = %s, want %s", got, want)
		}
		requireSameSnapshot(t, source.syncSnapshot, snapshotTables(t, db, syncPersistentTables...))
	})
	env.clock.Advance(2 * time.Minute)
	if _, err := env.prepare(); err != nil {
		t.Fatal(err)
	}
	env.withDB(func(db *sql.DB) {
		status, err := readRecoveryStatus(env.ctx, db)
		if err != nil {
			t.Fatal(err)
		}
		if !status.Pending || status.Generation == firstGeneration || status.StartedAt.Equal(firstStarted) {
			t.Fatalf("a second restore did not create a fresh generation: %+v", status)
		}
	})

	store := env.open(func(options *Options) { options.Preserve = true })
	defer store.Close()
	if _, err := store.CompleteRecovery(env.ctx, selfhost.SystemActor, firstGeneration, selfhost.RecoverySelection{OwnerIDs: []string{recoveryOwnerA}}); !errors.Is(err, selfhost.ErrRecoveryGenerationMismatch) {
		t.Fatalf("a generation from the first restore was accepted: %v", err)
	}
	page, err := store.ListAudit(env.ctx, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) < 2 || page.Entries[0].Action != "recovery.pending" || page.Entries[1].Action != "recovery.pending" {
		t.Fatalf("pending audit entries = %+v", page.Entries)
	}
	report, err := store.VerifyAudit(env.ctx)
	if err != nil || !report.OK {
		t.Fatalf("audit after preparation = %+v, %v", report, err)
	}
}

func TestCompleteRecoveryAppliesSelectionAndIsIdempotent(t *testing.T) {
	env := newRecoveryTestEnv(t)
	source := env.seedRichSource()
	if _, err := env.prepare(); err != nil {
		t.Fatal(err)
	}
	store := env.open(func(options *Options) { options.Preserve = true })
	defer store.Close()
	status, err := store.RecoveryStatus(env.ctx)
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.CompleteRecovery(env.ctx, selfhost.SystemActor, status.Generation, selfhost.RecoverySelection{
		OwnerIDs:  []string{recoveryOwnerA},
		DeviceIDs: []string{recoveryDeviceA, recoveryDeviceMember},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := selfhost.RecoveryResult{RetainedOwners: 1, RemovedOwners: 2, RetainedDevices: 2, RevokedDevices: 4}
	if result != want {
		t.Fatalf("result = %+v, want %+v", result, want)
	}
	completedAt := env.clock.Now()
	env.withDB(func(db *sql.DB) {
		completed, err := readRecoveryStatus(env.ctx, db)
		if err != nil {
			t.Fatal(err)
		}
		if completed.Pending || completed.Generation != status.Generation || completed.CompletedAt == nil || !completed.CompletedAt.Equal(completedAt) {
			t.Fatalf("completed recovery state = %+v", completed)
		}
		if count := mustCount(t, db, `
			SELECT COUNT(*) FROM owners WHERE id = ? AND removed_at IS NULL AND password_hash = ? AND totp_secret = X'010203' AND totp_last_counter = 7
		`, recoveryOwnerA, "hash-"+recoveryOwnerA); count != 1 {
			t.Fatal("the retained owner lost credentials")
		}
		if count := mustCount(t, db, `
			SELECT COUNT(*) FROM owners WHERE id = ? AND removed_at IS NOT NULL AND password_hash IS NULL AND totp_secret IS NULL
		`, recoveryOwnerB); count != 1 {
			t.Fatal("the unselected owner was not removed")
		}
		if count := mustCount(t, db, `
			SELECT COUNT(*) FROM owners WHERE id = ? AND removed_at IS NOT NULL AND password_hash IS NULL AND totp_secret IS NULL
		`, recoveryOwnerPending); count != 1 {
			t.Fatal("the pending owner was not removed")
		}
		if count := mustCount(t, db, `SELECT COUNT(*) FROM owners WHERE removed_at IS NULL AND id NOT IN (?, ?)`, recoveryOwnerA, recoveryOwnerRemoved); count != 0 {
			t.Fatal("an unselected owner still has active authority")
		}
		for _, id := range []string{recoveryDeviceA, recoveryDeviceMember} {
			if count := mustCount(t, db, `SELECT COUNT(*) FROM devices WHERE id = ? AND revoked_at IS NULL`, id); count != 1 {
				t.Fatalf("retained device %s is missing or revoked", id)
			}
			if got, want := mustHex(t, db, `SELECT hex(token_hash) FROM devices WHERE id = ?`, id), hex.EncodeToString(authkit.HashToken("token-"+id)); got != want {
				t.Fatalf("device %s token hash = %s, want %s", id, got, want)
			}
		}
		for _, id := range []string{recoveryDeviceAOther, recoveryDeviceB, recoveryDeviceMemberGone, recoveryDeviceExpired} {
			if count := mustCount(t, db, `SELECT COUNT(*) FROM devices WHERE id = ? AND revoked_at = ?`, id, completedAt.Unix()); count != 1 {
				t.Fatalf("unselected device %s was not revoked", id)
			}
		}
		if count := mustCount(t, db, `SELECT COUNT(*) FROM devices WHERE id = ? AND revoked_at = ?`, recoveryDeviceRevoked, env.clock.Now().Add(-2*time.Hour).Unix()); count != 1 {
			t.Fatal("an already revoked device was rewritten")
		}
		if count := mustCount(t, db, `SELECT COUNT(*) FROM devices WHERE revoked_at IS NULL`); count != 2 {
			t.Fatalf("%d devices remain active, want 2", count)
		}
		if count := mustCount(t, db, `SELECT COUNT(*) FROM sessions`); count != 0 {
			t.Fatalf("%d sessions remain", count)
		}
		if count := mustCount(t, db, `SELECT COUNT(*) FROM setup_tokens`); count != 0 {
			t.Fatalf("%d setup tokens remain", count)
		}
		if count := mustCount(t, db, `SELECT COUNT(*) FROM pairings WHERE used_at IS NULL AND cancelled_at IS NULL`); count != 0 {
			t.Fatalf("%d desktop pairings remain", count)
		}
		if count := mustCount(t, db, `SELECT COUNT(*) FROM sync_pairings`); count != 0 {
			t.Fatalf("%d Sync pairings remain", count)
		}
		requireSameSnapshot(t, source.syncSnapshot, snapshotTables(t, db, syncPersistentTables...))
	})
	required, err := store.SetupRequired(env.ctx)
	if err != nil || required {
		t.Fatalf("SetupRequired = %v, %v", required, err)
	}
	page, err := store.ListAudit(env.ctx, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 2 || page.Entries[0].Action != "recovery.completed" {
		t.Fatalf("audit entries = %+v", page.Entries)
	}
	detail := page.Entries[0].Detail
	if detail["retainedOwners"] != "1" || detail["removedOwners"] != "2" || detail["retainedDevices"] != "2" || detail["revokedDevices"] != "4" {
		t.Fatalf("completion audit detail = %+v", detail)
	}
	report, err := store.VerifyAudit(env.ctx)
	if err != nil || !report.OK {
		t.Fatalf("audit after completion = %+v, %v", report, err)
	}

	beforeCompletion := snapshotTables(t, store.db, "owners", "devices", "recovery", "audit_log")
	again, err := store.CompleteRecovery(env.ctx, selfhost.SystemActor, status.Generation, selfhost.RecoverySelection{
		OwnerIDs:  []string{recoveryOwnerB},
		DeviceIDs: []string{recoveryDeviceB},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !again.AlreadyComplete || again.RetainedOwners != 0 || again.RemovedOwners != 0 || again.RetainedDevices != 0 || again.RevokedDevices != 0 {
		t.Fatalf("repeated completion result = %+v", again)
	}
	requireSameSnapshot(t, beforeCompletion, snapshotTables(t, store.db, "owners", "devices", "recovery", "audit_log"))
	if _, err := store.CompleteRecovery(env.ctx, selfhost.SystemActor, strings.Repeat("a", 32), selfhost.RecoverySelection{}); !errors.Is(err, selfhost.ErrRecoveryGenerationMismatch) {
		t.Fatalf("a different generation was accepted: %v", err)
	}
}

func TestCompleteRecoveryAfterClockMovesBack(t *testing.T) {
	env := newRecoveryTestEnv(t)
	env.seedRichSource()
	if _, err := env.prepare(); err != nil {
		t.Fatal(err)
	}
	store := env.open(func(options *Options) { options.Preserve = true })
	status, err := store.RecoveryStatus(env.ctx)
	if err != nil {
		t.Fatal(err)
	}
	env.clock.Advance(-time.Hour)
	if _, err := store.CompleteRecovery(env.ctx, selfhost.SystemActor, status.Generation, selfhost.RecoverySelection{
		OwnerIDs: []string{recoveryOwnerA}, DeviceIDs: []string{recoveryDeviceA},
	}); err != nil {
		t.Fatalf("clock correction blocked approval: %v", err)
	}
	completed, err := store.RecoveryStatus(env.ctx)
	if err != nil || completed.Pending || completed.CompletedAt == nil || !completed.CompletedAt.Equal(status.StartedAt) {
		t.Fatalf("completed recovery after clock correction: %+v, %v", completed, err)
	}
}

func TestCompleteRecoveryRevokesEveryUnselectedDevice(t *testing.T) {
	env := newRecoveryTestEnv(t)
	env.seedRichSource()
	if _, err := env.prepare(); err != nil {
		t.Fatal(err)
	}
	store := env.open(func(options *Options) { options.Preserve = true })
	defer store.Close()
	status, err := store.RecoveryStatus(env.ctx)
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.CompleteRecovery(env.ctx, selfhost.SystemActor, status.Generation, selfhost.RecoverySelection{
		OwnerIDs: []string{recoveryOwnerA},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.RetainedDevices != 0 || result.RevokedDevices != 6 {
		t.Fatalf("result = %+v", result)
	}
	if count := mustCount(t, store.db, `SELECT COUNT(*) FROM devices WHERE revoked_at IS NULL`); count != 0 {
		t.Fatalf("%d devices remain active without an explicit selection", count)
	}
	if count := mustCount(t, store.db, `SELECT COUNT(*) FROM devices WHERE id = ? AND revoked_at IS NOT NULL`, recoveryDeviceExpired); count != 1 {
		t.Fatal("the expired device was not revoked")
	}
}

func TestCompleteRecoveryAllowsEmptyOwnerSelectionWithoutCompleteOwners(t *testing.T) {
	env := newRecoveryTestEnv(t)
	env.seedPendingOnlySource()
	if _, err := env.prepare(); err != nil {
		t.Fatal(err)
	}
	store := env.open(func(options *Options) { options.Preserve = true })
	defer store.Close()
	status, err := store.RecoveryStatus(env.ctx)
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.CompleteRecovery(env.ctx, selfhost.SystemActor, status.Generation, selfhost.RecoverySelection{})
	if err != nil {
		t.Fatal(err)
	}
	want := selfhost.RecoveryResult{RemovedOwners: 1, RevokedDevices: 1}
	if result != want {
		t.Fatalf("result = %+v, want %+v", result, want)
	}
	required, err := store.SetupRequired(env.ctx)
	if err != nil || !required {
		t.Fatalf("SetupRequired = %v, %v", required, err)
	}
	if count := mustCount(t, store.db, `SELECT COUNT(*) FROM owners WHERE removed_at IS NULL`); count != 0 {
		t.Fatalf("%d owners remain active", count)
	}
}

func TestCompleteRecoveryAllowsEmptyOwnerSelectionWithNoOwners(t *testing.T) {
	env := newRecoveryTestEnv(t)
	store := env.open(nil)
	env.mustClose(store)
	if _, err := env.prepare(); err != nil {
		t.Fatal(err)
	}
	approved := env.open(func(options *Options) { options.Preserve = true })
	defer approved.Close()
	status, err := approved.RecoveryStatus(env.ctx)
	if err != nil {
		t.Fatal(err)
	}
	result, err := approved.CompleteRecovery(env.ctx, selfhost.SystemActor, status.Generation, selfhost.RecoverySelection{})
	if err != nil {
		t.Fatal(err)
	}
	if result != (selfhost.RecoveryResult{}) {
		t.Fatalf("result = %+v, want an empty result", result)
	}
	required, err := approved.SetupRequired(env.ctx)
	if err != nil || !required {
		t.Fatalf("SetupRequired = %v, %v", required, err)
	}
}

func TestCompleteRecoveryRejectsInvalidSelectionsWithoutChanges(t *testing.T) {
	env := newRecoveryTestEnv(t)
	env.seedRichSource()
	if _, err := env.prepare(); err != nil {
		t.Fatal(err)
	}
	store := env.open(func(options *Options) { options.Preserve = true })
	defer store.Close()
	status, err := store.RecoveryStatus(env.ctx)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name      string
		selection selfhost.RecoverySelection
		want      error
	}{
		{"duplicate owner", selfhost.RecoverySelection{OwnerIDs: []string{recoveryOwnerA, recoveryOwnerA}}, selfhost.ErrRecoverySelectionInvalid},
		{"unknown owner", selfhost.RecoverySelection{OwnerIDs: []string{"owner-unknown"}}, selfhost.ErrRecoverySelectionInvalid},
		{"removed owner", selfhost.RecoverySelection{OwnerIDs: []string{recoveryOwnerRemoved}}, selfhost.ErrRecoverySelectionInvalid},
		{"incomplete owner", selfhost.RecoverySelection{OwnerIDs: []string{recoveryOwnerPending}}, selfhost.ErrRecoverySelectionInvalid},
		{"owner required", selfhost.RecoverySelection{}, selfhost.ErrRecoveryOwnerRequired},
		{"duplicate device", selfhost.RecoverySelection{OwnerIDs: []string{recoveryOwnerA}, DeviceIDs: []string{recoveryDeviceA, recoveryDeviceA}}, selfhost.ErrRecoverySelectionInvalid},
		{"unknown device", selfhost.RecoverySelection{OwnerIDs: []string{recoveryOwnerA}, DeviceIDs: []string{"device-unknown"}}, selfhost.ErrRecoverySelectionInvalid},
		{"revoked device", selfhost.RecoverySelection{OwnerIDs: []string{recoveryOwnerA}, DeviceIDs: []string{recoveryDeviceRevoked}}, selfhost.ErrRecoverySelectionInvalid},
		{"expired device", selfhost.RecoverySelection{OwnerIDs: []string{recoveryOwnerA}, DeviceIDs: []string{recoveryDeviceExpired}}, selfhost.ErrRecoverySelectionInvalid},
		{"device of non-retained owner", selfhost.RecoverySelection{OwnerIDs: []string{recoveryOwnerA}, DeviceIDs: []string{recoveryDeviceB}}, selfhost.ErrRecoverySelectionInvalid},
		{"device of removed member", selfhost.RecoverySelection{OwnerIDs: []string{recoveryOwnerA}, DeviceIDs: []string{recoveryDeviceMemberGone}}, selfhost.ErrRecoverySelectionInvalid},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := store.CompleteRecovery(env.ctx, selfhost.SystemActor, status.Generation, test.selection); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
	env.withDB(func(db *sql.DB) {
		pending, err := readRecoveryStatus(env.ctx, db)
		if err != nil || !pending.Pending || pending.Generation != status.Generation {
			t.Fatalf("recovery state changed: %+v, %v", pending, err)
		}
		if count := mustCount(t, db, `SELECT COUNT(*) FROM owners WHERE removed_at IS NULL`); count != 3 {
			t.Fatalf("%d owners remain active, want 3", count)
		}
		if count := mustCount(t, db, `SELECT COUNT(*) FROM devices WHERE revoked_at IS NULL`); count != 6 {
			t.Fatalf("%d devices remain active, want 6", count)
		}
		if count := mustCount(t, db, `SELECT COUNT(*) FROM audit_log`); count != 1 {
			t.Fatalf("%d audit rows exist, want 1", count)
		}
	})
}

func TestPrepareRestoreRollsBackOnWriteFailure(t *testing.T) {
	env := newRecoveryTestEnv(t)
	env.seedRichSource()
	env.withDB(func(db *sql.DB) {
		mustExec(t, db, `
			CREATE TRIGGER block_recovery_pending BEFORE INSERT ON audit_log
			WHEN NEW.action = 'recovery.pending'
			BEGIN SELECT RAISE(ABORT, 'blocked'); END
		`)
	})
	if _, err := env.prepare(); err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("prepare error = %v", err)
	}
	env.withDB(func(db *sql.DB) {
		status, err := readRecoveryStatus(env.ctx, db)
		if err != nil {
			t.Fatal(err)
		}
		if status.Pending {
			t.Fatal("a failed preparation left recovery pending")
		}
		if count := mustCount(t, db, `SELECT COUNT(*) FROM sessions`); count != 2 {
			t.Fatalf("%d sessions remain, want 2", count)
		}
		if count := mustCount(t, db, `SELECT COUNT(*) FROM owners WHERE removed_at IS NULL`); count != 3 {
			t.Fatalf("%d owners remain active, want 3", count)
		}
		if count := mustCount(t, db, `SELECT COUNT(*) FROM devices WHERE revoked_at IS NULL`); count != 6 {
			t.Fatalf("%d devices remain active, want 6", count)
		}
		if count := mustCount(t, db, `SELECT COUNT(*) FROM audit_log`); count != 0 {
			t.Fatalf("%d audit rows remain", count)
		}
	})
}

func TestCompleteRecoveryRollsBackOnWriteFailure(t *testing.T) {
	env := newRecoveryTestEnv(t)
	env.seedRichSource()
	if _, err := env.prepare(); err != nil {
		t.Fatal(err)
	}
	store := env.open(func(options *Options) { options.Preserve = true })
	defer store.Close()
	status, err := store.RecoveryStatus(env.ctx)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, store.db, `
		CREATE TRIGGER block_owner_removal BEFORE UPDATE ON owners
		BEGIN SELECT RAISE(ABORT, 'blocked'); END
	`)
	if _, err := store.CompleteRecovery(env.ctx, selfhost.SystemActor, status.Generation, selfhost.RecoverySelection{OwnerIDs: []string{recoveryOwnerA}, DeviceIDs: []string{recoveryDeviceA}}); err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("complete error = %v", err)
	}
	if count := mustCount(t, store.db, `SELECT COUNT(*) FROM owners WHERE removed_at IS NULL`); count != 3 {
		t.Fatalf("%d owners remain active, want 3", count)
	}
	if count := mustCount(t, store.db, `SELECT COUNT(*) FROM devices WHERE revoked_at IS NULL`); count != 6 {
		t.Fatalf("%d devices remain active, want 6", count)
	}
	if count := mustCount(t, store.db, `SELECT COUNT(*) FROM recovery WHERE pending = 1 AND generation = ?`, status.Generation); count != 1 {
		t.Fatal("a failed completion changed the pending generation")
	}
	if count := mustCount(t, store.db, `SELECT COUNT(*) FROM audit_log`); count != 1 {
		t.Fatalf("%d audit rows remain, want 1", count)
	}
}

func TestTriggerSuppressedRecoveryWritesAreDetected(t *testing.T) {
	t.Run("prepare", func(t *testing.T) {
		env := newRecoveryTestEnv(t)
		env.seedRichSource()
		env.withDB(func(db *sql.DB) {
			mustExec(t, db, `
				CREATE TRIGGER suppress_recovery_update BEFORE UPDATE ON recovery
				BEGIN SELECT RAISE(IGNORE); END
			`)
		})
		if _, err := env.prepare(); !errors.Is(err, selfhost.ErrRecoveryStateInvalid) {
			t.Fatalf("prepare error = %v, want %v", err, selfhost.ErrRecoveryStateInvalid)
		}
		env.withDB(func(db *sql.DB) {
			status, err := readRecoveryStatus(env.ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			if status.Pending {
				t.Fatal("a suppressed write left recovery pending")
			}
			if count := mustCount(t, db, `SELECT COUNT(*) FROM sessions`); count != 2 {
				t.Fatalf("%d sessions remain, want 2", count)
			}
			if count := mustCount(t, db, `SELECT COUNT(*) FROM audit_log`); count != 0 {
				t.Fatalf("%d audit rows remain", count)
			}
		})
	})

	t.Run("complete", func(t *testing.T) {
		env := newRecoveryTestEnv(t)
		env.seedRichSource()
		if _, err := env.prepare(); err != nil {
			t.Fatal(err)
		}
		store := env.open(func(options *Options) { options.Preserve = true })
		defer store.Close()
		status, err := store.RecoveryStatus(env.ctx)
		if err != nil {
			t.Fatal(err)
		}
		mustExec(t, store.db, `
			CREATE TRIGGER suppress_recovery_update BEFORE UPDATE ON recovery
			BEGIN SELECT RAISE(IGNORE); END
		`)
		if _, err := store.CompleteRecovery(env.ctx, selfhost.SystemActor, status.Generation, selfhost.RecoverySelection{OwnerIDs: []string{recoveryOwnerA}, DeviceIDs: []string{recoveryDeviceA}}); !errors.Is(err, selfhost.ErrRecoveryGenerationMismatch) {
			t.Fatalf("complete error = %v, want %v", err, selfhost.ErrRecoveryGenerationMismatch)
		}
		if count := mustCount(t, store.db, `SELECT COUNT(*) FROM owners WHERE removed_at IS NULL`); count != 3 {
			t.Fatalf("%d owners remain active, want 3", count)
		}
		if count := mustCount(t, store.db, `SELECT COUNT(*) FROM devices WHERE revoked_at IS NULL`); count != 6 {
			t.Fatalf("%d devices remain active, want 6", count)
		}
		if count := mustCount(t, store.db, `SELECT COUNT(*) FROM recovery WHERE pending = 1 AND generation = ?`, status.Generation); count != 1 {
			t.Fatal("a suppressed write changed the pending generation")
		}
		if count := mustCount(t, store.db, `SELECT COUNT(*) FROM audit_log`); count != 1 {
			t.Fatalf("%d audit rows remain, want 1", count)
		}
	})
}

func TestRequireRecoveredRejectsPendingBeforeSeeding(t *testing.T) {
	env := newRecoveryTestEnv(t)
	env.seedRichSource()
	if _, err := env.prepare(); err != nil {
		t.Fatal(err)
	}
	options := env.options()
	options.RequireRecovered = true
	options.Flags = []selfhost.FlagDefinition{{Key: "sync", Description: "Sync"}}
	if _, err := Open(env.ctx, options); !errors.Is(err, selfhost.ErrRecoveryPending) {
		t.Fatalf("serving open error = %v, want %v", err, selfhost.ErrRecoveryPending)
	}
	env.withDB(func(db *sql.DB) {
		if count := mustCount(t, db, `SELECT COUNT(*) FROM instance`); count != 1 {
			t.Fatalf("%d instance rows exist, want 1", count)
		}
		if count := mustCount(t, db, `SELECT COUNT(*) FROM flags`); count != 0 {
			t.Fatalf("%d flags were seeded before the recovery gate", count)
		}
	})

	options = env.options()
	options.ReadOnly = true
	options.RequireRecovered = true
	if _, err := Open(env.ctx, options); !errors.Is(err, selfhost.ErrRecoveryPending) {
		t.Fatalf("read only serving open error = %v, want %v", err, selfhost.ErrRecoveryPending)
	}

	inspected := env.open(func(current *Options) { current.ReadOnly = true })
	status, err := inspected.RecoveryStatus(env.ctx)
	if err != nil || !status.Pending {
		t.Fatalf("read only inspection status = %+v, %v", status, err)
	}
}

func TestRecoveryStateCorruptionFailsClosed(t *testing.T) {
	looseTable := `
		CREATE TABLE recovery (
			singleton INTEGER PRIMARY KEY,
			pending INTEGER NOT NULL,
			generation TEXT NOT NULL,
			started_at INTEGER NOT NULL,
			completed_at INTEGER
		) STRICT
	`
	cases := []struct {
		name   string
		damage func(*sql.DB)
	}{
		{"missing row", func(db *sql.DB) { mustExec(t, db, `DELETE FROM recovery`) }},
		{"missing table", func(db *sql.DB) { mustExec(t, db, `DROP TABLE recovery`) }},
		{"malformed generation", func(db *sql.DB) {
			mustExec(t, db, `DROP TABLE recovery`)
			mustExec(t, db, looseTable)
			mustExec(t, db, `INSERT INTO recovery VALUES (1, 1, 'not-hex', 10, NULL)`)
		}},
		{"inconsistent completion", func(db *sql.DB) {
			mustExec(t, db, `DROP TABLE recovery`)
			mustExec(t, db, looseTable)
			mustExec(t, db, `INSERT INTO recovery VALUES (1, 0, ?, 10, NULL)`, strings.Repeat("a", 32))
		}},
		{"unknown pending value", func(db *sql.DB) {
			mustExec(t, db, `DROP TABLE recovery`)
			mustExec(t, db, looseTable)
			mustExec(t, db, `INSERT INTO recovery VALUES (1, 2, ?, 10, NULL)`, strings.Repeat("a", 32))
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			env := newRecoveryTestEnv(t)
			store := env.open(nil)
			env.mustClose(store)
			env.withDB(test.damage)
			options := env.options()
			options.RequireRecovered = true
			if _, err := Open(env.ctx, options); !errors.Is(err, selfhost.ErrRecoveryStateInvalid) {
				t.Fatalf("open error = %v, want %v", err, selfhost.ErrRecoveryStateInvalid)
			}
			if _, err := env.prepare(); !errors.Is(err, selfhost.ErrRecoveryStateInvalid) {
				t.Fatalf("prepare error = %v, want %v", err, selfhost.ErrRecoveryStateInvalid)
			}
		})
	}
}

func TestPrepareRestoreRequiresTheOriginalInstance(t *testing.T) {
	env := newRecoveryTestEnv(t)
	store := env.open(nil)
	insertRecoveryOwner(t, store.db, recoveryOwnerA, true, false, env.clock.Now())
	env.mustClose(store)
	env.withDB(func(db *sql.DB) { mustExec(t, db, `DELETE FROM instance`) })
	if _, err := env.prepare(); !errors.Is(err, selfhost.ErrRecoveryStateInvalid) {
		t.Fatalf("prepare error = %v, want %v", err, selfhost.ErrRecoveryStateInvalid)
	}
	env.withDB(func(db *sql.DB) {
		if count := mustCount(t, db, `SELECT COUNT(*) FROM instance`); count != 0 {
			t.Fatal("preparation invented an instance identity")
		}
		status, err := readRecoveryStatus(env.ctx, db)
		if err != nil || status.Pending {
			t.Fatalf("recovery state = %+v, %v", status, err)
		}
		if count := mustCount(t, db, `SELECT COUNT(*) FROM audit_log`); count != 0 {
			t.Fatalf("%d audit rows remain", count)
		}
	})
}

func TestPreserveSkipsInstanceAndFlagSeeding(t *testing.T) {
	env := newRecoveryTestEnv(t)
	store := env.open(nil)
	env.mustClose(store)
	env.withDB(func(db *sql.DB) { mustExec(t, db, `DELETE FROM instance`) })
	preserved := env.open(func(options *Options) {
		options.Preserve = true
		options.Flags = []selfhost.FlagDefinition{{Key: "sync", Description: "Sync", Default: true}}
	})
	if _, err := preserved.Instance(env.ctx); !errors.Is(err, selfhost.ErrNotFound) {
		t.Fatalf("preserved instance error = %v, want %v", err, selfhost.ErrNotFound)
	}
	flags, err := preserved.Flags(env.ctx)
	if err != nil || len(flags) != 0 {
		t.Fatalf("preserved flags = %+v, %v", flags, err)
	}
}
