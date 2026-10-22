package syncproto

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

const testVaultID = "vaultAAAAAAAAAAAA01"

type testDevice struct {
	id  string
	key ed25519.PrivateKey
}

func newTestDevice(seedByte byte, id string) testDevice {
	return testDevice{id: id, key: ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seedByte}, ed25519.SeedSize))}
}

func (device testDevice) signingKey() string {
	return base64.RawURLEncoding.EncodeToString(device.key.Public().(ed25519.PublicKey))
}

func (device testDevice) packageKey() string {
	return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, X25519PublicKeyBytes))
}

func sealedLabelFor(seed byte) string {
	return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{seed}, 48))
}

func createEntry(signer testDevice) DeviceListEntry {
	return SignDeviceListEntry(signer.key, DeviceListEntry{
		VaultID:           testVaultID,
		Index:             1,
		Kind:              EntryCreate,
		SignerDeviceID:    signer.id,
		SubjectDeviceID:   signer.id,
		SubjectSigningKey: signer.signingKey(),
		SubjectPackageKey: signer.packageKey(),
		SealedLabel:       sealedLabelFor(1),
		EpochAfter:        1,
	})
}

func addEntry(previous DeviceListEntry, signer, subject testDevice, floor uint64) DeviceListEntry {
	return SignDeviceListEntry(signer.key, DeviceListEntry{
		VaultID:           testVaultID,
		Index:             previous.Index + 1,
		PreviousDigest:    previous.Digest(),
		Kind:              EntryAdd,
		SignerDeviceID:    signer.id,
		SubjectDeviceID:   subject.id,
		SubjectSigningKey: subject.signingKey(),
		SubjectPackageKey: subject.packageKey(),
		SealedLabel:       sealedLabelFor(2),
		EpochAfter:        previous.EpochAfter,
		SnapshotFloor:     floor,
	})
}

func removeEntry(previous DeviceListEntry, signer, subject testDevice) DeviceListEntry {
	return SignDeviceListEntry(signer.key, DeviceListEntry{
		VaultID:         testVaultID,
		Index:           previous.Index + 1,
		PreviousDigest:  previous.Digest(),
		Kind:            EntryRemove,
		SignerDeviceID:  signer.id,
		SubjectDeviceID: subject.id,
		EpochAfter:      previous.EpochAfter + 1,
	})
}

var (
	laptop  = newTestDevice(1, "deviceLaptop00000001")
	desktop = newTestDevice(2, "deviceDesktop0000001")
	phone   = newTestDevice(3, "devicePhone000000001")
	thief   = newTestDevice(9, "deviceThief000000001")
)

func threeDeviceChain() []DeviceListEntry {
	first := createEntry(laptop)
	second := addEntry(first, laptop, desktop, 4)
	third := addEntry(second, desktop, phone, 6)
	return []DeviceListEntry{first, second, third}
}

func TestDeviceListVerifiesAValidChain(t *testing.T) {
	list, err := VerifyDeviceList(threeDeviceChain())
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if list.Head() != 3 || list.Epoch() != 1 || len(list.Current()) != 3 {
		t.Fatalf("head %d epoch %d members %d", list.Head(), list.Epoch(), len(list.Current()))
	}
	if _, ok := list.MemberAt(phone.id, 2); ok {
		t.Fatal("a device is a member before the entry that added it")
	}
	if _, ok := list.MemberAt(phone.id, 3); !ok {
		t.Fatal("a device is a member from the entry that added it")
	}
}

func TestDeviceListRemovalRaisesTheEpochAndEndsMembership(t *testing.T) {
	chain := threeDeviceChain()
	chain = append(chain, removeEntry(chain[2], laptop, phone))
	list, err := VerifyDeviceList(chain)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if list.Epoch() != 2 {
		t.Fatalf("epoch = %d, want 2", list.Epoch())
	}
	if _, ok := list.MemberAt(phone.id, 4); ok {
		t.Fatal("a removed device is still a member at the removal entry")
	}
	if _, ok := list.MemberAt(phone.id, 3); !ok {
		t.Fatal("a removed device was a member before the removal")
	}
	if err := list.Extend(addEntry(chain[3], laptop, phone, 0)); err == nil {
		t.Fatal("a removed device id was added again")
	}
}

func TestDeviceListLeavingIsARemovalSignedByTheSubject(t *testing.T) {
	chain := threeDeviceChain()
	chain = append(chain, removeEntry(chain[2], phone, phone))
	if _, err := VerifyDeviceList(chain); err != nil {
		t.Fatalf("a device could not remove itself: %v", err)
	}
}

func TestDeviceListRefusesEveryBrokenRule(t *testing.T) {
	good := threeDeviceChain()
	resign := func(signer testDevice, entry DeviceListEntry) DeviceListEntry {
		return SignDeviceListEntry(signer.key, entry)
	}
	cases := []struct {
		name  string
		chain func() []DeviceListEntry
	}{
		{"a signature from a device that is not a member", func() []DeviceListEntry {
			return []DeviceListEntry{good[0], addEntry(good[0], thief, desktop, 0)}
		}},
		{"a member signature that does not match the entry", func() []DeviceListEntry {
			entry := good[1]
			entry.SnapshotFloor = 99
			return []DeviceListEntry{good[0], entry}
		}},
		{"a wrong previous digest", func() []DeviceListEntry {
			entry := good[1]
			entry.PreviousDigest = good[2].Digest()
			return []DeviceListEntry{good[0], resign(laptop, entry)}
		}},
		{"a skipped index", func() []DeviceListEntry {
			entry := good[1]
			entry.Index = 3
			return []DeviceListEntry{good[0], resign(laptop, entry)}
		}},
		{"a first entry that is not a create", func() []DeviceListEntry {
			entry := good[0]
			entry.Kind = EntryAdd
			return []DeviceListEntry{resign(laptop, entry)}
		}},
		{"a first entry signed by someone else", func() []DeviceListEntry {
			entry := good[0]
			entry.SignerDeviceID = thief.id
			return []DeviceListEntry{resign(thief, entry)}
		}},
		{"a first entry at epoch two", func() []DeviceListEntry {
			entry := good[0]
			entry.EpochAfter = 2
			return []DeviceListEntry{resign(laptop, entry)}
		}},
		{"a second create", func() []DeviceListEntry {
			entry := good[1]
			entry.Kind = EntryCreate
			entry.SignerDeviceID = desktop.id
			return []DeviceListEntry{good[0], resign(laptop, entry)}
		}},
		{"an add that changes the epoch", func() []DeviceListEntry {
			entry := good[1]
			entry.EpochAfter = 2
			return []DeviceListEntry{good[0], resign(laptop, entry)}
		}},
		{"an add of a device already in the list", func() []DeviceListEntry {
			return []DeviceListEntry{good[0], addEntry(good[0], laptop, laptop, 0)}
		}},
		{"a removal that does not raise the epoch", func() []DeviceListEntry {
			entry := removeEntry(good[1], laptop, desktop)
			entry.EpochAfter = 1
			return []DeviceListEntry{good[0], good[1], resign(laptop, entry)}
		}},
		{"a removal that skips an epoch", func() []DeviceListEntry {
			entry := removeEntry(good[1], laptop, desktop)
			entry.EpochAfter = 3
			return []DeviceListEntry{good[0], good[1], resign(laptop, entry)}
		}},
		{"a removal of a device that is not a member", func() []DeviceListEntry {
			return []DeviceListEntry{good[0], removeEntry(good[0], laptop, thief)}
		}},
		{"a removal that carries a key", func() []DeviceListEntry {
			entry := removeEntry(good[1], laptop, desktop)
			entry.SubjectSigningKey = desktop.signingKey()
			return []DeviceListEntry{good[0], good[1], resign(laptop, entry)}
		}},
		{"a snapshot floor on a removal", func() []DeviceListEntry {
			entry := removeEntry(good[1], laptop, desktop)
			entry.SnapshotFloor = 3
			return []DeviceListEntry{good[0], good[1], resign(laptop, entry)}
		}},
		{"an entry from another vault", func() []DeviceListEntry {
			entry := good[1]
			entry.VaultID = "vaultBBBBBBBBBBBB01"
			return []DeviceListEntry{good[0], resign(laptop, entry)}
		}},
		{"a label that is too long", func() []DeviceListEntry {
			entry := good[1]
			entry.SealedLabel = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, MaxSealedLabelBytes+1))
			return []DeviceListEntry{good[0], resign(laptop, entry)}
		}},
		{"a signature with padding or a newline", func() []DeviceListEntry {
			entry := good[1]
			entry.Signature += "\n"
			return []DeviceListEntry{good[0], entry}
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := VerifyDeviceList(testCase.chain()); err == nil {
				t.Fatal("the chain verified")
			}
		})
	}
}

func TestDeviceListRefusesAnEleventhDevice(t *testing.T) {
	chain := []DeviceListEntry{createEntry(laptop)}
	for index := 0; index < MaxDevicesPerVault-1; index++ {
		device := newTestDevice(byte(20+index), "deviceExtra"+strings.Repeat("0", 4)+string(rune('a'+index))+"0000")
		chain = append(chain, addEntry(chain[len(chain)-1], laptop, device, 0))
	}
	list, err := VerifyDeviceList(chain)
	if err != nil {
		t.Fatalf("ten devices: %v", err)
	}
	overflow := addEntry(chain[len(chain)-1], laptop, newTestDevice(99, "deviceOverflow00001"), 0)
	if err := list.Extend(overflow); err == nil {
		t.Fatal("an eleventh device was added")
	}
}

func TestDeviceListExtendLeavesTheListAloneOnFailure(t *testing.T) {
	chain := threeDeviceChain()
	list, err := VerifyDeviceList(chain)
	if err != nil {
		t.Fatal(err)
	}
	head, digest := list.Head(), list.HeadDigest()
	bad := addEntry(chain[2], thief, newTestDevice(5, "deviceFive0000000001"), 0)
	if err := list.Extend(bad); err == nil {
		t.Fatal("a bad entry extended the list")
	}
	if list.Head() != head || list.HeadDigest() != digest {
		t.Fatal("a failed extend changed the list")
	}
}

func headerOn(list *DeviceList, signer testDevice, previous *Header, revision uint64, ciphertext []byte) Header {
	index := list.Head()
	digest, _ := list.DigestAt(index)
	epoch, _ := list.EpochAt(index)
	sum := sha256.Sum256(ciphertext)
	header := Header{
		Version:          VersionV3,
		VaultID:          testVaultID,
		DeviceID:         signer.id,
		Revision:         revision,
		VaultEpoch:       epoch,
		DeviceListIndex:  index,
		DeviceListDigest: digest,
		CiphertextDigest: base64.RawURLEncoding.EncodeToString(sum[:]),
		CiphertextLength: uint64(len(ciphertext)),
		Nonce:            base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{5}, XChaChaNonceBytes)),
	}
	if previous != nil {
		header.PreviousDigest = previous.Digest()
	}
	return SignHeader(signer.key, header)
}

func TestHeaderChainVerifiesAndDetectsEveryBreak(t *testing.T) {
	list, err := VerifyDeviceList(threeDeviceChain())
	if err != nil {
		t.Fatal(err)
	}
	ciphertext := bytes.Repeat([]byte{9}, 64)
	first := headerOn(list, laptop, nil, 1, ciphertext)
	second := headerOn(list, desktop, &first, 2, ciphertext)
	third := headerOn(list, phone, &second, 3, ciphertext)
	if err := VerifyHeaderChain(list, nil, []Header{first, second, third}); err != nil {
		t.Fatalf("a valid chain: %v", err)
	}
	if err := first.VerifyCiphertext(ciphertext); err != nil {
		t.Fatalf("ciphertext: %v", err)
	}

	forkedSecond := headerOn(list, laptop, &first, 2, bytes.Repeat([]byte{8}, 64))
	onTheFork := headerOn(list, laptop, &forkedSecond, 3, ciphertext)
	cases := []struct {
		name     string
		previous *Header
		header   Header
	}{
		{"a revision built on a different second revision", &second, onTheFork},
		{"a skipped revision", &first, third},
		{"a header that does not name its predecessor", &first, headerOn(list, desktop, nil, 2, ciphertext)},
		{"a header signed by a device outside the list", &first, headerOn(list, thief, &first, 2, ciphertext)},
		{"a header edited after it was signed", &first, func() Header { h := second; h.CiphertextLength++; return h }()},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if err := VerifyHeader(list, testCase.previous, testCase.header); err == nil {
				t.Fatal("the header verified")
			}
		})
	}

	t.Run("a ciphertext that does not match its header", func(t *testing.T) {
		if err := first.VerifyCiphertext(bytes.Repeat([]byte{1}, 64)); !errors.Is(err, ErrHistoryBroken) {
			t.Fatalf("error = %v", err)
		}
		if err := first.VerifyCiphertext(ciphertext[:32]); !errors.Is(err, ErrHistoryBroken) {
			t.Fatalf("a truncated ciphertext: %v", err)
		}
	})
}

func TestHeaderMustNameAVerifiedDeviceListAndNeverGoBackwards(t *testing.T) {
	chain := threeDeviceChain()
	list, err := VerifyDeviceList(chain)
	if err != nil {
		t.Fatal(err)
	}
	early, err := VerifyDeviceList(chain[:2])
	if err != nil {
		t.Fatal(err)
	}
	ciphertext := bytes.Repeat([]byte{9}, 64)
	onThree := headerOn(list, laptop, nil, 1, ciphertext)
	onTwo := headerOn(early, laptop, &onThree, 2, ciphertext)
	if err := VerifyHeader(list, &onThree, onTwo); err == nil {
		t.Fatal("a header went back to an older device list entry")
	}
	if err := VerifyHeader(early, nil, onThree); err == nil {
		t.Fatal("a header named an entry this device has not verified")
	}
}

func TestHeaderFromARemovedDeviceIsRefusedAfterTheRemoval(t *testing.T) {
	chain := threeDeviceChain()
	chain = append(chain, removeEntry(chain[2], laptop, phone))
	list, err := VerifyDeviceList(chain)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext := bytes.Repeat([]byte{9}, 64)
	rekeyed := headerOn(list, laptop, nil, 1, ciphertext)
	late := headerOn(list, phone, &rekeyed, 2, ciphertext)
	if err := VerifyHeader(list, &rekeyed, late); err == nil {
		t.Fatal("a removed device wrote under the new epoch")
	}
}

func TestRequestSignatureBindsEveryPart(t *testing.T) {
	nonce := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{4}, RequestNonceBytes))
	request := RequestSignature{Method: "POST", Target: "/v1/sync/reset", Body: []byte(`{"vaultEpoch":2}`), Token: "token-one", Timestamp: 1_000_000, Nonce: nonce}
	signature := base64.RawURLEncoding.EncodeToString(ed25519.Sign(laptop.key, request.SigningPayload()))
	key := laptop.signingKey()
	if err := request.Verify(key, signature, 1_000_100); err != nil {
		t.Fatalf("a valid request: %v", err)
	}
	mutations := map[string]func(RequestSignature) RequestSignature{
		"method":    func(r RequestSignature) RequestSignature { r.Method = "DELETE"; return r },
		"target":    func(r RequestSignature) RequestSignature { r.Target = "/v1/sync/devices/x"; return r },
		"body":      func(r RequestSignature) RequestSignature { r.Body = []byte(`{"vaultEpoch":3}`); return r },
		"token":     func(r RequestSignature) RequestSignature { r.Token = "token-two"; return r },
		"timestamp": func(r RequestSignature) RequestSignature { r.Timestamp++; return r },
		"nonce": func(r RequestSignature) RequestSignature {
			r.Nonce = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{5}, RequestNonceBytes))
			return r
		},
	}
	for name, mutate := range mutations {
		if err := mutate(request).Verify(key, signature, 1_000_100); err == nil {
			t.Fatalf("a changed %s still verified", name)
		}
	}
	if err := request.Verify(key, signature, 1_000_000+RequestTimestampSkew+1); err == nil {
		t.Fatal("a stale request verified")
	}
	if err := request.Verify(key, signature, 1_000_000-RequestTimestampSkew-1); err == nil {
		t.Fatal("a request from the future verified")
	}
	if err := request.Verify(desktop.signingKey(), signature, 1_000_100); err == nil {
		t.Fatal("another device's key verified the request")
	}
}

func TestSignaturesDoNotCrossLabels(t *testing.T) {
	entry := createEntry(laptop)
	header := Header{Version: VersionV3}
	if bytes.Equal(entry.SigningPayload()[:20], header.SigningPayload()[:20]) {
		t.Fatal("device list and header payloads start the same way")
	}
	receipt := ReceiptV3{VaultID: testVaultID, Revision: 1, HeaderDigest: "x", VaultEpoch: 1, AcceptedAt: 5}
	signature := receipt.Sign(laptop.key)
	if !receipt.Verify(laptop.key.Public().(ed25519.PublicKey), signature) {
		t.Fatal("a receipt did not verify")
	}
	receipt.Revision = 2
	if receipt.Verify(laptop.key.Public().(ed25519.PublicKey), signature) {
		t.Fatal("a changed receipt verified")
	}
	if ed25519.Verify(laptop.key.Public().(ed25519.PublicKey), entry.SigningPayload(), signature) {
		t.Fatal("a receipt signature verified as a device list entry")
	}
}

func TestCanonicalDecodeRefusesNonCanonicalForms(t *testing.T) {
	valid := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	if _, err := decodeCanonical(valid, 32); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"padding":  base64.URLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 31)),
		"newline":  valid + "\n",
		"standard": strings.NewReplacer("-", "+", "_", "/").Replace(base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0xfb}, 32))),
		"trailing": valid[:len(valid)-1] + "B",
		"short":    valid[:40],
	} {
		if _, err := decodeCanonical(value, 32); err == nil {
			t.Fatalf("%s decoded", name)
		}
	}
}

func FuzzVerifyDeviceList(f *testing.F) {
	for _, entry := range threeDeviceChain() {
		f.Add(entry.VaultID, entry.Index, entry.PreviousDigest, entry.Kind, entry.SignerDeviceID, entry.SubjectDeviceID, entry.SubjectSigningKey, entry.SubjectPackageKey, entry.SealedLabel, entry.EpochAfter, entry.SnapshotFloor, entry.Signature)
	}
	f.Fuzz(func(t *testing.T, vault string, index uint64, previous, kind, signer, subject, signing, pkg, label string, epoch, floor uint64, signature string) {
		entry := DeviceListEntry{vault, index, previous, kind, signer, subject, signing, pkg, label, epoch, floor, signature}
		list, err := VerifyDeviceList([]DeviceListEntry{entry})
		if err == nil && list.Head() != 1 {
			t.Fatalf("a verified single entry has head %d", list.Head())
		}
	})
}

func TestDeviceListAcceptsAHybridPackageKeyAndNoOtherLength(t *testing.T) {
	hybrid := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{5}, XWingPublicKeyBytes))
	first := createEntry(laptop)
	withKey := func(encoded string) []DeviceListEntry {
		entry := addEntry(first, laptop, desktop, 4)
		entry.SubjectPackageKey = encoded
		return []DeviceListEntry{first, SignDeviceListEntry(laptop.key, entry)}
	}

	list, err := VerifyDeviceList(withKey(hybrid))
	if err != nil {
		t.Fatalf("a 1216 byte package key was refused: %v", err)
	}
	member, ok := list.MemberAt(desktop.id, 2)
	if !ok || member.PackageKey != hybrid {
		t.Fatal("the hybrid package key was not kept on the member")
	}
	if _, err := VerifyDeviceList(withKey(desktop.packageKey())); err != nil {
		t.Fatalf("a 32 byte package key was refused: %v", err)
	}

	for _, length := range []int{0, 1, 31, 33, 64, 1215, 1217, 64 * 1024} {
		encoded := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{5}, length))
		if _, err := VerifyDeviceList(withKey(encoded)); err == nil {
			t.Fatalf("a package key of %d bytes was accepted", length)
		}
	}
	if _, err := VerifyDeviceList(withKey(hybrid + "=")); err == nil {
		t.Fatal("a padded package key was accepted")
	}
	if _, err := VerifyDeviceList(withKey(hybrid + "\n")); err == nil {
		t.Fatal("a package key with a line break was accepted")

func TestNonceRetentionOutlastsTheWholeAcceptedClockWindow(t *testing.T) {
	if RequestNonceRetention < 2*RequestTimestampSkew+60 {
		t.Fatalf("a request stamped at the edge of the skew window must find its nonce still stored: retention %d, skew %d", RequestNonceRetention, RequestTimestampSkew)
	}
}
