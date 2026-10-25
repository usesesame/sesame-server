package syncv3http

import (
	"net/http"
	"time"

	"usesesame.app/backend/internal/syncproto"
	"usesesame.app/backend/internal/syncsvc"
)

type Host interface {
	Authenticate(response http.ResponseWriter, request *http.Request, operation string) (syncsvc.Caller, bool)
}

type Handler struct {
	service *syncsvc.Service
	host    Host
}

func New(service *syncsvc.Service, host Host) *Handler {
	return &Handler{service: service, host: host}
}

const transferDeadline = 3 * time.Minute

func allowLongTransfer(response http.ResponseWriter) {
	controller := http.NewResponseController(response)
	deadline := time.Now().Add(transferDeadline)
	_ = controller.SetReadDeadline(deadline)
	_ = controller.SetWriteDeadline(deadline)
}

func (handler *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/sync/state", handler.State)
	mux.HandleFunc("POST /v1/sync/vault", handler.CreateVault)
	mux.HandleFunc("GET /v1/sync/device-list", handler.DeviceList)
	mux.HandleFunc("GET /v1/sync/devices", handler.Devices)
	mux.HandleFunc("POST /v1/sync/pairings", handler.BeginPairing)
	mux.HandleFunc("GET /v1/sync/pairings/mine", handler.PairingStatus)
	mux.HandleFunc("POST /v1/sync/pairings/{deviceId}/answer", handler.AnswerPairing)
	mux.HandleFunc("POST /v1/sync/pairings/mine/confirm", handler.ConfirmPairing)
	mux.HandleFunc("DELETE /v1/sync/pairings/{deviceId}", handler.CancelPairing)
	mux.HandleFunc("POST /v1/sync/devices", handler.Approve)
	mux.HandleFunc("POST /v1/sync/devices/remove", handler.Remove)
	mux.HandleFunc("POST /v1/sync/devices/key", handler.UpgradeKey)
	mux.HandleFunc("POST /v1/sync/devices/rotate", handler.Rotate)
	mux.HandleFunc("GET /v1/sync/key-package", handler.KeyPackage)
	mux.HandleFunc("POST /v1/sync/activate", handler.Activate)
	mux.HandleFunc("POST /v1/sync/snapshots", handler.Upload)
	mux.HandleFunc("GET /v1/sync/headers", handler.Headers)
	mux.HandleFunc("GET /v1/sync/snapshot", handler.Download)
	mux.HandleFunc("POST /v1/sync/checkpoints", handler.PutCheckpoint)
	mux.HandleFunc("GET /v1/sync/checkpoints", handler.Checkpoints)
	mux.HandleFunc("POST /v1/sync/reset", handler.Reset)
}

func (handler *Handler) State(response http.ResponseWriter, request *http.Request) {
	caller, ok := handler.host.Authenticate(response, request, "state")
	if !ok {
		return
	}
	vault, actor, err := handler.service.VaultState(request.Context(), caller)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, stateResponse{
		VaultID:         vault.ID,
		VaultEpoch:      vault.Epoch,
		HeadIndex:       vault.HeadIndex,
		CurrentRevision: vault.CurrentRevision,
		InstanceKey:     encodeBase64(handler.service.InstancePublicKey()),
		DeviceID:        actor.Device.DeviceID,
		DeviceState:     deviceState(actor),
		ActivatedEpoch:  actor.Device.ActivatedEpoch,
	})
}

func deviceState(actor syncsvc.Actor) string {
	switch {
	case actor.Pairing != nil:
		return "pairing"
	case actor.Device.State == syncsvc.DeviceApproved:
		return "approved"
	default:
		return "none"
	}
}

func (handler *Handler) CreateVault(response http.ResponseWriter, request *http.Request) {
	caller, ok := handler.host.Authenticate(response, request, "vault")
	if !ok {
		return
	}
	input, signed, ok := readSignedJSON[createVaultRequest](response, request, smallBodyBytes)
	if !ok {
		return
	}
	vault, err := handler.service.CreateVault(request.Context(), caller, signed, syncsvc.CreateVaultInput{Entry: input.Entry})
	if err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusCreated, vaultResponse{VaultID: vault.ID, VaultEpoch: vault.Epoch, HeadIndex: vault.HeadIndex})
}

func (handler *Handler) DeviceList(response http.ResponseWriter, request *http.Request) {
	caller, ok := handler.host.Authenticate(response, request, "device-list")
	if !ok {
		return
	}
	after, ok := queryUint(response, request, "after", 64)
	if !ok {
		return
	}
	entries, err := handler.service.DeviceList(request.Context(), caller, after, 0)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, deviceListResponse{Entries: orEmpty(entries)})
}

func (handler *Handler) Devices(response http.ResponseWriter, request *http.Request) {
	caller, ok := handler.host.Authenticate(response, request, "devices")
	if !ok {
		return
	}
	_, devices, err := handler.service.Devices(request.Context(), caller)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	pairings, err := handler.service.PendingPairings(request.Context(), caller)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, devicesResponse{Devices: deviceRecords(devices), Pairings: pairingRecords(pairings)})
}

func (handler *Handler) BeginPairing(response http.ResponseWriter, request *http.Request) {
	caller, ok := handler.host.Authenticate(response, request, "pairing-begin")
	if !ok {
		return
	}
	input, signed, ok := readSignedJSON[beginPairingRequest](response, request, smallBodyBytes)
	if !ok {
		return
	}
	if _, ok := decodeFixedBase64(response, input.SigningKey, syncproto.Ed25519PublicKeyBytes); !ok {
		return
	}
	if _, ok := decodeOneOfBase64(response, input.PackageKey, syncproto.X25519PublicKeyBytes, syncproto.XWingPublicKeyBytes); !ok {
		return
	}
	message, ok := decodeBase64(response, input.Message)
	if !ok {
		return
	}
	err := handler.service.BeginPairing(request.Context(), caller, signed, syncsvc.PairingOffer{
		DeviceID: input.DeviceID, SigningKey: input.SigningKey, PackageKey: input.PackageKey, Message: message,
	})
	if err != nil {
		writeServiceError(response, err)
		return
	}
	response.WriteHeader(http.StatusCreated)
}

func (handler *Handler) PairingStatus(response http.ResponseWriter, request *http.Request) {
	caller, ok := handler.host.Authenticate(response, request, "pairing-mine")
	if !ok {
		return
	}
	pairing, err := handler.service.PairingStatus(request.Context(), caller)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, pairingStatusResponse{
		State:     pairing.State,
		Answer:    encodeBase64(pairing.Answer),
		ExpiresAt: formatTime(pairing.ExpiresAt),
	})
}

func (handler *Handler) AnswerPairing(response http.ResponseWriter, request *http.Request) {
	caller, ok := handler.host.Authenticate(response, request, "pairing-answer")
	if !ok {
		return
	}
	deviceID, ok := pathDeviceID(response, request)
	if !ok {
		return
	}
	input, signed, ok := readSignedJSON[pairingMessageRequest](response, request, smallBodyBytes)
	if !ok {
		return
	}
	message, ok := decodeBase64(response, input.Message)
	if !ok {
		return
	}
	if err := handler.service.AnswerPairing(request.Context(), caller, signed, deviceID, message); err != nil {
		writeServiceError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (handler *Handler) ConfirmPairing(response http.ResponseWriter, request *http.Request) {
	caller, ok := handler.host.Authenticate(response, request, "pairing-confirm")
	if !ok {
		return
	}
	input, signed, ok := readSignedJSON[pairingMessageRequest](response, request, smallBodyBytes)
	if !ok {
		return
	}
	message, ok := decodeBase64(response, input.Message)
	if !ok {
		return
	}
	if err := handler.service.ConfirmPairing(request.Context(), caller, signed, message); err != nil {
		writeServiceError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (handler *Handler) CancelPairing(response http.ResponseWriter, request *http.Request) {
	caller, ok := handler.host.Authenticate(response, request, "pairing-cancel")
	if !ok {
		return
	}
	deviceID, ok := pathDeviceID(response, request)
	if !ok {
		return
	}
	signed, ok := readSignedEmpty(response, request)
	if !ok {
		return
	}
	if err := handler.service.CancelPairing(request.Context(), caller, signed, deviceID); err != nil {
		writeServiceError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (handler *Handler) Approve(response http.ResponseWriter, request *http.Request) {
	caller, ok := handler.host.Authenticate(response, request, "approve")
	if !ok {
		return
	}
	input, signed, ok := readSignedJSON[approveRequest](response, request, keyChangeBodyBytes)
	if !ok {
		return
	}
	pkg, ok := input.Package.toService(response)
	if !ok {
		return
	}
	err := handler.service.Approve(request.Context(), caller, signed, syncsvc.ApproveInput{Entry: input.Entry, Package: pkg})
	if err != nil {
		writeServiceError(response, err)
		return
	}
	response.WriteHeader(http.StatusCreated)
}

func (handler *Handler) Remove(response http.ResponseWriter, request *http.Request) {
	caller, ok := handler.host.Authenticate(response, request, "remove")
	if !ok {
		return
	}
	input, signed, ok := readSignedJSON[removeRequest](response, request, keyChangeBodyBytes)
	if !ok {
		return
	}
	packages := make([]syncproto.KeyPackageV3, 0, len(input.Packages))
	for _, wire := range input.Packages {
		pkg, ok := wire.toService(response)
		if !ok {
			return
		}
		packages = append(packages, pkg)
	}
	err := handler.service.Remove(request.Context(), caller, signed, syncsvc.RemoveInput{Entry: input.Entry, Packages: packages})
	if err != nil {
		writeServiceError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (handler *Handler) Rotate(response http.ResponseWriter, request *http.Request) {
	caller, ok := handler.host.Authenticate(response, request, "rotate")
	if !ok {
		return
	}
	input, signed, ok := readSignedJSON[removeRequest](response, request, keyChangeBodyBytes)
	if !ok {
		return
	}
	packages := make([]syncproto.KeyPackageV3, 0, len(input.Packages))
	for _, wire := range input.Packages {
		pkg, ok := wire.toService(response)
		if !ok {
			return
		}
		packages = append(packages, pkg)
	}
	err := handler.service.Rotate(request.Context(), caller, signed, syncsvc.RotateInput{Entry: input.Entry, Packages: packages})
	if err != nil {
		writeServiceError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (handler *Handler) UpgradeKey(response http.ResponseWriter, request *http.Request) {
	caller, ok := handler.host.Authenticate(response, request, "device-key")
	if !ok {
		return
	}
	input, signed, ok := readSignedJSON[upgradeKeyRequest](response, request, smallBodyBytes)
	if !ok {
		return
	}
	err := handler.service.UpgradeKey(request.Context(), caller, signed, syncsvc.UpgradeKeyInput{Entry: input.Entry})
	if err != nil {
		writeServiceError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (handler *Handler) KeyPackage(response http.ResponseWriter, request *http.Request) {
	caller, ok := handler.host.Authenticate(response, request, "key-package")
	if !ok {
		return
	}
	pkg, err := handler.service.KeyPackage(request.Context(), caller)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, keyPackageResponse{Package: keyPackageToWire(pkg.KeyPackageV3)})
}

func (handler *Handler) Activate(response http.ResponseWriter, request *http.Request) {
	caller, ok := handler.host.Authenticate(response, request, "activate")
	if !ok {
		return
	}
	input, signed, ok := readSignedJSON[activateRequest](response, request, smallBodyBytes)
	if !ok {
		return
	}
	if !canonicalFixedBase64(input.PackageDigest, syncproto.DigestBytes) {
		writeError(response, codeInvalid)
		return
	}
	err := handler.service.Activate(request.Context(), caller, signed, syncsvc.ActivateInput{PackageDigest: input.PackageDigest})
	if err != nil {
		writeServiceError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (handler *Handler) Upload(response http.ResponseWriter, request *http.Request) {
	caller, ok := handler.host.Authenticate(response, request, "upload")
	if !ok {
		return
	}
	allowLongTransfer(response)
	input, signed, ok := readSignedJSON[uploadRequest](response, request, snapshotBodyBytes)
	if !ok {
		return
	}
	ciphertext, ok := decodeBase64(response, input.Ciphertext)
	if !ok {
		return
	}
	accepted, err := handler.service.Upload(request.Context(), caller, signed, syncsvc.UploadInput{Header: input.Header, Ciphertext: ciphertext})
	if err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusCreated, uploadResponse{
		Revision:   accepted.Header.Revision,
		Digest:     accepted.Header.Digest,
		Receipt:    accepted.Receipt,
		AcceptedAt: formatTime(accepted.Header.AcceptedAt),
	})
}

func (handler *Handler) Headers(response http.ResponseWriter, request *http.Request) {
	caller, ok := handler.host.Authenticate(response, request, "headers")
	if !ok {
		return
	}
	after, ok := queryUint(response, request, "after", 64)
	if !ok {
		return
	}
	limit, ok := queryUint(response, request, "limit", 31)
	if !ok {
		return
	}
	stored, err := handler.service.Headers(request.Context(), caller, after, int(limit))
	if err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, headersResponse{Headers: headerRecords(stored)})
}

func (handler *Handler) Download(response http.ResponseWriter, request *http.Request) {
	caller, ok := handler.host.Authenticate(response, request, "download")
	if !ok {
		return
	}
	allowLongTransfer(response)
	snapshot, err := handler.service.Snapshot(request.Context(), caller)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, snapshotResponse{
		Header:     snapshot.Header.Header,
		Digest:     snapshot.Header.Digest,
		Receipt:    snapshot.Header.Receipt,
		AcceptedAt: formatTime(snapshot.Header.AcceptedAt),
		Ciphertext: encodeBase64(snapshot.Ciphertext),
	})
}

func (handler *Handler) PutCheckpoint(response http.ResponseWriter, request *http.Request) {
	caller, ok := handler.host.Authenticate(response, request, "checkpoint-put")
	if !ok {
		return
	}
	input, signed, ok := readSignedJSON[checkpointRequest](response, request, smallBodyBytes)
	if !ok {
		return
	}
	if _, err := handler.service.PutCheckpoint(request.Context(), caller, signed, input.Checkpoint); err != nil {
		writeServiceError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (handler *Handler) Checkpoints(response http.ResponseWriter, request *http.Request) {
	caller, ok := handler.host.Authenticate(response, request, "checkpoints")
	if !ok {
		return
	}
	stored, err := handler.service.Checkpoints(request.Context(), caller)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, checkpointsResponse{Checkpoints: checkpointRecords(stored)})
}

func (handler *Handler) Reset(response http.ResponseWriter, request *http.Request) {
	caller, ok := handler.host.Authenticate(response, request, "reset")
	if !ok {
		return
	}
	input, signed, ok := readSignedJSON[resetRequest](response, request, smallBodyBytes)
	if !ok {
		return
	}
	if err := handler.service.Reset(request.Context(), caller, signed, syncsvc.ResetInput{VaultEpoch: input.VaultEpoch}); err != nil {
		writeServiceError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}
