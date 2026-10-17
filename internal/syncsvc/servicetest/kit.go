package servicetest

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"usesesame.app/backend/internal/syncproto"
	"usesesame.app/backend/internal/syncsvc"
)

type clock struct {
	mutex sync.Mutex
	now   time.Time
}

func (c *clock) Now() time.Time {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.now
}

func (c *clock) Advance(duration time.Duration) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.now = c.now.Add(duration)
}

type env struct {
	t       *testing.T
	svc     *syncsvc.Service
	storage syncsvc.Storage
	clock   *clock
	owner   string
	vaultID string
	nonce   int
}

type device struct {
	id      string
	token   string
	tokenID string
	key     ed25519.PrivateKey
}

func b64(value []byte) string {
	return base64.RawURLEncoding.EncodeToString(value)
}

func newEnv(t *testing.T, factory func(*testing.T) syncsvc.Storage, config syncsvc.Config) *env {
	t.Helper()
	clk := &clock{now: time.Unix(1_800_000_000, 0).UTC()}
	_, receiptKey, err := ed25519.GenerateKey(bytes.NewReader(bytes.Repeat([]byte{42}, 64)))
	if err != nil {
		t.Fatal(err)
	}
	config.ReceiptKey = receiptKey
	config.Now = clk.Now
	storage := factory(t)
	svc, err := syncsvc.New(storage, config)
	if err != nil {
		t.Fatal(err)
	}
	return &env{t: t, svc: svc, storage: storage, clock: clk, owner: "owner-one", vaultID: "vaultAAAAAAAAAAAA01"}
}

func (e *env) newDevice(name string) device {
	seed := sha256.Sum256([]byte("seed-" + name))
	key := ed25519.NewKeyFromSeed(seed[:])
	return device{
		id:      ("device" + name + strings.Repeat("0", 20))[:20],
		token:   "token-" + name,
		tokenID: "desktop-" + name,
		key:     key,
	}
}

func (d device) signingKey() string {
	return b64(d.key.Public().(ed25519.PublicKey))
}

func (d device) packageKey() string {
	return b64(bytes.Repeat([]byte{7}, 32))
}

func (e *env) caller(d device) syncsvc.Caller {
	return syncsvc.Caller{OwnerKey: e.owner, TokenDeviceID: d.tokenID, Token: d.token}
}

func (e *env) sign(d device, method, target string, body []byte) syncsvc.SignedRequest {
	return e.signWith(d, d.token, method, target, body, uint64(e.clock.Now().Unix()))
}

func (e *env) signWith(d device, token, method, target string, body []byte, timestamp uint64) syncsvc.SignedRequest {
	e.nonce++
	nonce := b64(bytes.Repeat([]byte{byte(e.nonce)}, syncproto.RequestNonceBytes))
	signed := syncproto.RequestSignature{Method: method, Target: target, Body: body, Token: token, Timestamp: timestamp, Nonce: nonce}
	return syncsvc.SignedRequest{
		Method: method, Target: target, Body: body, Timestamp: timestamp, Nonce: nonce,
		Signature: b64(ed25519.Sign(d.key, signed.SigningPayload())),
	}
}

func sealedLabel(seed byte) string {
	return b64(bytes.Repeat([]byte{seed}, 48))
}

func (e *env) createEntry(d device) syncproto.DeviceListEntry {
	return syncproto.SignDeviceListEntry(d.key, syncproto.DeviceListEntry{
		VaultID: e.vaultID, Index: 1, Kind: syncproto.EntryCreate,
		SignerDeviceID: d.id, SubjectDeviceID: d.id,
		SubjectSigningKey: d.signingKey(), SubjectPackageKey: d.packageKey(),
		SealedLabel: sealedLabel(1), EpochAfter: 1,
	})
}

func (e *env) createVault(d device) {
	e.t.Helper()
	entry := e.createEntry(d)
	if _, err := e.svc.CreateVault(context.Background(), e.caller(d), e.sign(d, "POST", "/v1/sync/vault", []byte("create")), syncsvc.CreateVaultInput{Entry: entry}); err != nil {
		e.t.Fatalf("create vault: %v", err)
	}
}

func (e *env) list(d device) *syncproto.DeviceList {
	e.t.Helper()
	entries, err := e.svc.DeviceList(context.Background(), e.caller(d), 0, 0)
	if err != nil {
		e.t.Fatalf("device list: %v", err)
	}
	list, err := syncproto.VerifyDeviceList(entries)
	if err != nil {
		e.t.Fatalf("the served device list does not verify: %v", err)
	}
	return list
}

func (e *env) keyPackage(sender, recipient device, epoch uint64, entryDigest string) syncproto.KeyPackageV3 {
	pkg := syncproto.KeyPackageV3{
		VaultID: e.vaultID, SenderDeviceID: sender.id, RecipientDeviceID: recipient.id,
		VaultEpoch: epoch, EntryDigest: entryDigest, Ciphertext: b64(bytes.Repeat([]byte{3}, 48)),
	}
	pkg.Signature = b64(ed25519.Sign(sender.key, pkg.SigningPayload()))
	return pkg
}

func (e *env) pair(approver, newcomer device) {
	e.t.Helper()
	e.confirmPairing(approver, newcomer)
	e.approve(approver, newcomer)
}

func (e *env) confirmPairing(approver, newcomer device) {
	e.t.Helper()
	ctx := context.Background()
	offer := syncsvc.PairingOffer{DeviceID: newcomer.id, SigningKey: newcomer.signingKey(), PackageKey: newcomer.packageKey(), Message: []byte("offer")}
	if err := e.svc.BeginPairing(ctx, e.caller(newcomer), e.sign(newcomer, "POST", "/v1/sync/pair", []byte("offer")), offer); err != nil {
		e.t.Fatalf("begin pairing: %v", err)
	}
	if err := e.svc.AnswerPairing(ctx, e.caller(approver), e.sign(approver, "POST", "/v1/sync/pair/answer", []byte("answer")), newcomer.id, []byte("answer")); err != nil {
		e.t.Fatalf("answer pairing: %v", err)
	}
	if err := e.svc.ConfirmPairing(ctx, e.caller(newcomer), e.sign(newcomer, "POST", "/v1/sync/pair/confirm", []byte("confirm")), []byte("confirm")); err != nil {
		e.t.Fatalf("confirm pairing: %v", err)
	}
}

func (e *env) addEntry(approver, newcomer device) syncproto.DeviceListEntry {
	list := e.list(approver)
	return syncproto.SignDeviceListEntry(approver.key, syncproto.DeviceListEntry{
		VaultID: e.vaultID, Index: list.Head() + 1, PreviousDigest: list.HeadDigest(),
		Kind: syncproto.EntryAdd, SignerDeviceID: approver.id, SubjectDeviceID: newcomer.id,
		SubjectSigningKey: newcomer.signingKey(), SubjectPackageKey: newcomer.packageKey(),
		SealedLabel: sealedLabel(2), EpochAfter: list.Epoch(),
	})
}

func (e *env) approve(approver, newcomer device) {
	e.t.Helper()
	ctx := context.Background()
	list := e.list(approver)
	entry := e.addEntry(approver, newcomer)
	input := syncsvc.ApproveInput{Entry: entry, Package: e.keyPackage(approver, newcomer, list.Epoch(), entry.Digest())}
	if err := e.svc.Approve(ctx, e.caller(approver), e.sign(approver, "POST", "/v1/sync/approve", []byte("approve")), input); err != nil {
		e.t.Fatalf("approve: %v", err)
	}
	e.activate(newcomer)
}

func (e *env) activate(d device) {
	e.t.Helper()
	ctx := context.Background()
	pkg, err := e.svc.KeyPackage(ctx, e.caller(d))
	if err != nil {
		e.t.Fatalf("key package: %v", err)
	}
	if err := e.svc.Activate(ctx, e.caller(d), e.sign(d, "POST", "/v1/sync/activate", []byte("activate")), syncsvc.ActivateInput{PackageDigest: pkg.Digest()}); err != nil {
		e.t.Fatalf("activate: %v", err)
	}
}

func (e *env) header(d device, revision uint64, previous *syncproto.Header, ciphertext []byte) syncproto.Header {
	return e.headerSeenBy(d, d, revision, previous, ciphertext)
}

func (e *env) headerSeenBy(d, viewer device, revision uint64, previous *syncproto.Header, ciphertext []byte) syncproto.Header {
	list := e.list(viewer)
	index := list.Head()
	digest, _ := list.DigestAt(index)
	epoch, _ := list.EpochAt(index)
	sum := sha256.Sum256(ciphertext)
	header := syncproto.Header{
		Version: syncproto.VersionV3, VaultID: e.vaultID, DeviceID: d.id, Revision: revision,
		VaultEpoch: epoch, DeviceListIndex: index, DeviceListDigest: digest,
		CiphertextDigest: b64(sum[:]), CiphertextLength: uint64(len(ciphertext)),
		Nonce: b64(bytes.Repeat([]byte{5}, syncproto.XChaChaNonceBytes)),
	}
	if previous != nil {
		header.PreviousDigest = previous.Digest()
	}
	return syncproto.SignHeader(d.key, header)
}

func (e *env) upload(d device, revision uint64, previous *syncproto.Header, ciphertext []byte) (syncsvc.Accepted, error) {
	return e.uploadSeenBy(d, d, revision, previous, ciphertext)
}

func (e *env) uploadSeenBy(d, viewer device, revision uint64, previous *syncproto.Header, ciphertext []byte) (syncsvc.Accepted, error) {
	header := e.headerSeenBy(d, viewer, revision, previous, ciphertext)
	request := e.sign(d, "POST", "/v1/sync/envelope", []byte(fmt.Sprintf("upload-%d", revision)))
	return e.svc.Upload(context.Background(), e.caller(d), request, syncsvc.UploadInput{Header: header, Ciphertext: ciphertext})
}

func (e *env) mustUpload(d device, revision uint64, previous *syncproto.Header, ciphertext []byte) syncsvc.Accepted {
	e.t.Helper()
	accepted, err := e.upload(d, revision, previous, ciphertext)
	if err != nil {
		e.t.Fatalf("upload revision %d: %v", revision, err)
	}
	return accepted
}

func (e *env) remove(signer, subject device, survivors ...device) error {
	return e.removeBoundTo("", signer, subject, survivors...)
}

func (e *env) removeBoundTo(boundTo string, signer, subject device, survivors ...device) error {
	list := e.list(signer)
	entry := syncproto.SignDeviceListEntry(signer.key, syncproto.DeviceListEntry{
		VaultID: e.vaultID, Index: list.Head() + 1, PreviousDigest: list.HeadDigest(),
		Kind: syncproto.EntryRemove, SignerDeviceID: signer.id, SubjectDeviceID: subject.id,
		EpochAfter: list.Epoch() + 1,
	})
	var packages []syncproto.KeyPackageV3
	for _, survivor := range survivors {
		digest := entry.Digest()
		if boundTo != "" {
			digest = boundTo
		}
		packages = append(packages, e.keyPackage(signer, survivor, entry.EpochAfter, digest))
	}
	return e.svc.Remove(context.Background(), e.caller(signer), e.sign(signer, "POST", "/v1/sync/remove", []byte("remove")), syncsvc.RemoveInput{Entry: entry, Packages: packages})
}

func (e *env) vault(d device) syncsvc.Vault {
	e.t.Helper()
	vault, _, err := e.svc.VaultState(context.Background(), e.caller(d))
	if err != nil {
		e.t.Fatalf("vault state: %v", err)
	}
	return vault
}

func (e *env) checkpointFor(d device, observedAt uint64) syncproto.Checkpoint {
	e.t.Helper()
	list := e.list(d)
	index := list.Head()
	digest, _ := list.DigestAt(index)
	checkpoint := syncproto.Checkpoint{
		Version: syncproto.VersionV3, VaultID: e.vaultID, DeviceID: d.id,
		DeviceListIndex: index, DeviceListDigest: digest, ObservedAt: observedAt,
	}
	if revision := e.vault(d).CurrentRevision; revision > 0 {
		stored, err := e.svc.Headers(context.Background(), e.caller(d), revision-1, 1)
		if err != nil || len(stored) != 1 {
			e.t.Fatalf("read the head header: %v", err)
		}
		checkpoint.HeaderRevision = revision
		checkpoint.HeaderDigest = stored[0].Digest
	}
	return syncproto.SignCheckpoint(d.key, checkpoint)
}

func (e *env) putCheckpoint(d device, checkpoint syncproto.Checkpoint) (syncsvc.StoredCheckpoint, error) {
	return e.svc.PutCheckpoint(context.Background(), e.caller(d), e.sign(d, "POST", "/v1/sync/checkpoints", []byte("checkpoint")), checkpoint)
}

func (e *env) now() uint64 {
	return uint64(e.clock.Now().Unix())
}

func content(seed byte) []byte {
	return bytes.Repeat([]byte{seed}, 1024)
}
