package syncproto

import (
	"bytes"
	"errors"
	"testing"
)

func checkpointFor(t *testing.T, list *DeviceList, signer testDevice, header Header) Checkpoint {
	t.Helper()
	digest, ok := list.DigestAt(list.Head())
	if !ok {
		t.Fatal("the list has no head")
	}
	return SignCheckpoint(signer.key, Checkpoint{
		Version: VersionV3, VaultID: testVaultID, DeviceID: signer.id, DeviceListIndex: list.Head(),
		DeviceListDigest: digest, HeaderRevision: header.Revision, HeaderDigest: header.Digest(), ObservedAt: 1_000_000,
	})
}

func TestCheckpointVerifiesAgainstTheChain(t *testing.T) {
	list, err := VerifyDeviceList(threeDeviceChain())
	if err != nil {
		t.Fatal(err)
	}
	first := headerOn(list, laptop, nil, 1, bytes.Repeat([]byte{9}, 64))
	checkpoint := checkpointFor(t, list, desktop, first)
	if err := checkpoint.VerifyAgainst(list); err != nil {
		t.Fatalf("a signed checkpoint from a member should verify: %v", err)
	}
	if err := checkpoint.Verify(desktop.signingKey()); err != nil {
		t.Fatal(err)
	}
}

func TestCheckpointWithNoHeaderYetIsValid(t *testing.T) {
	list, err := VerifyDeviceList(threeDeviceChain())
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := list.DigestAt(3)
	checkpoint := SignCheckpoint(desktop.key, Checkpoint{
		Version: VersionV3, VaultID: testVaultID, DeviceID: desktop.id, DeviceListIndex: 3,
		DeviceListDigest: digest, ObservedAt: 1_000_000,
	})
	if err := checkpoint.VerifyAgainst(list); err != nil {
		t.Fatal(err)
	}
}

func TestCheckpointRefusesEveryBrokenRule(t *testing.T) {
	list, err := VerifyDeviceList(threeDeviceChain())
	if err != nil {
		t.Fatal(err)
	}
	first := headerOn(list, laptop, nil, 1, bytes.Repeat([]byte{9}, 64))
	good := checkpointFor(t, list, desktop, first)
	other := headerOn(list, laptop, nil, 1, bytes.Repeat([]byte{8}, 64))

	cases := []struct {
		name   string
		edit   func(Checkpoint) Checkpoint
		resign bool
		want   error
	}{
		{"a changed time", func(c Checkpoint) Checkpoint { c.ObservedAt++; return c }, false, ErrCheckpointInvalid},
		{"a changed header digest", func(c Checkpoint) Checkpoint { c.HeaderDigest = other.Digest(); return c }, false, ErrCheckpointInvalid},
		{"a changed device", func(c Checkpoint) Checkpoint { c.DeviceID = laptop.id; return c }, false, ErrCheckpointInvalid},
		{"a wrong version", func(c Checkpoint) Checkpoint { c.Version = 2; return c }, true, ErrCheckpointInvalid},
		{"a zero time", func(c Checkpoint) Checkpoint { c.ObservedAt = 0; return c }, true, ErrCheckpointInvalid},
		{"a revision with no digest", func(c Checkpoint) Checkpoint { c.HeaderDigest = ""; return c }, true, ErrCheckpointInvalid},
		{"a digest with no revision", func(c Checkpoint) Checkpoint { c.HeaderRevision = 0; return c }, true, ErrCheckpointInvalid},
		{"a malformed digest", func(c Checkpoint) Checkpoint { c.DeviceListDigest = "short"; return c }, true, ErrCheckpointInvalid},
		{"a device list index of zero", func(c Checkpoint) Checkpoint { c.DeviceListIndex = 0; return c }, true, ErrCheckpointInvalid},
		{"an entry the verifier has not seen", func(c Checkpoint) Checkpoint { c.DeviceListIndex = 4; return c }, true, ErrHistoryBroken},
		{"a different device list digest", func(c Checkpoint) Checkpoint {
			digest, _ := list.DigestAt(2)
			c.DeviceListDigest = digest
			return c
		}, true, ErrHistoryBroken},
		{"another vault", func(c Checkpoint) Checkpoint { c.VaultID = "vaultBBBBBBBBBBBB01"; return c }, true, ErrCheckpointInvalid},
		{"a device that is not a member", func(c Checkpoint) Checkpoint { c.DeviceID = thief.id; return c }, true, ErrCheckpointInvalid},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			edited := test.edit(good)
			if test.resign {
				edited = SignCheckpoint(desktop.key, edited)
			}
			err := edited.VerifyAgainst(list)
			if err == nil {
				t.Fatal("a broken checkpoint verified")
			}
			if !errors.Is(err, test.want) {
				t.Fatalf("got %v, want %v", err, test.want)
			}
		})
	}
}

func TestCheckpointSignatureDoesNotCrossLabels(t *testing.T) {
	list, err := VerifyDeviceList(threeDeviceChain())
	if err != nil {
		t.Fatal(err)
	}
	first := headerOn(list, desktop, nil, 1, bytes.Repeat([]byte{9}, 64))
	checkpoint := checkpointFor(t, list, desktop, first)
	asHeader := checkpoint
	asHeader.Signature = first.Signature
	if err := asHeader.Verify(desktop.signingKey()); err == nil {
		t.Fatal("a header signature verified as a checkpoint")
	}
	forged := first
	forged.Signature = checkpoint.Signature
	if err := VerifyHeader(list, nil, forged); err == nil {
		t.Fatal("a checkpoint signature verified as a header")
	}
	if err := (Checkpoint{}).Validate(); err == nil {
		t.Fatal("an empty checkpoint validated")
	}
}

func FuzzCheckpointVerify(f *testing.F) {
	list, err := VerifyDeviceList(threeDeviceChain())
	if err != nil {
		f.Fatal(err)
	}
	first := headerOn(list, desktop, nil, 1, bytes.Repeat([]byte{9}, 64))
	digest, _ := list.DigestAt(3)
	good := SignCheckpoint(desktop.key, Checkpoint{
		Version: VersionV3, VaultID: testVaultID, DeviceID: desktop.id, DeviceListIndex: 3,
		DeviceListDigest: digest, HeaderRevision: 1, HeaderDigest: first.Digest(), ObservedAt: 1_000_000,
	})
	f.Add(good.VaultID, good.DeviceID, good.DeviceListDigest, good.HeaderDigest, good.Signature, uint64(3), uint64(1), uint64(1_000_000))
	f.Fuzz(func(t *testing.T, vault, device, listDigest, headerDigest, signature string, index, revision, observed uint64) {
		_ = Checkpoint{
			Version: VersionV3, VaultID: vault, DeviceID: device, DeviceListIndex: index, DeviceListDigest: listDigest,
			HeaderRevision: revision, HeaderDigest: headerDigest, ObservedAt: observed, Signature: signature,
		}.VerifyAgainst(list)
	})
}
