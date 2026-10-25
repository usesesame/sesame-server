package syncv3http

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"usesesame.app/backend/internal/syncproto"
	"usesesame.app/backend/internal/syncsvc"
)

const (
	signatureHeader    = "Sesame-Request-Signature"
	smallBodyBytes     = 64 << 10
	keyChangeBodyBytes = 1 << 20
	snapshotBodyBytes  = 15 << 20
	minIDLength        = 16
	maxIDLength        = 128
)

type createVaultRequest struct {
	Entry syncproto.DeviceListEntry `json:"entry"`
}

type beginPairingRequest struct {
	DeviceID   string `json:"deviceId"`
	SigningKey string `json:"signingKey"`
	PackageKey string `json:"packageKey"`
	Message    string `json:"message"`
}

type pairingMessageRequest struct {
	Message string `json:"message"`
}

type approveRequest struct {
	Entry   syncproto.DeviceListEntry `json:"entry"`
	Package keyPackageJSON            `json:"package"`
}

type removeRequest struct {
	Entry    syncproto.DeviceListEntry `json:"entry"`
	Packages []keyPackageJSON          `json:"packages"`
}

type upgradeKeyRequest struct {
	Entry syncproto.DeviceListEntry `json:"entry"`
}

type activateRequest struct {
	PackageDigest string `json:"packageDigest"`
}

type uploadRequest struct {
	Header     syncproto.Header `json:"header"`
	Ciphertext string           `json:"ciphertext"`
}

type checkpointRequest struct {
	Checkpoint syncproto.Checkpoint `json:"checkpoint"`
}

type resetRequest struct {
	VaultEpoch uint64 `json:"vaultEpoch"`
}

type keyPackageJSON struct {
	VaultID           string `json:"vaultId"`
	SenderDeviceID    string `json:"senderDeviceId"`
	RecipientDeviceID string `json:"recipientDeviceId"`
	VaultEpoch        uint64 `json:"vaultEpoch"`
	EntryDigest       string `json:"entryDigest"`
	Ciphertext        string `json:"ciphertext"`
	Signature         string `json:"signature"`
}

type vaultResponse struct {
	VaultID    string `json:"vaultId"`
	VaultEpoch uint64 `json:"vaultEpoch"`
	HeadIndex  uint64 `json:"headIndex"`
}

type stateResponse struct {
	VaultID         string `json:"vaultId"`
	VaultEpoch      uint64 `json:"vaultEpoch"`
	HeadIndex       uint64 `json:"headIndex"`
	CurrentRevision uint64 `json:"currentRevision"`
	InstanceKey     string `json:"instanceKey"`
	DeviceID        string `json:"deviceId"`
	DeviceState     string `json:"deviceState"`
	ActivatedEpoch  uint64 `json:"activatedEpoch"`
}

type deviceListResponse struct {
	Entries []syncproto.DeviceListEntry `json:"entries"`
}

type devicesResponse struct {
	Devices  []deviceRecord  `json:"devices"`
	Pairings []pairingRecord `json:"pairings"`
}

type deviceRecord struct {
	DeviceID       string `json:"deviceId"`
	State          string `json:"state"`
	AddedIndex     uint64 `json:"addedIndex"`
	ActivatedEpoch uint64 `json:"activatedEpoch"`
}

type pairingRecord struct {
	DeviceID   string `json:"deviceId"`
	SigningKey string `json:"signingKey"`
	PackageKey string `json:"packageKey"`
	State      string `json:"state"`
	Offer      string `json:"offer"`
	Answer     string `json:"answer"`
	Confirm    string `json:"confirm"`
	ExpiresAt  string `json:"expiresAt"`
}

type pairingStatusResponse struct {
	State     string `json:"state"`
	Answer    string `json:"answer"`
	ExpiresAt string `json:"expiresAt"`
}

type keyPackageResponse struct {
	Package keyPackageJSON `json:"package"`
}

type headersResponse struct {
	Headers []headerRecord `json:"headers"`
}

type headerRecord struct {
	syncproto.Header
	Digest     string `json:"digest"`
	Receipt    string `json:"receipt"`
	AcceptedAt string `json:"acceptedAt"`
}

type checkpointRecord struct {
	syncproto.Checkpoint
	AcceptedAt string `json:"acceptedAt"`
}

type checkpointsResponse struct {
	Checkpoints []checkpointRecord `json:"checkpoints"`
}

type snapshotResponse struct {
	Header     syncproto.Header `json:"header"`
	Digest     string           `json:"digest"`
	Receipt    string           `json:"receipt"`
	AcceptedAt string           `json:"acceptedAt"`
	Ciphertext string           `json:"ciphertext"`
}

type uploadResponse struct {
	Revision   uint64 `json:"revision"`
	Digest     string `json:"digest"`
	Receipt    string `json:"receipt"`
	AcceptedAt string `json:"acceptedAt"`
}

func readSignature(response http.ResponseWriter, request *http.Request) (syncsvc.SignedRequest, bool) {
	values := request.Header.Values(signatureHeader)
	if len(values) != 1 {
		writeError(response, codeSignatureInvalid)
		return syncsvc.SignedRequest{}, false
	}
	parts := strings.Split(values[0], ".")
	if len(parts) != 3 {
		writeError(response, codeSignatureInvalid)
		return syncsvc.SignedRequest{}, false
	}
	timestamp, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil || strconv.FormatUint(timestamp, 10) != parts[0] {
		writeError(response, codeSignatureInvalid)
		return syncsvc.SignedRequest{}, false
	}
	if !canonicalFixedBase64(parts[1], syncproto.RequestNonceBytes) {
		writeError(response, codeSignatureInvalid)
		return syncsvc.SignedRequest{}, false
	}
	if !canonicalFixedBase64(parts[2], syncproto.Ed25519SignatureBytes) {
		writeError(response, codeSignatureInvalid)
		return syncsvc.SignedRequest{}, false
	}
	return syncsvc.SignedRequest{
		Method:    request.Method,
		Target:    request.URL.RequestURI(),
		Timestamp: timestamp,
		Nonce:     parts[1],
		Signature: parts[2],
	}, true
}

func readBody(response http.ResponseWriter, request *http.Request, limit int64) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(response, request.Body, limit))
	if err != nil {
		writeError(response, codeInvalid)
		return nil, false
	}
	return body, true
}

func decodeJSON(response http.ResponseWriter, body []byte, target any) bool {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(response, codeInvalid)
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(response, codeInvalid)
		return false
	}
	return true
}

func readSignedJSON[T any](response http.ResponseWriter, request *http.Request, limit int64) (T, syncsvc.SignedRequest, bool) {
	var input T
	signed, ok := readSignature(response, request)
	if !ok {
		return input, signed, false
	}
	body, ok := readBody(response, request, limit)
	if !ok {
		return input, signed, false
	}
	if !decodeJSON(response, body, &input) {
		return input, signed, false
	}
	signed.Body = body
	return input, signed, true
}

func readSignedEmpty(response http.ResponseWriter, request *http.Request) (syncsvc.SignedRequest, bool) {
	signed, ok := readSignature(response, request)
	if !ok {
		return signed, false
	}
	body, ok := readBody(response, request, smallBodyBytes)
	if !ok {
		return signed, false
	}
	if len(body) != 0 {
		writeError(response, codeInvalid)
		return signed, false
	}
	return signed, true
}

func canonicalBase64(value string) ([]byte, bool) {
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || base64.RawURLEncoding.EncodeToString(decoded) != value {
		return nil, false
	}
	return decoded, true
}

func canonicalFixedBase64(value string, length int) bool {
	decoded, ok := canonicalBase64(value)
	return ok && len(decoded) == length
}

func decodeOneOfBase64(response http.ResponseWriter, value string, lengths ...int) ([]byte, bool) {
	for _, length := range lengths {
		if canonicalFixedBase64(value, length) {
			decoded, _ := canonicalBase64(value)
			return decoded, true
		}
	}
	writeError(response, codeInvalid)
	return nil, false
}

func decodeBase64(response http.ResponseWriter, value string) ([]byte, bool) {
	decoded, ok := canonicalBase64(value)
	if !ok {
		writeError(response, codeInvalid)
		return nil, false
	}
	return decoded, true
}

func decodeFixedBase64(response http.ResponseWriter, value string, length int) ([]byte, bool) {
	if !canonicalFixedBase64(value, length) {
		writeError(response, codeInvalid)
		return nil, false
	}
	decoded, _ := canonicalBase64(value)
	return decoded, true
}

func (wire keyPackageJSON) toService(response http.ResponseWriter) (syncproto.KeyPackageV3, bool) {
	if _, ok := decodeBase64(response, wire.Ciphertext); !ok {
		return syncproto.KeyPackageV3{}, false
	}
	if _, ok := decodeFixedBase64(response, wire.Signature, syncproto.Ed25519SignatureBytes); !ok {
		return syncproto.KeyPackageV3{}, false
	}
	return syncproto.KeyPackageV3{
		VaultID:           wire.VaultID,
		SenderDeviceID:    wire.SenderDeviceID,
		RecipientDeviceID: wire.RecipientDeviceID,
		VaultEpoch:        wire.VaultEpoch,
		EntryDigest:       wire.EntryDigest,
		Ciphertext:        wire.Ciphertext,
		Signature:         wire.Signature,
	}, true
}

func keyPackageToWire(pkg syncproto.KeyPackageV3) keyPackageJSON {
	return keyPackageJSON{
		VaultID:           pkg.VaultID,
		SenderDeviceID:    pkg.SenderDeviceID,
		RecipientDeviceID: pkg.RecipientDeviceID,
		VaultEpoch:        pkg.VaultEpoch,
		EntryDigest:       pkg.EntryDigest,
		Ciphertext:        pkg.Ciphertext,
		Signature:         pkg.Signature,
	}
}

func queryUint(response http.ResponseWriter, request *http.Request, name string, bitSize int) (uint64, bool) {
	value := request.URL.Query().Get(name)
	if value == "" {
		return 0, true
	}
	parsed, err := strconv.ParseUint(value, 10, bitSize)
	if err != nil {
		writeError(response, codeInvalid)
		return 0, false
	}
	return parsed, true
}

func pathDeviceID(response http.ResponseWriter, request *http.Request) (string, bool) {
	value := request.PathValue("deviceId")
	if !validID(value) {
		writeError(response, codeInvalid)
		return "", false
	}
	return value, true
}

func validID(value string) bool {
	if len(value) < minIDLength || len(value) > maxIDLength {
		return false
	}
	for _, char := range value {
		switch {
		case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char >= '0' && char <= '9', char == '_', char == '-':
		default:
			return false
		}
	}
	return true
}

func encodeBase64(value []byte) string {
	return base64.RawURLEncoding.EncodeToString(value)
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339)
}

func orEmpty[T any](values []T) []T {
	if values == nil {
		return []T{}
	}
	return values
}

func deviceRecords(devices []syncsvc.Device) []deviceRecord {
	records := make([]deviceRecord, 0, len(devices))
	for _, device := range devices {
		records = append(records, deviceRecord{
			DeviceID: device.DeviceID, State: device.State,
			AddedIndex: device.AddedIndex, ActivatedEpoch: device.ActivatedEpoch,
		})
	}
	return records
}

func pairingRecords(pairings []syncsvc.Pairing) []pairingRecord {
	records := make([]pairingRecord, 0, len(pairings))
	for _, pairing := range pairings {
		records = append(records, pairingRecord{
			DeviceID: pairing.DeviceID, SigningKey: pairing.SigningKey, PackageKey: pairing.PackageKey,
			State: pairing.State, Offer: encodeBase64(pairing.Offer), Answer: encodeBase64(pairing.Answer),
			Confirm: encodeBase64(pairing.Confirm), ExpiresAt: formatTime(pairing.ExpiresAt),
		})
	}
	return records
}

func headerRecords(stored []syncsvc.StoredHeader) []headerRecord {
	records := make([]headerRecord, 0, len(stored))
	for _, item := range stored {
		records = append(records, headerRecord{
			Header: item.Header, Digest: item.Digest, Receipt: item.Receipt, AcceptedAt: formatTime(item.AcceptedAt),
		})
	}
	return records
}

func checkpointRecords(stored []syncsvc.StoredCheckpoint) []checkpointRecord {
	records := make([]checkpointRecord, 0, len(stored))
	for _, item := range stored {
		records = append(records, checkpointRecord{Checkpoint: item.Checkpoint, AcceptedAt: formatTime(item.AcceptedAt)})
	}
	return records
}
