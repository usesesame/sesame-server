package syncsvc

import (
	"context"
	"errors"
	"time"

	"usesesame.app/backend/internal/syncproto"
)

var (
	ErrNotFound     = errors.New("sync record not found")
	ErrConflict     = errors.New("sync state changed, fetch it again")
	ErrForbidden    = errors.New("sync action is not allowed for this device")
	ErrInvalid      = errors.New("sync request is invalid")
	ErrSignature    = errors.New("sync request signature is invalid")
	ErrReplay       = errors.New("sync request was already used")
	ErrQuota        = errors.New("sync storage limit reached")
	ErrNoVault      = errors.New("this account has no synced vault")
	ErrVaultExists  = errors.New("this account already has a synced vault")
	ErrNotActivated = errors.New("sync device has not activated its key")
)

const (
	DevicePending  = "pending"
	DeviceApproved = "approved"
	DeviceRevoked  = "revoked"

	PairingOffered   = "offered"
	PairingAnswered  = "answered"
	PairingConfirmed = "confirmed"

	MaxPairingMessageBytes = 512
	PairingLifetime        = 10 * time.Minute
	MaxPendingPairings     = 3
	MaxHeadersPerPage      = 500
	MaxEntriesPerPage      = 500
	MaxHeaderRetention     = syncproto.MaxHeaderRetention
)

type Caller struct {
	OwnerKey      string
	TokenDeviceID string
	Token         string
}

type Vault struct {
	ID              string
	OwnerKey        string
	CurrentRevision uint64
	HeadIndex       uint64
	Epoch           uint64
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type Device struct {
	VaultID        string
	DeviceID       string
	TokenDeviceID  string
	SigningKey     string
	PackageKey     string
	State          string
	AddedIndex     uint64
	ActivatedEpoch uint64
	CreatedAt      time.Time
}

type Pairing struct {
	VaultID       string
	DeviceID      string
	TokenDeviceID string
	SigningKey    string
	PackageKey    string
	Offer         []byte
	Answer        []byte
	Confirm       []byte
	State         string
	ExpiresAt     time.Time
	CreatedAt     time.Time
}

type KeyPackage struct {
	syncproto.KeyPackageV3
	CreatedAt time.Time
}

type StoredHeader struct {
	syncproto.Header
	Digest     string
	Receipt    string
	AcceptedAt time.Time
}

type StoredCheckpoint struct {
	syncproto.Checkpoint
	AcceptedAt time.Time
}

type Actor struct {
	Vault   Vault
	Device  Device
	Pairing *Pairing
}

type SignedRequest struct {
	Method    string
	Target    string
	Body      []byte
	Timestamp uint64
	Nonce     string
	Signature string
}

type Tx interface {
	VaultByOwner(ownerKey string) (Vault, error)
	VaultByID(vaultID string) (Vault, error)
	InsertVault(vault Vault) error
	UpdateVault(vault Vault) error
	DeleteVault(vaultID string) error

	Devices(vaultID string) ([]Device, error)
	Device(vaultID, deviceID string) (Device, error)
	DeviceByToken(vaultID, tokenDeviceID string) (Device, error)
	PutDevice(device Device) error

	DeviceListEntries(vaultID string, after uint64, limit int) ([]syncproto.DeviceListEntry, error)
	AppendDeviceListEntry(entry syncproto.DeviceListEntry) error

	PutKeyPackage(pkg KeyPackage) error
	KeyPackage(vaultID, recipientDeviceID string) (KeyPackage, error)
	DeleteKeyPackages(vaultID string) error

	InsertHeader(header StoredHeader) error
	Headers(vaultID string, after uint64, limit int) ([]StoredHeader, error)
	Header(vaultID string, revision uint64) (StoredHeader, error)
	LatestHeader(vaultID string) (StoredHeader, error)
	PruneHeaders(vaultID string, keepFrom uint64) error

	PutCiphertext(vaultID string, revision uint64, ciphertext []byte) error
	Ciphertext(vaultID string, revision uint64) ([]byte, error)
	CiphertextBytes(vaultID string) (int64, error)
	OldestCiphertext(vaultID string) (revision uint64, size int64, err error)
	DeleteCiphertext(vaultID string, revision uint64) error
	DeleteCiphertextsBefore(vaultID string, revision uint64) error

	PutCheckpoint(checkpoint StoredCheckpoint) error
	Checkpoint(vaultID, deviceID string) (StoredCheckpoint, error)
	Checkpoints(vaultID string) ([]StoredCheckpoint, error)
	DeleteCheckpoint(vaultID, deviceID string) error

	PutPairing(pairing Pairing) error
	Pairing(vaultID, deviceID string) (Pairing, error)
	PairingByToken(vaultID, tokenDeviceID string) (Pairing, error)
	Pairings(vaultID string) ([]Pairing, error)
	DeletePairing(vaultID, deviceID string) error

	RecordNonce(scope, nonce string, now, expiresAt time.Time) (fresh bool, err error)
	PurgeExpired(now time.Time) error
}

type Storage interface {
	Update(ctx context.Context, fn func(Tx) error) error
	View(ctx context.Context, fn func(Tx) error) error
}
