package ops

import (
	"archive/tar"
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"usesesame.app/backend/internal/authkit"
	"usesesame.app/backend/internal/selfhost"
	"usesesame.app/backend/internal/selfhost/secrets"
	"usesesame.app/backend/internal/selfhost/sqlitestore"
)

func withHook(hook func(step string) error) Option {
	return func(c *config) { c.hook = hook }
}

type instance struct {
	dir   string
	store *sqlitestore.Store
	owner selfhost.Owner
}

func newInstance(t *testing.T, ownerName string, withConfig bool) *instance {
	t.Helper()
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := secrets.LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := sqlitestore.Open(ctx, sqlitestore.Options{Path: filepath.Join(dir, DatabaseName), AdminKey: loaded.AdminEncryptionKey})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	issue, err := store.StartFirstSetup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	details, err := store.SetupDetails(ctx, issue.Token)
	if err != nil {
		t.Fatal(err)
	}
	code, ok := authkit.TOTPCode(details.TOTPSecret, time.Now())
	if !ok {
		t.Fatal("totp code")
	}
	login, err := store.CompleteSetup(ctx, selfhost.CompleteSetupInput{Token: issue.Token, Name: ownerName, Password: "a fictional passphrase 42", Code: code})
	if err != nil {
		t.Fatal(err)
	}
	inst := &instance{dir: dir, store: store, owner: login.Session.Owner}
	by := selfhost.Actor{Kind: selfhost.ActorOwner, ID: inst.owner.ID}
	if _, err := store.CreateMember(ctx, by, "Grace"); err != nil {
		t.Fatal(err)
	}
	if withConfig {
		writeFile(t, filepath.Join(dir, ConfigName), []byte(`{"logLevel":"debug"}`), 0o600)
	}
	return inst
}

func (i *instance) by() selfhost.Actor {
	return selfhost.Actor{Kind: selfhost.ActorOwner, ID: i.owner.ID}
}

func (i *instance) backup(t *testing.T, options ...Option) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "backup.tar")
	if _, err := Backup(context.Background(), i.dir, out, options...); err != nil {
		t.Fatal(err)
	}
	return out
}

func writeFile(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

type unpacked struct {
	dir      string
	manifest Manifest
}

func unpack(t *testing.T, archive string) unpacked {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "unpacked")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest, err := extractArchive(archive, dir, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	return unpacked{dir: dir, manifest: manifest}
}

func (u unpacked) path(name string) string { return filepath.Join(u.dir, filepath.FromSlash(name)) }

func (u unpacked) repack(t *testing.T, mutate func(*Manifest)) string {
	t.Helper()
	manifest := u.manifest
	manifest.Members = nil
	sources := map[string]string{}
	for _, name := range append(requiredMembers(), ConfigName) {
		path := u.path(name)
		if _, err := os.Lstat(path); err != nil {
			continue
		}
		digest, size, err := hashFile(path)
		if err != nil {
			t.Fatal(err)
		}
		manifest.Members = append(manifest.Members, ManifestMember{Name: name, Size: size, SHA256: digest})
		sources[name] = path
	}
	if mutate != nil {
		mutate(&manifest)
	}
	out := filepath.Join(t.TempDir(), "repacked.tar")
	file, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := writeArchive(file, manifest, sources); err != nil {
		t.Fatal(err)
	}
	return out
}

type rawEntry struct {
	header tar.Header
	body   []byte
}

func writeRaw(t *testing.T, manifest any, entries []rawEntry) string {
	t.Helper()
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	all := append([]rawEntry{{header: tar.Header{Name: ManifestName, Typeflag: tar.TypeReg, Mode: 0o600}, body: encoded}}, entries...)
	for _, entry := range all {
		header := entry.header
		if header.Typeflag == tar.TypeReg && header.Size == 0 {
			header.Size = int64(len(entry.body))
		}
		if err := writer.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(entry.body); err != nil && header.Typeflag == tar.TypeReg {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "raw.tar")
	writeFile(t, out, buffer.Bytes(), 0o600)
	return out
}

func mustSQL(t *testing.T, path, statement string, args ...any) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(statement, args...); err != nil {
		t.Fatal(err)
	}
}

func fileDigest(t *testing.T, path string) string {
	t.Helper()
	digest, _, err := hashFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func assertRestored(t *testing.T, dir string, want snapshot, label string) {
	t.Helper()
	got := takeSnapshot(t, dir)
	if got.config != want.config {
		t.Fatalf("%s: config differs from the expected state", label)
	}
	for name, digest := range want.secrets {
		if got.secrets[name] != digest {
			t.Fatalf("%s: secret %s differs", label, name)
		}
	}
	if got.database == want.database {
		t.Fatalf("%s: the installed database is byte-identical to the archive, so no recovery state was written", label)
	}
	for _, sidecar := range []string{DatabaseName + "-wal", DatabaseName + "-shm"} {
		if _, err := os.Lstat(filepath.Join(dir, sidecar)); err == nil {
			t.Fatalf("%s: %s was left next to the restored database", label, sidecar)
		}
	}
	gotDurable := durableRows(t, filepath.Join(dir, DatabaseName))
	if len(want.durable) == 0 {
		t.Fatalf("%s: the expected state has no durable rows to compare", label)
	}
	for table, rows := range want.durable {
		if !slices.Equal(gotDurable[table], rows) {
			t.Fatalf("%s: table %s differs from the archive", label, table)
		}
	}
	for table := range gotDurable {
		if _, ok := want.durable[table]; !ok {
			t.Fatalf("%s: table %s is not in the archive", label, table)
		}
	}
	gotAudit := tableRows(t, filepath.Join(dir, DatabaseName), "audit_log")
	wantAudit := want.audit
	if len(gotAudit) != len(wantAudit)+1 || !slices.Equal(gotAudit[:len(wantAudit)], wantAudit) || !strings.Contains(gotAudit[len(wantAudit)], "recovery.pending") {
		t.Fatalf("%s: the audit log is not the archive's log followed by one recovery.pending entry", label)
	}
}

var restoreRewrittenTables = map[string]bool{"recovery": true, "sessions": true, "setup_tokens": true, "pairings": true, "sync_pairings": true, "audit_log": true}

func durableRows(t *testing.T, path string) map[string][]string {
	t.Helper()
	db := openImmutable(t, path)
	defer db.Close()
	out := map[string][]string{}
	for _, table := range durableTables(t, db) {
		out[table] = queryRows(t, db, table)
	}
	return out
}

func durableTables(t *testing.T, db *sql.DB) []string {
	t.Helper()
	names, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer names.Close()
	var tables []string
	for names.Next() {
		var name string
		if err := names.Scan(&name); err != nil {
			t.Fatal(err)
		}
		if !restoreRewrittenTables[name] {
			tables = append(tables, name)
		}
	}
	if err := names.Err(); err != nil {
		t.Fatal(err)
	}
	return tables
}

func tableRows(t *testing.T, path, table string) []string {
	t.Helper()
	db := openImmutable(t, path)
	defer db.Close()
	return queryRows(t, db, table)
}

func openImmutable(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&immutable=1")
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func queryRows(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query(`SELECT * FROM "` + table + `" ORDER BY rowid`)
	if err != nil {
		rows, err = db.Query(`SELECT * FROM "` + table + `"`)
		if err != nil {
			t.Fatal(err)
		}
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for index := range values {
			pointers[index] = &values[index]
		}
		if err := rows.Scan(pointers...); err != nil {
			t.Fatal(err)
		}
		fields := make([]string, len(values))
		for index, value := range values {
			if bytesValue, ok := value.([]byte); ok {
				fields[index] = hex.EncodeToString(bytesValue)
				continue
			}
			fields[index] = fmt.Sprint(value)
		}
		out = append(out, strings.Join(fields, "|"))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func zeroTime() (zero time.Time) { return }

func currentSchema(t *testing.T) int {
	t.Helper()
	value, err := supportedSchema(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func setSupportedForTest(t *testing.T, value int) {
	t.Helper()
	if _, err := supportedSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	supportedOnce.Lock()
	previous := supportedValue
	supportedValue = value
	supportedOnce.Unlock()
	t.Cleanup(func() {
		supportedOnce.Lock()
		supportedValue = previous
		supportedOnce.Unlock()
	})
}
