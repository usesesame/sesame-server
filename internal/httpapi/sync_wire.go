package httpapi

import (
	"encoding/base64"
	"time"

	"usesesame.app/backend/internal/syncstore"
)

func syncDeviceJSON(device syncstore.Device) map[string]any {
	entry := map[string]any{
		"deviceId": device.ID, "state": device.State, "deviceEpoch": device.DeviceEpoch, "label": device.Label,
		"signingPublicKey":    base64.RawURLEncoding.EncodeToString(device.SigningPublicKey),
		"encryptionPublicKey": base64.RawURLEncoding.EncodeToString(device.EncryptionPublicKey),
		"createdAt":           device.CreatedAt.UTC().Format(time.RFC3339),
	}
	if device.ApprovedAt != nil {
		entry["approvedAt"] = device.ApprovedAt.UTC().Format(time.RFC3339)
	}
	if device.RevokedAt != nil {
		entry["revokedAt"] = device.RevokedAt.UTC().Format(time.RFC3339)
	}
	return entry
}
