package syncproto

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type fixtureEntry struct {
	Entry          DeviceListEntry `json:"entry"`
	SigningPayload string          `json:"signingPayloadHex"`
	Digest         string          `json:"digest"`
}

type fixtureHeader struct {
	Header         Header `json:"header"`
	SigningPayload string `json:"signingPayloadHex"`
	Digest         string `json:"digest"`
	AAD            string `json:"aadHex"`
}

type fixtureRequest struct {
	Method         string `json:"method"`
	Target         string `json:"target"`
	Body           string `json:"body"`
	Token          string `json:"token"`
	Timestamp      uint64 `json:"timestamp"`
	Nonce          string `json:"nonce"`
	SigningPayload string `json:"signingPayloadHex"`
	Signature      string `json:"signature"`
	SignerKey      string `json:"signerKey"`
}

type fixtureReceipt struct {
	VaultID        string `json:"vaultId"`
	Revision       uint64 `json:"revision"`
	HeaderDigest   string `json:"headerDigest"`
	VaultEpoch     uint64 `json:"vaultEpoch"`
	AcceptedAt     uint64 `json:"acceptedAt"`
	SigningPayload string `json:"signingPayloadHex"`
	Signature      string `json:"signature"`
	SignerKey      string `json:"signerKey"`
}

type fixtureKeyPackage struct {
	VaultID           string `json:"vaultId"`
	SenderDeviceID    string `json:"senderDeviceId"`
	RecipientDeviceID string `json:"recipientDeviceId"`
	VaultEpoch        uint64 `json:"vaultEpoch"`
	EntryDigest       string `json:"entryDigest"`
	Ciphertext        string `json:"ciphertext"`
	Signature         string `json:"signature"`
	SigningPayload    string `json:"signingPayloadHex"`
	Digest            string `json:"digest"`
	SignerKey         string `json:"signerKey"`
}

type fixtureCheckpoint struct {
	Checkpoint     Checkpoint `json:"checkpoint"`
	SigningPayload string     `json:"signingPayloadHex"`
	SignerKey      string     `json:"signerKey"`
}

type fixtureBroken struct {
	Name  string            `json:"name"`
	Chain []DeviceListEntry `json:"chain"`
}

type fixtureBrokenHeader struct {
	Name     string  `json:"name"`
	Previous *Header `json:"previous"`
	Header   Header  `json:"header"`
}

type v3Fixture struct {
	Chain          []fixtureEntry        `json:"chain"`
	RemovalChain   []fixtureEntry        `json:"removalChain"`
	KeyChain       []fixtureEntry        `json:"keyChain"`
	Headers        []fixtureHeader       `json:"headers"`
	Ciphertext     string                `json:"ciphertext"`
	Request        fixtureRequest        `json:"request"`
	Receipt        fixtureReceipt        `json:"receipt"`
	KeyPackage     fixtureKeyPackage     `json:"keyPackage"`
	Checkpoint     fixtureCheckpoint     `json:"checkpoint"`
	BrokenChains   []fixtureBroken       `json:"brokenChains"`
	BrokenHeaders  []fixtureBrokenHeader `json:"brokenHeaders"`
	SeedByDevice   map[string]string     `json:"seedHexByDevice"`
	HeaderSignerID string                `json:"headerSignerId"`
}

func fixtureEntries(entries []DeviceListEntry) []fixtureEntry {
	out := make([]fixtureEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, fixtureEntry{entry, hex.EncodeToString(entry.SigningPayload()), entry.Digest()})
	}
	return out
}

func buildV3Fixture(t *testing.T) v3Fixture {
	t.Helper()
	chain := threeDeviceChain()
	removal := append(append([]DeviceListEntry(nil), chain...), removeEntry(chain[2], laptop, phone))
	list, err := VerifyDeviceList(chain)
	if err != nil {
		t.Fatal(err)
	}
	keyed := append(append([]DeviceListEntry(nil), chain...), keyEntry(chain[2], desktop, hybridKeyFor(5)))
	keyed = append(keyed, rotateEntry(keyed[3], laptop))
	ciphertext := bytes.Repeat([]byte{9}, 64)
	first := headerOn(list, laptop, nil, 1, ciphertext)
	second := headerOn(list, desktop, &first, 2, ciphertext)
	third := headerOn(list, phone, &second, 3, ciphertext)
	headers := []fixtureHeader{}
	for _, header := range []Header{first, second, third} {
		headers = append(headers, fixtureHeader{header, hex.EncodeToString(header.SigningPayload()), header.Digest(), hex.EncodeToString(SnapshotAADV3(header))})
	}

	nonce := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{4}, RequestNonceBytes))
	request := RequestSignature{Method: "POST", Target: "/v1/sync/reset", Body: []byte(`{"vaultEpoch":2}`), Token: "token-one", Timestamp: 1_000_000, Nonce: nonce}
	receipt := ReceiptV3{VaultID: testVaultID, Revision: 3, HeaderDigest: third.Digest(), VaultEpoch: 1, AcceptedAt: 1_000_001}

	keyPackage := KeyPackageV3{VaultID: testVaultID, SenderDeviceID: laptop.id, RecipientDeviceID: desktop.id, VaultEpoch: 1, EntryDigest: chain[1].Digest(), Ciphertext: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{3}, 96))}
	keyPackage.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(laptop.key, keyPackage.SigningPayload()))

	listDigest, _ := list.DigestAt(3)
	checkpoint := SignCheckpoint(desktop.key, Checkpoint{
		Version: VersionV3, VaultID: testVaultID, DeviceID: desktop.id, DeviceListIndex: 3,
		DeviceListDigest: listDigest, HeaderRevision: 3, HeaderDigest: third.Digest(), ObservedAt: 1_000_002,
	})

	resign := func(signer testDevice, entry DeviceListEntry) DeviceListEntry {
		return SignDeviceListEntry(signer.key, entry)
	}
	badSigner := addEntry(chain[0], thief, desktop, 0)
	wrongPrevious := chain[1]
	wrongPrevious.PreviousDigest = chain[2].Digest()
	epochJump := removeEntry(chain[1], laptop, desktop)
	epochJump.EpochAfter = 3
	reAdd := addEntry(removal[3], laptop, phone, 0)
	forkedSecond := headerOn(list, laptop, &first, 2, bytes.Repeat([]byte{8}, 64))
	onTheFork := headerOn(list, laptop, &forkedSecond, 3, ciphertext)
	edited := second
	edited.CiphertextLength++

	return v3Fixture{
		Chain:        fixtureEntries(chain),
		RemovalChain: fixtureEntries(removal),
		KeyChain:     fixtureEntries(keyed),
		Headers:      headers,
		Ciphertext:   base64.RawURLEncoding.EncodeToString(ciphertext),
		Request: fixtureRequest{
			Method: request.Method, Target: request.Target, Body: string(request.Body), Token: request.Token,
			Timestamp: request.Timestamp, Nonce: request.Nonce,
			SigningPayload: hex.EncodeToString(request.SigningPayload()),
			Signature:      base64.RawURLEncoding.EncodeToString(ed25519.Sign(laptop.key, request.SigningPayload())),
			SignerKey:      laptop.signingKey(),
		},
		Receipt: fixtureReceipt{
			VaultID: receipt.VaultID, Revision: receipt.Revision, HeaderDigest: receipt.HeaderDigest,
			VaultEpoch: receipt.VaultEpoch, AcceptedAt: receipt.AcceptedAt,
			SigningPayload: hex.EncodeToString(receipt.SigningPayload()),
			Signature:      base64.RawURLEncoding.EncodeToString(receipt.Sign(laptop.key)),
			SignerKey:      laptop.signingKey(),
		},
		KeyPackage: fixtureKeyPackage{
			VaultID: keyPackage.VaultID, SenderDeviceID: keyPackage.SenderDeviceID, RecipientDeviceID: keyPackage.RecipientDeviceID,
			VaultEpoch: keyPackage.VaultEpoch, EntryDigest: keyPackage.EntryDigest, Ciphertext: keyPackage.Ciphertext, Signature: keyPackage.Signature,
			SigningPayload: hex.EncodeToString(keyPackage.SigningPayload()), Digest: keyPackage.Digest(), SignerKey: laptop.signingKey(),
		},
		Checkpoint: fixtureCheckpoint{
			Checkpoint: checkpoint, SigningPayload: hex.EncodeToString(checkpoint.SigningPayload()), SignerKey: desktop.signingKey(),
		},
		BrokenChains: []fixtureBroken{
			{"a signature from a device that is not a member", []DeviceListEntry{chain[0], badSigner}},
			{"a wrong previous digest", []DeviceListEntry{chain[0], resign(laptop, wrongPrevious)}},
			{"a removal that skips an epoch", []DeviceListEntry{chain[0], chain[1], resign(laptop, epochJump)}},
			{"a removed device added again", append(append([]DeviceListEntry(nil), removal...), reAdd)},
			{"a key replacement by a device that already has the larger key", append(append([]DeviceListEntry(nil), keyed[:4]...), keyEntry(keyed[3], desktop, hybridKeyFor(6)))},
			{"a key replacement signed for another device", []DeviceListEntry{chain[0], chain[1], chain[2], resign(desktop, func() DeviceListEntry {
				entry := keyEntry(chain[2], desktop, hybridKeyFor(5))
				entry.SubjectDeviceID = phone.id
				return entry
			}())}},
			{"a rotation that does not raise the epoch", []DeviceListEntry{chain[0], chain[1], chain[2], resign(laptop, func() DeviceListEntry {
				entry := rotateEntry(chain[2], laptop)
				entry.EpochAfter = 1
				return entry
			}())}},
		},
		BrokenHeaders: []fixtureBrokenHeader{
			{"a revision built on a different second revision", &second, onTheFork},
			{"a revision that skips one", &first, third},
			{"a header edited after it was signed", &first, edited},
		},
		SeedByDevice: map[string]string{
			laptop.id:  hex.EncodeToString(bytes.Repeat([]byte{1}, ed25519.SeedSize)),
			desktop.id: hex.EncodeToString(bytes.Repeat([]byte{2}, ed25519.SeedSize)),
			phone.id:   hex.EncodeToString(bytes.Repeat([]byte{3}, ed25519.SeedSize)),
		},
		HeaderSignerID: laptop.id,
	}
}

func TestV3FixtureMatchesTheCode(t *testing.T) {
	encoded, err := json.MarshalIndent(buildV3Fixture(t), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	path := filepath.Join("testdata", "v3-fixture.json")
	if directory := os.Getenv("SESAME_WRITE_SYNC_FIXTURE"); directory != "" {
		if err := os.WriteFile(filepath.Join(directory, "v3-fixture.json"), encoded, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the fixture, regenerate it with SESAME_WRITE_SYNC_FIXTURE=testdata: %v", err)
	}
	if !bytes.Equal(stored, encoded) {
		t.Fatal("testdata/v3-fixture.json no longer matches the code, regenerate it and update the desktop copy")
	}
}
