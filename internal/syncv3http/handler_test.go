package syncv3http_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"usesesame.app/backend/internal/syncproto"
	"usesesame.app/backend/internal/syncsvc"
	"usesesame.app/backend/internal/syncsvc/memstore"
	"usesesame.app/backend/internal/syncv3http"
)

const (
	testOwner       = "owner-one"
	testVaultID     = "vaultAAAAAAAAAAAA01"
	deviceAID       = "deviceAAAAAAAAAAAA01"
	deviceBID       = "deviceBBBBBBBBBBBB02"
	signatureHeader = "Sesame-Request-Signature"
	smallBodyLimit  = 64 << 10
)

var testStart = time.Unix(1_800_000_000, 0).UTC()

var testPackageKey = encodeBase64(bytes.Repeat([]byte{7}, 32))

type storage struct {
	inner syncsvc.Storage
	calls int
	fail  error
}

func (s *storage) Update(ctx context.Context, fn func(syncsvc.Tx) error) error {
	s.calls++
	if s.fail != nil {
		return s.fail
	}
	return s.inner.Update(ctx, fn)
}

func (s *storage) View(ctx context.Context, fn func(syncsvc.Tx) error) error {
	s.calls++
	if s.fail != nil {
		return s.fail
	}
	return s.inner.View(ctx, fn)
}

type fakeHost struct {
	calls  int
	refuse string
}

func (h *fakeHost) Authenticate(response http.ResponseWriter, request *http.Request, operation string) (syncsvc.Caller, bool) {
	h.calls++
	if operation == h.refuse {
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusTooManyRequests)
		_, _ = response.Write([]byte(`{"error":{"code":"rate_limited","message":"Too many requests."}}`))
		return syncsvc.Caller{}, false
	}
	token := request.Header.Get("X-Test-Token")
	owner := request.Header.Get("X-Test-Owner")
	tokenDevice := request.Header.Get("X-Test-Token-Device")
	if token == "" || owner == "" || tokenDevice == "" {
		http.Error(response, "unauthorized", http.StatusUnauthorized)
		return syncsvc.Caller{}, false
	}
	return syncsvc.Caller{OwnerKey: owner, TokenDeviceID: tokenDevice, Token: token}, true
}

type device struct {
	id      string
	token   string
	tokenID string
	key     ed25519.PrivateKey
}

func newDevice(id, name string) device {
	return device{id: id, token: "token-" + name, tokenID: "desktop-" + name, key: ed25519.NewKeyFromSeed(seed(name))}
}

func (d device) signingKey() string {
	return encodeBase64(d.key.Public().(ed25519.PublicKey))
}

func seed(name string) []byte {
	sum := sha256.Sum256([]byte("sync-v3-http-test-" + name))
	return sum[:]
}

type fixture struct {
	t          *testing.T
	mux        *http.ServeMux
	storage    *storage
	host       *fakeHost
	receiptKey ed25519.PublicKey
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	receiptKey := ed25519.NewKeyFromSeed(seed("receipt"))
	store := &storage{inner: memstore.New()}
	service, err := syncsvc.New(store, syncsvc.Config{
		ReceiptKey: receiptKey,
		Now:        func() time.Time { return testStart },
	})
	if err != nil {
		t.Fatal(err)
	}
	host := &fakeHost{}
	mux := http.NewServeMux()
	syncv3http.New(service, host).Register(mux)
	return &fixture{t: t, mux: mux, storage: store, host: host, receiptKey: receiptKey.Public().(ed25519.PublicKey)}
}

func encodeBase64(value []byte) string {
	return base64.RawURLEncoding.EncodeToString(value)
}

func randomBase64(t *testing.T, length int) string {
	t.Helper()
	value := make([]byte, length)
	if _, err := rand.Read(value); err != nil {
		t.Fatal(err)
	}
	return encodeBase64(value)
}

func (f *fixture) sign(key ed25519.PrivateKey, token, method, target string, body []byte) string {
	f.t.Helper()
	nonce := randomBase64(f.t, syncproto.RequestNonceBytes)
	timestamp := uint64(testStart.Unix())
	payload := syncproto.RequestSignature{
		Method: method, Target: target, Body: body, Token: token, Timestamp: timestamp, Nonce: nonce,
	}.SigningPayload()
	return fmt.Sprintf("%d.%s.%s", timestamp, nonce, encodeBase64(ed25519.Sign(key, payload)))
}

func (f *fixture) serve(d device, method, target string, body io.Reader, signature string) *httptest.ResponseRecorder {
	f.t.Helper()
	request := httptest.NewRequest(method, target, body)
	request.Header.Set("X-Test-Owner", testOwner)
	request.Header.Set("X-Test-Token", d.token)
	request.Header.Set("X-Test-Token-Device", d.tokenID)
	if signature != "" {
		request.Header.Set(signatureHeader, signature)
	}
	recorder := httptest.NewRecorder()
	f.mux.ServeHTTP(recorder, request)
	return recorder
}

func (f *fixture) get(d device, target string) *httptest.ResponseRecorder {
	return f.serve(d, http.MethodGet, target, nil, "")
}

func (f *fixture) signed(d device, method, target string, body []byte) *httptest.ResponseRecorder {
	signature := f.sign(d.key, d.token, method, target, body)
	return f.serve(d, method, target, bytes.NewReader(body), signature)
}

func (f *fixture) signedJSON(d device, method, target string, value any) *httptest.ResponseRecorder {
	f.t.Helper()
	return f.signed(d, method, target, mustJSON(f.t, value))
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func decode(t *testing.T, recorder *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(recorder.Body.Bytes(), target); err != nil {
		t.Fatalf("decode %q: %v", recorder.Body.String(), err)
	}
}

type errorReply struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func expectStatus(t *testing.T, recorder *httptest.ResponseRecorder, status int) {
	t.Helper()
	if recorder.Code != status {
		t.Fatalf("status %d, want %d: %s", recorder.Code, status, recorder.Body.String())
	}
}

func expectError(t *testing.T, recorder *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	expectStatus(t, recorder, status)
	var reply errorReply
	decode(t, recorder, &reply)
	if reply.Error.Code != code {
		t.Fatalf("error code %q, want %q: %s", reply.Error.Code, code, recorder.Body.String())
	}
	if reply.Error.Message == "" {
		t.Fatalf("error %q has no message", code)
	}
}

type vaultBody struct {
	VaultID    string `json:"vaultId"`
	VaultEpoch uint64 `json:"vaultEpoch"`
	HeadIndex  uint64 `json:"headIndex"`
}

type stateBody struct {
	VaultID         string `json:"vaultId"`
	VaultEpoch      uint64 `json:"vaultEpoch"`
	HeadIndex       uint64 `json:"headIndex"`
	CurrentRevision uint64 `json:"currentRevision"`
	InstanceKey     string `json:"instanceKey"`
	DeviceID        string `json:"deviceId"`
	DeviceState     string `json:"deviceState"`
	ActivatedEpoch  uint64 `json:"activatedEpoch"`
}

type devicesBody struct {
	Devices []struct {
		DeviceID   string `json:"deviceId"`
		State      string `json:"state"`
		AddedIndex uint64 `json:"addedIndex"`
	} `json:"devices"`
	Pairings []struct {
		DeviceID string `json:"deviceId"`
		State    string `json:"state"`
		Offer    string `json:"offer"`
	} `json:"pairings"`
}

type keyPackageBody struct {
	VaultID           string `json:"vaultId"`
	SenderDeviceID    string `json:"senderDeviceId"`
	RecipientDeviceID string `json:"recipientDeviceId"`
	VaultEpoch        uint64 `json:"vaultEpoch"`
	EntryDigest       string `json:"entryDigest"`
	Ciphertext        string `json:"ciphertext"`
	Signature         string `json:"signature"`
}

func wireKeyPackage(pkg syncproto.KeyPackageV3) keyPackageBody {
	return keyPackageBody{
		VaultID: pkg.VaultID, SenderDeviceID: pkg.SenderDeviceID, RecipientDeviceID: pkg.RecipientDeviceID,
		VaultEpoch: pkg.VaultEpoch, EntryDigest: pkg.EntryDigest, Ciphertext: pkg.Ciphertext, Signature: pkg.Signature,
	}
}

func (f *fixture) verifiedList(d device) *syncproto.DeviceList {
	f.t.Helper()
	recorder := f.get(d, "/v1/sync/device-list?after=0")
	expectStatus(f.t, recorder, http.StatusOK)
	var body struct {
		Entries []syncproto.DeviceListEntry `json:"entries"`
	}
	decode(f.t, recorder, &body)
	list, err := syncproto.VerifyDeviceList(body.Entries)
	if err != nil {
		f.t.Fatalf("served device list does not verify: %v", err)
	}
	return list
}

func (f *fixture) createVault(owner device) {
	f.t.Helper()
	entry := syncproto.SignDeviceListEntry(owner.key, syncproto.DeviceListEntry{
		VaultID: testVaultID, Index: 1, Kind: syncproto.EntryCreate,
		SignerDeviceID: owner.id, SubjectDeviceID: owner.id,
		SubjectSigningKey: owner.signingKey(), SubjectPackageKey: testPackageKey,
		SealedLabel: encodeBase64(bytes.Repeat([]byte{1}, 48)), EpochAfter: 1,
	})
	expectStatus(f.t, f.signedJSON(owner, http.MethodPost, "/v1/sync/vault", map[string]any{"entry": entry}), http.StatusCreated)
}

func (f *fixture) beginPairing(newcomer device) *httptest.ResponseRecorder {
	return f.signedJSON(newcomer, http.MethodPost, "/v1/sync/pairings", map[string]any{
		"deviceId": newcomer.id, "signingKey": newcomer.signingKey(), "packageKey": testPackageKey,
		"message": encodeBase64([]byte("offer")),
	})
}

func TestHappyPathOverHTTP(t *testing.T) {
	f := newFixture(t)
	owner := newDevice(deviceAID, "a")
	newcomer := newDevice(deviceBID, "b")

	expectError(t, f.get(owner, "/v1/sync/state"), http.StatusNotFound, "sync_vault_not_found")

	entry := syncproto.SignDeviceListEntry(owner.key, syncproto.DeviceListEntry{
		VaultID: testVaultID, Index: 1, Kind: syncproto.EntryCreate,
		SignerDeviceID: owner.id, SubjectDeviceID: owner.id,
		SubjectSigningKey: owner.signingKey(), SubjectPackageKey: testPackageKey,
		SealedLabel: encodeBase64(bytes.Repeat([]byte{1}, 48)), EpochAfter: 1,
	})
	created := f.signedJSON(owner, http.MethodPost, "/v1/sync/vault", map[string]any{"entry": entry})
	expectStatus(t, created, http.StatusCreated)
	var vault vaultBody
	decode(t, created, &vault)
	if vault != (vaultBody{VaultID: testVaultID, VaultEpoch: 1, HeadIndex: 1}) {
		t.Fatalf("created vault %+v", vault)
	}

	state := f.get(owner, "/v1/sync/state")
	expectStatus(t, state, http.StatusOK)
	var ownerState stateBody
	decode(t, state, &ownerState)
	if ownerState.DeviceState != "approved" || ownerState.DeviceID != owner.id || ownerState.CurrentRevision != 0 ||
		ownerState.ActivatedEpoch != 1 || ownerState.InstanceKey != encodeBase64(f.receiptKey) {
		t.Fatalf("owner state %+v", ownerState)
	}

	expectStatus(t, f.beginPairing(newcomer), http.StatusCreated)

	devices := f.get(owner, "/v1/sync/devices")
	expectStatus(t, devices, http.StatusOK)
	var listed devicesBody
	decode(t, devices, &listed)
	if len(listed.Devices) != 1 || listed.Devices[0].DeviceID != owner.id || listed.Devices[0].AddedIndex != 1 {
		t.Fatalf("devices %+v", listed.Devices)
	}
	if len(listed.Pairings) != 1 || listed.Pairings[0].DeviceID != newcomer.id || listed.Pairings[0].State != "offered" ||
		listed.Pairings[0].Offer != encodeBase64([]byte("offer")) {
		t.Fatalf("pairings %+v", listed.Pairings)
	}

	mine := f.get(newcomer, "/v1/sync/pairings/mine")
	expectStatus(t, mine, http.StatusOK)
	var status struct {
		State string `json:"state"`
	}
	decode(t, mine, &status)
	if status.State != "offered" {
		t.Fatalf("pairing state %q", status.State)
	}

	expectStatus(t, f.signedJSON(owner, http.MethodPost, "/v1/sync/pairings/"+newcomer.id+"/answer",
		map[string]any{"message": encodeBase64([]byte("answer"))}), http.StatusNoContent)
	expectStatus(t, f.signedJSON(newcomer, http.MethodPost, "/v1/sync/pairings/mine/confirm",
		map[string]any{"message": encodeBase64([]byte("confirm"))}), http.StatusNoContent)

	list := f.verifiedList(owner)
	addEntry := syncproto.SignDeviceListEntry(owner.key, syncproto.DeviceListEntry{
		VaultID: testVaultID, Index: list.Head() + 1, PreviousDigest: list.HeadDigest(),
		Kind: syncproto.EntryAdd, SignerDeviceID: owner.id, SubjectDeviceID: newcomer.id,
		SubjectSigningKey: newcomer.signingKey(), SubjectPackageKey: testPackageKey,
		SealedLabel: encodeBase64(bytes.Repeat([]byte{2}, 48)), EpochAfter: list.Epoch(),
	})
	pkg := syncproto.KeyPackageV3{
		VaultID: testVaultID, SenderDeviceID: owner.id, RecipientDeviceID: newcomer.id,
		VaultEpoch: list.Epoch(), EntryDigest: addEntry.Digest(), Ciphertext: encodeBase64(bytes.Repeat([]byte{3}, 48)),
	}
	pkg.Signature = encodeBase64(ed25519.Sign(owner.key, pkg.SigningPayload()))
	expectStatus(t, f.signedJSON(owner, http.MethodPost, "/v1/sync/devices", map[string]any{
		"entry": addEntry, "package": wireKeyPackage(pkg),
	}), http.StatusCreated)

	keyPackage := f.get(newcomer, "/v1/sync/key-package")
	expectStatus(t, keyPackage, http.StatusOK)
	var delivered struct {
		Package keyPackageBody `json:"package"`
	}
	decode(t, keyPackage, &delivered)
	got := syncproto.KeyPackageV3{
		VaultID: delivered.Package.VaultID, SenderDeviceID: delivered.Package.SenderDeviceID,
		RecipientDeviceID: delivered.Package.RecipientDeviceID, VaultEpoch: delivered.Package.VaultEpoch,
		EntryDigest: delivered.Package.EntryDigest, Ciphertext: delivered.Package.Ciphertext, Signature: delivered.Package.Signature,
	}
	if got != pkg {
		t.Fatalf("delivered package %+v, want %+v", got, pkg)
	}

	expectStatus(t, f.signedJSON(newcomer, http.MethodPost, "/v1/sync/activate",
		map[string]any{"packageDigest": got.Digest()}), http.StatusNoContent)

	list = f.verifiedList(owner)
	ciphertext := bytes.Repeat([]byte{9}, 1024)
	digest := sha256.Sum256(ciphertext)
	header := syncproto.Header{
		Version: syncproto.VersionV3, VaultID: testVaultID, DeviceID: owner.id, Revision: 1,
		VaultEpoch: list.Epoch(), DeviceListIndex: list.Head(), DeviceListDigest: list.HeadDigest(),
		CiphertextDigest: encodeBase64(digest[:]), CiphertextLength: uint64(len(ciphertext)),
		Nonce: randomBase64(t, syncproto.XChaChaNonceBytes),
	}
	header = syncproto.SignHeader(owner.key, header)
	uploaded := f.signedJSON(owner, http.MethodPost, "/v1/sync/snapshots", map[string]any{
		"header": header, "ciphertext": encodeBase64(ciphertext),
	})
	expectStatus(t, uploaded, http.StatusCreated)
	var receipt struct {
		Revision   uint64 `json:"revision"`
		Digest     string `json:"digest"`
		Receipt    string `json:"receipt"`
		AcceptedAt string `json:"acceptedAt"`
	}
	decode(t, uploaded, &receipt)
	if receipt.Revision != 1 || receipt.Digest != header.Digest() || receipt.AcceptedAt == "" {
		t.Fatalf("upload receipt %+v", receipt)
	}
	if _, err := base64.RawURLEncoding.DecodeString(receipt.Receipt); err != nil || len(receipt.Receipt) != 86 {
		t.Fatalf("receipt %q is not an Ed25519 signature", receipt.Receipt)
	}

	headers := f.get(owner, "/v1/sync/headers?after=0&limit=10")
	expectStatus(t, headers, http.StatusOK)
	var listedHeaders struct {
		Headers []struct {
			Revision uint64 `json:"revision"`
			Digest   string `json:"digest"`
		} `json:"headers"`
	}
	decode(t, headers, &listedHeaders)
	if len(listedHeaders.Headers) != 1 || listedHeaders.Headers[0].Revision != 1 || listedHeaders.Headers[0].Digest != header.Digest() {
		t.Fatalf("headers %+v", listedHeaders.Headers)
	}

	snapshot := f.get(owner, "/v1/sync/snapshot")
	expectStatus(t, snapshot, http.StatusOK)
	var stored struct {
		Header     syncproto.Header `json:"header"`
		Digest     string           `json:"digest"`
		Ciphertext string           `json:"ciphertext"`
	}
	decode(t, snapshot, &stored)
	if stored.Header != header || stored.Digest != header.Digest() || stored.Ciphertext != encodeBase64(ciphertext) {
		t.Fatalf("snapshot does not match the upload")
	}

	list = f.verifiedList(owner)
	removeEntry := syncproto.SignDeviceListEntry(owner.key, syncproto.DeviceListEntry{
		VaultID: testVaultID, Index: list.Head() + 1, PreviousDigest: list.HeadDigest(),
		Kind: syncproto.EntryRemove, SignerDeviceID: owner.id, SubjectDeviceID: newcomer.id,
		EpochAfter: list.Epoch() + 1,
	})
	expectStatus(t, f.signedJSON(owner, http.MethodPost, "/v1/sync/devices/remove", map[string]any{
		"entry": removeEntry, "packages": []keyPackageBody{},
	}), http.StatusNoContent)

	state = f.get(owner, "/v1/sync/state")
	decode(t, state, &ownerState)
	if ownerState.VaultEpoch != 2 || ownerState.HeadIndex != removeEntry.Index || ownerState.CurrentRevision != 1 || ownerState.ActivatedEpoch != 2 {
		t.Fatalf("owner state after remove %+v", ownerState)
	}
	removed := f.get(newcomer, "/v1/sync/state")
	expectStatus(t, removed, http.StatusOK)
	var removedState stateBody
	decode(t, removed, &removedState)
	if removedState.DeviceState != "none" {
		t.Fatalf("removed device state %q, want none", removedState.DeviceState)
	}

	expectStatus(t, f.signedJSON(owner, http.MethodPost, "/v1/sync/reset", map[string]any{"vaultEpoch": 2}), http.StatusNoContent)
	expectError(t, f.get(owner, "/v1/sync/state"), http.StatusNotFound, "sync_vault_not_found")
}

func TestMissingSignatureIsRejected(t *testing.T) {
	f := newFixture(t)
	owner := newDevice(deviceAID, "a")
	body := mustJSON(t, map[string]any{"entry": map[string]any{}})
	recorder := f.serve(owner, http.MethodPost, "/v1/sync/vault", bytes.NewReader(body), "")
	expectError(t, recorder, http.StatusUnauthorized, "sync_signature_invalid")
	if f.storage.calls != 0 {
		t.Fatalf("service was called %d times for a request with no signature", f.storage.calls)
	}
}

func TestMalformedSignatureIsRejected(t *testing.T) {
	f := newFixture(t)
	owner := newDevice(deviceAID, "a")
	target := "/v1/sync/reset"
	body := mustJSON(t, map[string]any{"vaultEpoch": 1})
	valid := f.sign(owner.key, owner.token, http.MethodPost, target, body)
	parts := strings.Split(valid, ".")
	cases := map[string]string{
		"not a signature":        "garbage",
		"two parts":              parts[0] + "." + parts[1],
		"four parts":             valid + ".extra",
		"leading zero timestamp": "0" + valid,
		"negative timestamp":     "-" + valid,
		"short nonce":            parts[0] + "." + parts[1][:10] + "." + parts[2],
		"padded nonce":           parts[0] + "." + parts[1] + "==." + parts[2],
		"short signature":        parts[0] + "." + parts[1] + "." + parts[2][:10],
		"padded signature":       parts[0] + "." + parts[1] + "." + parts[2] + "==",
	}
	for name, signature := range cases {
		t.Run(name, func(t *testing.T) {
			recorder := f.serve(owner, http.MethodPost, target, bytes.NewReader(body), signature)
			expectError(t, recorder, http.StatusUnauthorized, "sync_signature_invalid")
		})
	}
	if f.storage.calls != 0 {
		t.Fatalf("service was called %d times for malformed signatures", f.storage.calls)
	}
}

type endlessBody struct {
	read int
}

func (body *endlessBody) Read(buffer []byte) (int, error) {
	for index := range buffer {
		buffer[index] = 'a'
	}
	body.read += len(buffer)
	return len(buffer), nil
}

func TestOversizeBodyIsRejectedWithoutReadingIt(t *testing.T) {
	f := newFixture(t)
	owner := newDevice(deviceAID, "a")
	target := "/v1/sync/vault"
	reader := &endlessBody{}
	signature := f.sign(owner.key, owner.token, http.MethodPost, target, nil)
	recorder := f.serve(owner, http.MethodPost, target, reader, signature)
	expectError(t, recorder, http.StatusBadRequest, "sync_invalid")
	if reader.read > smallBodyLimit+1 {
		t.Fatalf("handler read %d bytes from a body limited to %d", reader.read, smallBodyLimit)
	}
	if f.storage.calls != 0 {
		t.Fatalf("service was called %d times for an oversize body", f.storage.calls)
	}
}

func TestUnknownFieldIsRejected(t *testing.T) {
	f := newFixture(t)
	owner := newDevice(deviceAID, "a")
	entry := syncproto.SignDeviceListEntry(owner.key, syncproto.DeviceListEntry{
		VaultID: testVaultID, Index: 1, Kind: syncproto.EntryCreate,
		SignerDeviceID: owner.id, SubjectDeviceID: owner.id,
		SubjectSigningKey: owner.signingKey(), SubjectPackageKey: testPackageKey,
		SealedLabel: encodeBase64(bytes.Repeat([]byte{1}, 48)), EpochAfter: 1,
	})
	topLevel := map[string]any{"entry": entry, "extra": true}
	nested := map[string]any{"entry": map[string]any{"vaultId": testVaultID, "bogus": 1}}
	for name, value := range map[string]any{"top level": topLevel, "nested": nested} {
		t.Run(name, func(t *testing.T) {
			expectError(t, f.signedJSON(owner, http.MethodPost, "/v1/sync/vault", value), http.StatusBadRequest, "sync_invalid")
		})
	}
	if f.storage.calls != 0 {
		t.Fatalf("service was called %d times for unknown fields", f.storage.calls)
	}
}

func TestTrailingDataIsRejected(t *testing.T) {
	f := newFixture(t)
	owner := newDevice(deviceAID, "a")
	valid := mustJSON(t, map[string]any{"vaultEpoch": 1})
	for name, suffix := range map[string]string{"object": " {}", "text": "x"} {
		t.Run(name, func(t *testing.T) {
			body := append(append([]byte{}, valid...), suffix...)
			expectError(t, f.signed(owner, http.MethodPost, "/v1/sync/reset", body), http.StatusBadRequest, "sync_invalid")
		})
	}
}

func TestInvalidQueryIsRejected(t *testing.T) {
	f := newFixture(t)
	owner := newDevice(deviceAID, "a")
	expectError(t, f.get(owner, "/v1/sync/device-list?after=-1"), http.StatusBadRequest, "sync_invalid")
	expectError(t, f.get(owner, "/v1/sync/headers?after=abc"), http.StatusBadRequest, "sync_invalid")
}

func TestReplayedRequestIsRejected(t *testing.T) {
	f := newFixture(t)
	owner := newDevice(deviceAID, "a")
	newcomer := newDevice(deviceBID, "b")
	f.createVault(owner)

	body := mustJSON(t, map[string]any{
		"deviceId": newcomer.id, "signingKey": newcomer.signingKey(), "packageKey": testPackageKey,
		"message": encodeBase64([]byte("offer")),
	})
	target := "/v1/sync/pairings"
	signature := f.sign(newcomer.key, newcomer.token, http.MethodPost, target, body)
	expectStatus(t, f.serve(newcomer, http.MethodPost, target, bytes.NewReader(body), signature), http.StatusCreated)
	expectError(t, f.serve(newcomer, http.MethodPost, target, bytes.NewReader(body), signature), http.StatusConflict, "sync_replayed")
}

func TestStolenTokenWithWrongSignatureIsRejected(t *testing.T) {
	f := newFixture(t)
	owner := newDevice(deviceAID, "a")
	attacker := newDevice(deviceBID, "attacker")
	entry := syncproto.SignDeviceListEntry(owner.key, syncproto.DeviceListEntry{
		VaultID: testVaultID, Index: 1, Kind: syncproto.EntryCreate,
		SignerDeviceID: owner.id, SubjectDeviceID: owner.id,
		SubjectSigningKey: owner.signingKey(), SubjectPackageKey: testPackageKey,
		SealedLabel: encodeBase64(bytes.Repeat([]byte{1}, 48)), EpochAfter: 1,
	})
	body := mustJSON(t, map[string]any{"entry": entry})
	signature := f.sign(attacker.key, owner.token, http.MethodPost, "/v1/sync/vault", body)
	expectError(t, f.serve(owner, http.MethodPost, "/v1/sync/vault", bytes.NewReader(body), signature), http.StatusUnauthorized, "sync_signature_invalid")
}

func TestHostRefusalStopsTheHandler(t *testing.T) {
	f := newFixture(t)
	owner := newDevice(deviceAID, "a")
	entry := syncproto.SignDeviceListEntry(owner.key, syncproto.DeviceListEntry{
		VaultID: testVaultID, Index: 1, Kind: syncproto.EntryCreate,
		SignerDeviceID: owner.id, SubjectDeviceID: owner.id,
		SubjectSigningKey: owner.signingKey(), SubjectPackageKey: testPackageKey,
		SealedLabel: encodeBase64(bytes.Repeat([]byte{1}, 48)), EpochAfter: 1,
	})
	f.host.refuse = "vault"
	callsBefore := f.storage.calls
	recorder := f.signedJSON(owner, http.MethodPost, "/v1/sync/vault", map[string]any{"entry": entry})
	expectError(t, recorder, http.StatusTooManyRequests, "rate_limited")
	if f.storage.calls != callsBefore {
		t.Fatalf("service ran %d times after the host refused", f.storage.calls-callsBefore)
	}
	if f.host.calls == 0 {
		t.Fatal("host was not asked to authenticate")
	}
}

func TestUnknownServiceErrorIsHidden(t *testing.T) {
	const internal = "disk exploded at /var/lib/sync/vault.db"
	f := newFixture(t)
	owner := newDevice(deviceAID, "a")
	entry := syncproto.SignDeviceListEntry(owner.key, syncproto.DeviceListEntry{
		VaultID: testVaultID, Index: 1, Kind: syncproto.EntryCreate,
		SignerDeviceID: owner.id, SubjectDeviceID: owner.id,
		SubjectSigningKey: owner.signingKey(), SubjectPackageKey: testPackageKey,
		SealedLabel: encodeBase64(bytes.Repeat([]byte{1}, 48)), EpochAfter: 1,
	})
	f.storage.fail = errors.New(internal)

	responses := map[string]*httptest.ResponseRecorder{
		"state":       f.get(owner, "/v1/sync/state"),
		"vault":       f.signedJSON(owner, http.MethodPost, "/v1/sync/vault", map[string]any{"entry": entry}),
		"device-list": f.get(owner, "/v1/sync/device-list"),
	}
	for name, recorder := range responses {
		t.Run(name, func(t *testing.T) {
			expectStatus(t, recorder, http.StatusServiceUnavailable)
			if strings.Contains(recorder.Body.String(), "disk") || strings.Contains(recorder.Body.String(), "/var/lib") {
				t.Fatalf("response leaks the internal error: %s", recorder.Body.String())
			}
		})
	}

	recorder := f.get(owner, "/v1/sync/state")
	expectStatus(t, recorder, http.StatusServiceUnavailable)
	want := `{"error":{"code":"sync_unavailable","message":"Sesame Sync is temporarily unavailable."}}` + "\n"
	if recorder.Body.String() != want {
		t.Fatalf("body %q, want %q", recorder.Body.String(), want)
	}
}

func (f *fixture) checkpoint(d device, observedAt uint64) syncproto.Checkpoint {
	f.t.Helper()
	list := f.verifiedList(d)
	return syncproto.SignCheckpoint(d.key, syncproto.Checkpoint{
		Version: syncproto.VersionV3, VaultID: testVaultID, DeviceID: d.id, DeviceListIndex: list.Head(),
		DeviceListDigest: list.HeadDigest(), ObservedAt: observedAt,
	})
}

func TestCheckpointsOverHTTP(t *testing.T) {
	f := newFixture(t)
	owner := newDevice(deviceAID, "a")
	f.createVault(owner)
	now := uint64(testStart.Unix())

	empty := f.get(owner, "/v1/sync/checkpoints")
	expectStatus(t, empty, http.StatusOK)
	if !strings.Contains(empty.Body.String(), `"checkpoints":[]`) {
		t.Fatalf("an empty list should be an empty array: %s", empty.Body.String())
	}

	checkpoint := f.checkpoint(owner, now)
	expectStatus(t, f.signedJSON(owner, http.MethodPost, "/v1/sync/checkpoints", map[string]any{"checkpoint": checkpoint}), http.StatusNoContent)

	listed := f.get(owner, "/v1/sync/checkpoints")
	expectStatus(t, listed, http.StatusOK)
	var body struct {
		Checkpoints []struct {
			syncproto.Checkpoint
			AcceptedAt string `json:"acceptedAt"`
		} `json:"checkpoints"`
	}
	decode(t, listed, &body)
	if len(body.Checkpoints) != 1 || body.Checkpoints[0].Checkpoint != checkpoint || body.Checkpoints[0].AcceptedAt == "" {
		t.Fatalf("checkpoints %+v", body.Checkpoints)
	}
	if err := body.Checkpoints[0].VerifyAgainst(f.verifiedList(owner)); err != nil {
		t.Fatalf("a served checkpoint does not verify: %v", err)
	}
}

func TestCheckpointRefusalsOverHTTP(t *testing.T) {
	f := newFixture(t)
	owner := newDevice(deviceAID, "a")
	f.createVault(owner)
	now := uint64(testStart.Unix())
	good := f.checkpoint(owner, now)

	forged := good
	forged.ObservedAt++
	expectError(t, f.signedJSON(owner, http.MethodPost, "/v1/sync/checkpoints", map[string]any{"checkpoint": forged}), http.StatusBadRequest, "sync_invalid")

	other := good
	other.DeviceID = deviceBID
	other = syncproto.SignCheckpoint(owner.key, other)
	expectError(t, f.signedJSON(owner, http.MethodPost, "/v1/sync/checkpoints", map[string]any{"checkpoint": other}), http.StatusForbidden, "sync_forbidden")

	expectError(t, f.signedJSON(owner, http.MethodPost, "/v1/sync/checkpoints", map[string]any{"checkpoint": good, "extra": 1}), http.StatusBadRequest, "sync_invalid")
	expectError(t, f.signed(owner, http.MethodPost, "/v1/sync/checkpoints", append(mustJSON(t, map[string]any{"checkpoint": good}), []byte(` {}`)...)), http.StatusBadRequest, "sync_invalid")
	expectError(t, f.serve(owner, http.MethodPost, "/v1/sync/checkpoints", bytes.NewReader(mustJSON(t, map[string]any{"checkpoint": good})), ""), http.StatusUnauthorized, "sync_signature_invalid")

	expectStatus(t, f.signedJSON(owner, http.MethodPost, "/v1/sync/checkpoints", map[string]any{"checkpoint": good}), http.StatusNoContent)
	expectError(t, f.signedJSON(owner, http.MethodPost, "/v1/sync/checkpoints", map[string]any{"checkpoint": good}), http.StatusConflict, "sync_conflict")

	stranger := newDevice(deviceBID, "b")
	expectError(t, f.get(stranger, "/v1/sync/checkpoints"), http.StatusForbidden, "sync_forbidden")

	f.host.refuse = "checkpoint-put"
	expectError(t, f.signedJSON(owner, http.MethodPost, "/v1/sync/checkpoints", map[string]any{"checkpoint": f.checkpoint(owner, now+1)}), http.StatusTooManyRequests, "rate_limited")
	f.host.refuse = "checkpoints"
	expectError(t, f.get(owner, "/v1/sync/checkpoints"), http.StatusTooManyRequests, "rate_limited")
}

func TestPairingOffersAcceptAClassicalOrAHybridPackageKeyAndNothingElse(t *testing.T) {
	offer := func(f *fixture, newcomer device, packageKey string) *httptest.ResponseRecorder {
		return f.signedJSON(newcomer, http.MethodPost, "/v1/sync/pairings", map[string]any{
			"deviceId": newcomer.id, "signingKey": newcomer.signingKey(), "packageKey": packageKey,
			"message": encodeBase64([]byte("offer")),
		})
	}
	hybrid := encodeBase64(bytes.Repeat([]byte{5}, syncproto.XWingPublicKeyBytes))

	f := newFixture(t)
	owner := newDevice(deviceAID, "a")
	f.createVault(owner)
	expectStatus(t, offer(f, newDevice(deviceBID, "b"), hybrid), http.StatusCreated)

	for _, length := range []int{0, 31, 33, 1215, 1217} {
		f := newFixture(t)
		f.createVault(owner)
		bad := encodeBase64(bytes.Repeat([]byte{5}, length))
		expectError(t, offer(f, newDevice(deviceBID, "b"), bad), http.StatusBadRequest, "sync_invalid")
	}
}

func TestAHybridDeviceIsApprovedAndReceivesAHybridSizedPackageOverHTTP(t *testing.T) {
	hybridKey := encodeBase64(bytes.Repeat([]byte{5}, syncproto.XWingPublicKeyBytes))
	hybridPackage := bytes.Repeat([]byte{3}, 1+1120+24+48)

	for _, size := range []int{15, syncproto.MaxEncryptedKeyPackageBytes + 1} {
		f := newFixture(t)
		owner := newDevice(deviceAID, "a")
		newcomer := newDevice(deviceBID, "b")
		f.createVault(owner)
		f.confirmPairing(owner, newcomer, hybridKey)
		list := f.verifiedList(owner)
		addEntry := hybridAddEntry(owner, newcomer, list, hybridKey)
		pkg := signedPackage(owner, newcomer, list, addEntry, bytes.Repeat([]byte{3}, size))
		recorder := f.signedJSON(owner, http.MethodPost, "/v1/sync/devices", map[string]any{
			"entry": addEntry, "package": wireKeyPackage(pkg),
		})
		if recorder.Code == http.StatusCreated {
			t.Fatalf("a key package of %d bytes was accepted", size)
		}
	}

	f := newFixture(t)
	owner := newDevice(deviceAID, "a")
	newcomer := newDevice(deviceBID, "b")
	f.createVault(owner)
	f.confirmPairing(owner, newcomer, hybridKey)
	list := f.verifiedList(owner)
	addEntry := hybridAddEntry(owner, newcomer, list, hybridKey)
	pkg := signedPackage(owner, newcomer, list, addEntry, hybridPackage)
	expectStatus(t, f.signedJSON(owner, http.MethodPost, "/v1/sync/devices", map[string]any{
		"entry": addEntry, "package": wireKeyPackage(pkg),
	}), http.StatusCreated)

	served := f.verifiedList(newcomer)
	member, ok := served.MemberAt(newcomer.id, addEntry.Index)
	if !ok || member.PackageKey != hybridKey {
		t.Fatal("the service did not keep the hybrid package key on the new member")
	}
	keyPackage := f.get(newcomer, "/v1/sync/key-package")
	expectStatus(t, keyPackage, http.StatusOK)
	var delivered struct {
		Package keyPackageBody `json:"package"`
	}
	decode(t, keyPackage, &delivered)
	if delivered.Package.Ciphertext != pkg.Ciphertext || delivered.Package.Signature != pkg.Signature {
		t.Fatal("the delivered package differs from the one the approver signed")
	}
}

func (f *fixture) confirmPairing(owner, newcomer device, packageKey string) {
	f.t.Helper()
	expectStatus(f.t, f.signedJSON(newcomer, http.MethodPost, "/v1/sync/pairings", map[string]any{
		"deviceId": newcomer.id, "signingKey": newcomer.signingKey(), "packageKey": packageKey,
		"message": encodeBase64([]byte("offer")),
	}), http.StatusCreated)
	expectStatus(f.t, f.signedJSON(owner, http.MethodPost, "/v1/sync/pairings/"+newcomer.id+"/answer",
		map[string]any{"message": encodeBase64([]byte("answer"))}), http.StatusNoContent)
	expectStatus(f.t, f.signedJSON(newcomer, http.MethodPost, "/v1/sync/pairings/mine/confirm",
		map[string]any{"message": encodeBase64([]byte("confirm"))}), http.StatusNoContent)
}

func hybridAddEntry(owner, newcomer device, list *syncproto.DeviceList, packageKey string) syncproto.DeviceListEntry {
	return syncproto.SignDeviceListEntry(owner.key, syncproto.DeviceListEntry{
		VaultID: testVaultID, Index: list.Head() + 1, PreviousDigest: list.HeadDigest(),
		Kind: syncproto.EntryAdd, SignerDeviceID: owner.id, SubjectDeviceID: newcomer.id,
		SubjectSigningKey: newcomer.signingKey(), SubjectPackageKey: packageKey,
		SealedLabel: encodeBase64(bytes.Repeat([]byte{2}, 48)), EpochAfter: list.Epoch(),
	})
}

func signedPackage(owner, newcomer device, list *syncproto.DeviceList, entry syncproto.DeviceListEntry, ciphertext []byte) syncproto.KeyPackageV3 {
	pkg := syncproto.KeyPackageV3{
		VaultID: testVaultID, SenderDeviceID: owner.id, RecipientDeviceID: newcomer.id,
		VaultEpoch: list.Epoch(), EntryDigest: entry.Digest(), Ciphertext: encodeBase64(ciphertext),
	}
	pkg.Signature = encodeBase64(ed25519.Sign(owner.key, pkg.SigningPayload()))
	return pkg
}
