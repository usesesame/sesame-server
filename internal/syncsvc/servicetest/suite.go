package servicetest

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"usesesame.app/backend/internal/syncproto"
	"usesesame.app/backend/internal/syncsvc"
)

type Factory func(t *testing.T) syncsvc.Storage

func Run(t *testing.T, factory Factory) {
	t.Run("a vault is created once per owner", func(t *testing.T) { testCreateVault(t, factory) })
	t.Run("pairing adds a device and it can write", func(t *testing.T) { testPairing(t, factory) })
	t.Run("signed requests refuse every forgery", func(t *testing.T) { testSignedRequests(t, factory) })
	t.Run("a signed request that failed after verification is spent", func(t *testing.T) { testFailedRequestIsSpent(t, factory) })
	t.Run("uploads refuse every break in the history", func(t *testing.T) { testUploadRules(t, factory) })
	t.Run("old ciphertext ages out by size and never the previous head", func(t *testing.T) { testRetentionBySize(t, factory) })
	t.Run("removing a device rekeys the vault", func(t *testing.T) { testRemove(t, factory) })
	t.Run("leaving is a removal signed by the leaver", func(t *testing.T) { testLeave(t, factory) })
	t.Run("a removed connection can pair with a fresh identity", func(t *testing.T) { testRejoin(t, factory) })
	t.Run("reset needs a device signature and the current epoch", func(t *testing.T) { testReset(t, factory) })
	t.Run("pairing is limited, expires and stays inside one account", func(t *testing.T) { testPairingLimits(t, factory) })
	t.Run("headers outlive ciphertext and are served in order", func(t *testing.T) { testHeaders(t, factory) })
	t.Run("checkpoints let devices compare what each saw", func(t *testing.T) { testCheckpoints(t, factory) })
	t.Run("checkpoints refuse forgery, rollback and strangers", func(t *testing.T) { testCheckpointRules(t, factory) })
}

func expect(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func testCreateVault(t *testing.T, factory Factory) {
	e := newEnv(t, factory, syncsvc.Config{})
	laptop := e.newDevice("laptop")
	e.createVault(laptop)
	vault := e.vault(laptop)
	if vault.ID != e.vaultID || vault.Epoch != 1 || vault.HeadIndex != 1 || vault.CurrentRevision != 0 {
		t.Fatalf("vault = %+v", vault)
	}

	_, err := e.svc.CreateVault(context.Background(), e.caller(laptop), e.sign(laptop, "POST", "/v1/sync/vault", []byte("again")), syncsvc.CreateVaultInput{Entry: e.createEntry(laptop)})
	expect(t, err, syncsvc.ErrVaultExists)

	other := e.newDevice("other")
	otherEnv := *e
	otherEnv.owner = "owner-two"
	_, err = otherEnv.svc.CreateVault(context.Background(), otherEnv.caller(other), otherEnv.sign(other, "POST", "/v1/sync/vault", []byte("x")), syncsvc.CreateVaultInput{Entry: otherEnv.createEntry(other)})
	if err == nil {
		t.Fatal("a second account took an existing vault id")
	}
}

func testPairing(t *testing.T, factory Factory) {
	e := newEnv(t, factory, syncsvc.Config{})
	laptop, desktop := e.newDevice("laptop"), e.newDevice("desktop")
	e.createVault(laptop)
	first := e.mustUpload(laptop, 1, nil, content(1))
	e.pair(laptop, desktop)

	if list := e.list(desktop); list.Head() != 2 || len(list.Current()) != 2 {
		t.Fatalf("device list head %d members %d", list.Head(), len(list.Current()))
	}
	second := e.mustUpload(desktop, 2, &first.Header.Header, content(2))
	if second.Vault.CurrentRevision != 2 {
		t.Fatalf("revision = %d", second.Vault.CurrentRevision)
	}
	snapshot, err := e.svc.Snapshot(context.Background(), e.caller(laptop))
	if err != nil || snapshot.Header.Revision != 2 || !bytes.Equal(snapshot.Ciphertext, content(2)) {
		t.Fatalf("snapshot = %+v, %v", snapshot.Header.Revision, err)
	}
	if err := snapshot.Header.VerifyCiphertext(snapshot.Ciphertext); err != nil {
		t.Fatalf("served ciphertext does not match its header: %v", err)
	}

	tablet := e.newDevice("tablet")
	e.confirmPairing(laptop, tablet)
	entry := e.addEntry(laptop, tablet)
	list := e.list(laptop)
	ctx := context.Background()
	for name, digest := range map[string]string{
		"another entry":  second.Header.Digest,
		"the head entry": list.HeadDigest(),
	} {
		input := syncsvc.ApproveInput{Entry: entry, Package: e.keyPackage(laptop, tablet, list.Epoch(), digest)}
		err := e.svc.Approve(ctx, e.caller(laptop), e.sign(laptop, "POST", "/v1/sync/approve", []byte("bound-"+name)), input)
		if !errors.Is(err, syncsvc.ErrInvalid) {
			t.Fatalf("a package bound to %s was accepted: %v", name, err)
		}
	}
	input := syncsvc.ApproveInput{Entry: entry, Package: e.keyPackage(laptop, tablet, list.Epoch(), entry.Digest())}
	if err := e.svc.Approve(ctx, e.caller(laptop), e.sign(laptop, "POST", "/v1/sync/approve", []byte("bound-right")), input); err != nil {
		t.Fatalf("a package bound to its own entry was refused: %v", err)
	}
}

func testSignedRequests(t *testing.T, factory Factory) {
	e := newEnv(t, factory, syncsvc.Config{})
	laptop, thief := e.newDevice("laptop"), e.newDevice("thief")
	e.createVault(laptop)
	ctx := context.Background()
	reset := func(request syncsvc.SignedRequest, epoch uint64) error {
		return e.svc.Reset(ctx, e.caller(laptop), request, syncsvc.ResetInput{VaultEpoch: epoch})
	}

	request := e.sign(laptop, "POST", "/v1/sync/reset", []byte("reset"))
	request.Signature = e.sign(thief, "POST", "/v1/sync/reset", []byte("reset")).Signature
	expect(t, reset(request, 1), syncsvc.ErrSignature)

	stale := e.signWith(laptop, laptop.token, "POST", "/v1/sync/reset", []byte("reset"), uint64(e.clock.Now().Unix())-uint64(syncproto.RequestTimestampSkew)-1)
	expect(t, reset(stale, 1), syncsvc.ErrSignature)

	otherToken := e.signWith(laptop, "a-different-token", "POST", "/v1/sync/reset", []byte("reset"), uint64(e.clock.Now().Unix()))
	expect(t, reset(otherToken, 1), syncsvc.ErrSignature)

	tamperedBody := e.sign(laptop, "POST", "/v1/sync/reset", []byte("reset"))
	tamperedBody.Body = []byte("something else")
	expect(t, reset(tamperedBody, 1), syncsvc.ErrSignature)

	wrongRoute := e.sign(laptop, "POST", "/v1/sync/other", []byte("reset"))
	wrongRoute.Target = "/v1/sync/reset"
	expect(t, reset(wrongRoute, 1), syncsvc.ErrSignature)

	if err := e.svc.Reset(ctx, syncsvc.Caller{OwnerKey: e.owner, TokenDeviceID: "desktop-unknown", Token: "token-unknown"}, e.sign(thief, "POST", "/v1/sync/reset", []byte("reset")), syncsvc.ResetInput{VaultEpoch: 1}); err == nil {
		t.Fatal("a token that belongs to no sync device reset the vault")
	}
	if e.vault(laptop).ID != e.vaultID {
		t.Fatal("a refused reset changed the vault")
	}

	good := e.sign(laptop, "POST", "/v1/sync/devices/leave-check", []byte("probe"))
	if err := e.svc.CancelPairing(ctx, e.caller(laptop), good, "deviceNobody0000000001"); err != nil {
		t.Fatalf("a valid signed request failed: %v", err)
	}
	expect(t, e.svc.CancelPairing(ctx, e.caller(laptop), good, "deviceNobody0000000001"), syncsvc.ErrReplay)
}

func testFailedRequestIsSpent(t *testing.T, factory Factory) {
	e := newEnv(t, factory, syncsvc.Config{})
	laptop := e.newDevice("laptop")
	e.createVault(laptop)
	ctx := context.Background()
	reset := func(request syncsvc.SignedRequest, epoch uint64) error {
		return e.svc.Reset(ctx, e.caller(laptop), request, syncsvc.ResetInput{VaultEpoch: epoch})
	}

	request := e.sign(laptop, "POST", "/v1/sync/reset", []byte("reset"))
	first := reset(request, 99)
	if first == nil {
		t.Fatal("a reset for the wrong epoch was accepted")
	}
	if errors.Is(first, syncsvc.ErrSignature) || errors.Is(first, syncsvc.ErrReplay) {
		t.Fatalf("the first attempt failed for the wrong reason: %v", first)
	}

	expect(t, reset(request, 1), syncsvc.ErrReplay)
	if e.vault(laptop).ID != e.vaultID {
		t.Fatal("a replayed request changed the vault")
	}

	fresh := e.sign(laptop, "POST", "/v1/sync/reset", []byte("reset"))
	if err := reset(fresh, 1); err != nil {
		t.Fatalf("a freshly signed request failed after a spent one: %v", err)
	}
}

func testUploadRules(t *testing.T, factory Factory) {
	e := newEnv(t, factory, syncsvc.Config{})
	laptop, desktop, thief := e.newDevice("laptop"), e.newDevice("desktop"), e.newDevice("thief")
	e.createVault(laptop)
	e.pair(laptop, desktop)
	first := e.mustUpload(laptop, 1, nil, content(1))
	head := first.Header.Header

	_, err := e.upload(laptop, 2, &head, content(2))
	if err != nil {
		t.Fatalf("a valid second revision: %v", err)
	}
	vault := e.vault(laptop)
	if vault.CurrentRevision != 2 {
		t.Fatalf("revision = %d", vault.CurrentRevision)
	}

	_, err = e.upload(desktop, 2, &head, content(9))
	expect(t, err, syncsvc.ErrConflict)

	_, err = e.upload(desktop, 4, &head, content(4))
	expect(t, err, syncsvc.ErrConflict)

	stored, _ := e.svc.Headers(context.Background(), e.caller(laptop), 0, 0)
	second := stored[1].Header
	wrongPrevious := e.header(desktop, 3, &head, content(3))
	_, err = e.svc.Upload(context.Background(), e.caller(desktop), e.sign(desktop, "POST", "/v1/sync/envelope", []byte("x")), syncsvc.UploadInput{Header: wrongPrevious, Ciphertext: content(3)})
	expect(t, err, syncsvc.ErrConflict)

	mismatched := e.header(desktop, 3, &second, content(3))
	_, err = e.svc.Upload(context.Background(), e.caller(desktop), e.sign(desktop, "POST", "/v1/sync/envelope", []byte("y")), syncsvc.UploadInput{Header: mismatched, Ciphertext: content(8)})
	expect(t, err, syncsvc.ErrInvalid)

	forgedSigner := e.header(desktop, 3, &second, content(3))
	forgedSigner.DeviceID = laptop.id
	_, err = e.svc.Upload(context.Background(), e.caller(desktop), e.sign(desktop, "POST", "/v1/sync/envelope", []byte("z")), syncsvc.UploadInput{Header: forgedSigner, Ciphertext: content(3)})
	expect(t, err, syncsvc.ErrForbidden)

	outsider := e.header(laptop, 3, &second, content(3))
	outsider = syncproto.SignHeader(thief.key, outsider)
	_, err = e.svc.Upload(context.Background(), e.caller(laptop), e.sign(laptop, "POST", "/v1/sync/envelope", []byte("w")), syncsvc.UploadInput{Header: outsider, Ciphertext: content(3)})
	if err == nil {
		t.Fatal("a header signed by a device outside the list was accepted")
	}

	if e.vault(laptop).CurrentRevision != 2 {
		t.Fatal("a refused upload moved the vault")
	}

	entries, _ := e.svc.DeviceList(context.Background(), e.caller(laptop), 0, 0)
	older, err := syncproto.VerifyDeviceList(entries[:1])
	if err != nil {
		t.Fatal(err)
	}
	oldDigest, _ := older.DigestAt(1)
	staleList := e.header(laptop, 3, &second, content(3))
	staleList.DeviceListIndex, staleList.DeviceListDigest = 1, oldDigest
	staleList = syncproto.SignHeader(laptop.key, staleList)
	_, err = e.svc.Upload(context.Background(), e.caller(laptop), e.sign(laptop, "POST", "/v1/sync/envelope", []byte("v")), syncsvc.UploadInput{Header: staleList, Ciphertext: content(3)})
	expect(t, err, syncsvc.ErrConflict)

	_, err = e.upload(desktop, 3, &second, content(3))
	if err != nil {
		t.Fatalf("a valid third revision: %v", err)
	}

	_, err = e.svc.Upload(context.Background(), e.caller(thief), e.sign(thief, "POST", "/v1/sync/envelope", []byte("t")), syncsvc.UploadInput{Header: e.header(laptop, 4, &second, content(4)), Ciphertext: content(4)})
	if err == nil {
		t.Fatal("a token with no sync device uploaded")
	}
}

func testRetentionBySize(t *testing.T, factory Factory) {
	e := newEnv(t, factory, syncsvc.Config{ByteBudget: 3 * 1024})
	laptop := e.newDevice("laptop")
	e.createVault(laptop)
	var previous *syncproto.Header
	for revision := uint64(1); revision <= 6; revision++ {
		accepted := e.mustUpload(laptop, revision, previous, content(byte(revision)))
		previous = &accepted.Header.Header
	}
	var kept []uint64
	_ = e.storage.View(context.Background(), func(tx syncsvc.Tx) error {
		for revision := uint64(1); revision <= 6; revision++ {
			if _, err := tx.Ciphertext(e.vaultID, revision); err == nil {
				kept = append(kept, revision)
			}
		}
		return nil
	})
	if len(kept) != 3 || kept[0] != 4 || kept[2] != 6 {
		t.Fatalf("kept ciphertexts %v, want 4 5 6", kept)
	}
	headers, err := e.svc.Headers(context.Background(), e.caller(laptop), 0, 0)
	if err != nil || len(headers) != 6 {
		t.Fatalf("headers = %d, %v, want all six", len(headers), err)
	}

	small := newEnv(t, factory, syncsvc.Config{ByteBudget: 1536})
	laptop = small.newDevice("laptop")
	small.createVault(laptop)
	first := small.mustUpload(laptop, 1, nil, content(1))
	_, err = small.upload(laptop, 2, &first.Header.Header, content(2))
	expect(t, err, syncsvc.ErrQuota)
	if small.vault(laptop).CurrentRevision != 1 {
		t.Fatal("a refused upload moved the vault")
	}
}

func testRemove(t *testing.T, factory Factory) {
	e := newEnv(t, factory, syncsvc.Config{})
	laptop, desktop, phone := e.newDevice("laptop"), e.newDevice("desktop"), e.newDevice("phone")
	e.createVault(laptop)
	e.pair(laptop, desktop)
	e.pair(laptop, phone)
	first := e.mustUpload(laptop, 1, nil, content(1))

	expect(t, e.remove(laptop, phone), syncsvc.ErrInvalid)
	expect(t, e.remove(laptop, phone, desktop, phone), syncsvc.ErrInvalid)
	otherEntry := e.addEntry(laptop, e.newDevice("stranger"))
	expect(t, e.removeBoundTo(otherEntry.Digest(), laptop, phone, desktop), syncsvc.ErrInvalid)
	expect(t, e.removeBoundTo(first.Header.Digest, laptop, phone, desktop), syncsvc.ErrInvalid)
	if e.vault(laptop).Epoch != 1 || len(e.list(laptop).Current()) != 3 {
		t.Fatal("a refused removal changed the vault")
	}

	if err := e.remove(laptop, phone, desktop); err != nil {
		t.Fatalf("remove: %v", err)
	}
	vault := e.vault(laptop)
	if vault.Epoch != 2 || vault.HeadIndex != 4 {
		t.Fatalf("vault after removal = %+v", vault)
	}
	if _, err := e.uploadSeenBy(phone, laptop, 2, &first.Header.Header, content(2)); err == nil {
		t.Fatal("a removed device uploaded")
	}
	if _, err := e.svc.Snapshot(context.Background(), e.caller(phone)); err == nil {
		t.Fatal("a removed device downloaded")
	}

	if _, err := e.upload(desktop, 2, &first.Header.Header, content(2)); !errors.Is(err, syncsvc.ErrNotActivated) {
		t.Fatalf("a survivor wrote before taking the new key: %v", err)
	}
	e.activate(desktop)
	second := e.mustUpload(desktop, 2, &first.Header.Header, content(2))
	if second.Header.VaultEpoch != 2 {
		t.Fatalf("epoch = %d", second.Header.VaultEpoch)
	}
	_ = e.storage.View(context.Background(), func(tx syncsvc.Tx) error {
		if _, err := tx.Ciphertext(e.vaultID, 1); err == nil {
			t.Fatal("the old epoch ciphertext survived the first upload under the new key")
		}
		return nil
	})
}

func testLeave(t *testing.T, factory Factory) {
	e := newEnv(t, factory, syncsvc.Config{})
	laptop, desktop := e.newDevice("laptop"), e.newDevice("desktop")
	e.createVault(laptop)
	e.pair(laptop, desktop)
	if err := e.remove(desktop, desktop, laptop); err != nil {
		t.Fatalf("leave: %v", err)
	}
	if _, err := e.uploadSeenBy(desktop, laptop, 1, nil, content(1)); err == nil {
		t.Fatal("a device that left uploaded")
	}
	if e.vault(laptop).Epoch != 2 {
		t.Fatal("leaving did not rekey")
	}
	if err := e.remove(laptop, desktop); err == nil {
		t.Fatal("a device that already left was removed again")
	}
}

func testReset(t *testing.T, factory Factory) {
	e := newEnv(t, factory, syncsvc.Config{})
	laptop := e.newDevice("laptop")
	e.createVault(laptop)
	e.mustUpload(laptop, 1, nil, content(1))
	ctx := context.Background()
	expect(t, e.svc.Reset(ctx, e.caller(laptop), e.sign(laptop, "POST", "/v1/sync/reset", []byte("r")), syncsvc.ResetInput{VaultEpoch: 7}), syncsvc.ErrConflict)
	if err := e.svc.Reset(ctx, e.caller(laptop), e.sign(laptop, "POST", "/v1/sync/reset", []byte("r2")), syncsvc.ResetInput{VaultEpoch: 1}); err != nil {
		t.Fatalf("reset: %v", err)
	}
	_, _, err := e.svc.VaultState(ctx, e.caller(laptop))
	expect(t, err, syncsvc.ErrNoVault)
	e.createVault(laptop)
	if e.vault(laptop).CurrentRevision != 0 {
		t.Fatal("a vault created after a reset kept old revisions")
	}
}

func testPairingLimits(t *testing.T, factory Factory) {
	e := newEnv(t, factory, syncsvc.Config{})
	laptop := e.newDevice("laptop")
	e.createVault(laptop)
	ctx := context.Background()
	var newcomers []device
	for _, name := range []string{"alpha", "bravo", "charlie", "delta"} {
		newcomers = append(newcomers, e.newDevice(name))
	}
	for index, newcomer := range newcomers {
		offer := syncsvc.PairingOffer{DeviceID: newcomer.id, SigningKey: newcomer.signingKey(), PackageKey: newcomer.packageKey(), Message: []byte("offer")}
		err := e.svc.BeginPairing(ctx, e.caller(newcomer), e.sign(newcomer, "POST", "/v1/sync/pair", []byte("offer")), offer)
		if index < syncsvc.MaxPendingPairings && err != nil {
			t.Fatalf("pairing %d: %v", index, err)
		}
		if index == syncsvc.MaxPendingPairings {
			expect(t, err, syncsvc.ErrQuota)
		}
	}
	pending, err := e.svc.PendingPairings(ctx, e.caller(laptop))
	if err != nil || len(pending) != syncsvc.MaxPendingPairings {
		t.Fatalf("pending = %d, %v", len(pending), err)
	}

	e.clock.Advance(syncsvc.PairingLifetime + time.Second)
	pending, err = e.svc.PendingPairings(ctx, e.caller(laptop))
	if err != nil || len(pending) != 0 {
		t.Fatalf("expired pairings were listed: %d, %v", len(pending), err)
	}
	expired := newcomers[0]
	expect(t, e.svc.ConfirmPairing(ctx, e.caller(expired), e.sign(expired, "POST", "/v1/sync/pair/confirm", []byte("c")), []byte("c")), syncsvc.ErrForbidden)

	stranger := e.newDevice("stranger")
	other := *e
	other.owner = "owner-two"
	if _, _, err := other.svc.VaultState(ctx, other.caller(stranger)); !errors.Is(err, syncsvc.ErrNoVault) {
		t.Fatalf("another account saw a vault: %v", err)
	}
	if err := other.svc.BeginPairing(ctx, other.caller(stranger), other.sign(stranger, "POST", "/v1/sync/pair", []byte("o")), syncsvc.PairingOffer{DeviceID: stranger.id, SigningKey: stranger.signingKey(), PackageKey: stranger.packageKey(), Message: []byte("o")}); !errors.Is(err, syncsvc.ErrNoVault) {
		t.Fatalf("another account paired into a vault it does not own: %v", err)
	}

	skipping := e.newDevice("skipper")
	offer := syncsvc.PairingOffer{DeviceID: skipping.id, SigningKey: skipping.signingKey(), PackageKey: skipping.packageKey(), Message: []byte("offer")}
	if err := e.svc.BeginPairing(ctx, e.caller(skipping), e.sign(skipping, "POST", "/v1/sync/pair", []byte("offer")), offer); err != nil {
		t.Fatalf("fresh pairing after expiry: %v", err)
	}
	entry := e.addEntry(laptop, skipping)
	err = e.svc.Approve(ctx, e.caller(laptop), e.sign(laptop, "POST", "/v1/sync/approve", []byte("a")), syncsvc.ApproveInput{Entry: entry, Package: e.keyPackage(laptop, skipping, 1, entry.Digest())})
	expect(t, err, syncsvc.ErrConflict)
}

func testHeaders(t *testing.T, factory Factory) {
	e := newEnv(t, factory, syncsvc.Config{HeaderRetention: 3})
	laptop := e.newDevice("laptop")
	e.createVault(laptop)
	var previous *syncproto.Header
	for revision := uint64(1); revision <= 5; revision++ {
		accepted := e.mustUpload(laptop, revision, previous, content(byte(revision)))
		previous = &accepted.Header.Header
	}
	headers, err := e.svc.Headers(context.Background(), e.caller(laptop), 0, 0)
	if err != nil || len(headers) != 3 || headers[0].Revision != 3 || headers[2].Revision != 5 {
		t.Fatalf("headers = %d, %v", len(headers), err)
	}
	after, err := e.svc.Headers(context.Background(), e.caller(laptop), 4, 0)
	if err != nil || len(after) != 1 || after[0].Revision != 5 {
		t.Fatalf("headers after 4 = %d, %v", len(after), err)
	}
	for _, header := range headers {
		list := e.list(laptop)
		if err := syncproto.VerifyHeader(list, nil, header.Header); err != nil {
			t.Fatalf("a served header does not verify: %v", err)
		}
	}
}

func testCheckpoints(t *testing.T, factory Factory) {
	e := newEnv(t, factory, syncsvc.Config{})
	ctx := context.Background()
	laptop, desktop := e.newDevice("laptop"), e.newDevice("desktop")
	e.createVault(laptop)
	e.pair(laptop, desktop)
	first := e.mustUpload(laptop, 1, nil, content(1))

	seen, err := e.putCheckpoint(desktop, e.checkpointFor(desktop, e.now()))
	if err != nil {
		t.Fatalf("a member's checkpoint was refused: %v", err)
	}
	if seen.HeaderRevision != 1 || seen.HeaderDigest != first.Header.Digest || seen.DeviceListIndex != 2 {
		t.Fatalf("stored checkpoint = %+v", seen.Checkpoint)
	}
	if !seen.AcceptedAt.Equal(e.clock.Now()) {
		t.Fatalf("accepted at %v", seen.AcceptedAt)
	}

	for _, viewer := range []device{laptop, desktop} {
		stored, err := e.svc.Checkpoints(ctx, e.caller(viewer))
		if err != nil || len(stored) != 1 || stored[0].DeviceID != desktop.id {
			t.Fatalf("checkpoints for %s = %+v, %v", viewer.id, stored, err)
		}
		list := e.list(viewer)
		if err := stored[0].VerifyAgainst(list); err != nil {
			t.Fatalf("a served checkpoint does not verify against the chain: %v", err)
		}
	}

	e.clock.Advance(time.Minute)
	if _, err := e.putCheckpoint(laptop, e.checkpointFor(laptop, e.now())); err != nil {
		t.Fatal(err)
	}
	stored, err := e.svc.Checkpoints(ctx, e.caller(desktop))
	if err != nil || len(stored) != 2 {
		t.Fatalf("both devices should have a checkpoint: %+v, %v", stored, err)
	}

	second := e.mustUpload(desktop, 2, &first.Header.Header, content(2))
	e.clock.Advance(time.Minute)
	updated, err := e.putCheckpoint(desktop, e.checkpointFor(desktop, e.now()))
	if err != nil || updated.HeaderRevision != 2 || updated.HeaderDigest != second.Header.Digest {
		t.Fatalf("an advanced checkpoint = %+v, %v", updated.Checkpoint, err)
	}
	stored, _ = e.svc.Checkpoints(ctx, e.caller(laptop))
	if len(stored) != 2 {
		t.Fatalf("a device must hold one checkpoint, found %d", len(stored))
	}

	if err := e.svc.Reset(ctx, e.caller(laptop), e.sign(laptop, "POST", "/v1/sync/reset", []byte("reset")), syncsvc.ResetInput{VaultEpoch: 1}); err != nil {
		t.Fatal(err)
	}
	e.createVault(laptop)
	stored, err = e.svc.Checkpoints(ctx, e.caller(laptop))
	if err != nil || len(stored) != 0 {
		t.Fatalf("a reset vault kept checkpoints: %+v, %v", stored, err)
	}
}

func testCheckpointRules(t *testing.T, factory Factory) {
	e := newEnv(t, factory, syncsvc.Config{})
	ctx := context.Background()
	laptop, desktop, phone := e.newDevice("laptop"), e.newDevice("desktop"), e.newDevice("phone")
	e.createVault(laptop)
	e.pair(laptop, desktop)
	e.pair(laptop, phone)
	e.mustUpload(laptop, 1, nil, content(1))
	good := e.checkpointFor(desktop, e.now())

	forged := good
	forged.ObservedAt++
	_, err := e.putCheckpoint(desktop, forged)
	expect(t, err, syncsvc.ErrInvalid)

	asLaptop := e.checkpointFor(laptop, e.now())
	_, err = e.putCheckpoint(desktop, asLaptop)
	expect(t, err, syncsvc.ErrForbidden)

	otherVault := good
	otherVault.VaultID = "vaultBBBBBBBBBBBB01"
	otherVault = syncproto.SignCheckpoint(desktop.key, otherVault)
	_, err = e.putCheckpoint(desktop, otherVault)
	expect(t, err, syncsvc.ErrForbidden)

	stale := syncproto.SignCheckpoint(desktop.key, func() syncproto.Checkpoint { c := good; c.ObservedAt = e.now() - 3600; return c }())
	_, err = e.putCheckpoint(desktop, stale)
	expect(t, err, syncsvc.ErrInvalid)
	future := syncproto.SignCheckpoint(desktop.key, func() syncproto.Checkpoint { c := good; c.ObservedAt = e.now() + 3600; return c }())
	_, err = e.putCheckpoint(desktop, future)
	expect(t, err, syncsvc.ErrInvalid)

	_, err = e.svc.PutCheckpoint(ctx, e.caller(desktop), e.sign(laptop, "POST", "/v1/sync/checkpoints", []byte("checkpoint")), good)
	expect(t, err, syncsvc.ErrSignature)

	if _, err := e.putCheckpoint(desktop, good); err != nil {
		t.Fatalf("a good checkpoint was refused: %v", err)
	}
	_, err = e.putCheckpoint(desktop, good)
	expect(t, err, syncsvc.ErrConflict)

	e.clock.Advance(time.Minute)
	older := e.checkpointFor(desktop, e.now())
	older.DeviceListIndex--
	older = syncproto.SignCheckpoint(desktop.key, older)
	_, err = e.putCheckpoint(desktop, older)
	expect(t, err, syncsvc.ErrConflict)
	behind := e.checkpointFor(desktop, e.now())
	behind.HeaderRevision, behind.HeaderDigest = 0, ""
	behind = syncproto.SignCheckpoint(desktop.key, behind)
	_, err = e.putCheckpoint(desktop, behind)
	expect(t, err, syncsvc.ErrConflict)
	backInTime := syncproto.SignCheckpoint(desktop.key, func() syncproto.Checkpoint {
		c := e.checkpointFor(desktop, e.now())
		c.ObservedAt = good.ObservedAt - 1
		return c
	}())
	_, err = e.putCheckpoint(desktop, backInTime)
	expect(t, err, syncsvc.ErrConflict)

	stranger := syncsvc.Caller{OwnerKey: "owner-two", TokenDeviceID: desktop.tokenID, Token: desktop.token}
	_, err = e.svc.Checkpoints(ctx, stranger)
	expect(t, err, syncsvc.ErrNoVault)
	_, err = e.svc.PutCheckpoint(ctx, stranger, e.sign(desktop, "POST", "/v1/sync/checkpoints", []byte("checkpoint")), good)
	expect(t, err, syncsvc.ErrNoVault)

	e.clock.Advance(time.Minute)
	if _, err := e.putCheckpoint(phone, e.checkpointFor(phone, e.now())); err != nil {
		t.Fatal(err)
	}
	if err := e.remove(laptop, phone, desktop); err != nil {
		t.Fatal(err)
	}
	remaining, err := e.svc.Checkpoints(ctx, e.caller(laptop))
	if err != nil {
		t.Fatal(err)
	}
	for _, checkpoint := range remaining {
		if checkpoint.DeviceID == phone.id {
			t.Fatal("a removed device kept its checkpoint")
		}
	}
	e.clock.Advance(time.Minute)
	_, err = e.putCheckpoint(phone, e.checkpointFor(laptop, e.now()))
	expect(t, err, syncsvc.ErrForbidden)
	_, err = e.putCheckpoint(desktop, e.checkpointFor(desktop, e.now()))
	expect(t, err, syncsvc.ErrNotActivated)
	_, err = e.svc.Checkpoints(ctx, e.caller(phone))
	expect(t, err, syncsvc.ErrForbidden)

	e.clock.Advance(time.Minute)
	if _, err := e.putCheckpoint(laptop, e.checkpointFor(laptop, e.now())); err != nil {
		t.Fatalf("the remover could not record a checkpoint: %v", err)
	}
	if _, err := e.putCheckpoint(laptop, e.checkpointFor(laptop, e.now())); err == nil {
		t.Fatal("a checkpoint with an unchanged time was accepted twice")
	}
}

func testRejoin(t *testing.T, factory Factory) {
	for name, leave := range map[string]bool{"removed": false, "left": true} {
		t.Run(name, func(t *testing.T) { testRejoinAfterRemoval(t, factory, leave) })
	}
}

func testRejoinAfterRemoval(t *testing.T, factory Factory, leave bool) {
	e := newEnv(t, factory, syncsvc.Config{})
	ctx := context.Background()
	laptop, phone := e.newDevice("laptop"), e.newDevice("phone")
	e.createVault(laptop)
	e.pair(laptop, phone)
	first := e.mustUpload(laptop, 1, nil, content(1))
	if leave {
		if err := e.remove(phone, phone, laptop); err != nil {
			t.Fatal(err)
		}
		e.activate(laptop)
	} else if err := e.remove(laptop, phone); err != nil {
		t.Fatal(err)
	}
	replacement := e.newDevice("replacement")
	replacement.tokenID, replacement.token = phone.tokenID, phone.token
	e.confirmPairing(laptop, replacement)
	if _, err := e.svc.Snapshot(ctx, e.caller(replacement)); !errors.Is(err, syncsvc.ErrForbidden) {
		t.Fatalf("a pending replacement downloaded: %v", err)
	}
	request := e.sign(phone, "POST", "/v1/sync/pairings/mine/confirm", []byte("confirm"))
	expect(t, e.svc.ConfirmPairing(ctx, e.caller(phone), request, []byte("confirm")), syncsvc.ErrSignature)
	e.approve(laptop, replacement)
	list := e.list(laptop)
	if _, member := list.MemberAt(phone.id, list.Head()); member {
		t.Fatal("pairing restored the removed identity")
	}
	if member, ok := list.MemberAt(replacement.id, list.Head()); !ok || member.AddedIndex != 4 {
		t.Fatal("pairing did not append the fresh identity")
	}
	second := e.mustUpload(replacement, 2, &first.Header.Header, content(2))
	if second.Header.VaultEpoch != 2 {
		t.Fatal("the replacement wrote under the removed key epoch")
	}
	reset := e.sign(phone, "POST", "/v1/sync/reset", []byte("reset"))
	expect(t, e.svc.Reset(ctx, e.caller(phone), reset, syncsvc.ResetInput{VaultEpoch: 2}), syncsvc.ErrSignature)
	if e.vault(laptop).CurrentRevision != 2 {
		t.Fatal("the old signing key changed the rejoined vault")
	}
}
