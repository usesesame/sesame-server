package syncsvc

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"usesesame.app/backend/internal/syncproto"
)

const defaultByteBudget = 128 * 1024 * 1024

type Config struct {
	ReceiptKey      ed25519.PrivateKey
	Now             func() time.Time
	ByteBudget      int64
	HeaderRetention uint64
}

type Service struct {
	storage Storage
	config  Config
}

func New(storage Storage, config Config) (*Service, error) {
	if len(config.ReceiptKey) != ed25519.PrivateKeySize {
		return nil, errors.New("syncsvc: the receipt key is required")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.ByteBudget <= 0 {
		config.ByteBudget = defaultByteBudget
	}
	if config.HeaderRetention == 0 {
		config.HeaderRetention = MaxHeaderRetention
	}
	return &Service{storage: nonceKeeper{inner: storage}, config: config}, nil
}

func (s *Service) now() time.Time {
	return s.config.Now().UTC()
}

func (s *Service) unix() uint64 {
	now := s.now().Unix()
	if now < 0 {
		return 0
	}
	return uint64(now)
}

func (s *Service) InstancePublicKey() ed25519.PublicKey {
	return s.config.ReceiptKey.Public().(ed25519.PublicKey)
}

func (s *Service) actorFor(tx Tx, caller Caller) (Actor, error) {
	if caller.OwnerKey == "" || caller.TokenDeviceID == "" || caller.Token == "" {
		return Actor{}, ErrForbidden
	}
	vault, err := tx.VaultByOwner(caller.OwnerKey)
	if errors.Is(err, ErrNotFound) {
		return Actor{}, ErrNoVault
	}
	if err != nil {
		return Actor{}, err
	}
	device, err := tx.DeviceByToken(vault.ID, caller.TokenDeviceID)
	if err == nil && device.State != DeviceRevoked {
		return Actor{Vault: vault, Device: device}, nil
	}
	if err != nil && !errors.Is(err, ErrNotFound) {
		return Actor{}, err
	}
	pairing, err := tx.PairingByToken(vault.ID, caller.TokenDeviceID)
	if errors.Is(err, ErrNotFound) {
		if device.State == DeviceRevoked {
			return Actor{Vault: vault, Device: device}, nil
		}
		return Actor{}, ErrForbidden
	}
	if err != nil {
		return Actor{}, err
	}
	if !s.now().Before(pairing.ExpiresAt) {
		return Actor{}, ErrForbidden
	}
	return Actor{Vault: vault, Pairing: &pairing, Device: Device{
		VaultID: vault.ID, DeviceID: pairing.DeviceID, TokenDeviceID: pairing.TokenDeviceID,
		SigningKey: pairing.SigningKey, PackageKey: pairing.PackageKey, State: DevicePending,
	}}, nil
}

func (s *Service) verifyRequest(tx Tx, scope, signingKey string, caller Caller, request SignedRequest) error {
	signed := syncproto.RequestSignature{
		Method: request.Method, Target: request.Target, Body: request.Body,
		Token: caller.Token, Timestamp: request.Timestamp, Nonce: request.Nonce,
	}
	if err := signed.Verify(signingKey, request.Signature, s.unix()); err != nil {
		return ErrSignature
	}
	expires := s.now().Add(time.Duration(syncproto.RequestNonceRetention) * time.Second)
	fresh, err := tx.RecordNonce(scope, request.Nonce, s.now(), expires)
	if err != nil {
		return err
	}
	if !fresh {
		return ErrReplay
	}
	return nil
}

func (s *Service) signedActor(tx Tx, caller Caller, request SignedRequest) (Actor, error) {
	actor, err := s.actorFor(tx, caller)
	if err != nil {
		return Actor{}, err
	}
	if err := s.verifyRequest(tx, actor.Vault.ID+"/"+actor.Device.DeviceID, actor.Device.SigningKey, caller, request); err != nil {
		return Actor{}, err
	}
	return actor, nil
}

func requireApproved(actor Actor) error {
	if actor.Pairing != nil || actor.Device.State != DeviceApproved {
		return ErrForbidden
	}
	return nil
}

func requireActivated(actor Actor) error {
	if err := requireApproved(actor); err != nil {
		return err
	}
	if actor.Device.ActivatedEpoch != actor.Vault.Epoch {
		return ErrNotActivated
	}
	return nil
}

func loadList(tx Tx, vaultID string) (*syncproto.DeviceList, error) {
	list := syncproto.NewDeviceList()
	var after uint64
	for {
		entries, err := tx.DeviceListEntries(vaultID, after, MaxEntriesPerPage)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if err := list.Extend(entry); err != nil {
				return nil, fmt.Errorf("stored device list does not verify: %w", err)
			}
			after = entry.Index
		}
		if len(entries) < MaxEntriesPerPage {
			return list, nil
		}
	}
}

type CreateVaultInput struct {
	Entry syncproto.DeviceListEntry
}

func (s *Service) CreateVault(ctx context.Context, caller Caller, request SignedRequest, input CreateVaultInput) (Vault, error) {
	var created Vault
	err := s.storage.Update(ctx, func(tx Tx) error {
		if caller.OwnerKey == "" || caller.TokenDeviceID == "" || caller.Token == "" {
			return ErrForbidden
		}
		if _, err := tx.VaultByOwner(caller.OwnerKey); err == nil {
			return ErrVaultExists
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		list, err := syncproto.VerifyDeviceList([]syncproto.DeviceListEntry{input.Entry})
		if err != nil {
			return fmt.Errorf("%w: %w", ErrInvalid, err)
		}
		if err := s.verifyRequest(tx, "owner/"+caller.OwnerKey, input.Entry.SubjectSigningKey, caller, request); err != nil {
			return err
		}
		now := s.now()
		vault := Vault{ID: input.Entry.VaultID, OwnerKey: caller.OwnerKey, Epoch: list.Epoch(), HeadIndex: 1, CreatedAt: now, UpdatedAt: now}
		if err := tx.InsertVault(vault); err != nil {
			return err
		}
		if err := tx.AppendDeviceListEntry(input.Entry); err != nil {
			return err
		}
		if err := tx.PutDevice(Device{
			VaultID: vault.ID, DeviceID: input.Entry.SubjectDeviceID, TokenDeviceID: caller.TokenDeviceID,
			SigningKey: input.Entry.SubjectSigningKey, PackageKey: input.Entry.SubjectPackageKey,
			State: DeviceApproved, AddedIndex: 1, ActivatedEpoch: vault.Epoch, CreatedAt: now,
		}); err != nil {
			return err
		}
		created = vault
		return nil
	})
	return created, err
}

type PairingOffer struct {
	DeviceID   string
	SigningKey string
	PackageKey string
	Message    []byte
}

func (s *Service) BeginPairing(ctx context.Context, caller Caller, request SignedRequest, offer PairingOffer) error {
	return s.storage.Update(ctx, func(tx Tx) error {
		if caller.OwnerKey == "" || caller.TokenDeviceID == "" || caller.Token == "" {
			return ErrForbidden
		}
		vault, err := tx.VaultByOwner(caller.OwnerKey)
		if errors.Is(err, ErrNotFound) {
			return ErrNoVault
		}
		if err != nil {
			return err
		}
		if len(offer.Message) == 0 || len(offer.Message) > MaxPairingMessageBytes {
			return ErrInvalid
		}
		probe := syncproto.DeviceListEntry{
			VaultID: vault.ID, Index: 2, PreviousDigest: base64.RawURLEncoding.EncodeToString(make([]byte, syncproto.DigestBytes)),
			Kind: syncproto.EntryAdd, SignerDeviceID: offer.DeviceID, SubjectDeviceID: offer.DeviceID,
			SubjectSigningKey: offer.SigningKey, SubjectPackageKey: offer.PackageKey,
			SealedLabel: base64.RawURLEncoding.EncodeToString(make([]byte, syncproto.MinSealedLabelBytes)),
			EpochAfter:  vault.Epoch, Signature: base64.RawURLEncoding.EncodeToString(make([]byte, syncproto.Ed25519SignatureBytes)),
		}
		if err := probe.Validate(); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalid, err)
		}
		if err := s.verifyRequest(tx, "owner/"+caller.OwnerKey, offer.SigningKey, caller, request); err != nil {
			return err
		}
		if existing, err := tx.DeviceByToken(vault.ID, caller.TokenDeviceID); err == nil {
			if existing.State != DeviceRevoked {
				return ErrForbidden
			}
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		if _, err := tx.Device(vault.ID, offer.DeviceID); err == nil {
			return ErrConflict
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		if previous, err := tx.PairingByToken(vault.ID, caller.TokenDeviceID); err == nil {
			if err := tx.DeletePairing(vault.ID, previous.DeviceID); err != nil {
				return err
			}
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		if err := tx.PurgeExpired(s.now()); err != nil {
			return err
		}
		pairings, err := tx.Pairings(vault.ID)
		if err != nil {
			return err
		}
		if len(pairings) >= MaxPendingPairings {
			return ErrQuota
		}
		now := s.now()
		return tx.PutPairing(Pairing{
			VaultID: vault.ID, DeviceID: offer.DeviceID, TokenDeviceID: caller.TokenDeviceID,
			SigningKey: offer.SigningKey, PackageKey: offer.PackageKey, Offer: offer.Message,
			State: PairingOffered, ExpiresAt: now.Add(PairingLifetime), CreatedAt: now,
		})
	})
}

func (s *Service) PairingStatus(ctx context.Context, caller Caller) (Pairing, error) {
	var out Pairing
	err := s.storage.View(ctx, func(tx Tx) error {
		actor, err := s.actorFor(tx, caller)
		if err != nil {
			return err
		}
		if actor.Pairing == nil {
			return ErrForbidden
		}
		out = *actor.Pairing
		return nil
	})
	return out, err
}

func (s *Service) PendingPairings(ctx context.Context, caller Caller) ([]Pairing, error) {
	var out []Pairing
	err := s.storage.View(ctx, func(tx Tx) error {
		actor, err := s.actorFor(tx, caller)
		if err != nil {
			return err
		}
		if err := requireApproved(actor); err != nil {
			return err
		}
		pairings, err := tx.Pairings(actor.Vault.ID)
		if err != nil {
			return err
		}
		now := s.now()
		for _, pairing := range pairings {
			if now.Before(pairing.ExpiresAt) {
				out = append(out, pairing)
			}
		}
		return nil
	})
	return out, err
}

func (s *Service) AnswerPairing(ctx context.Context, caller Caller, request SignedRequest, deviceID string, message []byte) error {
	return s.storage.Update(ctx, func(tx Tx) error {
		actor, err := s.signedActor(tx, caller, request)
		if err != nil {
			return err
		}
		if err := requireActivated(actor); err != nil {
			return err
		}
		if len(message) == 0 || len(message) > MaxPairingMessageBytes {
			return ErrInvalid
		}
		pairing, err := tx.Pairing(actor.Vault.ID, deviceID)
		if err != nil {
			return err
		}
		if pairing.State != PairingOffered || !s.now().Before(pairing.ExpiresAt) {
			return ErrConflict
		}
		pairing.Answer = message
		pairing.State = PairingAnswered
		return tx.PutPairing(pairing)
	})
}

func (s *Service) ConfirmPairing(ctx context.Context, caller Caller, request SignedRequest, message []byte) error {
	return s.storage.Update(ctx, func(tx Tx) error {
		actor, err := s.signedActor(tx, caller, request)
		if err != nil {
			return err
		}
		if actor.Pairing == nil {
			return ErrForbidden
		}
		if len(message) == 0 || len(message) > MaxPairingMessageBytes {
			return ErrInvalid
		}
		pairing := *actor.Pairing
		if pairing.State != PairingAnswered {
			return ErrConflict
		}
		pairing.Confirm = message
		pairing.State = PairingConfirmed
		return tx.PutPairing(pairing)
	})
}

func (s *Service) CancelPairing(ctx context.Context, caller Caller, request SignedRequest, deviceID string) error {
	return s.storage.Update(ctx, func(tx Tx) error {
		actor, err := s.signedActor(tx, caller, request)
		if err != nil {
			return err
		}
		if actor.Pairing != nil {
			return tx.DeletePairing(actor.Vault.ID, actor.Pairing.DeviceID)
		}
		if err := requireApproved(actor); err != nil {
			return err
		}
		return tx.DeletePairing(actor.Vault.ID, deviceID)
	})
}

type ApproveInput struct {
	Entry   syncproto.DeviceListEntry
	Package syncproto.KeyPackageV3
}

func (s *Service) Approve(ctx context.Context, caller Caller, request SignedRequest, input ApproveInput) error {
	return s.storage.Update(ctx, func(tx Tx) error {
		actor, err := s.signedActor(tx, caller, request)
		if err != nil {
			return err
		}
		if err := requireActivated(actor); err != nil {
			return err
		}
		entry, pkg := input.Entry, input.Package
		if entry.Kind != syncproto.EntryAdd || entry.SignerDeviceID != actor.Device.DeviceID || entry.VaultID != actor.Vault.ID {
			return ErrForbidden
		}
		pairing, err := tx.Pairing(actor.Vault.ID, entry.SubjectDeviceID)
		if err != nil {
			return err
		}
		if pairing.State != PairingConfirmed || !s.now().Before(pairing.ExpiresAt) {
			return ErrConflict
		}
		if pairing.SigningKey != entry.SubjectSigningKey || pairing.PackageKey != entry.SubjectPackageKey {
			return ErrForbidden
		}
		if entry.Index != actor.Vault.HeadIndex+1 {
			return ErrConflict
		}
		list, err := loadList(tx, actor.Vault.ID)
		if err != nil {
			return err
		}
		if err := list.Extend(entry); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalid, err)
		}
		if pkg.VaultID != actor.Vault.ID || pkg.SenderDeviceID != actor.Device.DeviceID ||
			pkg.RecipientDeviceID != entry.SubjectDeviceID || pkg.VaultEpoch != actor.Vault.Epoch || pkg.EntryDigest != entry.Digest() {
			return ErrInvalid
		}
		if err := pkg.Verify(actor.Device.SigningKey); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalid, err)
		}
		now := s.now()
		if err := tx.AppendDeviceListEntry(entry); err != nil {
			return err
		}
		if err := tx.PutDevice(Device{
			VaultID: actor.Vault.ID, DeviceID: entry.SubjectDeviceID, TokenDeviceID: pairing.TokenDeviceID,
			SigningKey: entry.SubjectSigningKey, PackageKey: entry.SubjectPackageKey,
			State: DeviceApproved, AddedIndex: entry.Index, CreatedAt: now,
		}); err != nil {
			return err
		}
		if err := tx.PutKeyPackage(KeyPackage{KeyPackageV3: pkg, CreatedAt: now}); err != nil {
			return err
		}
		if err := tx.DeletePairing(actor.Vault.ID, entry.SubjectDeviceID); err != nil {
			return err
		}
		vault := actor.Vault
		vault.HeadIndex = entry.Index
		vault.UpdatedAt = now
		return tx.UpdateVault(vault)
	})
}

func (s *Service) KeyPackage(ctx context.Context, caller Caller) (KeyPackage, error) {
	var out KeyPackage
	err := s.storage.View(ctx, func(tx Tx) error {
		actor, err := s.actorFor(tx, caller)
		if err != nil {
			return err
		}
		if err := requireApproved(actor); err != nil {
			return err
		}
		out, err = tx.KeyPackage(actor.Vault.ID, actor.Device.DeviceID)
		return err
	})
	return out, err
}

type ActivateInput struct {
	PackageDigest string
}

func (s *Service) Activate(ctx context.Context, caller Caller, request SignedRequest, input ActivateInput) error {
	return s.storage.Update(ctx, func(tx Tx) error {
		actor, err := s.signedActor(tx, caller, request)
		if err != nil {
			return err
		}
		if err := requireApproved(actor); err != nil {
			return err
		}
		pkg, err := tx.KeyPackage(actor.Vault.ID, actor.Device.DeviceID)
		if err != nil {
			return err
		}
		if pkg.VaultEpoch != actor.Vault.Epoch || pkg.Digest() != input.PackageDigest {
			return ErrConflict
		}
		device := actor.Device
		device.ActivatedEpoch = actor.Vault.Epoch
		return tx.PutDevice(device)
	})
}

type RemoveInput struct {
	Entry    syncproto.DeviceListEntry
	Packages []syncproto.KeyPackageV3
}

func (s *Service) Remove(ctx context.Context, caller Caller, request SignedRequest, input RemoveInput) error {
	return s.storage.Update(ctx, func(tx Tx) error {
		actor, err := s.signedActor(tx, caller, request)
		if err != nil {
			return err
		}
		if err := requireActivated(actor); err != nil {
			return err
		}
		entry := input.Entry
		if entry.Kind != syncproto.EntryRemove || entry.SignerDeviceID != actor.Device.DeviceID || entry.VaultID != actor.Vault.ID {
			return ErrForbidden
		}
		if entry.Index != actor.Vault.HeadIndex+1 {
			return ErrConflict
		}
		list, err := loadList(tx, actor.Vault.ID)
		if err != nil {
			return err
		}
		if err := list.Extend(entry); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalid, err)
		}
		expected := map[string]bool{}
		for _, member := range list.Current() {
			if member.DeviceID != actor.Device.DeviceID {
				expected[member.DeviceID] = true
			}
		}
		if len(input.Packages) != len(expected) {
			return fmt.Errorf("%w: the survivor set is incomplete", ErrInvalid)
		}
		now := s.now()
		if err := tx.DeleteKeyPackages(actor.Vault.ID); err != nil {
			return err
		}
		for _, pkg := range input.Packages {
			if !expected[pkg.RecipientDeviceID] {
				return fmt.Errorf("%w: unexpected or repeated recipient", ErrInvalid)
			}
			delete(expected, pkg.RecipientDeviceID)
			if pkg.VaultID != actor.Vault.ID || pkg.SenderDeviceID != actor.Device.DeviceID || pkg.VaultEpoch != entry.EpochAfter || pkg.EntryDigest != entry.Digest() {
				return ErrInvalid
			}
			if err := pkg.Verify(actor.Device.SigningKey); err != nil {
				return fmt.Errorf("%w: %w", ErrInvalid, err)
			}
			if err := tx.PutKeyPackage(KeyPackage{KeyPackageV3: pkg, CreatedAt: now}); err != nil {
				return err
			}
		}
		if err := tx.AppendDeviceListEntry(entry); err != nil {
			return err
		}
		if err := tx.DeleteCheckpoint(actor.Vault.ID, entry.SubjectDeviceID); err != nil {
			return err
		}
		devices, err := tx.Devices(actor.Vault.ID)
		if err != nil {
			return err
		}
		for _, device := range devices {
			switch {
			case device.DeviceID == entry.SubjectDeviceID:
				device.State = DeviceRevoked
			case device.DeviceID == actor.Device.DeviceID && device.State == DeviceApproved:
				device.ActivatedEpoch = entry.EpochAfter
			default:
				continue
			}
			if err := tx.PutDevice(device); err != nil {
				return err
			}
		}
		pairings, err := tx.Pairings(actor.Vault.ID)
		if err != nil {
			return err
		}
		for _, pairing := range pairings {
			if err := tx.DeletePairing(actor.Vault.ID, pairing.DeviceID); err != nil {
				return err
			}
		}
		vault := actor.Vault
		vault.HeadIndex = entry.Index
		vault.Epoch = entry.EpochAfter
		vault.UpdatedAt = now
		return tx.UpdateVault(vault)
	})
}

type UploadInput struct {
	Header     syncproto.Header
	Ciphertext []byte
}

type Accepted struct {
	Header  StoredHeader
	Vault   Vault
	Receipt string
}

func (s *Service) Upload(ctx context.Context, caller Caller, request SignedRequest, input UploadInput) (Accepted, error) {
	var accepted Accepted
	err := s.storage.Update(ctx, func(tx Tx) error {
		actor, err := s.signedActor(tx, caller, request)
		if err != nil {
			return err
		}
		if err := requireActivated(actor); err != nil {
			return err
		}
		header := input.Header
		if err := header.Validate(); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalid, err)
		}
		if header.VaultID != actor.Vault.ID || header.DeviceID != actor.Device.DeviceID {
			return ErrForbidden
		}
		vault := actor.Vault
		next, ok := checkedNext(vault.CurrentRevision)
		if !ok || header.Revision != next || header.VaultEpoch != vault.Epoch || header.DeviceListIndex != vault.HeadIndex {
			return ErrConflict
		}
		if err := header.VerifyCiphertext(input.Ciphertext); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalid, err)
		}
		list, err := loadList(tx, vault.ID)
		if err != nil {
			return err
		}
		var previous *syncproto.Header
		if vault.CurrentRevision > 0 {
			latest, err := tx.LatestHeader(vault.ID)
			if err != nil {
				return err
			}
			if header.PreviousDigest != latest.Digest {
				return ErrConflict
			}
			previous = &latest.Header
		}
		if err := syncproto.VerifyHeader(list, previous, header); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalid, err)
		}
		if err := s.makeRoom(tx, vault, int64(len(input.Ciphertext))); err != nil {
			return err
		}
		now := s.now()
		digest := header.Digest()
		receipt := syncproto.ReceiptV3{
			VaultID: vault.ID, Revision: header.Revision, HeaderDigest: digest,
			VaultEpoch: header.VaultEpoch, AcceptedAt: uint64(now.Unix()),
		}
		signature := base64.RawURLEncoding.EncodeToString(receipt.Sign(s.config.ReceiptKey))
		stored := StoredHeader{Header: header, Digest: digest, Receipt: signature, AcceptedAt: now}
		if err := tx.InsertHeader(stored); err != nil {
			return err
		}
		if err := tx.PutCiphertext(vault.ID, header.Revision, input.Ciphertext); err != nil {
			return err
		}
		if previous != nil && previous.VaultEpoch < header.VaultEpoch {
			if err := tx.DeleteCiphertextsBefore(vault.ID, header.Revision); err != nil {
				return err
			}
		}
		vault.CurrentRevision = header.Revision
		vault.UpdatedAt = now
		if err := tx.UpdateVault(vault); err != nil {
			return err
		}
		if header.Revision > s.config.HeaderRetention {
			if err := tx.PruneHeaders(vault.ID, header.Revision-s.config.HeaderRetention+1); err != nil {
				return err
			}
		}
		accepted = Accepted{Header: stored, Vault: vault, Receipt: signature}
		return nil
	})
	return accepted, err
}

func (s *Service) PutCheckpoint(ctx context.Context, caller Caller, request SignedRequest, checkpoint syncproto.Checkpoint) (StoredCheckpoint, error) {
	var stored StoredCheckpoint
	err := s.storage.Update(ctx, func(tx Tx) error {
		actor, err := s.signedActor(tx, caller, request)
		if err != nil {
			return err
		}
		if err := requireActivated(actor); err != nil {
			return err
		}
		if checkpoint.VaultID != actor.Vault.ID || checkpoint.DeviceID != actor.Device.DeviceID {
			return ErrForbidden
		}
		if err := checkpoint.Verify(actor.Device.SigningKey); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalid, err)
		}
		if !syncproto.WithinRequestSkew(checkpoint.ObservedAt, s.unix()) {
			return fmt.Errorf("%w: the checkpoint time is not close to the service clock", ErrInvalid)
		}
		previous, err := tx.Checkpoint(actor.Vault.ID, actor.Device.DeviceID)
		if err == nil {
			if checkpoint.ObservedAt <= previous.ObservedAt || checkpoint.DeviceListIndex < previous.DeviceListIndex ||
				checkpoint.HeaderRevision < previous.HeaderRevision {
				return ErrConflict
			}
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		stored = StoredCheckpoint{Checkpoint: checkpoint, AcceptedAt: s.now()}
		return tx.PutCheckpoint(stored)
	})
	return stored, err
}

func (s *Service) Checkpoints(ctx context.Context, caller Caller) ([]StoredCheckpoint, error) {
	var out []StoredCheckpoint
	err := s.storage.View(ctx, func(tx Tx) error {
		actor, err := s.actorFor(tx, caller)
		if err != nil {
			return err
		}
		if err := requireApproved(actor); err != nil {
			return err
		}
		out, err = tx.Checkpoints(actor.Vault.ID)
		return err
	})
	return out, err
}

func (s *Service) makeRoom(tx Tx, vault Vault, incoming int64) error {
	if incoming > s.config.ByteBudget {
		return ErrQuota
	}
	stored, err := tx.CiphertextBytes(vault.ID)
	if err != nil {
		return err
	}
	for stored+incoming > s.config.ByteBudget {
		revision, size, err := tx.OldestCiphertext(vault.ID)
		if errors.Is(err, ErrNotFound) || (err == nil && revision >= vault.CurrentRevision) {
			return ErrQuota
		}
		if err != nil {
			return err
		}
		if err := tx.DeleteCiphertext(vault.ID, revision); err != nil {
			return err
		}
		stored -= size
	}
	return nil
}

func (s *Service) DeviceList(ctx context.Context, caller Caller, after uint64, limit int) ([]syncproto.DeviceListEntry, error) {
	var out []syncproto.DeviceListEntry
	err := s.storage.View(ctx, func(tx Tx) error {
		actor, err := s.actorFor(tx, caller)
		if err != nil {
			return err
		}
		if actor.Pairing == nil && actor.Device.State != DeviceApproved {
			return ErrForbidden
		}
		if actor.Pairing != nil && actor.Pairing.State != PairingConfirmed {
			return ErrForbidden
		}
		out, err = tx.DeviceListEntries(actor.Vault.ID, after, clamp(limit, MaxEntriesPerPage))
		return err
	})
	return out, err
}

func (s *Service) Headers(ctx context.Context, caller Caller, after uint64, limit int) ([]StoredHeader, error) {
	var out []StoredHeader
	err := s.storage.View(ctx, func(tx Tx) error {
		actor, err := s.actorFor(tx, caller)
		if err != nil {
			return err
		}
		if err := requireApproved(actor); err != nil {
			return err
		}
		out, err = tx.Headers(actor.Vault.ID, after, clamp(limit, MaxHeadersPerPage))
		return err
	})
	return out, err
}

type Snapshot struct {
	Header     StoredHeader
	Ciphertext []byte
	Vault      Vault
}

func (s *Service) Snapshot(ctx context.Context, caller Caller) (Snapshot, error) {
	var out Snapshot
	err := s.storage.View(ctx, func(tx Tx) error {
		actor, err := s.actorFor(tx, caller)
		if err != nil {
			return err
		}
		if err := requireApproved(actor); err != nil {
			return err
		}
		out.Vault = actor.Vault
		if actor.Vault.CurrentRevision == 0 {
			return ErrNotFound
		}
		header, err := tx.Header(actor.Vault.ID, actor.Vault.CurrentRevision)
		if err != nil {
			return err
		}
		ciphertext, err := tx.Ciphertext(actor.Vault.ID, actor.Vault.CurrentRevision)
		if err != nil {
			return err
		}
		out.Header, out.Ciphertext = header, ciphertext
		return nil
	})
	return out, err
}

func (s *Service) Devices(ctx context.Context, caller Caller) (Vault, []Device, error) {
	var vault Vault
	var devices []Device
	err := s.storage.View(ctx, func(tx Tx) error {
		actor, err := s.actorFor(tx, caller)
		if err != nil {
			return err
		}
		if actor.Pairing != nil {
			return ErrForbidden
		}
		vault = actor.Vault
		devices, err = tx.Devices(actor.Vault.ID)
		return err
	})
	return vault, devices, err
}

func (s *Service) VaultState(ctx context.Context, caller Caller) (Vault, Actor, error) {
	var vault Vault
	var actor Actor
	err := s.storage.View(ctx, func(tx Tx) error {
		var err error
		actor, err = s.actorFor(tx, caller)
		vault = actor.Vault
		return err
	})
	return vault, actor, err
}

type ResetInput struct {
	VaultEpoch uint64
}

func (s *Service) Reset(ctx context.Context, caller Caller, request SignedRequest, input ResetInput) error {
	return s.storage.Update(ctx, func(tx Tx) error {
		actor, err := s.signedActor(tx, caller, request)
		if err != nil {
			return err
		}
		if err := requireApproved(actor); err != nil {
			return err
		}
		if input.VaultEpoch != actor.Vault.Epoch {
			return ErrConflict
		}
		return tx.DeleteVault(actor.Vault.ID)
	})
}

func (s *Service) Maintain(ctx context.Context) error {
	return s.storage.Update(ctx, func(tx Tx) error {
		return tx.PurgeExpired(s.now())
	})
}

func clamp(limit, maximum int) int {
	if limit <= 0 || limit > maximum {
		return maximum
	}
	return limit
}

func checkedNext(value uint64) (uint64, bool) {
	if value == ^uint64(0) {
		return 0, false
	}
	return value + 1, true
}

type Overview struct {
	HasVault        bool
	Devices         int
	CurrentRevision uint64
	StoredBytes     int64
	UpdatedAt       time.Time
}

func (s *Service) Overview(ctx context.Context, ownerKey string) (Overview, error) {
	var out Overview
	err := s.storage.View(ctx, func(tx Tx) error {
		vault, err := tx.VaultByOwner(ownerKey)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		devices, err := tx.Devices(vault.ID)
		if err != nil {
			return err
		}
		stored, err := tx.CiphertextBytes(vault.ID)
		if err != nil {
			return err
		}
		for _, device := range devices {
			if device.State == DeviceApproved {
				out.Devices++
			}
		}
		out.HasVault = true
		out.CurrentRevision = vault.CurrentRevision
		out.StoredBytes = stored
		out.UpdatedAt = vault.UpdatedAt
		return nil
	})
	return out, err
}

func (s *Service) AdminReset(ctx context.Context, ownerKey string) error {
	return s.storage.Update(ctx, func(tx Tx) error {
		vault, err := tx.VaultByOwner(ownerKey)
		if err != nil {
			return err
		}
		return tx.DeleteVault(vault.ID)
	})
}
