package syncv3http

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"usesesame.app/backend/internal/syncproto"
)

func sampleEntry() syncproto.DeviceListEntry {
	return syncproto.DeviceListEntry{
		VaultID: "vaultAAAAAAAAAAAA01", Index: 2, PreviousDigest: "prevdigest", Kind: "add",
		SignerDeviceID: "deviceAAAAAAAAAAAA01", SubjectDeviceID: "deviceBBBBBBBBBBBB01",
		SubjectSigningKey: "signingkey", SubjectPackageKey: "packagekey", SealedLabel: "sealed",
		EpochAfter: 1, SnapshotFloor: 4, Signature: "signature",
	}
}

func sampleHeader() syncproto.Header {
	return syncproto.Header{
		Version: 3, VaultID: "vaultAAAAAAAAAAAA01", DeviceID: "deviceAAAAAAAAAAAA01", Revision: 5, VaultEpoch: 1,
		DeviceListIndex: 2, DeviceListDigest: "listdigest", PreviousDigest: "prevdigest",
		CiphertextDigest: "ciphertextdigest", CiphertextLength: 64, Nonce: "nonce", Signature: "signature",
	}
}

func samplePackage() keyPackageJSON {
	return keyPackageJSON{
		VaultID: "vaultAAAAAAAAAAAA01", SenderDeviceID: "deviceAAAAAAAAAAAA01", RecipientDeviceID: "deviceBBBBBBBBBBBB01",
		VaultEpoch: 1, EntryDigest: "entrydigest", Ciphertext: "ciphertext", Signature: "signature",
	}
}

func sampleCheckpoint() syncproto.Checkpoint {
	return syncproto.Checkpoint{
		Version: 3, VaultID: "vaultAAAAAAAAAAAA01", DeviceID: "deviceAAAAAAAAAAAA01", DeviceListIndex: 2,
		DeviceListDigest: "listdigest", HeaderRevision: 5, HeaderDigest: "headerdigest", ObservedAt: 1_800_000_000,
		Signature: "signature",
	}
}

func wireFixture() map[string]any {
	return map[string]any{
		"requests": map[string]any{
			"createVault":  createVaultRequest{Entry: sampleEntry()},
			"beginPairing": beginPairingRequest{DeviceID: "deviceBBBBBBBBBBBB01", SigningKey: "signingkey", PackageKey: "packagekey", Message: "message"},
			"message":      pairingMessageRequest{Message: "message"},
			"approve":      approveRequest{Entry: sampleEntry(), Package: samplePackage()},
			"remove":       removeRequest{Entry: sampleEntry(), Packages: []keyPackageJSON{samplePackage()}},
			"activate":     activateRequest{PackageDigest: "packagedigest"},
			"upload":       uploadRequest{Header: sampleHeader(), Ciphertext: "ciphertext"},
			"reset":        resetRequest{VaultEpoch: 2},
			"checkpoint":   checkpointRequest{Checkpoint: sampleCheckpoint()},
		},
		"responses": map[string]any{
			"state":      stateResponse{VaultID: "vaultAAAAAAAAAAAA01", VaultEpoch: 1, HeadIndex: 2, CurrentRevision: 5, InstanceKey: "instancekey", DeviceID: "deviceAAAAAAAAAAAA01", DeviceState: "approved", ActivatedEpoch: 1},
			"deviceList": deviceListResponse{Entries: []syncproto.DeviceListEntry{sampleEntry()}},
			"devices": devicesResponse{
				Devices:  []deviceRecord{{DeviceID: "deviceAAAAAAAAAAAA01", State: "approved", AddedIndex: 1, ActivatedEpoch: 1}},
				Pairings: []pairingRecord{{DeviceID: "deviceBBBBBBBBBBBB01", SigningKey: "signingkey", PackageKey: "packagekey", State: "offered", Offer: "offer", ExpiresAt: "2026-10-10T09:10:00Z"}},
			},
			"pairingStatus": pairingStatusResponse{State: "offered", ExpiresAt: "2026-10-10T09:10:00Z"},
			"keyPackage":    keyPackageResponse{Package: samplePackage()},
			"headers": headersResponse{Headers: []headerRecord{{
				Header: sampleHeader(), Digest: "digest", Receipt: "receipt", AcceptedAt: "2026-10-10T09:00:00Z",
			}}},
			"snapshot": snapshotResponse{Header: sampleHeader(), Digest: "digest", Receipt: "receipt", AcceptedAt: "2026-10-10T09:00:00Z", Ciphertext: "ciphertext"},
			"upload":   uploadResponse{Revision: 5, Digest: "digest", Receipt: "receipt", AcceptedAt: "2026-10-10T09:00:00Z"},
			"checkpoints": checkpointsResponse{Checkpoints: []checkpointRecord{{
				Checkpoint: sampleCheckpoint(), AcceptedAt: "2026-10-10T09:00:00Z",
			}}},
		},
	}
}

func TestWireFixtureMatchesTheTypes(t *testing.T) {
	encoded, err := json.MarshalIndent(wireFixture(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	if directory := os.Getenv("SESAME_WRITE_SYNC_FIXTURE"); directory != "" {
		if err := os.WriteFile(filepath.Join(directory, "wire-fixture.json"), encoded, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stored, err := os.ReadFile(filepath.Join("testdata", "wire-fixture.json"))
	if err != nil {
		t.Fatalf("read the fixture, regenerate it with SESAME_WRITE_SYNC_FIXTURE=testdata: %v", err)
	}
	if !bytes.Equal(stored, encoded) {
		t.Fatal("testdata/wire-fixture.json no longer matches the wire types, regenerate it and update the desktop copy")
	}
}
