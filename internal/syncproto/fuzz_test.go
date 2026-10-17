package syncproto

import "testing"

func FuzzVerifyHeader(f *testing.F) {
	list, err := VerifyDeviceList(threeDeviceChain())
	if err != nil {
		f.Fatal(err)
	}
	first := headerOn(list, laptop, nil, 1, make([]byte, 64))
	f.Add(first.VaultID, first.DeviceID, first.Revision, first.VaultEpoch, first.DeviceListIndex, first.DeviceListDigest, first.PreviousDigest, first.CiphertextDigest, first.CiphertextLength, first.Nonce, first.Signature)
	f.Fuzz(func(t *testing.T, vault, device string, revision, epoch, index uint64, listDigest, previous, ciphertextDigest string, length uint64, nonce, signature string) {
		header := Header{VersionV3, vault, device, revision, epoch, index, listDigest, previous, ciphertextDigest, length, nonce, signature}
		if err := VerifyHeader(list, nil, header); err == nil && header != first {
			t.Fatalf("a fuzzed header verified: %+v", header)
		}
	})
}

func FuzzRequestSignature(f *testing.F) {
	f.Add("POST", "/v1/sync/reset", []byte("body"), "token", uint64(1_000_000), "AAAAAAAAAAAAAAAAAAAAAA", "x", uint64(1_000_000))
	f.Fuzz(func(t *testing.T, method, target string, body []byte, token string, timestamp uint64, nonce, signature string, now uint64) {
		request := RequestSignature{Method: method, Target: target, Body: body, Token: token, Timestamp: timestamp, Nonce: nonce}
		if err := request.Verify(laptop.signingKey(), signature, now); err == nil {
			t.Fatalf("a fuzzed signature verified")
		}
	})
}
