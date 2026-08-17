package httpapi

import (
	"encoding/base64"
	"errors"
	"net/http"
	"time"

	"usesesame.app/backend/internal/syncproto"
	"usesesame.app/backend/internal/syncstore"
)

// The recipient is always the authenticated caller from its desktop token,
// never a caller-supplied deviceId.
func (a *api) syncKeyPackage(response http.ResponseWriter, request *http.Request) {
	store, ok := a.requireSync(response, request)
	if !ok {
		return
	}
	if !allowMethod(response, request, http.MethodGet) {
		return
	}
	vault, caller, ok := a.syncVaultForApprovedDevice(response, request, store, syncKeyPackageLimit)
	if !ok {
		return
	}
	pkg, err := store.KeyPackageFor(request.Context(), vault.ID, caller.ID)
	switch {
	case errors.Is(err, syncstore.ErrNotFound):
		writeError(response, http.StatusNotFound, "sync_key_package_not_found", "This device has not been approved yet.")
		return
	case err != nil:
		writeError(response, http.StatusServiceUnavailable, "sync_unavailable", "Sesame Sync is temporarily unavailable.")
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{
		"vaultId":           pkg.VaultID,
		"senderDeviceId":    pkg.SenderDeviceID,
		"recipientDeviceId": pkg.RecipientDeviceID,
		"ciphertext":        base64.RawURLEncoding.EncodeToString(pkg.Ciphertext),
		"signature":         base64.RawURLEncoding.EncodeToString(pkg.Signature),
	})
}

func (a *api) syncEnvelope(response http.ResponseWriter, request *http.Request) {
	store, ok := a.requireSync(response, request)
	if !ok {
		return
	}
	if !a.requireAccounts(response) {
		return
	}
	switch request.Method {
	case http.MethodGet:
		a.syncDownloadEnvelope(response, request, store)
	case http.MethodPost:
		a.syncUploadEnvelope(response, request, store)
	default:
		response.Header().Set("Allow", "GET, POST, OPTIONS")
		writeError(response, http.StatusMethodNotAllowed, "method_not_allowed", "This endpoint does not allow that method.")
	}
}

func (a *api) syncDownloadEnvelope(response http.ResponseWriter, request *http.Request, store *syncstore.Store) {
	vault, _, ok := a.syncVaultForApprovedDevice(response, request, store, syncDownloadLimit)
	if !ok {
		return
	}
	envelope, err := store.LatestEnvelope(request.Context(), vault.ID)
	switch {
	case errors.Is(err, syncstore.ErrNotFound):
		writeJSON(response, http.StatusOK, map[string]any{"vaultId": vault.ID, "vaultEpoch": vault.VaultEpoch, "revision": 0, "envelope": nil})
		return
	case err != nil:
		writeError(response, http.StatusServiceUnavailable, "sync_unavailable", "Sesame Sync is temporarily unavailable.")
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{
		"vaultId":    vault.ID,
		"vaultEpoch": vault.VaultEpoch,
		"revision":   envelope.Revision,
		"envelope":   syncEnvelopeJSON(envelope),
		// The client recomputes the digest before trusting either.
		"digest":     envelope.Digest,
		"receipt":    envelope.Receipt,
		"uploadedAt": envelope.CreatedAt.UTC().Format(time.RFC3339),
	})
}

func (a *api) syncUploadEnvelope(response http.ResponseWriter, request *http.Request, store *syncstore.Store) {
	vault, _, ok := a.syncVaultForApprovedDevice(response, request, store, syncUploadLimit)
	if !ok {
		return
	}
	var wire syncproto.Envelope
	if !decodeJSONBodyLimited(response, request, &wire, "invalid_sync_envelope", "The sync payload could not be read.", maxSyncBodyBytes) {
		return
	}
	// syncproto validates framing only; nothing below decrypts.
	if err := wire.Validate(); err != nil || wire.VaultID != vault.ID {
		writeError(response, http.StatusBadRequest, "invalid_sync_envelope", "The sync payload is invalid.")
		return
	}
	nonce, ciphertext, signature, ok := decodeSyncEnvelopeBytes(response, wire)
	if !ok {
		return
	}

	updated, accepted, err := store.AppendEnvelopeAccepted(request.Context(), syncstore.Envelope{
		VaultID: wire.VaultID, Revision: wire.Revision, PreviousRevision: wire.PreviousRevision,
		DeviceID: wire.DeviceID, VaultEpoch: wire.VaultEpoch, DeviceEpoch: wire.DeviceEpoch,
		Operation: wire.Operation, TombstoneID: wire.TombstoneID, PreviousDigest: wire.PreviousDigest,
		Nonce: nonce, Ciphertext: ciphertext, Signature: signature,
	})
	switch {
	case errors.Is(err, syncstore.ErrConflict):
		// Never retried by overwriting. Re-reading the head reports the actual
		// authoritative state; the pre-attempt snapshot is only a safe fallback.
		currentRevision, vaultEpoch := vault.CurrentRevision, vault.VaultEpoch
		if head, headErr := store.LatestEnvelope(request.Context(), vault.ID); headErr == nil {
			currentRevision, vaultEpoch = head.Revision, head.VaultEpoch
		}
		writeJSON(response, http.StatusConflict, map[string]any{
			"error":           "sync_conflict",
			"message":         "Another device changed this vault. Review the difference before syncing.",
			"currentRevision": currentRevision,
			"vaultEpoch":      vaultEpoch,
		})
		return
	case errors.Is(err, syncstore.ErrApprovalRejected):
		writeError(response, http.StatusForbidden, "sync_device_not_approved", "This device is not approved to sync.")
		return
	case errors.Is(err, syncstore.ErrNotFound):
		writeError(response, http.StatusNotFound, "sync_device_not_found", "That device is not registered.")
		return
	case errors.Is(err, syncstore.ErrQuotaExceeded):
		writeError(response, http.StatusConflict, "sync_storage_limit_reached", "This vault has reached its Sync storage limit. Older revisions age out automatically as new ones are added.")
		return
	case err != nil:
		writeError(response, http.StatusServiceUnavailable, "sync_unavailable", "Sesame Sync is temporarily unavailable.")
		return
	}
	// The client compares the digest before recording either, so a service
	// cannot choose where the next revision chains from. Both come from the
	// committed transaction, not a second query.
	writeJSON(response, http.StatusOK, map[string]any{
		"vaultId":    updated.ID,
		"revision":   updated.CurrentRevision,
		"vaultEpoch": updated.VaultEpoch,
		"digest":     accepted.Digest,
		"receipt":    accepted.Receipt,
	})
}

// Decodes framing only; handlers must never inspect the ciphertext.
func decodeSyncEnvelopeBytes(response http.ResponseWriter, wire syncproto.Envelope) (nonce, ciphertext, signature []byte, ok bool) {
	var err error
	if nonce, err = base64.RawURLEncoding.DecodeString(wire.Nonce); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_sync_envelope", "The sync payload is invalid.")
		return nil, nil, nil, false
	}
	if ciphertext, err = base64.RawURLEncoding.DecodeString(wire.Ciphertext); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_sync_envelope", "The sync payload is invalid.")
		return nil, nil, nil, false
	}
	if signature, err = base64.RawURLEncoding.DecodeString(wire.Signature); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_sync_envelope", "The sync payload is invalid.")
		return nil, nil, nil, false
	}
	return nonce, ciphertext, signature, true
}

// The only response representation; it deliberately exposes no decoded ciphertext fields.
func syncEnvelopeJSON(envelope syncstore.Envelope) map[string]any {
	return map[string]any{
		"version":          syncproto.Version,
		"vaultId":          envelope.VaultID,
		"deviceId":         envelope.DeviceID,
		"revision":         envelope.Revision,
		"previousRevision": envelope.PreviousRevision,
		"vaultEpoch":       envelope.VaultEpoch,
		"deviceEpoch":      envelope.DeviceEpoch,
		"operation":        envelope.Operation,
		"tombstoneId":      envelope.TombstoneID,
		"previousDigest":   envelope.PreviousDigest,
		"nonce":            base64.RawURLEncoding.EncodeToString(envelope.Nonce),
		"ciphertext":       base64.RawURLEncoding.EncodeToString(envelope.Ciphertext),
		"signature":        base64.RawURLEncoding.EncodeToString(envelope.Signature),
	}
}
