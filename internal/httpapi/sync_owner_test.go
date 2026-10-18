package httpapi

import (
	"crypto/ed25519"
	"strings"
	"testing"

	"usesesame.app/backend/internal/syncsvc"
	"usesesame.app/backend/internal/syncsvc/memstore"
)

const syncOwnerTestPepper = "owner-pepper-for-tests-0123456789abcdef"

func TestSyncOwnerKeyDoesNotRevealTheAccount(t *testing.T) {
	account := "account-fixture-0001"
	key := syncOwnerKey(syncOwnerTestPepper, account)
	if strings.Contains(key, account) || len(key) != 43 {
		t.Fatalf("owner key = %q", key)
	}
	if key != syncOwnerKey(syncOwnerTestPepper, account) {
		t.Fatal("the owner key changed between calls")
	}
	if key == syncOwnerKey(syncOwnerTestPepper, "account-fixture-0002") {
		t.Fatal("two accounts share an owner key")
	}
	if key == syncOwnerKey(syncOwnerTestPepper+"x", account) {
		t.Fatal("a different pepper gave the same owner key")
	}
}

func TestSyncStaysClosedWithoutAUsableOwnerPepper(t *testing.T) {
	_, receiptKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := syncsvc.New(memstore.New(), syncsvc.Config{ReceiptKey: receiptKey})
	if err != nil {
		t.Fatal(err)
	}
	for name, pepper := range map[string]string{
		"missing":   "",
		"too short": strings.Repeat("a", syncOwnerPepperMinimumLength-1),
	} {
		if (&api{config: Config{Sync: service, SyncOwnerPepper: pepper}}).syncHandler() != nil {
			t.Fatalf("Sync opened with a %s pepper", name)
		}
	}
	if (&api{config: Config{Sync: service, SyncOwnerPepper: syncOwnerTestPepper}}).syncHandler() == nil {
		t.Fatal("Sync stayed closed with a usable pepper")
	}
}
