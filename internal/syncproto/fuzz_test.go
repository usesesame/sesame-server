package syncproto

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"testing"
)

func validEnvelopeJSON(tb testing.TB) string {
	tb.Helper()
	nonce := make([]byte, XChaChaNonceBytes)
	if _, err := rand.Read(nonce); err != nil {
		tb.Fatalf("seed nonce: %v", err)
	}
	ciphertext := make([]byte, 64)
	if _, err := rand.Read(ciphertext); err != nil {
		tb.Fatalf("seed ciphertext: %v", err)
	}
	digest := make([]byte, 32)
	if _, err := rand.Read(digest); err != nil {
		tb.Fatalf("seed digest: %v", err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		tb.Fatalf("seed signing key: %v", err)
	}
	signature := ed25519.Sign(privateKey, []byte("fictional signing payload"))
	envelope := Envelope{
		Version:          Version,
		VaultID:          "vault-000000000001",
		DeviceID:         "device-0000000001",
		Revision:         2,
		PreviousRevision: 1,
		VaultEpoch:       2,
		DeviceEpoch:      5,
		Operation:        "snapshot",
		PreviousDigest:   base64.RawURLEncoding.EncodeToString(digest),
		Nonce:            base64.RawURLEncoding.EncodeToString(nonce),
		Ciphertext:       base64.RawURLEncoding.EncodeToString(ciphertext),
		Signature:        base64.RawURLEncoding.EncodeToString(signature),
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		tb.Fatalf("seed envelope: %v", err)
	}
	_ = publicKey
	return string(body)
}

func FuzzDecode(f *testing.F) {
	f.Add(validEnvelopeJSON(f))
	f.Add(`{}`)
	f.Add(`{"version":2,"vaultId":"vault-000000000001","deviceId":"device-0000000001","revision":1,"previousRevision":0,"vaultEpoch":1,"deviceEpoch":1,"operation":"snapshot","previousDigest":"","nonce":"","ciphertext":"","signature":""}`)
	f.Add(`{"version":2,"vaultId":"vault-000000000001","deviceId":"device-0000000001","revision":2,"previousRevision":1,"vaultEpoch":1,"deviceEpoch":1,"operation":"snapshot","previousDigest":"AAAA","nonce":"AAAA","ciphertext":"AAAA","signature":"AAAA","extra":true}`)
	f.Add(`[1,2,3]`)
	f.Add(``)
	f.Fuzz(func(t *testing.T, body string) {
		envelope, err := Decode(bytes.NewReader([]byte(body)))
		if err != nil {
			return
		}
		if envelope.Revision == 0 || envelope.Revision != envelope.PreviousRevision+1 {
			t.Errorf("accepted an envelope with a broken revision chain: %+v", envelope)
		}
		if envelope.Version != Version || envelope.Operation != "snapshot" || envelope.TombstoneID != "" {
			t.Errorf("accepted an envelope outside the protocol: %+v", envelope)
		}
		if _, err := envelope.SigningPayload(); err != nil {
			t.Errorf("accepted an envelope whose signing payload cannot be built: %v", err)
		}
	})
}

func FuzzVerifyActivation(f *testing.F) {
	f.Add("not-base64!!", "vault-000000000001", "device-0000000001", uint64(1), "ciphertext", "proof")
	f.Add("MCowBQYDK2VwAyEA", "vault-000000000001", "device-0000000001", uint64(1), "ciphertext", "AAAA")
	f.Add("", "", "", uint64(0), "", "")
	f.Fuzz(func(t *testing.T, encodedPublicKey, vaultID, deviceID string, deviceEpoch uint64, ciphertext, proof string) {
		_ = VerifyActivation(encodedPublicKey, vaultID, deviceID, deviceEpoch, ciphertext, proof)
	})
}

func FuzzVerifyRevocationIntent(f *testing.F) {
	f.Add("MCowBQYDK2VwAyEA", "vault-000000000001", "device-0000000001", "device-0000000002", uint64(1), "AAAA")
	f.Add("", "", "", "", uint64(0), "")
	f.Fuzz(func(t *testing.T, encodedPublicKey, vaultID, callerDeviceID, targetDeviceID string, callerEpoch uint64, intent string) {
		_ = VerifyRevocationIntent(encodedPublicKey, vaultID, callerDeviceID, targetDeviceID, callerEpoch, intent)
	})
}

func FuzzVerifyReceipt(f *testing.F) {
	f.Add("MCowBQYDK2VwAyEA", "AAAA", "AAAA")
	f.Add("", "", "")
	f.Fuzz(func(t *testing.T, encodedPublicKey, encodedReceipt, signature string) {
		_, _ = VerifyReceipt(encodedPublicKey, encodedReceipt, signature)
	})
}
