package server_test

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"usesesame.app/backend/internal/selfhost"
	"usesesame.app/backend/internal/selfhost/server"
	"usesesame.app/backend/internal/selfhost/sqlitestore"
	"usesesame.app/backend/internal/syncproto"
	"usesesame.app/backend/internal/syncsvc"
)

func withSync(t *testing.T) option {
	return func(cfg *server.Config) {
		store, ok := cfg.Store.(*sqlitestore.Store)
		if !ok {
			t.Fatal("the sync tests need the SQLite store")
		}
		service, err := syncsvc.New(store.SyncStorage(), syncsvc.Config{ReceiptKey: cfg.SigningKey, Now: cfg.Now})
		if err != nil {
			t.Fatal(err)
		}
		cfg.Sync = service
	}
}

func rawB64(value []byte) string {
	return base64.RawURLEncoding.EncodeToString(value)
}

type syncDevice struct {
	e        *env
	token    string
	tokenID  string
	id       string
	key      ed25519.PrivateKey
	nonce    int
	vaultID  string
	previous *syncproto.Header
}

func (e *env) newSyncDevice(token, tokenID, name string) *syncDevice {
	seed := sha256.Sum256([]byte("sync-device-" + name))
	return &syncDevice{
		e: e, token: token, tokenID: tokenID, key: ed25519.NewKeyFromSeed(seed[:]),
		id: ("device" + name + "0000000000000000")[:20], vaultID: "vaultAAAAAAAAAAAA01",
	}
}

func (d *syncDevice) signingKey() string { return rawB64(d.key.Public().(ed25519.PublicKey)) }
func (d *syncDevice) packageKey() string { return rawB64(bytes.Repeat([]byte{7}, 32)) }

func (d *syncDevice) call(method, path string, body any, wrongSignature bool) (code int, response map[string]any) {
	d.e.t.Helper()
	var raw []byte
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			d.e.t.Fatal(err)
		}
		raw = encoded
	}
	d.nonce++
	nonce := rawB64(bytes.Repeat([]byte{byte(d.nonce)}, syncproto.RequestNonceBytes))
	timestamp := uint64(d.e.clock.Now().Unix())
	signed := syncproto.RequestSignature{Method: method, Target: path, Body: raw, Token: d.token, Timestamp: timestamp, Nonce: nonce}
	signature := ed25519.Sign(d.key, signed.SigningPayload())
	if wrongSignature {
		signature[0] ^= 1
	}
	header := bearer(d.token)
	if method != http.MethodGet {
		header["Sesame-Request-Signature"] = fmt.Sprintf("%d.%s.%s", timestamp, nonce, rawB64(signature))
	}
	recorder := d.e.do(req{method: method, path: path, raw: string(raw), header: header})
	parsed := map[string]any{}
	_ = json.Unmarshal(recorder.Body.Bytes(), &parsed)
	return recorder.Code, parsed
}

func (d *syncDevice) createVault() {
	d.e.t.Helper()
	entry := syncproto.SignDeviceListEntry(d.key, syncproto.DeviceListEntry{
		VaultID: d.vaultID, Index: 1, Kind: syncproto.EntryCreate, SignerDeviceID: d.id, SubjectDeviceID: d.id,
		SubjectSigningKey: d.signingKey(), SubjectPackageKey: d.packageKey(),
		SealedLabel: rawB64(bytes.Repeat([]byte{1}, 48)), EpochAfter: 1,
	})
	code, body := d.call(http.MethodPost, "/v1/sync/vault", map[string]any{"entry": entry}, false)
	if code != http.StatusCreated {
		d.e.t.Fatalf("create vault = %d %v", code, body)
	}
}

func (d *syncDevice) deviceList() *syncproto.DeviceList {
	d.e.t.Helper()
	recorder := d.e.do(req{path: "/v1/sync/device-list", header: bearer(d.token)})
	expectStatus(d.e.t, recorder, http.StatusOK)
	var parsed struct {
		Entries []syncproto.DeviceListEntry `json:"entries"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &parsed); err != nil {
		d.e.t.Fatal(err)
	}
	list, err := syncproto.VerifyDeviceList(parsed.Entries)
	if err != nil {
		d.e.t.Fatalf("the served device list does not verify: %v", err)
	}
	return list
}

func (d *syncDevice) upload(seed byte) (int, map[string]any) {
	d.e.t.Helper()
	list := d.deviceList()
	ciphertext := bytes.Repeat([]byte{seed}, 256)
	index := list.Head()
	digest, _ := list.DigestAt(index)
	epoch, _ := list.EpochAt(index)
	sum := sha256.Sum256(ciphertext)
	revision := uint64(1)
	header := syncproto.Header{
		Version: syncproto.VersionV3, VaultID: d.vaultID, DeviceID: d.id, VaultEpoch: epoch,
		DeviceListIndex: index, DeviceListDigest: digest, CiphertextDigest: rawB64(sum[:]),
		CiphertextLength: uint64(len(ciphertext)), Nonce: rawB64(bytes.Repeat([]byte{5}, syncproto.XChaChaNonceBytes)),
	}
	if d.previous != nil {
		revision = d.previous.Revision + 1
		header.PreviousDigest = d.previous.Digest()
	}
	header.Revision = revision
	header = syncproto.SignHeader(d.key, header)
	code, body := d.call(http.MethodPost, "/v1/sync/snapshots", map[string]any{"header": header, "ciphertext": rawB64(ciphertext)}, false)
	if code == http.StatusCreated {
		stored := header
		d.previous = &stored
	}
	return code, body
}

func enableSync(o *owner) {
	o.e.t.Helper()
	o.e.clock.Advance(selfhost.RecentAuthWindow + time.Minute)
	expectError(o.e.t, o.do(req{method: http.MethodPatch, path: "/v1/owner/flags/" + server.FlagSync, body: map[string]bool{"enabled": true}}), http.StatusForbidden, "step_up_required")
	o.stepUp()
	expectStatus(o.e.t, o.do(req{method: http.MethodPatch, path: "/v1/owner/flags/" + server.FlagSync, body: map[string]bool{"enabled": true}}), http.StatusOK)
}

func TestSyncIsOffUntilTheOwnerTurnsItOn(t *testing.T) {
	e := newSQLiteEnv(t, withSync(t))
	issue, _ := e.setupStore.StartFirstSetup(t.Context())
	o := e.completeSetup(issue.Token, "Dana")
	token, tokenID := e.pairedDevice(o)
	laptop := e.newSyncDevice(token, tokenID, "laptop")

	code, body := laptop.call(http.MethodPost, "/v1/sync/vault", map[string]any{}, false)
	if code != http.StatusForbidden || fmt.Sprint(body["error"]) == "" {
		t.Fatalf("sync before the flag = %d %v", code, body)
	}
	expectError(t, e.do(req{path: "/v1/sync/state", header: bearer(token)}), http.StatusForbidden, "sync_unavailable")
	if modules := decodeBody(t, e.do(req{path: "/v1/instance"}))["modules"].([]any); len(modules) != 0 {
		t.Fatalf("modules before the flag = %v", modules)
	}
	if status := decodeBody(t, e.do(req{path: "/v1/desktop/status", header: bearer(token)})); status["syncAvailable"] != false {
		t.Fatalf("status = %v", status)
	}

	enableSync(o)

	instance := decodeBody(t, e.do(req{path: "/v1/instance"}))
	if modules := instance["modules"].([]any); len(modules) != 1 || modules[0] != "sync" || instance["syncProtocol"] != float64(3) {
		t.Fatalf("instance = %v", instance)
	}
	if status := decodeBody(t, e.do(req{path: "/v1/desktop/status", header: bearer(token)})); status["syncAvailable"] != true {
		t.Fatalf("status = %v", status)
	}
	capabilities := decodeBody(t, e.do(req{path: "/v1/capabilities"}))
	payload, _ := base64.RawURLEncoding.DecodeString(capabilities["payload"].(string))
	var document struct {
		Features map[string]bool `json:"features"`
	}
	if err := json.Unmarshal(payload, &document); err != nil || !document.Features["sync"] {
		t.Fatalf("capabilities = %s %v", payload, err)
	}
}

func TestSyncRoundTripsThroughTheSelfHostedServer(t *testing.T) {
	e := newSQLiteEnv(t, withSync(t))
	issue, _ := e.setupStore.StartFirstSetup(t.Context())
	o := e.completeSetup(issue.Token, "Dana")
	enableSync(o)
	token, tokenID := e.pairedDevice(o)
	laptop := e.newSyncDevice(token, tokenID, "laptop")

	laptop.createVault()
	if code, body := laptop.upload(1); code != http.StatusCreated {
		t.Fatalf("upload = %d %v", code, body)
	}
	if code, body := laptop.upload(2); code != http.StatusCreated || body["revision"] != float64(2) {
		t.Fatalf("second upload = %d %v", code, body)
	}
	state := decodeBody(t, e.do(req{path: "/v1/sync/state", header: bearer(token)}))
	if state["currentRevision"] != float64(2) || state["deviceState"] != "approved" {
		t.Fatalf("state = %v", state)
	}
	instance := decodeBody(t, e.do(req{path: "/v1/instance"}))
	if state["instanceKey"] != instance["capabilityPublicKey"] {
		t.Fatalf("the receipt key %v is not the pinned capability key %v", state["instanceKey"], instance["capabilityPublicKey"])
	}
	snapshot := decodeBody(t, e.do(req{path: "/v1/sync/snapshot", header: bearer(token)}))
	acceptedAt, err := time.Parse(time.RFC3339, snapshot["acceptedAt"].(string))
	if err != nil {
		t.Fatal(err)
	}
	receipt := syncproto.ReceiptV3{VaultID: laptop.vaultID, Revision: 2, HeaderDigest: snapshot["digest"].(string), VaultEpoch: 1, AcceptedAt: uint64(acceptedAt.Unix())}
	signature, err := base64.RawURLEncoding.DecodeString(snapshot["receipt"].(string))
	if err != nil || !receipt.Verify(e.cfg.SigningKey.Public().(ed25519.PublicKey), signature) {
		t.Fatalf("the receipt does not verify under the instance key: %v", err)
	}

	overview := decodeBody(t, o.do(req{path: "/v1/owner/sync"}))
	holders := overview["holders"].([]any)
	first := holders[0].(map[string]any)
	if overview["enabled"] != true || first["hasVault"] != true || first["devices"] != float64(1) || first["currentRevision"] != float64(2) {
		t.Fatalf("overview = %v", overview)
	}
	if _, present := first["name"].(string); !present {
		t.Fatalf("overview names no holder: %v", first)
	}
}

func TestSyncRefusesAStolenTokenAndKeepsAccountsApart(t *testing.T) {
	e := newSQLiteEnv(t, withSync(t))
	issue, _ := e.setupStore.StartFirstSetup(t.Context())
	o := e.completeSetup(issue.Token, "Dana")
	enableSync(o)
	token, tokenID := e.pairedDevice(o)
	laptop := e.newSyncDevice(token, tokenID, "laptop")
	laptop.createVault()
	laptop.upload(1)

	code, _ := laptop.call(http.MethodPost, "/v1/sync/reset", map[string]any{"vaultEpoch": 1}, true)
	if code != http.StatusUnauthorized {
		t.Fatalf("a reset with a wrong signature = %d", code)
	}
	recorder := e.do(req{method: http.MethodPost, path: "/v1/sync/reset", raw: `{"vaultEpoch":1}`, header: bearer(token)})
	expectError(t, recorder, http.StatusUnauthorized, "sync_signature_invalid")
	if state := decodeBody(t, e.do(req{path: "/v1/sync/state", header: bearer(token)})); state["currentRevision"] != float64(1) {
		t.Fatalf("a refused reset changed the vault: %v", state)
	}

	members := o.do(req{method: http.MethodPost, path: "/v1/owner/members", body: map[string]string{"name": "Ana"}})
	expectStatus(t, members, http.StatusCreated)
	memberID := decodeBody(t, members)["member"].(map[string]any)["id"].(string)
	issued := o.pairing(false, memberID)
	linked := decodeBody(t, e.link(issued["code"].(string), "Ana phone"))
	memberToken := linked["accessToken"].(string)
	expectError(t, e.do(req{path: "/v1/sync/state", header: bearer(memberToken)}), http.StatusNotFound, "sync_vault_not_found")
	expectError(t, e.do(req{path: "/v1/sync/snapshot", header: bearer(memberToken)}), http.StatusNotFound, "sync_vault_not_found")

	deviceID := linked["device"].(map[string]any)["deviceId"].(string)
	o.stepUp()
	expectStatus(t, o.do(req{method: http.MethodDelete, path: "/v1/owner/devices/" + tokenID}), http.StatusNoContent)
	expectError(t, e.do(req{path: "/v1/sync/state", header: bearer(token)}), http.StatusUnauthorized, "not_authenticated")
	_ = deviceID
}

func TestTheOwnerCanResetASyncedVaultButNotReadIt(t *testing.T) {
	e := newSQLiteEnv(t, withSync(t))
	issue, _ := e.setupStore.StartFirstSetup(t.Context())
	o := e.completeSetup(issue.Token, "Dana")
	enableSync(o)
	token, tokenID := e.pairedDevice(o)
	laptop := e.newSyncDevice(token, tokenID, "laptop")
	laptop.createVault()
	laptop.upload(1)

	overview := decodeBody(t, o.do(req{path: "/v1/owner/sync"}))
	for _, holder := range overview["holders"].([]any) {
		record := holder.(map[string]any)
		for key := range record {
			switch key {
			case "kind", "id", "name", "hasVault", "devices", "currentRevision", "storedBytes", "updatedAt":
			default:
				t.Fatalf("the console overview exposes %q", key)
			}
		}
	}
	holder := overview["holders"].([]any)[0].(map[string]any)
	path := "/v1/owner/sync/" + holder["kind"].(string) + "/" + holder["id"].(string)
	o.stepUp()
	expectStatus(t, o.do(req{method: http.MethodDelete, path: path}), http.StatusNoContent)
	expectError(t, e.do(req{path: "/v1/sync/state", header: bearer(token)}), http.StatusNotFound, "sync_vault_not_found")
	if again := decodeBody(t, o.do(req{path: "/v1/owner/sync"}))["holders"].([]any)[0].(map[string]any); again["hasVault"] != false {
		t.Fatalf("overview after reset = %v", again)
	}
	expectError(t, o.do(req{method: http.MethodDelete, path: "/v1/owner/sync/owner/not-a-real-holder"}), http.StatusNotFound, "not_found")
	audit := o.do(req{path: "/v1/owner/audit"}).Body.String()
	if !bytes.Contains([]byte(audit), []byte("sync.reset")) {
		t.Fatalf("the reset is not in the audit log: %s", audit)
	}
}
