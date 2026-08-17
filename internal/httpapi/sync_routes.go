package httpapi

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"

	"usesesame.app/backend/internal/accounts"
	"usesesame.app/backend/internal/syncproto"
	"usesesame.app/backend/internal/syncstore"
)

// Sesame Sync HTTP surface. Sync is NOT enabled: every route refuses while the
// cloud_sync_available flag is false. Authentication is the
// desktop device token, there is no cookie path, and ciphertext is moved,
// never inspected.

const (
	maxSyncBodyBytes = syncproto.MaxEnvelopeBytes
	syncRateWindow   = time.Minute
)

func (a *api) registerSyncRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/v1/sync/enroll/begin", a.syncEnrollBegin)
	mux.HandleFunc("/v1/sync/enroll/finish", a.syncEnrollFinish)
	mux.HandleFunc("/v1/sync/devices", a.syncDevices)
	mux.HandleFunc("/v1/sync/devices/", a.syncDeviceRoute)
	mux.HandleFunc("/v1/sync/key-package", a.syncKeyPackage)
	mux.HandleFunc("/v1/sync/activate", a.syncActivateDevice)
	mux.HandleFunc("/v1/sync/reset", a.syncResetVault)
	mux.HandleFunc("/v1/sync/envelope", a.syncEnvelope)
}

// Sync fails closed: unlike capabilityEnabled, the fallback here is an explicit false.
func (a *api) requireSync(response http.ResponseWriter, request *http.Request) (*syncstore.Store, bool) {
	if !a.syncEnabled(request.Context()) {
		writeError(response, http.StatusForbidden, "sync_unavailable", "Sesame Sync is not available.")
		return nil, false
	}
	return a.config.Sync, true
}

type syncLimit struct {
	operation         string
	perIP, perAccount int
	perDevice         int
	needsEntitlement  bool
}

var (
	syncEnrollBeginLimit  = syncLimit{"sync-enroll-begin", 30, 10, 5, true}
	syncEnrollFinishLimit = syncLimit{"sync-enroll-finish", 30, 10, 5, true}
	syncDevicesLimit      = syncLimit{"sync-devices", 200, 60, 30, false}
	syncApproveLimit      = syncLimit{"sync-approve", 60, 20, 10, true}
	syncRevokeLimit       = syncLimit{"sync-revoke", 60, 20, 10, false}
	syncKeyPackageLimit   = syncLimit{"sync-key-package", 90, 30, 15, false}
	syncDownloadLimit     = syncLimit{"sync-download", 300, 120, 60, false}
	syncUploadLimit       = syncLimit{"sync-upload", 180, 60, 30, true}
	syncActivateLimit     = syncLimit{"sync-activate", 60, 20, 10, false}
	syncResetLimit        = syncLimit{"sync-reset", 10, 5, 3, false}
)

func (a *api) requireSyncCaller(response http.ResponseWriter, request *http.Request, limit syncLimit) (accounts.DesktopConnection, bool) {
	if !a.requireAccounts(response) {
		return accounts.DesktopConnection{}, false
	}
	if !a.allowRequest(response, request, limit.operation, limit.perIP, syncRateWindow) {
		return accounts.DesktopConnection{}, false
	}
	connection, ok := a.desktopConnectionForRequest(response, request)
	if !ok {
		return accounts.DesktopConnection{}, false
	}
	if !a.allowKeyed(response, request, limit.operation+":account:"+connection.AccountID, limit.perAccount, syncRateWindow) {
		return accounts.DesktopConnection{}, false
	}
	if !a.allowKeyed(response, request, limit.operation+":device:"+connection.DeviceID, limit.perDevice, syncRateWindow) {
		return accounts.DesktopConnection{}, false
	}
	if limit.needsEntitlement && !a.syncEntitled(response, request, connection.AccountID) {
		return accounts.DesktopConnection{}, false
	}
	return connection, true
}

type syncEntitlementStore interface {
	AccountAccess(context.Context, string) (accounts.Access, error)
}

func (a *api) syncEntitled(response http.ResponseWriter, request *http.Request, accountID string) bool {
	store, ok := a.config.Accounts.(syncEntitlementStore)
	if !ok {
		writeError(response, http.StatusServiceUnavailable, "sync_unavailable", "Sesame Sync is temporarily unavailable.")
		return false
	}
	access, err := store.AccountAccess(request.Context(), accountID)
	if err != nil {
		// Fail closed: an entitlement that cannot be read is not an entitlement.
		writeError(response, http.StatusServiceUnavailable, "sync_unavailable", "Sesame Sync is temporarily unavailable.")
		return false
	}
	if !access.DownloadsAllowed {
		writeError(response, http.StatusPaymentRequired, "sync_not_entitled",
			"This account's Sesame subscription is not active. Your synced vault stays available to download and to remove devices from.")
		return false
	}
	return true
}

func (a *api) syncVaultForDevice(response http.ResponseWriter, request *http.Request, store *syncstore.Store, limit syncLimit) (syncstore.Vault, accounts.DesktopConnection, bool) {
	connection, ok := a.requireSyncCaller(response, request, limit)
	if !ok {
		return syncstore.Vault{}, accounts.DesktopConnection{}, false
	}
	vault, err := store.VaultForAccount(request.Context(), connection.AccountID)
	if errors.Is(err, syncstore.ErrNotFound) {
		writeError(response, http.StatusNotFound, "sync_vault_not_found", "This account has no synced vault.")
		return syncstore.Vault{}, accounts.DesktopConnection{}, false
	}
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "sync_unavailable", "Sesame Sync is temporarily unavailable.")
		return syncstore.Vault{}, accounts.DesktopConnection{}, false
	}
	return vault, connection, true
}

func (a *api) syncVaultForApprovedDevice(response http.ResponseWriter, request *http.Request, store *syncstore.Store, limit syncLimit) (syncstore.Vault, syncstore.Device, bool) {
	vault, connection, ok := a.syncVaultForDevice(response, request, store, limit)
	if !ok {
		return syncstore.Vault{}, syncstore.Device{}, false
	}
	device, err := store.DeviceForDesktop(request.Context(), vault.ID, connection.DeviceID)
	switch {
	case errors.Is(err, syncstore.ErrNotFound), errors.Is(err, syncstore.ErrDesktopBindingRequired):
		writeError(response, http.StatusForbidden, "sync_device_not_approved", "This desktop is not set up for Sesame Sync on this vault.")
		return syncstore.Vault{}, syncstore.Device{}, false
	case err != nil:
		writeError(response, http.StatusServiceUnavailable, "sync_unavailable", "Sesame Sync is temporarily unavailable.")
		return syncstore.Vault{}, syncstore.Device{}, false
	}
	if device.State != string(syncproto.DeviceApproved) {
		writeError(response, http.StatusForbidden, "sync_device_not_approved", "This desktop is not approved for Sesame Sync on this vault.")
		return syncstore.Vault{}, syncstore.Device{}, false
	}
	return vault, device, true
}

type syncEnrollBeginRequest struct {
	VaultID string `json:"vaultId"`
}

func (a *api) syncEnrollBegin(response http.ResponseWriter, request *http.Request) {
	store, ok := a.requireSync(response, request)
	if !ok {
		return
	}
	if !allowMethod(response, request, http.MethodPost) {
		return
	}
	connection, ok := a.requireSyncCaller(response, request, syncEnrollBeginLimit)
	if !ok {
		return
	}
	var input syncEnrollBeginRequest
	if !decodeJSONBodyWith(response, request, &input, "invalid_sync_enrollment", "Enrollment details could not be read.") {
		return
	}
	if !validOpaqueSyncID(input.VaultID) {
		writeError(response, http.StatusBadRequest, "invalid_sync_enrollment", "Enrollment details are invalid.")
		return
	}
	vault, err := store.EnsureVault(request.Context(), connection.AccountID, input.VaultID)
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "sync_unavailable", "Sesame Sync is temporarily unavailable.")
		return
	}
	challenge, err := store.IssueChallenge(request.Context(), vault.ID)
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "sync_unavailable", "Sesame Sync is temporarily unavailable.")
		return
	}
	writeJSON(response, http.StatusCreated, map[string]any{
		"vaultId":    vault.ID,
		"vaultEpoch": vault.VaultEpoch,
		"challenge":  base64.RawURLEncoding.EncodeToString(challenge),
		"expiresAt":  time.Now().Add(syncstore.ChallengeTTL).UTC().Format(time.RFC3339),
	})
}

type syncEnrollFinishRequest struct {
	DeviceID            string `json:"deviceId"`
	SigningPublicKey    string `json:"signingPublicKey"`
	EncryptionPublicKey string `json:"encryptionPublicKey"`
	Challenge           string `json:"challenge"`
	Proof               string `json:"proof"`
	Label               string `json:"label"`
}

func (a *api) syncEnrollFinish(response http.ResponseWriter, request *http.Request) {
	store, ok := a.requireSync(response, request)
	if !ok {
		return
	}
	if !allowMethod(response, request, http.MethodPost) {
		return
	}
	vault, connection, ok := a.syncVaultForDevice(response, request, store, syncEnrollFinishLimit)
	if !ok {
		return
	}
	var input syncEnrollFinishRequest
	if !decodeJSONBodyWith(response, request, &input, "invalid_sync_enrollment", "Enrollment details could not be read.") {
		return
	}

	enrollment := syncproto.DeviceEnrollment{
		VaultID:             vault.ID,
		DeviceID:            input.DeviceID,
		SigningPublicKey:    input.SigningPublicKey,
		EncryptionPublicKey: input.EncryptionPublicKey,
		Challenge:           input.Challenge,
		Proof:               input.Proof,
		CreatedAt:           time.Now(),
	}
	if err := enrollment.Validate(); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_sync_enrollment", "Enrollment details are invalid.")
		return
	}
	signingKey, err := base64.RawURLEncoding.DecodeString(input.SigningPublicKey)
	if err != nil {
		writeError(response, http.StatusBadRequest, "invalid_sync_enrollment", "Enrollment details are invalid.")
		return
	}
	encryptionKey, err := base64.RawURLEncoding.DecodeString(input.EncryptionPublicKey)
	if err != nil {
		writeError(response, http.StatusBadRequest, "invalid_sync_enrollment", "Enrollment details are invalid.")
		return
	}
	challenge, err := base64.RawURLEncoding.DecodeString(input.Challenge)
	if err != nil {
		writeError(response, http.StatusBadRequest, "invalid_sync_enrollment", "Enrollment details are invalid.")
		return
	}

	device, err := store.EnrollDevice(request.Context(), syncstore.Enrollment{
		VaultID:             vault.ID,
		DeviceID:            input.DeviceID,
		SigningPublicKey:    signingKey,
		EncryptionPublicKey: encryptionKey,
		Challenge:           challenge,
		Label:               trimSyncLabel(input.Label),
		// The Sync identity is the authenticated desktop connection, never the request body.
		DesktopDeviceID: connection.DeviceID,
	})
	switch {
	case errors.Is(err, syncstore.ErrChallengeUnusable):
		// One response for unknown, expired, and used challenges: no enumeration.
		writeError(response, http.StatusConflict, "sync_enrollment_expired", "Start device setup again from an unlocked Sesame desktop.")
		return
	case errors.Is(err, syncstore.ErrQuotaExceeded):
		writeError(response, http.StatusConflict, "sync_device_limit_reached", "This vault already has the maximum number of devices. Remove one, then set this device up again.")
		return
	case err != nil:
		writeError(response, http.StatusServiceUnavailable, "sync_unavailable", "Sesame Sync is temporarily unavailable.")
		return
	}

	writeJSON(response, http.StatusCreated, syncDeviceJSON(device))
}

func (a *api) syncDevices(response http.ResponseWriter, request *http.Request) {
	store, ok := a.requireSync(response, request)
	if !ok {
		return
	}
	if !allowMethod(response, request, http.MethodGet) {
		return
	}
	vault, _, ok := a.syncVaultForApprovedDevice(response, request, store, syncDevicesLimit)
	if !ok {
		return
	}
	devices, err := store.Devices(request.Context(), vault.ID)
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "sync_unavailable", "Sesame Sync is temporarily unavailable.")
		return
	}
	listed := make([]map[string]any, 0, len(devices))
	for _, device := range devices {
		listed = append(listed, syncDeviceJSON(device))
	}
	writeJSON(response, http.StatusOK, map[string]any{"vaultId": vault.ID, "vaultEpoch": vault.VaultEpoch, "devices": listed})
}

type syncApproveRequest struct {
	SenderDeviceID     string `json:"senderDeviceId"`
	ExpectedVaultEpoch uint64 `json:"expectedVaultEpoch"`
	Ciphertext         string `json:"ciphertext"`
	Signature          string `json:"signature"`
}

func (a *api) syncDeviceRoute(response http.ResponseWriter, request *http.Request) {
	store, ok := a.requireSync(response, request)
	if !ok {
		return
	}
	if !a.requireAccounts(response) {
		return
	}
	rest := strings.TrimPrefix(request.URL.Path, "/v1/sync/devices/")
	deviceID, action, _ := strings.Cut(rest, "/")
	if !validOpaqueSyncID(deviceID) {
		writeError(response, http.StatusNotFound, "sync_device_not_found", "That device is not registered.")
		return
	}
	switch action {
	case "approve":
		a.syncApproveDevice(response, request, store, deviceID)
	case "deny":
		a.syncDenyDevice(response, request, store, deviceID)
	case "rekey":
		a.syncRekeyDevice(response, request, store, deviceID)
	case "":
		a.syncRevokeDevice(response, request, store, deviceID)
	default:
		writeError(response, http.StatusNotFound, "not_found", "That endpoint does not exist.")
	}
}

// The service cannot produce this key package: it holds no vault key.
func (a *api) syncApproveDevice(response http.ResponseWriter, request *http.Request, store *syncstore.Store, deviceID string) {
	if !allowMethod(response, request, http.MethodPost) {
		return
	}
	vault, _, ok := a.syncVaultForApprovedDevice(response, request, store, syncApproveLimit)
	if !ok {
		return
	}
	var input syncApproveRequest
	if !decodeJSONBodyWith(response, request, &input, "invalid_sync_approval", "Approval details could not be read.") {
		return
	}
	pkg := syncproto.EncryptedKeyPackage{
		VaultID: vault.ID, SenderDeviceID: input.SenderDeviceID, RecipientDeviceID: deviceID,
		Ciphertext: input.Ciphertext, Signature: input.Signature, CreatedAt: time.Now(),
	}
	if input.ExpectedVaultEpoch == 0 || pkg.Validate() != nil {
		writeError(response, http.StatusBadRequest, "invalid_sync_approval", "Approval details are invalid.")
		return
	}
	// Opaque bytes from here down: moved, never read.
	ciphertext, err := base64.RawURLEncoding.DecodeString(input.Ciphertext)
	if err != nil {
		writeError(response, http.StatusBadRequest, "invalid_sync_approval", "Approval details are invalid.")
		return
	}
	signature, err := base64.RawURLEncoding.DecodeString(input.Signature)
	if err != nil {
		writeError(response, http.StatusBadRequest, "invalid_sync_approval", "Approval details are invalid.")
		return
	}
	device, err := store.ApproveDevice(request.Context(), syncstore.KeyPackage{
		VaultID: vault.ID, SenderDeviceID: input.SenderDeviceID, RecipientDeviceID: deviceID,
		ExpectedVaultEpoch: input.ExpectedVaultEpoch, Ciphertext: ciphertext, Signature: signature,
	})
	switch {
	case errors.Is(err, syncstore.ErrApprovalRejected):
		writeError(response, http.StatusForbidden, "sync_approval_rejected", "Approve this device from a Sesame desktop that is already syncing.")
		return
	case err != nil:
		writeError(response, http.StatusServiceUnavailable, "sync_unavailable", "Sesame Sync is temporarily unavailable.")
		return
	}
	writeJSON(response, http.StatusOK, syncDeviceJSON(device))
}

// It only ever acts on the caller's own device ID.
func (a *api) syncRevokeDevice(response http.ResponseWriter, request *http.Request, store *syncstore.Store, deviceID string) {
	if !allowMethod(response, request, http.MethodDelete) {
		return
	}
	vault, caller, ok := a.syncVaultForApprovedDevice(response, request, store, syncRevokeLimit)
	if !ok {
		return
	}
	if caller.ID != deviceID {
		writeError(response, http.StatusForbidden, "sync_leave_only",
			"This route only removes the calling device. Use the signed rekey ceremony to remove a different device.")
		return
	}
	err := store.LeaveVault(request.Context(), vault.ID, deviceID)
	switch {
	case errors.Is(err, syncstore.ErrNotFound):
		writeError(response, http.StatusNotFound, "sync_device_not_found", "That device is not registered.")
		return
	case err != nil:
		writeError(response, http.StatusServiceUnavailable, "sync_unavailable", "Sesame Sync is temporarily unavailable.")
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

type syncRekeyRequest struct {
	Envelope  syncproto.Envelope `json:"envelope"`
	Survivors []struct {
		DeviceID   string `json:"deviceId"`
		Ciphertext string `json:"ciphertext"`
		Signature  string `json:"signature"`
	} `json:"survivors"`
	Intent string `json:"intent"`
}

func (a *api) syncRekeyDevice(response http.ResponseWriter, request *http.Request, store *syncstore.Store, deviceID string) {
	if !allowMethod(response, request, http.MethodPost) {
		return
	}
	vault, caller, ok := a.syncVaultForApprovedDevice(response, request, store, syncRevokeLimit)
	if !ok {
		return
	}
	if caller.ID == deviceID {
		writeError(response, http.StatusBadRequest, "invalid_sync_request",
			"Turn Sesame Sync off on this device instead of removing it from itself.")
		return
	}

	var input syncRekeyRequest
	if !decodeJSONBodyLimited(response, request, &input, "invalid_sync_envelope",
		"The sync payload could not be read.", maxSyncBodyBytes) {
		return
	}

	// Removal is signed by the calling device, so an account token alone cannot authorise it.
	if !syncproto.VerifyRevocationIntent(
		base64.RawURLEncoding.EncodeToString(caller.SigningPublicKey),
		vault.ID, caller.ID, deviceID, caller.DeviceEpoch, input.Intent,
	) {
		writeError(response, http.StatusForbidden, "sync_intent_invalid",
			"This device did not authorise removing that device.")
		return
	}

	if err := input.Envelope.Validate(); err != nil || input.Envelope.VaultID != vault.ID {
		writeError(response, http.StatusBadRequest, "invalid_sync_envelope", "The sync payload is invalid.")
		return
	}
	nonce, ciphertext, signature, ok := decodeSyncEnvelopeBytes(response, input.Envelope)
	if !ok {
		return
	}

	survivors := make([]syncstore.SurvivorPackage, 0, len(input.Survivors))
	for _, survivor := range input.Survivors {
		if !validOpaqueSyncID(survivor.DeviceID) {
			writeError(response, http.StatusBadRequest, "invalid_sync_request", "That device is not registered.")
			return
		}
		// Opaque bytes.
		wrapped, err := base64.RawURLEncoding.DecodeString(survivor.Ciphertext)
		if err != nil || len(wrapped) < 16 || len(wrapped) > syncproto.MaxEncryptedKeyPackageBytes {
			writeError(response, http.StatusBadRequest, "invalid_sync_approval", "Approval details are invalid.")
			return
		}
		sealed, err := base64.RawURLEncoding.DecodeString(survivor.Signature)
		if err != nil || len(sealed) != syncproto.Ed25519SignatureBytes {
			writeError(response, http.StatusBadRequest, "invalid_sync_approval", "Approval details are invalid.")
			return
		}
		survivors = append(survivors, syncstore.SurvivorPackage{
			RecipientDeviceID: survivor.DeviceID,
			Ciphertext:        wrapped,
			Signature:         sealed,
		})
	}

	updated, err := store.RevokeAndRekey(request.Context(), syncstore.Rekey{
		VaultID:           vault.ID,
		RevokedDeviceID:   deviceID,
		InitiatorDeviceID: caller.ID,
		Envelope: syncstore.Envelope{
			VaultID: input.Envelope.VaultID, Revision: input.Envelope.Revision,
			PreviousRevision: input.Envelope.PreviousRevision, DeviceID: input.Envelope.DeviceID,
			VaultEpoch: input.Envelope.VaultEpoch, DeviceEpoch: input.Envelope.DeviceEpoch,
			Operation: input.Envelope.Operation, TombstoneID: input.Envelope.TombstoneID,
			PreviousDigest: input.Envelope.PreviousDigest,
			Nonce:          nonce, Ciphertext: ciphertext, Signature: signature,
		},
		Survivors: survivors,
	})
	switch {
	case errors.Is(err, syncstore.ErrNotFound):
		writeError(response, http.StatusNotFound, "sync_device_not_found", "That device is not registered.")
		return
	case errors.Is(err, syncstore.ErrApprovalRejected):
		writeError(response, http.StatusForbidden, "sync_rekey_rejected",
			"Sesame could not complete that removal. Open Sesame Sync and try again.")
		return
	case errors.Is(err, syncstore.ErrConflict):
		writeError(response, http.StatusConflict, "sync_conflict",
			"Another device changed this vault first. Download the latest version, then remove the device again.")
		return
	case err != nil:
		writeError(response, http.StatusServiceUnavailable, "sync_unavailable", "Sesame Sync is temporarily unavailable.")
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{
		"vaultId":    updated.ID,
		"vaultEpoch": updated.VaultEpoch,
		"revision":   updated.CurrentRevision,
	})
}

// A pending device never held the vault key, so there is nothing to rotate.
func (a *api) syncDenyDevice(response http.ResponseWriter, request *http.Request, store *syncstore.Store, deviceID string) {
	if !allowMethod(response, request, http.MethodPost) {
		return
	}
	vault, _, ok := a.syncVaultForApprovedDevice(response, request, store, syncRevokeLimit)
	if !ok {
		return
	}
	err := store.DenyDevice(request.Context(), vault.ID, deviceID)
	switch {
	case errors.Is(err, syncstore.ErrApprovalRejected):
		writeError(response, http.StatusConflict, "sync_device_not_pending",
			"That device is already set up. Remove it instead, which also changes the vault key.")
		return
	case err != nil:
		writeError(response, http.StatusServiceUnavailable, "sync_unavailable", "Sesame Sync is temporarily unavailable.")
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (a *api) syncActivateDevice(response http.ResponseWriter, request *http.Request) {
	store, ok := a.requireSync(response, request)
	if !ok {
		return
	}
	if !allowMethod(response, request, http.MethodPost) {
		return
	}
	// Deliberately the weaker gate: an activating device is not yet live.
	vault, connection, ok := a.syncVaultForDevice(response, request, store, syncActivateLimit)
	if !ok {
		return
	}
	device, err := store.DeviceForDesktop(request.Context(), vault.ID, connection.DeviceID)
	if err != nil {
		writeError(response, http.StatusForbidden, "sync_device_not_approved",
			"This desktop is not set up for Sesame Sync on this vault.")
		return
	}
	var input struct {
		Proof string `json:"proof"`
	}
	if !decodeJSONBodyWith(response, request, &input, "invalid_sync_request", "That request could not be read.") {
		return
	}
	proof, err := base64.RawURLEncoding.DecodeString(input.Proof)
	if err != nil || len(proof) != syncproto.Ed25519SignatureBytes {
		writeError(response, http.StatusBadRequest, "invalid_sync_request", "That request is invalid.")
		return
	}
	switch err := store.ActivateDevice(request.Context(), vault.ID, device.ID, proof); {
	case errors.Is(err, syncstore.ErrNotFound):
		writeError(response, http.StatusNotFound, "sync_key_package_not_found", "This device has not been approved yet.")
		return
	case errors.Is(err, syncstore.ErrApprovalRejected):
		writeError(response, http.StatusForbidden, "sync_activation_rejected", "Sesame could not confirm this device.")
		return
	case err != nil:
		writeError(response, http.StatusServiceUnavailable, "sync_unavailable", "Sesame Sync is temporarily unavailable.")
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

// Destructive: empties a vault with no approved device left and refuses while
// any remain. It deliberately does not require an approved Sync device, because by definition there is none.
func (a *api) syncResetVault(response http.ResponseWriter, request *http.Request) {
	store, ok := a.requireSync(response, request)
	if !ok {
		return
	}
	if !allowMethod(response, request, http.MethodPost) {
		return
	}
	vault, _, ok := a.syncVaultForDevice(response, request, store, syncResetLimit)
	if !ok {
		return
	}
	switch err := store.ResetVault(request.Context(), vault.ID); {
	case errors.Is(err, syncstore.ErrApprovalRejected):
		writeError(response, http.StatusConflict, "sync_devices_remain",
			"This vault still has a device that can read it. Remove the others from that device instead.")
		return
	case err != nil:
		writeError(response, http.StatusServiceUnavailable, "sync_unavailable", "Sesame Sync is temporarily unavailable.")
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func validOpaqueSyncID(value string) bool {
	if len(value) != 22 {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(decoded) == 16
}

func trimSyncLabel(label string) string {
	trimmed := strings.TrimSpace(label)
	if len(trimmed) > 64 {
		trimmed = trimmed[:64]
	}
	// User text shown on another device: strip control characters so it cannot rewrite a terminal or log line.
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, trimmed)
}
