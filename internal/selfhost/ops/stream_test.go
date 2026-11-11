package ops

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"usesesame.app/backend/internal/selfhost/secrets"
)

type copySnapshotter struct {
	source string
}

func (s copySnapshotter) Backup(ctx context.Context, destination string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return copyFileLimited(s.source, destination, 1<<62)
}

type failingWriter struct {
	err error
}

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

type cancelingWriter struct {
	cancel context.CancelFunc
	buffer bytes.Buffer
	writes int
}

func (w *cancelingWriter) Write(p []byte) (int, error) {
	if w.writes == 0 {
		w.writes++
		n, err := w.buffer.Write(p)
		w.cancel()
		return n, err
	}
	return w.buffer.Write(p)
}

func streamFixture(t *testing.T) (*instance, unpacked, []Option) {
	t.Helper()
	inst := newInstance(t, "Ada", true)
	u := unpack(t, inst.backup(t))
	fixed := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	return inst, u, []Option{WithSnapshotter(copySnapshotter{source: u.path(DatabaseName)}), WithClock(func() time.Time { return fixed })}
}

func emptyWorkDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "work")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func assertEmptyDir(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("%s holds %v after the operation", dir, entries)
	}
}

func TestBackupToMatchesTheFileArchive(t *testing.T) {
	ctx := context.Background()
	inst, _, options := streamFixture(t)
	file := filepath.Join(t.TempDir(), "backup.tar")
	fileManifest, err := Backup(ctx, inst.dir, file, options...)
	if err != nil {
		t.Fatal(err)
	}
	var stream bytes.Buffer
	streamManifest, err := BackupTo(ctx, inst.dir, &stream, options...)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fileManifest, streamManifest) {
		t.Fatalf("stream manifest = %+v, file manifest = %+v", streamManifest, fileManifest)
	}
	if !bytes.Equal(readFile(t, file), stream.Bytes()) {
		t.Fatal("the streamed archive differs from the file archive")
	}
	work := emptyWorkDir(t)
	streamReport, err := CheckReader(ctx, bytes.NewReader(stream.Bytes()), WithWorkDir(work))
	if err != nil {
		t.Fatal(err)
	}
	fileReport, err := Check(ctx, file)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fileReport, streamReport) {
		t.Fatalf("stream report = %+v, file report = %+v", streamReport, fileReport)
	}
	if !streamReport.SecretsVerified || !streamReport.HasConfig {
		t.Fatalf("report = %+v", streamReport)
	}
	assertEmptyDir(t, work)
}

func TestBackupToRequiresAnOutput(t *testing.T) {
	if _, err := BackupTo(context.Background(), t.TempDir(), nil); err == nil {
		t.Fatal("a nil output was accepted")
	}
}

func TestBackupToStopsBeforeOutputWhenVerificationDoesNotFinish(t *testing.T) {
	inst, _, options := streamFixture(t)
	failure := errors.New("verification did not finish")
	hook := func(step string) error {
		if step == "backup-before-publish" {
			return failure
		}
		return nil
	}
	work := emptyWorkDir(t)
	var stream bytes.Buffer
	if _, err := BackupTo(context.Background(), inst.dir, &stream, append(options, withHook(hook), WithWorkDir(work))...); !errors.Is(err, failure) {
		t.Fatalf("err = %v", err)
	}
	if stream.Len() != 0 {
		t.Fatalf("a failed verification wrote %d bytes", stream.Len())
	}
	assertEmptyDir(t, work)
}

func TestBackupToRemovesPrivateWorkAndPropagatesWriterFailures(t *testing.T) {
	ctx := context.Background()
	inst, _, options := streamFixture(t)
	work := emptyWorkDir(t)
	failure := errors.New("the consumer closed")
	if _, err := BackupTo(ctx, inst.dir, failingWriter{err: failure}, append(options, WithWorkDir(work))...); !errors.Is(err, failure) {
		t.Fatalf("err = %v", err)
	}
	assertEmptyDir(t, work)

	var stream bytes.Buffer
	if _, err := BackupTo(ctx, inst.dir, &stream, append(options, WithWorkDir(work))...); err != nil {
		t.Fatal(err)
	}
	assertEmptyDir(t, work)
}

func TestBackupToTakesNoInstanceLock(t *testing.T) {
	inst := newInstance(t, "Ada", true)
	lock, err := AcquireInstanceLock(inst.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	var stream bytes.Buffer
	if _, err := BackupTo(context.Background(), inst.dir, &stream); err != nil {
		t.Fatalf("an online snapshot needed the instance lock: %v", err)
	}
	if _, err := CheckReader(context.Background(), bytes.NewReader(stream.Bytes()), WithWorkDir(emptyWorkDir(t))); err != nil {
		t.Fatal(err)
	}
}

func TestBackupToStopsOnCanceledContext(t *testing.T) {
	inst, _, options := streamFixture(t)
	work := emptyWorkDir(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stream bytes.Buffer
	if _, err := BackupTo(ctx, inst.dir, &stream, append(options, WithWorkDir(work))...); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if stream.Len() != 0 {
		t.Fatalf("a canceled backup wrote %d bytes", stream.Len())
	}
	assertEmptyDir(t, work)
}

func TestBackupToStopsWhenTheContextIsCanceledDuringTheStream(t *testing.T) {
	inst, _, options := streamFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	writer := &cancelingWriter{cancel: cancel}
	if _, err := BackupTo(ctx, inst.dir, writer, options...); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if writer.buffer.Len() == 0 {
		t.Fatal("the test never reached the output")
	}
}

func TestBackupToChecksLimitsBeforeAnyOutput(t *testing.T) {
	ctx := context.Background()
	inst, _, options := streamFixture(t)
	file := filepath.Join(t.TempDir(), "backup.tar")
	manifest, err := Backup(ctx, inst.dir, file, options...)
	if err != nil {
		t.Fatal(err)
	}
	sizes := map[string]int64{}
	for _, member := range manifest.Members {
		sizes[member.Name] = member.Size
	}
	dbSize, configSize := sizes[DatabaseName], sizes[ConfigName]
	if dbSize < 1 || configSize < 1 {
		t.Fatalf("sizes = %v", sizes)
	}
	overLimit := []struct {
		name   string
		limits Limits
	}{
		{name: "database one byte over", limits: Limits{MaxDatabaseBytes: dbSize - 1, MaxConfigBytes: configSize}},
		{name: "config one byte over", limits: Limits{MaxDatabaseBytes: dbSize, MaxConfigBytes: configSize - 1}},
		{name: "archive far over", limits: Limits{MaxDatabaseBytes: 1, MaxConfigBytes: 1}},
	}
	for _, tc := range overLimit {
		t.Run(tc.name, func(t *testing.T) {
			work := emptyWorkDir(t)
			var stream bytes.Buffer
			if _, err := BackupTo(ctx, inst.dir, &stream, append(options, WithLimits(tc.limits), WithWorkDir(work))...); err == nil {
				t.Fatal("an over-limit backup succeeded")
			}
			if stream.Len() != 0 {
				t.Fatalf("an over-limit backup wrote %d bytes", stream.Len())
			}
			assertEmptyDir(t, work)
		})
	}
	t.Run("exact limits pass", func(t *testing.T) {
		var stream bytes.Buffer
		limits := Limits{MaxDatabaseBytes: dbSize, MaxConfigBytes: configSize}
		if _, err := BackupTo(ctx, inst.dir, &stream, append(options, WithLimits(limits))...); err != nil {
			t.Fatal(err)
		}
		if stream.Len() == 0 {
			t.Fatal("the archive was empty")
		}
	})
}

func TestDefaultLimitsRemainBounded(t *testing.T) {
	limits := DefaultLimits()
	if limits.MaxDatabaseBytes != 4<<30 {
		t.Fatalf("database limit = %d", limits.MaxDatabaseBytes)
	}
	if limits.MaxConfigBytes != 1<<20 {
		t.Fatalf("config limit = %d", limits.MaxConfigBytes)
	}
}

func TestCheckReaderMatchesFileCheckAndLeavesTheSourceAlone(t *testing.T) {
	inst, _, _ := streamFixture(t)
	archive := inst.backup(t)
	before := takeSnapshot(t, inst.dir)
	work := emptyWorkDir(t)
	report, err := CheckReader(context.Background(), bytes.NewReader(readFile(t, archive)), WithWorkDir(work))
	if err != nil {
		t.Fatal(err)
	}
	fileReport, err := Check(context.Background(), archive)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(report, fileReport) {
		t.Fatalf("stream report = %+v, file report = %+v", report, fileReport)
	}
	assertSnapshot(t, inst.dir, before, "after checking a stream")
	assertEmptyDir(t, work)
}

func TestCheckReaderRequiresAPrivateWorkDirectory(t *testing.T) {
	inst, _, _ := streamFixture(t)
	read := false
	input := readerFunc(func([]byte) (int, error) {
		read = true
		return 0, errors.New("the input must not be read")
	})
	if _, err := CheckReader(context.Background(), input); err == nil || !strings.Contains(err.Error(), "WithWorkDir") {
		t.Fatalf("err = %v", err)
	}
	if read {
		t.Fatal("the input was read without a work directory")
	}
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := CheckReader(context.Background(), bytes.NewReader(readFile(t, inst.backup(t))), WithWorkDir(missing)); err == nil {
		t.Fatal("a missing work directory was used")
	}
}

func TestCheckReaderRejectsTruncatedAndMalformedStreams(t *testing.T) {
	inst, _, _ := streamFixture(t)
	archive := inst.backup(t)
	data := readFile(t, archive)
	for _, cut := range []int{0, 10, 700, len(data) / 2, len(data) - 1600} {
		work := emptyWorkDir(t)
		if _, err := CheckReader(context.Background(), bytes.NewReader(data[:cut]), WithWorkDir(work)); err == nil {
			t.Fatalf("archive cut at %d of %d bytes passed", cut, len(data))
		}
		assertEmptyDir(t, work)
	}
	flipped := bytes.Clone(data)
	offset := bytes.Index(flipped, []byte("SQLite format 3"))
	if offset < 0 {
		t.Fatal("database marker not found")
	}
	flipped[offset+30] ^= 0xFF
	work := emptyWorkDir(t)
	_, err := CheckReader(context.Background(), bytes.NewReader(flipped), WithWorkDir(work))
	wantInvalid(t, err, "")
	assertEmptyDir(t, work)
}

func TestCheckReaderRejectsTrailingAndOversizedInput(t *testing.T) {
	inst, _, _ := streamFixture(t)
	data := append(readFile(t, inst.backup(t)), []byte("trailing")...)
	work := emptyWorkDir(t)
	_, err := CheckReader(context.Background(), bytes.NewReader(data), WithWorkDir(work))
	wantInvalid(t, err, "follows the end")
	assertEmptyDir(t, work)

	limits := Limits{MaxDatabaseBytes: 1, MaxConfigBytes: 1}
	work = emptyWorkDir(t)
	oversized := bytes.NewReader(make([]byte, limits.maxArchiveBytes()+1))
	_, err = CheckReader(context.Background(), oversized, WithWorkDir(work), WithLimits(limits))
	wantInvalid(t, err, "over the limit")
	assertEmptyDir(t, work)
}

func TestCheckReaderRejectsWrongSecrets(t *testing.T) {
	inst, _, _ := streamFixture(t)
	other := newInstance(t, "Bea", false)
	u := unpack(t, inst.backup(t))
	writeFile(t, u.path("secrets/"+secrets.AdminKeyFile), readFile(t, filepath.Join(secrets.Dir(other.dir), secrets.AdminKeyFile)), 0o600)
	work := emptyWorkDir(t)
	_, err := CheckReader(context.Background(), bytes.NewReader(readFile(t, u.repack(t, nil))), WithWorkDir(work))
	if !errors.Is(err, ErrSecretsMismatch) {
		t.Fatalf("err = %v", err)
	}
	assertEmptyDir(t, work)
}

func TestCheckReaderRejectsEscapingMembers(t *testing.T) {
	inst, _, _ := streamFixture(t)
	u := unpack(t, inst.backup(t))
	escape := filepath.Join(t.TempDir(), "escape")
	manifest := u.manifest
	manifest.Members = append(append([]ManifestMember{}, u.manifest.Members...), ManifestMember{Name: "../escape", Size: 5, SHA256: strings.Repeat("a", 64)})
	path := writeRaw(t, manifest, []rawEntry{{header: tar.Header{Name: "../escape", Typeflag: tar.TypeReg, Mode: 0o600}, body: []byte("owned")}})
	work := emptyWorkDir(t)
	_, err := CheckReader(context.Background(), bytes.NewReader(readFile(t, path)), WithWorkDir(work))
	wantInvalid(t, err, "not allowed")
	if _, err := os.Lstat(escape); err == nil {
		t.Fatal("a stream created a file outside its work directory")
	}
	assertEmptyDir(t, work)
}

func TestCheckReaderPreservesReadFailuresAndCancellation(t *testing.T) {
	inst, _, _ := streamFixture(t)
	readFailure := errors.New("input pipe failed")
	reads := 0
	input := readerFunc(func(p []byte) (int, error) {
		if reads > 0 {
			return 0, readFailure
		}
		reads++
		return copy(p, "partial input"), nil
	})
	work := emptyWorkDir(t)
	if _, err := CheckReader(context.Background(), input, WithWorkDir(work)); !errors.Is(err, readFailure) {
		t.Fatalf("err = %v", err)
	}
	assertEmptyDir(t, work)

	read := false
	untouched := readerFunc(func([]byte) (int, error) {
		read = true
		return 0, io.EOF
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	work = emptyWorkDir(t)
	if _, err := CheckReader(ctx, untouched, WithWorkDir(work)); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if read {
		t.Fatal("a canceled check read the input")
	}
	assertEmptyDir(t, work)

	data := readFile(t, inst.backup(t))
	ctx, cancel = context.WithCancel(context.Background())
	reads = 0
	midRead := readerFunc(func(p []byte) (int, error) {
		if reads > 0 {
			return 0, io.EOF
		}
		reads++
		n := copy(p, data)
		cancel()
		return n, nil
	})
	work = emptyWorkDir(t)
	if _, err := CheckReader(ctx, midRead, WithWorkDir(work)); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	assertEmptyDir(t, work)
}

func TestStageBackupInputIsPrivate(t *testing.T) {
	dir := t.TempDir()
	path, err := stageBackupInput(context.Background(), dir, strings.NewReader("archive input"), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	assertMode(t, path, 0o600)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}
