package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
)

const SyncOwnerPepperMinimumLength = 32

func syncOwnerPepperUsable(pepper string) bool {
	return len(pepper) >= SyncOwnerPepperMinimumLength
}

func syncOwnerKey(pepper, accountID string) string {
	mac := hmac.New(sha256.New, []byte(pepper))
	mac.Write([]byte("sesame-sync-owner-v1\x00"))
	mac.Write([]byte(accountID))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
