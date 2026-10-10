package ops

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidArchive, fmt.Sprintf(format, args...))
}

func writeArchive(w io.Writer, manifest Manifest, sources map[string]string) error {
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	writer := tar.NewWriter(w)
	if err := writeMember(writer, ManifestName, int64(len(encoded)), manifest.CreatedAt, bytes.NewReader(encoded)); err != nil {
		return err
	}
	for _, member := range manifest.Members {
		in, info, err := openRegular(sources[member.Name])
		if err != nil {
			return err
		}
		if info.Size() != member.Size {
			in.Close()
			return fmt.Errorf("%s changed size while the backup was being written", member.Name)
		}
		err = writeMember(writer, member.Name, member.Size, manifest.CreatedAt, in)
		in.Close()
		if err != nil {
			return err
		}
	}
	return writer.Close()
}

func writeMember(writer *tar.Writer, name string, size int64, modTime time.Time, body io.Reader) error {
	header := &tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o600, Size: size, ModTime: modTime.UTC(), Format: tar.FormatUSTAR}
	if err := writer.WriteHeader(header); err != nil {
		return err
	}
	written, err := io.Copy(writer, body)
	if err != nil {
		return err
	}
	if written != size {
		return fmt.Errorf("%s changed size while the backup was being written", name)
	}
	return nil
}

func decodeManifest(raw []byte, limits Limits) (Manifest, error) {
	var manifest Manifest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, invalid("manifest is not valid: %v", err)
	}
	if decoder.More() {
		return Manifest{}, invalid("manifest has trailing data")
	}
	if manifest.Format != FormatVersion {
		return Manifest{}, invalid("format %q is not %q", manifest.Format, FormatVersion)
	}
	if manifest.SchemaVersion < 1 {
		return Manifest{}, invalid("schema version %d is not valid", manifest.SchemaVersion)
	}
	if manifest.CreatedAt.IsZero() {
		return Manifest{}, invalid("created time is missing")
	}
	if len(manifest.Members) == 0 || len(manifest.Members) > maxMembers {
		return Manifest{}, invalid("manifest lists %d members", len(manifest.Members))
	}
	seen := map[string]bool{}
	for _, member := range manifest.Members {
		if !allowedMember(member.Name) {
			return Manifest{}, invalid("member %q is not allowed", member.Name)
		}
		if seen[member.Name] {
			return Manifest{}, invalid("member %q is listed twice", member.Name)
		}
		seen[member.Name] = true
		if err := validMemberSize(member, limits); err != nil {
			return Manifest{}, err
		}
		if !validDigest(member.SHA256) {
			return Manifest{}, invalid("member %q has a malformed digest", member.Name)
		}
	}
	for _, name := range requiredMembers() {
		if !seen[name] {
			return Manifest{}, invalid("member %q is missing from the manifest", name)
		}
	}
	return manifest, nil
}

func validMemberSize(member ManifestMember, limits Limits) error {
	var max int64
	switch member.Name {
	case DatabaseName:
		max = limits.MaxDatabaseBytes
	case ConfigName:
		max = limits.MaxConfigBytes
	default:
		if member.Size != 32 {
			return invalid("member %q must be 32 bytes, not %d", member.Name, member.Size)
		}
		return nil
	}
	if member.Size < 0 || member.Size > max {
		return invalid("member %q is %d bytes, which is over the limit of %d", member.Name, member.Size, max)
	}
	return nil
}

func validDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, character := range value {
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

type zeroTail struct{ reader io.Reader }

func (z zeroTail) verify() error {
	buffer := make([]byte, 32<<10)
	for {
		n, err := z.reader.Read(buffer)
		for _, value := range buffer[:n] {
			if value != 0 {
				return invalid("data follows the end of the archive")
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func extractArchive(path, destination string, limits Limits) (Manifest, error) {
	file, info, err := openRegular(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("backup file cannot be opened: %w", err)
	}
	defer file.Close()
	if info.Size() > limits.maxArchiveBytes() {
		return Manifest{}, invalid("file is %d bytes, which is over the limit of %d", info.Size(), limits.maxArchiveBytes())
	}
	reader := tar.NewReader(io.LimitReader(file, limits.maxArchiveBytes()+1))
	header, err := reader.Next()
	if err != nil {
		return Manifest{}, invalid("first member cannot be read: %v", err)
	}
	if header.Name != ManifestName || !regularHeader(header) {
		return Manifest{}, invalid("first member must be a regular file named %s", ManifestName)
	}
	if header.Size < 1 || header.Size > maxManifestBytes {
		return Manifest{}, invalid("manifest is %d bytes", header.Size)
	}
	raw := make([]byte, header.Size)
	if _, err := io.ReadFull(reader, raw); err != nil {
		return Manifest{}, invalid("manifest cannot be read: %v", err)
	}
	manifest, err := decodeManifest(raw, limits)
	if err != nil {
		return Manifest{}, err
	}
	expected := map[string]ManifestMember{}
	for _, member := range manifest.Members {
		expected[member.Name] = member
	}
	seen := map[string]bool{}
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Manifest{}, invalid("archive cannot be read: %v", err)
		}
		member, listed := expected[header.Name]
		switch {
		case !regularHeader(header):
			return Manifest{}, invalid("member %q is not a regular file, or is a sparse file", header.Name)
		case !listed:
			return Manifest{}, invalid("member %q is not in the manifest", header.Name)
		case seen[header.Name]:
			return Manifest{}, invalid("member %q appears twice", header.Name)
		case header.Size != member.Size:
			return Manifest{}, invalid("member %q is %d bytes, but the manifest says %d", header.Name, header.Size, member.Size)
		}
		seen[header.Name] = true
		if err := copyMember(reader, member, destination); err != nil {
			return Manifest{}, err
		}
	}
	for _, member := range manifest.Members {
		if !seen[member.Name] {
			return Manifest{}, invalid("member %q is missing from the archive", member.Name)
		}
	}
	if err := (zeroTail{reader: io.LimitReader(file, limits.maxArchiveBytes())}).verify(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func regularHeader(header *tar.Header) bool {
	if header.Typeflag != tar.TypeReg || header.Linkname != "" {
		return false
	}
	for key := range header.PAXRecords {
		if strings.HasPrefix(key, "GNU.sparse") {
			return false
		}
	}
	return true
}

func copyMember(reader io.Reader, member ManifestMember, destination string) error {
	digest := sha256.New()
	var sink io.Writer = digest
	var out *os.File
	if destination != "" {
		target := filepath.Join(destination, filepath.FromSlash(member.Name))
		if strings.Contains(member.Name, "/") {
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
		}
		var err error
		out, err = os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		sink = io.MultiWriter(out, digest)
	}
	written, err := io.Copy(sink, io.LimitReader(reader, member.Size))
	if out != nil {
		if err == nil {
			err = out.Sync()
		}
		if closeErr := out.Close(); err == nil {
			err = closeErr
		}
	}
	if err != nil {
		return invalid("member %q cannot be read: %v", member.Name, err)
	}
	if written != member.Size {
		return invalid("member %q is shorter than the manifest says", member.Name)
	}
	if hex.EncodeToString(digest.Sum(nil)) != member.SHA256 {
		return invalid("member %q does not match its recorded SHA-256", member.Name)
	}
	return nil
}

func peekManifest(path string) (Manifest, error) {
	file, _, err := openRegular(path)
	if err != nil {
		return Manifest{}, err
	}
	defer file.Close()
	reader := tar.NewReader(io.LimitReader(file, maxManifestBytes+2048))
	header, err := reader.Next()
	if err != nil {
		return Manifest{}, err
	}
	if header.Name != ManifestName || !regularHeader(header) || header.Size < 1 || header.Size > maxManifestBytes {
		return Manifest{}, errors.New("first member is not a manifest")
	}
	raw := make([]byte, header.Size)
	if _, err := io.ReadFull(reader, raw); err != nil {
		return Manifest{}, err
	}
	return decodeManifest(raw, DefaultLimits())
}
