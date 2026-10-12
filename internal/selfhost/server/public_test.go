package server_test

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"usesesame.app/backend/internal/selfhost/server"
)

func capabilityDocumentOf(t *testing.T, e *env) map[string]any {
	t.Helper()
	recorder := e.do(req{path: "/v1/capabilities"})
	expectStatus(t, recorder, http.StatusOK)
	envelope := decodeBody(t, recorder)
	payload, err := base64.RawURLEncoding.DecodeString(envelope["payload"].(string))
	if err != nil {
		t.Fatal(err)
	}
	signature, err := base64.RawURLEncoding.DecodeString(envelope["signature"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if !ed25519.Verify(e.public, payload, signature) {
		t.Fatal("capability signature does not verify with the instance key")
	}
	var document map[string]any
	if err := json.Unmarshal(payload, &document); err != nil {
		t.Fatal(err)
	}
	return document
}

func TestCapabilitiesAreSignedByTheInstanceKey(t *testing.T) {
	e := newEnv(t)
	document := capabilityDocumentOf(t, e)
	if document["schemaVersion"] != float64(1) || document["minimumDesktopVersion"] != "0.0.0" {
		t.Fatalf("document = %v", document)
	}
	features := document["features"].(map[string]any)
	if features["desktopLinking"] != true || features["sync"] != false || features["downloads"] != false || features["updater"] != false {
		t.Fatalf("features = %v", features)
	}
	status := document["serviceStatus"].(map[string]any)
	if status["sync"] != false || status["desktop"] != true {
		t.Fatalf("serviceStatus = %v", status)
	}
	expires, err := time.Parse(time.RFC3339, document["expiresAt"].(string))
	if err != nil || !expires.After(e.clock.Now()) || expires.After(e.clock.Now().Add(6*time.Minute)) {
		t.Fatalf("expiresAt = %v (%v)", document["expiresAt"], err)
	}
	envelope := decodeBody(t, e.do(req{path: "/v1/capabilities"}))
	instance := decodeBody(t, e.do(req{path: "/v1/instance"}))
	if envelope["keyId"] != instance["capabilityKeyId"] {
		t.Fatalf("keyId %v differs from the instance document %v", envelope["keyId"], instance["capabilityKeyId"])
	}
}

func TestCapabilityEnvelopeCannotBeForged(t *testing.T) {
	e := newEnv(t)
	recorder := e.do(req{path: "/v1/capabilities"})
	envelope := decodeBody(t, recorder)
	payload, _ := base64.RawURLEncoding.DecodeString(envelope["payload"].(string))
	signature, _ := base64.RawURLEncoding.DecodeString(envelope["signature"].(string))
	tampered := append([]byte{}, payload...)
	tampered[len(tampered)-3] ^= 1
	if ed25519.Verify(e.public, tampered, signature) {
		t.Fatal("a modified payload still verifies")
	}
}

func TestCapabilitiesSupportConditionalRequests(t *testing.T) {
	e := newEnv(t)
	first := e.do(req{path: "/v1/capabilities"})
	etag := first.Header().Get("ETag")
	if etag == "" || first.Header().Get("Cache-Control") != "public, max-age=60" {
		t.Fatalf("headers = %v", first.Header())
	}
	second := e.do(req{path: "/v1/capabilities", header: map[string]string{"If-None-Match": etag}})
	expectStatus(t, second, http.StatusNotModified)
	if second.Body.Len() != 0 {
		t.Fatal("a 304 response must have no body")
	}
}

func TestInstanceDocument(t *testing.T) {
	e := newEnv(t)
	recorder := e.do(req{path: "/v1/instance", host: "other.example.net"})
	expectStatus(t, recorder, http.StatusOK)
	body := decodeBody(t, recorder)
	raw, err := base64.RawURLEncoding.DecodeString(body["capabilityPublicKey"].(string))
	if err != nil || len(raw) != ed25519.PublicKeySize || !ed25519.PublicKey(raw).Equal(e.public) {
		t.Fatalf("capabilityPublicKey = %v (%v)", body["capabilityPublicKey"], err)
	}
	digest := sha256.Sum256(raw)
	if body["fingerprint"] != hex.EncodeToString(digest[:]) {
		t.Fatalf("fingerprint = %v", body["fingerprint"])
	}
	want := map[string]any{"instanceId": "inst0001", "name": "Home server", "version": "9.9.9-test", "commit": "abc1234", "apiVersion": float64(1), "minimumClientVersion": "0.0.0", "profile": "selfhost", "setupRequired": true}
	for key, value := range want {
		if body[key] != value {
			t.Fatalf("%s = %v, want %v", key, body[key], value)
		}
	}
	if modules, ok := body["modules"].([]any); !ok || len(modules) != 0 {
		t.Fatalf("modules = %v", body["modules"])
	}
	if _, present := body["maximumClientVersion"]; present {
		t.Fatal("maximumClientVersion must be omitted when none is set")
	}
	if body["capabilityKeyId"] == "" {
		t.Fatal("no capabilityKeyId")
	}

	e.createFirstOwner("Dana")
	if decodeBody(t, e.do(req{path: "/v1/instance"}))["setupRequired"] != false {
		t.Fatal("setupRequired should clear after setup")
	}
}

func TestInstanceReportsAMaximumClientVersionWhenSet(t *testing.T) {
	e := newEnv(t, func(cfg *server.Config) { cfg.MaximumClientVersion = "1.9.0"; cfg.MinimumClientVersion = "0.3.0" })
	body := decodeBody(t, e.do(req{path: "/v1/instance"}))
	if body["maximumClientVersion"] != "1.9.0" || body["minimumClientVersion"] != "0.3.0" {
		t.Fatalf("body = %v", body)
	}
}

func TestProbesAndConsoleConfig(t *testing.T) {
	e := newEnv(t)
	live := e.do(req{path: "/livez", host: "10.0.0.5:8787"})
	expectStatus(t, live, http.StatusOK)
	if decodeBody(t, live)["status"] != "ok" {
		t.Fatal("livez is not ok")
	}
	expectStatus(t, e.do(req{path: "/readyz", host: "127.0.0.1:8787"}), http.StatusOK)
	e.store.pingErr = errors.New("database offline")
	expectStatus(t, e.do(req{path: "/readyz"}), http.StatusServiceUnavailable)

	config := decodeBody(t, e.do(req{path: "/config.json"}))
	if config["version"] != "9.9.9-test" || config["apiBase"] != "" || config["setupRequired"] != true {
		t.Fatalf("config.json = %v", config)
	}
}

func TestStorePanicsBecomeAnErrorEnvelope(t *testing.T) {
	e := newEnv(t)
	e.store.panicPing = true
	expectError(t, e.do(req{path: "/readyz"}), http.StatusInternalServerError, "internal_error")
}
