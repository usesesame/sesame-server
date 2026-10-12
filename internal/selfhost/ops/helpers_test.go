package ops

import (
	"archive/tar"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
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
