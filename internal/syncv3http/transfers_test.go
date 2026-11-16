package syncv3http

import "testing"

func TestOneOwnerUploadsOneSnapshotAtATime(t *testing.T) {
	gate := newTransferGate(defaultUploadBudgetBytes, defaultDownloadLimit)
	release, ok := gate.beginUpload("owner-a", 1<<20)
	if !ok {
		t.Fatal("the first upload was refused")
	}
	if _, ok := gate.beginUpload("owner-a", 1<<20); ok {
		t.Fatal("the same owner started a second upload")
	}
	if _, ok := gate.beginUpload("owner-b", 1<<20); !ok {
		t.Fatal("another owner was refused while the budget had room")
	}
	release()
	if _, ok := gate.beginUpload("owner-a", 1<<20); !ok {
		t.Fatal("the owner could not upload after the first upload finished")
	}
}

func TestUploadsShareAByteBudget(t *testing.T) {
	gate := newTransferGate(2*snapshotBodyBytes, defaultDownloadLimit)
	releaseA, ok := gate.beginUpload("owner-a", snapshotBodyBytes)
	if !ok {
		t.Fatal("the first full upload was refused")
	}
	if _, ok := gate.beginUpload("owner-b", -1); !ok {
		t.Fatal("an upload of unknown length did not fit the remaining budget")
	}
	if _, ok := gate.beginUpload("owner-c", 1); ok {
		t.Fatal("an upload was admitted past the budget")
	}
	releaseA()
	for index := range snapshotBodyBytes / smallBodyBytes {
		if _, ok := gate.beginUpload(string(rune('d'+index)), 1); !ok {
			t.Fatalf("small upload %d did not fit the freed budget", index)
		}
	}
	if _, ok := gate.beginUpload("owner-z", 1); ok {
		t.Fatal("a small upload was admitted past the budget")
	}
}

func TestAnOversizedDeclarationCountsAsAFullUpload(t *testing.T) {
	if uploadWeight(snapshotBodyBytes+1) != snapshotBodyBytes || uploadWeight(-1) != snapshotBodyBytes {
		t.Fatal("an oversized or unknown length must weigh as much as the largest upload")
	}
	if uploadWeight(0) != smallBodyBytes {
		t.Fatal("a tiny upload must still weigh something")
	}
}

func TestDownloadsAreLimitedInTotalAndPerOwner(t *testing.T) {
	gate := newTransferGate(defaultUploadBudgetBytes, 4)
	releaseFirst, ok := gate.beginDownload("owner-a")
	if !ok {
		t.Fatal("the first download was refused")
	}
	if _, ok := gate.beginDownload("owner-a"); !ok {
		t.Fatal("a second device of the same owner was refused")
	}
	if _, ok := gate.beginDownload("owner-a"); ok {
		t.Fatal("one owner took more than half of the download slots")
	}
	if _, ok := gate.beginDownload("owner-b"); !ok {
		t.Fatal("another owner was refused while slots were free")
	}
	if _, ok := gate.beginDownload("owner-c"); !ok {
		t.Fatal("the last slot was refused")
	}
	if _, ok := gate.beginDownload("owner-d"); ok {
		t.Fatal("a download was admitted past the limit")
	}
	releaseFirst()
	if _, ok := gate.beginDownload("owner-d"); !ok {
		t.Fatal("a freed slot was not reusable")
	}
}
