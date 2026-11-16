package syncv3http

import (
	"bytes"
	"encoding/base64"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"usesesame.app/backend/internal/syncsvc"
)

func reencodedCanonicalBase64(value string) ([]byte, bool) {
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || base64.RawURLEncoding.EncodeToString(decoded) != value {
		return nil, false
	}
	return decoded, true
}

func TestCanonicalBase64AcceptsExactlyWhatReencodingAccepts(t *testing.T) {
	inputs := []string{
		"", "A", "AA", "AB", "AAA", "AAB", "AAAA", "AAAAA", "QQ", "QR", "QUI", "QUJ",
		"AAAA\n", "AA\nAA", "\rAAAA", "AAAA\r\n", "\n", "AA==", "AAA=", "AA=A",
		"AA+A", "AA/A", "AA-A", "AA_A", "AA A", "AA\tA", "AA\x00A", "AAé",
		base64.RawURLEncoding.EncodeToString([]byte("fictional vault bytes")),
		base64.StdEncoding.EncodeToString([]byte("fictional vault bytes?>")),
	}
	alphabet := []byte("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_+/=\r\n \x00")
	random := rand.New(rand.NewPCG(1, 2))
	for range 20000 {
		value := make([]byte, random.IntN(12))
		for index := range value {
			value[index] = alphabet[random.IntN(len(alphabet))]
		}
		inputs = append(inputs, string(value))
	}
	for _, input := range inputs {
		want, wantOK := reencodedCanonicalBase64(input)
		got, gotOK := canonicalBase64(input)
		if gotOK != wantOK || !bytes.Equal(got, want) {
			t.Fatalf("canonicalBase64(%q) = %v, %v; the re-encoding check gives %v, %v", input, got, gotOK, want, wantOK)
		}
	}
}

func TestSnapshotStreamIsByteIdenticalToTheEncodedResponse(t *testing.T) {
	header := sampleHeader()
	header.VaultID = "vault<&> é"
	snapshots := map[string]syncsvc.Snapshot{
		"empty ciphertext": {},
		"short ciphertext": {Ciphertext: []byte{1, 2, 3, 4, 5}},
		"long ciphertext":  {Ciphertext: bytes.Repeat([]byte{0xfb, 0xff, 0x00}, 50_000)},
	}
	for name, snapshot := range snapshots {
		t.Run(name, func(t *testing.T) {
			snapshot.Header.Header = header
			snapshot.Header.Digest = "digest"
			snapshot.Header.Receipt = "receipt"
			snapshot.Header.AcceptedAt = time.Unix(1_800_000_000, 0)
			want := httptest.NewRecorder()
			writeJSON(want, http.StatusOK, snapshotResponse{
				Header: snapshot.Header.Header, Digest: snapshot.Header.Digest, Receipt: snapshot.Header.Receipt,
				AcceptedAt: formatTime(snapshot.Header.AcceptedAt), Ciphertext: encodeBase64(snapshot.Ciphertext),
			})
			got := httptest.NewRecorder()
			writeSnapshot(got, snapshot)
			if got.Code != want.Code || got.Header().Get("Content-Type") != want.Header().Get("Content-Type") {
				t.Fatalf("status %d %q, want %d %q", got.Code, got.Header().Get("Content-Type"), want.Code, want.Header().Get("Content-Type"))
			}
			if !bytes.Equal(got.Body.Bytes(), want.Body.Bytes()) {
				t.Fatalf("streamed snapshot differs from the encoded response:\n%q\n%q", got.Body.String(), want.Body.String())
			}
		})
	}
}

func TestTrailingBase64RefusesAValueThatDoesNotEndInTheNamedField(t *testing.T) {
	values := map[string]any{
		"another last field": uploadResponse{Revision: 1},
		"field not empty":    snapshotResponse{Ciphertext: "AQ"},
	}
	for name, value := range values {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			writeJSONWithTrailingBase64(recorder, http.StatusOK, value, "ciphertext", []byte{1})
			if recorder.Code != http.StatusServiceUnavailable || bytes.Contains(recorder.Body.Bytes(), []byte("AQ")) {
				t.Fatalf("status %d, want 503: %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}
