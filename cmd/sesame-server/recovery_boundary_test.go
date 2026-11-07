package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"

	"usesesame.app/backend/internal/selfhost"
	"usesesame.app/backend/internal/selfhost/config"
	"usesesame.app/backend/internal/selfhost/secrets"
)

func TestRecoveryStatusEscapesPrincipalIdentifiers(t *testing.T) {
	var output bytes.Buffer
	ownerID := "owner\x1b[2J"
	deviceID := "device\nforged line"
	holderID := "holder\rforged line"
	err := writeRecoveryStatus(Invocation{Stdout: &output}, false, selfhost.RecoveryStatus{Pending: true, Generation: strings.Repeat("a", 32)},
		[]selfhost.Owner{{ID: ownerID, Name: "Ada"}},
		[]selfhost.Device{{ID: deviceID, Name: "Laptop", Holder: selfhost.Holder{Kind: selfhost.HolderOwner, ID: holderID, Name: "Ada"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{ownerID, deviceID, holderID} {
		if strings.Contains(output.String(), id) || !strings.Contains(output.String(), strconv.Quote(id)) {
			t.Fatalf("status did not escape identifier %q: %q", id, output.String())
		}
	}
}

func TestServingOpenPreservesRecoveryError(t *testing.T) {
	instance := newRecoveryTestInstance(t, "Ada")
	dir := restoreRecoveryArchive(t, instance.backup(t))
	cfg, err := config.Load(lookupFrom(recoveryValues(dir)))
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := secrets.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openStore(context.Background(), cfg, loaded, storeServing); !errors.Is(err, selfhost.ErrRecoveryPending) {
		t.Fatalf("serving open lost the pending recovery error: %v", err)
	}
}

func TestStoreOpenRejectsUnknownModeWithoutCreatingDatabase(t *testing.T) {
	dir := t.TempDir()
	cfg, err := config.Load(lookupFrom(recoveryValues(dir)))
	if err != nil {
		t.Fatal(err)
	}
	store, err := openStore(context.Background(), cfg, &secrets.Secrets{AdminEncryptionKey: make([]byte, 32)}, 255)
	if store != nil {
		_ = store.Close()
	}
	if err == nil {
		t.Fatal("an unknown store mode was accepted")
	}
	if _, err := os.Lstat(databasePath(cfg)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an unknown mode changed the database: %v", err)
	}
}
