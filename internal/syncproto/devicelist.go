package syncproto

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
)

const (
	EntryCreate = "create"
	EntryAdd    = "add"
	EntryRemove = "remove"
	EntryKey    = "key"
	EntryRotate = "rotate"
)

var (
	ErrDeviceListInvalid = errors.New("sync device list entry is invalid")
	ErrDeviceListBroken  = errors.New("sync device list does not verify")
)

type DeviceListEntry struct {
	VaultID           string `json:"vaultId"`
	Index             uint64 `json:"index"`
	PreviousDigest    string `json:"previousDigest"`
	Kind              string `json:"kind"`
	SignerDeviceID    string `json:"signerDeviceId"`
	SubjectDeviceID   string `json:"subjectDeviceId"`
	SubjectSigningKey string `json:"subjectSigningKey"`
	SubjectPackageKey string `json:"subjectPackageKey"`
	SealedLabel       string `json:"sealedLabel"`
	EpochAfter        uint64 `json:"epochAfter"`
	SnapshotFloor     uint64 `json:"snapshotFloor"`
	Signature         string `json:"signature"`
}

func (entry DeviceListEntry) fields(label string) *canonical {
	return newCanonical(label).
		text(entry.VaultID).
		number(entry.Index).
		text(entry.PreviousDigest).
		text(entry.Kind).
		text(entry.SignerDeviceID).
		text(entry.SubjectDeviceID).
		text(entry.SubjectSigningKey).
		text(entry.SubjectPackageKey).
		text(entry.SealedLabel).
		number(entry.EpochAfter).
		number(entry.SnapshotFloor)
}

func (entry DeviceListEntry) SigningPayload() []byte {
	return entry.fields(LabelDeviceList).bytes()
}

func (entry DeviceListEntry) Digest() string {
	return entry.fields(LabelDeviceListDigest).text(entry.Signature).digest()
}

func (entry DeviceListEntry) Validate() error {
	if !opaqueID.MatchString(entry.VaultID) || !opaqueID.MatchString(entry.SignerDeviceID) || !opaqueID.MatchString(entry.SubjectDeviceID) {
		return fmt.Errorf("%w: identifiers", ErrDeviceListInvalid)
	}
	if entry.Index == 0 || entry.EpochAfter == 0 {
		return fmt.Errorf("%w: index or epoch", ErrDeviceListInvalid)
	}
	if (entry.Index == 1) != (entry.PreviousDigest == "") {
		return fmt.Errorf("%w: previous digest", ErrDeviceListInvalid)
	}
	if entry.PreviousDigest != "" && !validDigest(entry.PreviousDigest) {
		return fmt.Errorf("%w: previous digest", ErrDeviceListInvalid)
	}
	switch entry.Kind {
	case EntryCreate, EntryAdd:
		if _, err := decodeCanonical(entry.SubjectSigningKey, Ed25519PublicKeyBytes); err != nil {
			return fmt.Errorf("%w: signing key", ErrDeviceListInvalid)
		}
		if _, err := decodeCanonicalOneOf(entry.SubjectPackageKey, X25519PublicKeyBytes, XWingPublicKeyBytes); err != nil {
			return fmt.Errorf("%w: package key", ErrDeviceListInvalid)
		}
		if _, err := decodeCanonicalRange(entry.SealedLabel, MinSealedLabelBytes, MaxSealedLabelBytes); err != nil {
			return fmt.Errorf("%w: label", ErrDeviceListInvalid)
		}
	case EntryRemove:
		if entry.SubjectSigningKey != "" || entry.SubjectPackageKey != "" || entry.SealedLabel != "" {
			return fmt.Errorf("%w: a removal carries no key or label", ErrDeviceListInvalid)
		}
	case EntryKey:
		if entry.SignerDeviceID != entry.SubjectDeviceID || entry.SubjectSigningKey != "" || entry.SealedLabel != "" {
			return fmt.Errorf("%w: a key replacement is signed by its own device and carries only the new package key", ErrDeviceListInvalid)
		}
		if _, err := decodeCanonical(entry.SubjectPackageKey, XWingPublicKeyBytes); err != nil {
			return fmt.Errorf("%w: package key", ErrDeviceListInvalid)
		}
	case EntryRotate:
		if entry.SignerDeviceID != entry.SubjectDeviceID || entry.SubjectSigningKey != "" || entry.SubjectPackageKey != "" || entry.SealedLabel != "" {
			return fmt.Errorf("%w: a rotation is signed by its own device and carries no key or label", ErrDeviceListInvalid)
		}
	default:
		return fmt.Errorf("%w: kind", ErrDeviceListInvalid)
	}
	if entry.Kind != EntryAdd && entry.SnapshotFloor != 0 {
		return fmt.Errorf("%w: snapshot floor", ErrDeviceListInvalid)
	}
	if _, err := decodeCanonical(entry.Signature, Ed25519SignatureBytes); err != nil {
		return fmt.Errorf("%w: signature", ErrDeviceListInvalid)
	}
	return nil
}

type DeviceListMember struct {
	DeviceID     string
	SigningKey   string
	PackageKey   string
	SealedLabel  string
	AddedIndex   uint64
	RemovedIndex uint64
}

func (member DeviceListMember) memberAt(index uint64) bool {
	return member.AddedIndex <= index && (member.RemovedIndex == 0 || index < member.RemovedIndex)
}

type DeviceList struct {
	vaultID string
	members map[string]DeviceListMember
	digests []string
	epochs  []uint64
}

func (list *DeviceList) VaultID() string {
	return list.vaultID
}

func (list *DeviceList) Head() uint64 {
	return uint64(len(list.digests))
}

func (list *DeviceList) HeadDigest() string {
	if len(list.digests) == 0 {
		return ""
	}
	return list.digests[len(list.digests)-1]
}

func (list *DeviceList) Epoch() uint64 {
	if len(list.epochs) == 0 {
		return 0
	}
	return list.epochs[len(list.epochs)-1]
}

func (list *DeviceList) DigestAt(index uint64) (string, bool) {
	if index == 0 || index > uint64(len(list.digests)) {
		return "", false
	}
	return list.digests[index-1], true
}

func (list *DeviceList) EpochAt(index uint64) (uint64, bool) {
	if index == 0 || index > uint64(len(list.epochs)) {
		return 0, false
	}
	return list.epochs[index-1], true
}

func (list *DeviceList) Member(deviceID string) (DeviceListMember, bool) {
	member, ok := list.members[deviceID]
	return member, ok
}

func (list *DeviceList) MemberAt(deviceID string, index uint64) (DeviceListMember, bool) {
	member, ok := list.members[deviceID]
	if !ok || !member.memberAt(index) {
		return DeviceListMember{}, false
	}
	return member, true
}

func (list *DeviceList) Current() []DeviceListMember {
	current := make([]DeviceListMember, 0, len(list.members))
	for _, member := range list.members {
		if member.RemovedIndex == 0 {
			current = append(current, member)
		}
	}
	return current
}

func (list *DeviceList) Clone() *DeviceList {
	clone := &DeviceList{
		vaultID: list.vaultID,
		members: make(map[string]DeviceListMember, len(list.members)),
		digests: append([]string(nil), list.digests...),
		epochs:  append([]uint64(nil), list.epochs...),
	}
	for id, member := range list.members {
		clone.members[id] = member
	}
	return clone
}

func NewDeviceList() *DeviceList {
	return &DeviceList{members: map[string]DeviceListMember{}}
}

func VerifyDeviceList(entries []DeviceListEntry) (*DeviceList, error) {
	list := NewDeviceList()
	for _, entry := range entries {
		if err := list.Extend(entry); err != nil {
			return nil, err
		}
	}
	return list, nil
}

func (list *DeviceList) Extend(entry DeviceListEntry) error {
	if err := entry.Validate(); err != nil {
		return err
	}
	next, ok := checkedNext(list.Head())
	if !ok || entry.Index != next {
		return fmt.Errorf("%w: index %d does not follow %d", ErrDeviceListBroken, entry.Index, list.Head())
	}
	if entry.Index == 1 {
		return list.extendCreate(entry)
	}
	if entry.VaultID != list.vaultID {
		return fmt.Errorf("%w: vault", ErrDeviceListBroken)
	}
	if entry.PreviousDigest != list.HeadDigest() {
		return fmt.Errorf("%w: previous digest", ErrDeviceListBroken)
	}
	signer, ok := list.members[entry.SignerDeviceID]
	if !ok || signer.RemovedIndex != 0 {
		return fmt.Errorf("%w: signer is not a member", ErrDeviceListBroken)
	}
	if err := verifyEntrySignature(entry, signer.SigningKey); err != nil {
		return err
	}
	switch entry.Kind {
	case EntryCreate:
		return fmt.Errorf("%w: only the first entry creates", ErrDeviceListBroken)
	case EntryAdd:
		return list.extendAdd(entry)
	case EntryRemove:
		return list.extendRemove(entry)
	case EntryKey:
		return list.extendKey(entry, signer)
	case EntryRotate:
		return list.extendRotate(entry)
	default:
		return fmt.Errorf("%w: kind", ErrDeviceListBroken)
	}
}

func (list *DeviceList) extendCreate(entry DeviceListEntry) error {
	if entry.Kind != EntryCreate || entry.SignerDeviceID != entry.SubjectDeviceID || entry.EpochAfter != 1 || entry.SnapshotFloor != 0 {
		return fmt.Errorf("%w: the first entry must be a self signed create at epoch 1", ErrDeviceListBroken)
	}
	if err := verifyEntrySignature(entry, entry.SubjectSigningKey); err != nil {
		return err
	}
	list.vaultID = entry.VaultID
	list.members[entry.SubjectDeviceID] = memberFromEntry(entry)
	list.append(entry, 1)
	return nil
}

func (list *DeviceList) extendAdd(entry DeviceListEntry) error {
	if _, seen := list.members[entry.SubjectDeviceID]; seen {
		return fmt.Errorf("%w: device id was already used", ErrDeviceListBroken)
	}
	if entry.EpochAfter != list.Epoch() {
		return fmt.Errorf("%w: an add does not change the epoch", ErrDeviceListBroken)
	}
	if len(list.Current()) >= MaxDevicesPerVault {
		return fmt.Errorf("%w: too many devices", ErrDeviceListBroken)
	}
	list.members[entry.SubjectDeviceID] = memberFromEntry(entry)
	list.append(entry, entry.EpochAfter)
	return nil
}

func (list *DeviceList) extendRemove(entry DeviceListEntry) error {
	subject, ok := list.members[entry.SubjectDeviceID]
	if !ok || subject.RemovedIndex != 0 {
		return fmt.Errorf("%w: subject is not a member", ErrDeviceListBroken)
	}
	nextEpoch, ok := checkedNext(list.Epoch())
	if !ok || entry.EpochAfter != nextEpoch {
		return fmt.Errorf("%w: a removal raises the epoch by one", ErrDeviceListBroken)
	}
	subject.RemovedIndex = entry.Index
	list.members[entry.SubjectDeviceID] = subject
	list.append(entry, entry.EpochAfter)
	return nil
}

func (list *DeviceList) extendKey(entry DeviceListEntry, signer DeviceListMember) error {
	if entry.EpochAfter != list.Epoch() {
		return fmt.Errorf("%w: a key replacement does not change the epoch", ErrDeviceListBroken)
	}
	if _, err := decodeCanonical(signer.PackageKey, X25519PublicKeyBytes); err != nil {
		return fmt.Errorf("%w: only a device with the older package key can replace it", ErrDeviceListBroken)
	}
	signer.PackageKey = entry.SubjectPackageKey
	list.members[signer.DeviceID] = signer
	list.append(entry, entry.EpochAfter)
	return nil
}

func (list *DeviceList) extendRotate(entry DeviceListEntry) error {
	nextEpoch, ok := checkedNext(list.Epoch())
	if !ok || entry.EpochAfter != nextEpoch {
		return fmt.Errorf("%w: a rotation raises the epoch by one", ErrDeviceListBroken)
	}
	list.append(entry, entry.EpochAfter)
	return nil
}

func (list *DeviceList) append(entry DeviceListEntry, epoch uint64) {
	list.digests = append(list.digests, entry.Digest())
	list.epochs = append(list.epochs, epoch)
}

func memberFromEntry(entry DeviceListEntry) DeviceListMember {
	return DeviceListMember{
		DeviceID:    entry.SubjectDeviceID,
		SigningKey:  entry.SubjectSigningKey,
		PackageKey:  entry.SubjectPackageKey,
		SealedLabel: entry.SealedLabel,
		AddedIndex:  entry.Index,
	}
}

func verifyEntrySignature(entry DeviceListEntry, encodedKey string) error {
	key, err := decodeCanonical(encodedKey, Ed25519PublicKeyBytes)
	if err != nil {
		return fmt.Errorf("%w: signer key", ErrDeviceListBroken)
	}
	signature, err := decodeCanonical(entry.Signature, Ed25519SignatureBytes)
	if err != nil || !ed25519.Verify(ed25519.PublicKey(key), entry.SigningPayload(), signature) {
		return fmt.Errorf("%w: signature", ErrDeviceListBroken)
	}
	return nil
}

func SignDeviceListEntry(key ed25519.PrivateKey, entry DeviceListEntry) DeviceListEntry {
	entry.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, entry.SigningPayload()))
	return entry
}

func sha256Bytes(value []byte) [DigestBytes]byte {
	return sha256.Sum256(value)
}
