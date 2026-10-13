package memstore

import (
	"context"
	"sort"
	"sync"
	"time"

	"usesesame.app/backend/internal/syncproto"
	"usesesame.app/backend/internal/syncsvc"
)

type state struct {
	vaults      map[string]syncsvc.Vault
	devices     map[string]syncsvc.Device
	entries     map[string][]syncproto.DeviceListEntry
	packages    map[string]syncsvc.KeyPackage
	headers     map[string]map[uint64]syncsvc.StoredHeader
	ciphertexts map[string]map[uint64][]byte
	pairings    map[string]syncsvc.Pairing
	checkpoints map[string]syncsvc.StoredCheckpoint
	nonces      map[string]time.Time
}

func newState() *state {
	return &state{
		vaults:      map[string]syncsvc.Vault{},
		devices:     map[string]syncsvc.Device{},
		entries:     map[string][]syncproto.DeviceListEntry{},
		packages:    map[string]syncsvc.KeyPackage{},
		headers:     map[string]map[uint64]syncsvc.StoredHeader{},
		ciphertexts: map[string]map[uint64][]byte{},
		pairings:    map[string]syncsvc.Pairing{},
		checkpoints: map[string]syncsvc.StoredCheckpoint{},
		nonces:      map[string]time.Time{},
	}
}

func (s *state) clone() *state {
	out := newState()
	for key, value := range s.vaults {
		out.vaults[key] = value
	}
	for key, value := range s.devices {
		out.devices[key] = value
	}
	for key, value := range s.entries {
		out.entries[key] = append([]syncproto.DeviceListEntry(nil), value...)
	}
	for key, value := range s.packages {
		out.packages[key] = value
	}
	for key, value := range s.headers {
		copied := map[uint64]syncsvc.StoredHeader{}
		for revision, header := range value {
			copied[revision] = header
		}
		out.headers[key] = copied
	}
	for key, value := range s.ciphertexts {
		copied := map[uint64][]byte{}
		for revision, bytes := range value {
			copied[revision] = append([]byte(nil), bytes...)
		}
		out.ciphertexts[key] = copied
	}
	for key, value := range s.pairings {
		out.pairings[key] = value
	}
	for key, value := range s.checkpoints {
		out.checkpoints[key] = value
	}
	for key, value := range s.nonces {
		out.nonces[key] = value
	}
	return out
}

type Store struct {
	mutex sync.Mutex
	data  *state
}

func New() *Store {
	return &Store{data: newState()}
}

func (s *Store) Update(ctx context.Context, fn func(syncsvc.Tx) error) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	working := s.data.clone()
	if err := fn(&tx{data: working}); err != nil {
		return err
	}
	s.data = working
	return nil
}

func (s *Store) View(ctx context.Context, fn func(syncsvc.Tx) error) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return fn(&tx{data: s.data})
}

type tx struct {
	data *state
}

func key(parts ...string) string {
	out := ""
	for _, part := range parts {
		out += part + "\x00"
	}
	return out
}

func (t *tx) VaultByOwner(ownerKey string) (syncsvc.Vault, error) {
	for _, vault := range t.data.vaults {
		if vault.OwnerKey == ownerKey {
			return vault, nil
		}
	}
	return syncsvc.Vault{}, syncsvc.ErrNotFound
}

func (t *tx) VaultByID(vaultID string) (syncsvc.Vault, error) {
	vault, ok := t.data.vaults[vaultID]
	if !ok {
		return syncsvc.Vault{}, syncsvc.ErrNotFound
	}
	return vault, nil
}

func (t *tx) InsertVault(vault syncsvc.Vault) error {
	if _, exists := t.data.vaults[vault.ID]; exists {
		return syncsvc.ErrConflict
	}
	if _, err := t.VaultByOwner(vault.OwnerKey); err == nil {
		return syncsvc.ErrConflict
	}
	t.data.vaults[vault.ID] = vault
	return nil
}

func (t *tx) UpdateVault(vault syncsvc.Vault) error {
	if _, ok := t.data.vaults[vault.ID]; !ok {
		return syncsvc.ErrNotFound
	}
	t.data.vaults[vault.ID] = vault
	return nil
}

func (t *tx) DeleteVault(vaultID string) error {
	delete(t.data.vaults, vaultID)
	delete(t.data.entries, vaultID)
	delete(t.data.headers, vaultID)
	delete(t.data.ciphertexts, vaultID)
	for id, device := range t.data.devices {
		if device.VaultID == vaultID {
			delete(t.data.devices, id)
		}
	}
	for id, pkg := range t.data.packages {
		if pkg.VaultID == vaultID {
			delete(t.data.packages, id)
		}
	}
	for id, pairing := range t.data.pairings {
		if pairing.VaultID == vaultID {
			delete(t.data.pairings, id)
		}
	}
	for id, checkpoint := range t.data.checkpoints {
		if checkpoint.VaultID == vaultID {
			delete(t.data.checkpoints, id)
		}
	}
	return nil
}

func (t *tx) Devices(vaultID string) ([]syncsvc.Device, error) {
	var out []syncsvc.Device
	for _, device := range t.data.devices {
		if device.VaultID == vaultID {
			out = append(out, device)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AddedIndex < out[j].AddedIndex })
	return out, nil
}

func (t *tx) Device(vaultID, deviceID string) (syncsvc.Device, error) {
	device, ok := t.data.devices[key(vaultID, deviceID)]
	if !ok {
		return syncsvc.Device{}, syncsvc.ErrNotFound
	}
	return device, nil
}

func (t *tx) DeviceByToken(vaultID, tokenDeviceID string) (syncsvc.Device, error) {
	var found syncsvc.Device
	for _, device := range t.data.devices {
		if device.VaultID != vaultID || device.TokenDeviceID != tokenDeviceID {
			continue
		}
		if found.DeviceID == "" || (found.State == syncsvc.DeviceRevoked && device.State != syncsvc.DeviceRevoked) ||
			(found.State == device.State && device.AddedIndex > found.AddedIndex) {
			found = device
		}
	}
	if found.DeviceID == "" {
		return syncsvc.Device{}, syncsvc.ErrNotFound
	}
	return found, nil
}

func (t *tx) PutDevice(device syncsvc.Device) error {
	if device.State != syncsvc.DeviceRevoked {
		for _, existing := range t.data.devices {
			if existing.VaultID == device.VaultID && existing.TokenDeviceID == device.TokenDeviceID &&
				existing.DeviceID != device.DeviceID && existing.State != syncsvc.DeviceRevoked {
				return syncsvc.ErrConflict
			}
		}
	}
	t.data.devices[key(device.VaultID, device.DeviceID)] = device
	return nil
}

func (t *tx) DeviceListEntries(vaultID string, after uint64, limit int) ([]syncproto.DeviceListEntry, error) {
	var out []syncproto.DeviceListEntry
	for _, entry := range t.data.entries[vaultID] {
		if entry.Index > after && len(out) < limit {
			out = append(out, entry)
		}
	}
	return out, nil
}

func (t *tx) AppendDeviceListEntry(entry syncproto.DeviceListEntry) error {
	existing := t.data.entries[entry.VaultID]
	if uint64(len(existing))+1 != entry.Index {
		return syncsvc.ErrConflict
	}
	t.data.entries[entry.VaultID] = append(existing, entry)
	return nil
}

func (t *tx) PutKeyPackage(pkg syncsvc.KeyPackage) error {
	t.data.packages[key(pkg.VaultID, pkg.RecipientDeviceID)] = pkg
	return nil
}

func (t *tx) KeyPackage(vaultID, recipientDeviceID string) (syncsvc.KeyPackage, error) {
	pkg, ok := t.data.packages[key(vaultID, recipientDeviceID)]
	if !ok {
		return syncsvc.KeyPackage{}, syncsvc.ErrNotFound
	}
	return pkg, nil
}

func (t *tx) DeleteKeyPackages(vaultID string) error {
	for id, pkg := range t.data.packages {
		if pkg.VaultID == vaultID {
			delete(t.data.packages, id)
		}
	}
	return nil
}

func (t *tx) InsertHeader(header syncsvc.StoredHeader) error {
	byRevision := t.data.headers[header.VaultID]
	if byRevision == nil {
		byRevision = map[uint64]syncsvc.StoredHeader{}
		t.data.headers[header.VaultID] = byRevision
	}
	if _, exists := byRevision[header.Revision]; exists {
		return syncsvc.ErrConflict
	}
	byRevision[header.Revision] = header
	return nil
}

func (t *tx) Headers(vaultID string, after uint64, limit int) ([]syncsvc.StoredHeader, error) {
	var revisions []uint64
	for revision := range t.data.headers[vaultID] {
		if revision > after {
			revisions = append(revisions, revision)
		}
	}
	sort.Slice(revisions, func(i, j int) bool { return revisions[i] < revisions[j] })
	var out []syncsvc.StoredHeader
	for _, revision := range revisions {
		if len(out) >= limit {
			break
		}
		out = append(out, t.data.headers[vaultID][revision])
	}
	return out, nil
}

func (t *tx) Header(vaultID string, revision uint64) (syncsvc.StoredHeader, error) {
	header, ok := t.data.headers[vaultID][revision]
	if !ok {
		return syncsvc.StoredHeader{}, syncsvc.ErrNotFound
	}
	return header, nil
}

func (t *tx) LatestHeader(vaultID string) (syncsvc.StoredHeader, error) {
	var latest syncsvc.StoredHeader
	found := false
	for _, header := range t.data.headers[vaultID] {
		if !found || header.Revision > latest.Revision {
			latest, found = header, true
		}
	}
	if !found {
		return syncsvc.StoredHeader{}, syncsvc.ErrNotFound
	}
	return latest, nil
}

func (t *tx) PruneHeaders(vaultID string, keepFrom uint64) error {
	for revision := range t.data.headers[vaultID] {
		if revision < keepFrom {
			delete(t.data.headers[vaultID], revision)
		}
	}
	return nil
}

func (t *tx) PutCiphertext(vaultID string, revision uint64, ciphertext []byte) error {
	byRevision := t.data.ciphertexts[vaultID]
	if byRevision == nil {
		byRevision = map[uint64][]byte{}
		t.data.ciphertexts[vaultID] = byRevision
	}
	byRevision[revision] = append([]byte(nil), ciphertext...)
	return nil
}

func (t *tx) Ciphertext(vaultID string, revision uint64) ([]byte, error) {
	ciphertext, ok := t.data.ciphertexts[vaultID][revision]
	if !ok {
		return nil, syncsvc.ErrNotFound
	}
	return append([]byte(nil), ciphertext...), nil
}

func (t *tx) CiphertextBytes(vaultID string) (int64, error) {
	var total int64
	for _, ciphertext := range t.data.ciphertexts[vaultID] {
		total += int64(len(ciphertext))
	}
	return total, nil
}

func (t *tx) OldestCiphertext(vaultID string) (uint64, int64, error) {
	found := false
	var oldest uint64
	var size int64
	for revision, ciphertext := range t.data.ciphertexts[vaultID] {
		if !found || revision < oldest {
			oldest, size, found = revision, int64(len(ciphertext)), true
		}
	}
	if !found {
		return 0, 0, syncsvc.ErrNotFound
	}
	return oldest, size, nil
}

func (t *tx) DeleteCiphertext(vaultID string, revision uint64) error {
	delete(t.data.ciphertexts[vaultID], revision)
	return nil
}

func (t *tx) DeleteCiphertextsBefore(vaultID string, revision uint64) error {
	for existing := range t.data.ciphertexts[vaultID] {
		if existing < revision {
			delete(t.data.ciphertexts[vaultID], existing)
		}
	}
	return nil
}

func (t *tx) PutCheckpoint(checkpoint syncsvc.StoredCheckpoint) error {
	t.data.checkpoints[key(checkpoint.VaultID, checkpoint.DeviceID)] = checkpoint
	return nil
}

func (t *tx) Checkpoint(vaultID, deviceID string) (syncsvc.StoredCheckpoint, error) {
	checkpoint, ok := t.data.checkpoints[key(vaultID, deviceID)]
	if !ok {
		return syncsvc.StoredCheckpoint{}, syncsvc.ErrNotFound
	}
	return checkpoint, nil
}

func (t *tx) Checkpoints(vaultID string) ([]syncsvc.StoredCheckpoint, error) {
	var out []syncsvc.StoredCheckpoint
	for _, checkpoint := range t.data.checkpoints {
		if checkpoint.VaultID == vaultID {
			out = append(out, checkpoint)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DeviceID < out[j].DeviceID })
	return out, nil
}

func (t *tx) DeleteCheckpoint(vaultID, deviceID string) error {
	delete(t.data.checkpoints, key(vaultID, deviceID))
	return nil
}

func (t *tx) PutPairing(pairing syncsvc.Pairing) error {
	t.data.pairings[key(pairing.VaultID, pairing.DeviceID)] = pairing
	return nil
}

func (t *tx) Pairing(vaultID, deviceID string) (syncsvc.Pairing, error) {
	pairing, ok := t.data.pairings[key(vaultID, deviceID)]
	if !ok {
		return syncsvc.Pairing{}, syncsvc.ErrNotFound
	}
	return pairing, nil
}

func (t *tx) PairingByToken(vaultID, tokenDeviceID string) (syncsvc.Pairing, error) {
	for _, pairing := range t.data.pairings {
		if pairing.VaultID == vaultID && pairing.TokenDeviceID == tokenDeviceID {
			return pairing, nil
		}
	}
	return syncsvc.Pairing{}, syncsvc.ErrNotFound
}

func (t *tx) Pairings(vaultID string) ([]syncsvc.Pairing, error) {
	var out []syncsvc.Pairing
	for _, pairing := range t.data.pairings {
		if pairing.VaultID == vaultID {
			out = append(out, pairing)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (t *tx) DeletePairing(vaultID, deviceID string) error {
	delete(t.data.pairings, key(vaultID, deviceID))
	return nil
}

func (t *tx) RecordNonce(scope, nonce string, now, expiresAt time.Time) (bool, error) {
	id := key(scope, nonce)
	if until, seen := t.data.nonces[id]; seen && now.Before(until) {
		return false, nil
	}
	t.data.nonces[id] = expiresAt
	return true, nil
}

func (t *tx) PurgeExpired(now time.Time) error {
	for id, until := range t.data.nonces {
		if !now.Before(until) {
			delete(t.data.nonces, id)
		}
	}
	for id, pairing := range t.data.pairings {
		if !now.Before(pairing.ExpiresAt) {
			delete(t.data.pairings, id)
		}
	}
	return nil
}
