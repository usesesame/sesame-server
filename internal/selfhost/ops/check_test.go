package ops

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"usesesame.app/backend/internal/selfhost"
	"usesesame.app/backend/internal/selfhost/secrets"
)

func wantInvalid(t *testing.T, err error, contains string) {
	t.Helper()
	if !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("err = %v, want an invalid archive error", err)
	}
	if contains != "" && !strings.Contains(err.Error(), contains) {
		t.Fatalf("err = %v, want text %q", err, contains)
	}
}

func TestCheckRejectsTruncatedArchive(t *testing.T) {
	inst := newInstance(t, "Ada", true)
	archive := inst.backup(t)
	data := readFile(t, archive)
	for _, cut := range []int{0, 10, 700, len(data) / 2, len(data) - 1600} {
		path := filepath.Join(t.TempDir(), "cut.tar")
		writeFile(t, path, data[:cut], 0o600)
		if _, err := Check(context.Background(), path); err == nil {
			t.Fatalf("archive cut at %d of %d bytes passed", cut, len(data))
		}
	}
}

func TestCheckRejectsFlippedBytes(t *testing.T) {
	inst := newInstance(t, "Ada", true)
	archive := inst.backup(t)
	data := readFile(t, archive)
	markers := map[string][]byte{
		"database": []byte("SQLite format 3"),
		"config":   []byte(`"logLevel"`),
	}
	for name, marker := range markers {
		offset := bytes.Index(data, marker)
		if offset < 0 {
			t.Fatalf("%s marker not found", name)
		}
		flipped := append([]byte(nil), data...)
		flipped[offset+len(marker)+3] ^= 0x01
		path := filepath.Join(t.TempDir(), "flipped.tar")
		writeFile(t, path, flipped, 0o600)
		_, err := Check(context.Background(), path)
		wantInvalid(t, err, "SHA-256")
	}
	secretBytes := readFile(t, filepath.Join(secrets.Dir(inst.dir), secrets.AdminKeyFile))
	offset := bytes.Index(data, secretBytes)
	if offset < 0 {
		t.Fatal("admin key not found in archive")
	}
	flipped := append([]byte(nil), data...)
	flipped[offset] ^= 0x80
	path := filepath.Join(t.TempDir(), "flipped-secret.tar")
	writeFile(t, path, flipped, 0o600)
	_, err := Check(context.Background(), path)
	wantInvalid(t, err, "SHA-256")
}

func TestCheckRejectsDataAfterEnd(t *testing.T) {
	inst := newInstance(t, "Ada", false)
	data := append(readFile(t, inst.backup(t)), []byte("trailing")...)
	path := filepath.Join(t.TempDir(), "trailing.tar")
	writeFile(t, path, data, 0o600)
	_, err := Check(context.Background(), path)
	wantInvalid(t, err, "follows the end")
}

func TestCheckRejectsArchiveThatIsALink(t *testing.T) {
	inst := newInstance(t, "Ada", false)
	archive := inst.backup(t)
	link := filepath.Join(t.TempDir(), "link.tar")
	if err := os.Symlink(archive, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Check(context.Background(), link); err == nil {
		t.Fatal("symlinked archive was read")
	}
}

func TestCheckRejectsTraversalMembers(t *testing.T) {
	inst := newInstance(t, "Ada", false)
	u := unpack(t, inst.backup(t))
	manifest := u.manifest
	escape := filepath.Join(t.TempDir(), "escape")
	for _, name := range []string{"../escape", "secrets/../../escape", "/etc/passwd", "secrets/../sesame.db", "./sesame.db", "secrets//signing.key", "sesame.db/../x"} {
		extra := rawEntry{header: tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o600}, body: []byte("owned")}
		entries := []rawEntry{extra}
		path := writeRaw(t, manifest, entries)
		_, err := Check(context.Background(), path)
		if err == nil {
			t.Fatalf("member %q was accepted", name)
		}
	}

	tampered := manifest
	tampered.Members = append(append([]ManifestMember{}, manifest.Members...), ManifestMember{Name: "../escape", Size: 5, SHA256: strings.Repeat("a", 64)})
	path := writeRaw(t, tampered, nil)
	_, err := Check(context.Background(), path)
	wantInvalid(t, err, "not allowed")

	tampered.Members = append(append([]ManifestMember{}, manifest.Members...), ManifestMember{Name: "secrets/extra.key", Size: 32, SHA256: strings.Repeat("a", 64)})
	_, err = Check(context.Background(), writeRaw(t, tampered, nil))
	wantInvalid(t, err, "not allowed")

	if _, err := os.Lstat(escape); err == nil {
		t.Fatal("a file was created outside the working directory")
	}
	parent := filepath.Dir(filepath.Dir(escape))
	if _, err := os.Lstat(filepath.Join(parent, "escape")); err == nil {
		t.Fatal("a file escaped to the parent directory")
	}
}

func rawEntries(t *testing.T, u unpacked, mutate func(name string, entry *rawEntry)) []rawEntry {
	t.Helper()
	var entries []rawEntry
	for _, member := range u.manifest.Members {
		entry := rawEntry{header: tar.Header{Name: member.Name, Typeflag: tar.TypeReg, Mode: 0o600}, body: readFile(t, u.path(member.Name))}
		if mutate != nil {
			mutate(member.Name, &entry)
		}
		entries = append(entries, entry)
	}
	return entries
}

func TestCheckAcceptsHandBuiltValidArchive(t *testing.T) {
	inst := newInstance(t, "Ada", true)
	u := unpack(t, inst.backup(t))
	path := writeRaw(t, u.manifest, rawEntries(t, u, nil))
	if _, err := Check(context.Background(), path); err != nil {
		t.Fatalf("control archive rejected: %v", err)
	}
}

func TestCheckRejectsLinkMembers(t *testing.T) {
	inst := newInstance(t, "Ada", true)
	u := unpack(t, inst.backup(t))
	for _, kind := range []byte{tar.TypeSymlink, tar.TypeLink, tar.TypeDir, tar.TypeChar, tar.TypeFifo} {
		path := writeRaw(t, u.manifest, rawEntries(t, u, func(name string, entry *rawEntry) {
			if name == DatabaseName {
				entry.header.Typeflag = kind
				entry.header.Linkname = "/etc/passwd"
				entry.body = nil
			}
		}))
		_, err := Check(context.Background(), path)
		wantInvalid(t, err, "")
	}
	path := writeRaw(t, u.manifest, rawEntries(t, u, func(name string, entry *rawEntry) {
		if name == "secrets/"+secrets.SigningKeyFile {
			entry.header.Typeflag = tar.TypeSymlink
			entry.header.Linkname = "../../outside"
			entry.body = nil
		}
	}))
	_, err := Check(context.Background(), path)
	wantInvalid(t, err, "not a regular file")
}

func TestCheckRejectsOversizedMembers(t *testing.T) {
	inst := newInstance(t, "Ada", true)
	archive := inst.backup(t)
	size := int64(len(readFile(t, filepath.Join(inst.dir, DatabaseName))))
	_, err := Check(context.Background(), archive, WithLimits(Limits{MaxDatabaseBytes: 1024, MaxConfigBytes: 1 << 20}))
	if err == nil {
		t.Fatal("database over the limit passed")
	}
	wantInvalid(t, err, "")
	_, err = Check(context.Background(), archive, WithLimits(Limits{MaxDatabaseBytes: size * 4, MaxConfigBytes: 4}))
	wantInvalid(t, err, "over the limit")

	u := unpack(t, inst.backup(t))
	path := writeRaw(t, u.manifest, rawEntries(t, u, func(name string, entry *rawEntry) {
		if name == ConfigName {
			entry.body = append(entry.body, bytes.Repeat([]byte("x"), 1000)...)
		}
	}))
	_, err = Check(context.Background(), path)
	wantInvalid(t, err, "but the manifest says")

	big := u.manifest
	big.Members = append([]ManifestMember{}, u.manifest.Members...)
	for index := range big.Members {
		if big.Members[index].Name == secrets.DirName+"/"+secrets.SigningKeyFile {
			big.Members[index].Size = 1 << 30
		}
	}
	_, err = Check(context.Background(), writeRaw(t, big, nil))
	wantInvalid(t, err, "must be 32 bytes")

	huge := filepath.Join(t.TempDir(), "huge.tar")
	file, err := os.Create(huge)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(1 << 20); err != nil {
		t.Fatal(err)
	}
	file.Close()
	_, err = Check(context.Background(), huge, WithLimits(Limits{MaxDatabaseBytes: 10, MaxConfigBytes: 10}))
	wantInvalid(t, err, "over the limit")
}

func TestCheckRejectsManifestMismatches(t *testing.T) {
	inst := newInstance(t, "Ada", true)
	u := unpack(t, inst.backup(t))
	cases := map[string]struct {
		mutate func(*Manifest)
		text   string
	}{
		"wrong digest": {func(m *Manifest) { m.Members[0].SHA256 = strings.Repeat("0", 64) }, "SHA-256"},
		"wrong size":   {func(m *Manifest) { m.Members[0].Size++ }, "but the manifest says"},
		"bad digest":   {func(m *Manifest) { m.Members[0].SHA256 = "XYZ" }, "malformed digest"},
		"upper digest": {func(m *Manifest) { m.Members[0].SHA256 = strings.ToUpper(m.Members[0].SHA256) }, "malformed digest"},
		"wrong format": {func(m *Manifest) { m.Format = "other" }, "format"},
		"no schema":    {func(m *Manifest) { m.SchemaVersion = 0 }, "schema version"},
		"no time":      {func(m *Manifest) { m.CreatedAt = zeroTime() }, "created time"},
		"missing db":   {func(m *Manifest) { m.Members = m.Members[1:] }, "missing from the manifest"},
		"duplicate":    {func(m *Manifest) { m.Members = append(m.Members, m.Members[0]) }, "twice"},
		"no members":   {func(m *Manifest) { m.Members = nil }, "members"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			manifest := u.manifest
			manifest.Members = append([]ManifestMember{}, u.manifest.Members...)
			tc.mutate(&manifest)
			path := writeRaw(t, manifest, rawEntries(t, u, nil))
			_, err := Check(context.Background(), path)
			wantInvalid(t, err, tc.text)
		})
	}
}

func TestCheckRejectsUnknownManifestFields(t *testing.T) {
	inst := newInstance(t, "Ada", false)
	u := unpack(t, inst.backup(t))
	type extended struct {
		Manifest
		Extra string `json:"extra"`
	}
	path := writeRaw(t, extended{Manifest: u.manifest, Extra: "x"}, rawEntries(t, u, nil))
	_, err := Check(context.Background(), path)
	wantInvalid(t, err, "manifest is not valid")
}

func TestCheckRejectsMemberMissingFromArchive(t *testing.T) {
	inst := newInstance(t, "Ada", false)
	u := unpack(t, inst.backup(t))
	entries := rawEntries(t, u, nil)
	path := writeRaw(t, u.manifest, entries[:len(entries)-1])
	_, err := Check(context.Background(), path)
	wantInvalid(t, err, "missing from the archive")

	duplicate := append(entries, entries[0])
	_, err = Check(context.Background(), writeRaw(t, u.manifest, duplicate))
	wantInvalid(t, err, "twice")

	notManifestFirst := writeRawNoManifest(t, entries)
	_, err = Check(context.Background(), notManifestFirst)
	wantInvalid(t, err, "first member")
}

func writeRawNoManifest(t *testing.T, entries []rawEntry) string {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for _, entry := range entries {
		header := entry.header
		header.Size = int64(len(entry.body))
		if err := writer.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(entry.body); err != nil {
			t.Fatal(err)
		}
	}
	_ = writer.Close()
	path := filepath.Join(t.TempDir(), "no-manifest.tar")
	writeFile(t, path, buffer.Bytes(), 0o600)
	return path
}

func TestCheckRejectsNewerSchemaThanBinary(t *testing.T) {
	inst := newInstance(t, "Ada", false)
	u := unpack(t, inst.backup(t))
	mustSQL(t, u.path(DatabaseName), `INSERT INTO schema_migrations (version, name, applied_at) VALUES (9999, 'future', 1)`)
	path := u.repack(t, func(m *Manifest) { m.SchemaVersion = 9999 })
	_, err := Check(context.Background(), path)
	if !errors.Is(err, selfhost.ErrSchemaTooNew) {
		t.Fatalf("err = %v, want a schema too new error", err)
	}
	var detail selfhost.SchemaTooNewError
	if !errors.As(err, &detail) || detail.Stored != 9999 {
		t.Fatalf("detail = %+v", detail)
	}

	relabelled := u.repack(t, func(m *Manifest) { m.SchemaVersion = 1 })
	_, err = Check(context.Background(), relabelled)
	if !errors.Is(err, selfhost.ErrSchemaTooNew) {
		t.Fatalf("a newer database relabelled as older err = %v", err)
	}

	mustSQL(t, u.path(DatabaseName), `DELETE FROM schema_migrations WHERE version = 9999`)
	older := u.repack(t, func(m *Manifest) { m.SchemaVersion = 9999 })
	_, err = Check(context.Background(), older)
	if !errors.Is(err, selfhost.ErrSchemaTooNew) {
		t.Fatalf("a manifest claiming a newer schema err = %v", err)
	}
}

func TestCheckRejectsManifestSchemaDifferentFromDatabase(t *testing.T) {
	current := currentSchema(t)
	setSupportedForTest(t, current+2)
	inst := newInstance(t, "Ada", false)
	u := unpack(t, inst.backup(t))
	path := u.repack(t, func(m *Manifest) { m.SchemaVersion = current + 1 })
	_, err := Check(context.Background(), path)
	wantInvalid(t, err, fmt.Sprintf("manifest says schema version %d", current+1))
}

func TestCheckDetectsSecretsFromAnotherInstance(t *testing.T) {
	inst := newInstance(t, "Ada", false)
	other := newInstance(t, "Bea", false)
	u := unpack(t, inst.backup(t))
	foreign := readFile(t, filepath.Join(secrets.Dir(other.dir), secrets.AdminKeyFile))
	writeFile(t, u.path("secrets/"+secrets.AdminKeyFile), foreign, 0o600)
	_, err := Check(context.Background(), u.repack(t, nil))
	if !errors.Is(err, ErrSecretsMismatch) {
		t.Fatalf("err = %v, want a secrets mismatch", err)
	}
	if strings.Contains(err.Error(), string(foreign)) {
		t.Fatal("error leaks key material")
	}

	signing := readFile(t, filepath.Join(secrets.Dir(other.dir), secrets.SigningKeyFile))
	u2 := unpack(t, inst.backup(t))
	writeFile(t, u2.path("secrets/"+secrets.SigningKeyFile), signing, 0o600)
	if _, err := Check(context.Background(), u2.repack(t, nil)); err != nil {
		t.Fatalf("a different signing key does not touch stored secrets, err = %v", err)
	}
}

func TestCheckRejectsUnusableSecrets(t *testing.T) {
	inst := newInstance(t, "Ada", false)
	for name, value := range map[string][]byte{"all zero": make([]byte, 32)} {
		u := unpack(t, inst.backup(t))
		writeFile(t, u.path("secrets/"+secrets.IPPepperFile), value, 0o600)
		_, err := Check(context.Background(), u.repack(t, nil))
		if err == nil {
			t.Fatalf("%s secret passed", name)
		}
		wantInvalid(t, err, "secrets are not usable")
	}
}

func TestCheckFreshInstanceWithoutOwnerStillPasses(t *testing.T) {
	inst := newInstance(t, "Ada", false)
	u := unpack(t, inst.backup(t))
	mustSQL(t, u.path(DatabaseName), `UPDATE owners SET totp_secret = NULL, password_hash = NULL`)
	report, err := Check(context.Background(), u.repack(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	if report.SecretsVerified || report.OwnersVerified != 0 {
		t.Fatalf("report = %+v", report)
	}
}

func TestCheckDetectsAuditTampering(t *testing.T) {
	inst := newInstance(t, "Ada", false)
	u := unpack(t, inst.backup(t))
	mustSQL(t, u.path(DatabaseName), `DROP TRIGGER audit_log_no_update`)
	mustSQL(t, u.path(DatabaseName), `UPDATE audit_log SET target = 'forged' WHERE seq = 1`)
	_, err := Check(context.Background(), u.repack(t, nil))
	if !errors.Is(err, ErrDatabaseDamaged) || !strings.Contains(err.Error(), "audit chain breaks at row 1") {
		t.Fatalf("err = %v", err)
	}

	u2 := unpack(t, inst.backup(t))
	mustSQL(t, u2.path(DatabaseName), `DROP TRIGGER audit_log_no_delete`)
	mustSQL(t, u2.path(DatabaseName), `DELETE FROM audit_log WHERE seq = 2`)
	_, err = Check(context.Background(), u2.repack(t, nil))
	if !errors.Is(err, ErrDatabaseDamaged) || !strings.Contains(err.Error(), "audit chain") {
		t.Fatalf("deleted row err = %v", err)
	}
}

func TestCheckDetectsDamagedDatabase(t *testing.T) {
	inst := newInstance(t, "Ada", false)
	u := unpack(t, inst.backup(t))
	path := u.path(DatabaseName)
	data := readFile(t, path)
	if len(data) < 4096*8 {
		t.Fatalf("database is only %d bytes", len(data))
	}
	for offset := 4096 * 2; offset < 4096*6; offset++ {
		data[offset] = 0xFF
	}
	writeFile(t, path, data, 0o600)
	_, err := Check(context.Background(), u.repack(t, nil))
	if !errors.Is(err, ErrDatabaseDamaged) {
		t.Fatalf("err = %v", err)
	}

	u2 := unpack(t, inst.backup(t))
	writeFile(t, u2.path(DatabaseName), bytes.Repeat([]byte("not a database "), 1000), 0o600)
	_, err = Check(context.Background(), u2.repack(t, nil))
	if err == nil {
		t.Fatal("garbage database passed")
	}
}

func TestCheckOfOlderSchemaMigratesACopyOnly(t *testing.T) {
	current := currentSchema(t)
	setSupportedForTest(t, current+1)
	inst := newInstance(t, "Ada", false)
	archive := inst.backup(t)
	before := fileDigest(t, archive)
	report, err := Check(context.Background(), archive)
	if err != nil {
		t.Fatal(err)
	}
	if !report.NeedsMigration || report.SchemaVersion != current || report.Supported != current+1 {
		t.Fatalf("report = %+v", report)
	}
	if fileDigest(t, archive) != before {
		t.Fatal("check changed the archive")
	}
}

func TestCheckLeavesNoWorkFiles(t *testing.T) {
	inst := newInstance(t, "Ada", true)
	archive := inst.backup(t)
	dir := filepath.Dir(archive)
	if _, err := Check(context.Background(), archive); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("work files left behind: %v", entries)
	}
	bad := filepath.Join(dir, "bad.tar")
	writeFile(t, bad, []byte("nope"), 0o600)
	_, _ = Check(context.Background(), bad)
	entries, _ = os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatalf("work files left behind after a failed check: %v", entries)
	}
}

func withLegacyRegularTypeflag(t *testing.T, archive []byte, name string) []byte {
	t.Helper()
	patched := bytes.Clone(archive)
	found := false
	for offset := 0; offset+512 <= len(patched) && patched[offset] != 0; {
		block := patched[offset : offset+512]
		size, err := strconv.ParseInt(strings.Trim(string(block[124:136]), "\x00 "), 8, 64)
		if err != nil {
			t.Fatal(err)
		}
		if string(bytes.TrimRight(block[:100], "\x00")) == name {
			block[156] = 0
			sum := 0
			for index, value := range block {
				if index >= 148 && index < 156 {
					value = ' '
				}
				sum += int(value)
			}
			copy(block[148:156], fmt.Sprintf("%06o\x00 ", sum))
			found = true
		}
		offset += 512 + int((size+511)/512*512)
	}
	if !found {
		t.Fatalf("member %q is not in the archive", name)
	}
	return patched
}

func TestCheckAcceptsLegacyRegularTypeflag(t *testing.T) {
	inst := newInstance(t, "Ada", true)
	u := unpack(t, inst.backup(t))
	raw := readFile(t, writeRaw(t, u.manifest, rawEntries(t, u, nil)))
	path := filepath.Join(t.TempDir(), "legacy.tar")
	writeFile(t, path, withLegacyRegularTypeflag(t, raw, DatabaseName), 0o600)
	reader := tar.NewReader(bytes.NewReader(readFile(t, path)))
	for {
		header, err := reader.Next()
		if err != nil {
			t.Fatalf("the patched archive has no %s member: %v", DatabaseName, err)
		}
		if header.Name == DatabaseName {
			if header.Typeflag != tar.TypeReg {
				t.Fatalf("the reader reports typeflag %q for a NUL typeflag, want %q", header.Typeflag, tar.TypeReg)
			}
			break
		}
	}
	if _, err := Check(context.Background(), path); err != nil {
		t.Fatalf("an archive with a NUL regular file typeflag was rejected: %v", err)
	}
}

func tarBlock(name string, typeflag byte, size int) []byte {
	block := make([]byte, 512)
	copy(block[0:100], name)
	copy(block[100:108], "0000600\x00")
	copy(block[108:116], "0000000\x00")
	copy(block[116:124], "0000000\x00")
	copy(block[124:136], fmt.Sprintf("%011o\x00", size))
	copy(block[136:148], "00000000000\x00")
	copy(block[148:156], "        ")
	block[156] = typeflag
	copy(block[257:265], "ustar\x0000")
	sum := 0
	for _, value := range block {
		sum += int(value)
	}
	copy(block[148:156], fmt.Sprintf("%06o\x00 ", sum))
	return block
}

func paxRecords(records [][2]string) []byte {
	var out []byte
	for _, record := range records {
		body := " " + record[0] + "=" + record[1] + "\n"
		length := len(body) + 1
		for len(strconv.Itoa(length))+len(body) != length {
			length = len(strconv.Itoa(length)) + len(body)
		}
		out = append(out, []byte(strconv.Itoa(length)+body)...)
	}
	return out
}

func padBlock(data []byte) []byte {
	if remainder := len(data) % 512; remainder != 0 {
		data = append(data, make([]byte, 512-remainder)...)
	}
	return data
}

func sparseArchive(t *testing.T, u unpacked, realSize int64, records [][2]string) string {
	t.Helper()
	zeros := sha256.Sum256(make([]byte, realSize))
	manifest := u.manifest
	manifest.Members = append([]ManifestMember{}, u.manifest.Members...)
	for index := range manifest.Members {
		if manifest.Members[index].Name == DatabaseName {
			manifest.Members[index].Size = realSize
			manifest.Members[index].SHA256 = hex.EncodeToString(zeros[:])
		}
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var buffer bytes.Buffer
	regular := func(name string, body []byte) {
		buffer.Write(tarBlock(name, tar.TypeReg, len(body)))
		buffer.Write(padBlock(append([]byte{}, body...)))
	}
	regular(ManifestName, encoded)
	for _, member := range u.manifest.Members {
		body := readFile(t, u.path(member.Name))
		if member.Name != DatabaseName {
			regular(member.Name, body)
			continue
		}
		extended := paxRecords(records)
		buffer.Write(tarBlock("PaxHeader/"+member.Name, tar.TypeXHeader, len(extended)))
		buffer.Write(padBlock(extended))
		regular("GNUSparseFile.0/"+member.Name, padBlock([]byte("0\n")))
	}
	buffer.Write(make([]byte, 1024))
	path := filepath.Join(t.TempDir(), "sparse.tar")
	writeFile(t, path, buffer.Bytes(), 0o600)
	return path
}

func TestExtractRefusesSparseMembers(t *testing.T) {
	inst := newInstance(t, "Ada", true)
	u := unpack(t, inst.backup(t))
	const realSize = 8 << 20
	limits := Limits{MaxDatabaseBytes: 16 << 20, MaxConfigBytes: 1 << 20}
	size := strconv.Itoa(realSize)
	path := sparseArchive(t, u, realSize, [][2]string{{"GNU.sparse.major", "1"}, {"GNU.sparse.minor", "0"}, {"GNU.sparse.name", DatabaseName}, {"GNU.sparse.realsize", size}})
	destination := t.TempDir()
	_, err := extractArchive(path, destination, limits)
	wantInvalid(t, err, "sparse file")
	if info, statErr := os.Stat(filepath.Join(destination, DatabaseName)); statErr == nil && info.Size() > 0 {
		t.Fatalf("a sparse member wrote %d bytes", info.Size())
	}
	_, err = Check(context.Background(), path, WithLimits(limits))
	wantInvalid(t, err, "sparse file")
}
