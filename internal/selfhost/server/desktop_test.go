package server_test

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestDesktopFlowMatchesTheHostedWireShapes(t *testing.T) {
	e := newEnv(t)
	o := e.createFirstOwner("Dana")
	issued := o.pairing(true, "")
	code := issued["code"].(string)
	if len(code) < 32 || len(code) > 128 {
		t.Fatalf("pairing code length %d is outside what the desktop accepts", len(code))
	}
	linked := e.link(code, "Dana's laptop")
	expectStatus(t, linked, http.StatusCreated)
	body := decodeBody(t, linked)
	token, _ := body["accessToken"].(string)
	device, _ := body["device"].(map[string]any)
	if token == "" || body["syncAvailable"] != false {
		t.Fatalf("link body = %v", body)
	}
	if _, err := time.Parse(time.RFC3339, body["expiresAt"].(string)); err != nil {
		t.Fatalf("expiresAt is not RFC 3339: %v", err)
	}
	for _, key := range []string{"deviceId", "deviceName", "connectedAt", "expiresAt", "lastSeenAt", "protocolVersion", "browserHelperCapable"} {
		if _, ok := device[key]; !ok {
			t.Fatalf("device is missing %q: %v", key, device)
		}
	}
	if device["deviceName"] != "Dana's laptop" {
		t.Fatalf("deviceName = %v", device["deviceName"])
	}

	for _, scheme := range []string{"Sesame ", "Bearer "} {
		status := e.do(req{path: "/v1/desktop/status", header: map[string]string{"Authorization": scheme + token}})
		expectStatus(t, status, http.StatusOK)
		statusBody := decodeBody(t, status)
		if statusBody["connected"] != true || statusBody["syncAvailable"] != false || statusBody["browserHelperAvailable"] != false {
			t.Fatalf("status body = %v", statusBody)
		}
	}

	heartbeat := e.do(req{method: http.MethodPost, path: "/v1/desktop/heartbeat", header: bearer(token), body: map[string]any{
		"appVersion": "0.3.0", "platform": "linux", "architecture": "x86_64", "updateChannel": "stable", "protocolVersion": 1,
		"browserHelperCapable": true, "browserHelperObserved": false,
	}})
	expectStatus(t, heartbeat, http.StatusOK)
	hbDevice := decodeBody(t, heartbeat)["device"].(map[string]any)
	if hbDevice["appVersion"] != "0.3.0" || hbDevice["browserHelperCapable"] != true {
		t.Fatalf("heartbeat device = %v", hbDevice)
	}

	desktopConfig := e.do(req{path: "/v1/desktop/config", header: bearer(token)})
	expectStatus(t, desktopConfig, http.StatusOK)
	configBody := decodeBody(t, desktopConfig)
	if configBody["minimumProtocolVersion"] != float64(1) || configBody["syncAvailable"] != false {
		t.Fatalf("config body = %v", configBody)
	}
	if helper := configBody["browserHelper"].(map[string]any); helper["capable"] != true {
		t.Fatalf("browser helper = %v", helper)
	}

	disconnect := e.do(req{method: http.MethodDelete, path: "/v1/desktop/connection", header: bearer(token)})
	expectStatus(t, disconnect, http.StatusNoContent)
	expectError(t, e.do(req{path: "/v1/desktop/status", header: bearer(token)}), http.StatusUnauthorized, "not_authenticated")
}

func TestPairingCodeIsSingleUse(t *testing.T) {
	e := newEnv(t)
	o := e.createFirstOwner("Dana")
	code := o.pairing(true, "")["code"].(string)
	expectStatus(t, e.link(code, "First laptop"), http.StatusCreated)
	expectError(t, e.link(code, "Second laptop"), http.StatusUnauthorized, "invalid_desktop_link")
}

func TestExpiredCancelledAndUnknownPairingCodesAreRejected(t *testing.T) {
	e := newEnv(t)
	o := e.createFirstOwner("Dana")

	expiring := o.pairing(true, "")["code"].(string)
	e.clock.Advance(10*time.Minute + time.Second)
	expectError(t, e.link(expiring, "Late laptop"), http.StatusUnauthorized, "invalid_desktop_link")

	cancelled := o.pairing(true, "")
	expectStatus(t, o.do(req{method: http.MethodDelete, path: "/v1/owner/pairings/" + cancelled["pairingId"].(string)}), http.StatusNoContent)
	expectError(t, e.link(cancelled["code"].(string), "Cancelled laptop"), http.StatusUnauthorized, "invalid_desktop_link")

	expectError(t, e.link(strings.Repeat("A", 43), "Guess laptop"), http.StatusUnauthorized, "invalid_desktop_link")
}

func TestDesktopLinkValidatesTheCodeAndDeviceName(t *testing.T) {
	e := newEnv(t)
	cases := []struct{ code, name string }{
		{strings.Repeat("a", 31), "Laptop"},
		{strings.Repeat("a", 129), "Laptop"},
		{strings.Repeat("a", 43), ""},
		{strings.Repeat("a", 43), "   "},
		{strings.Repeat("a", 43), strings.Repeat("n", 65)},
		{strings.Repeat("a", 43), "Läptop"},
		{strings.Repeat("a", 43), "Lap\ntop"},
	}
	for index, c := range cases {
		recorder := e.do(req{method: http.MethodPost, path: "/v1/desktop/link", body: map[string]string{"code": c.code, "deviceName": c.name}, remote: "203.0.113." + string(rune('1'+index)) + ":9"})
		expectError(t, recorder, http.StatusBadRequest, "invalid_desktop_link")
	}
}

func TestDesktopLinkIsRateLimited(t *testing.T) {
	e := newEnv(t)
	for index := 0; index < 8; index++ {
		expectError(t, e.link(strings.Repeat("a", 43), "Laptop"), http.StatusUnauthorized, "invalid_desktop_link")
	}
	limited := e.link(strings.Repeat("a", 43), "Laptop")
	expectError(t, limited, http.StatusTooManyRequests, "too_many_attempts")
	if limited.Header().Get("Retry-After") == "" {
		t.Fatal("missing Retry-After")
	}
}

func TestDesktopRoutesRejectBrowserOrigins(t *testing.T) {
	e := newEnv(t)
	recorder := e.do(req{method: http.MethodPost, path: "/v1/desktop/link", body: map[string]string{"code": strings.Repeat("a", 43), "deviceName": "Laptop"}, header: map[string]string{"Origin": "https://evil.example.net"}})
	expectError(t, recorder, http.StatusForbidden, "origin_not_allowed")
}

func TestRevokedDeviceTokenFailsOnEveryDesktopRoute(t *testing.T) {
	e := newEnv(t)
	o := e.createFirstOwner("Dana")
	token, deviceID := e.pairedDevice(o)
	o.stepUp()
	expectStatus(t, o.do(req{method: http.MethodDelete, path: "/v1/owner/devices/" + deviceID}), http.StatusNoContent)

	heartbeatBody := map[string]any{"appVersion": "0.3.0", "platform": "linux", "architecture": "x86_64", "updateChannel": "stable", "protocolVersion": 1}
	expectError(t, e.do(req{path: "/v1/desktop/status", header: bearer(token)}), http.StatusUnauthorized, "not_authenticated")
	expectError(t, e.do(req{path: "/v1/desktop/config", header: bearer(token)}), http.StatusUnauthorized, "not_authenticated")
	expectError(t, e.do(req{method: http.MethodPost, path: "/v1/desktop/heartbeat", header: bearer(token), body: heartbeatBody}), http.StatusUnauthorized, "not_authenticated")
	expectStatus(t, e.do(req{method: http.MethodDelete, path: "/v1/desktop/connection", header: bearer(token)}), http.StatusNoContent)
	if !containsFold(e.store.audit[len(e.store.audit)-1].Action, "device") {
		t.Fatalf("unexpected last audit action %q", e.store.audit[len(e.store.audit)-1].Action)
	}
}

func TestExpiredDeviceTokenFailsOnDesktopRoutes(t *testing.T) {
	e := newEnv(t)
	o := e.createFirstOwner("Dana")
	token, _ := e.pairedDevice(o)
	e.clock.Advance(91 * 24 * time.Hour)
	expectError(t, e.do(req{path: "/v1/desktop/status", header: bearer(token)}), http.StatusUnauthorized, "not_authenticated")
}

func TestDesktopRoutesRejectMissingAndMalformedCredentials(t *testing.T) {
	e := newEnv(t)
	routes := []routeCase{
		{http.MethodGet, "/v1/desktop/status", nil},
		{http.MethodGet, "/v1/desktop/config", nil},
		{http.MethodPost, "/v1/desktop/heartbeat", map[string]any{"protocolVersion": 1}},
		{http.MethodDelete, "/v1/desktop/connection", nil},
	}
	headers := []map[string]string{
		nil,
		{"Authorization": ""},
		{"Authorization": "Sesame "},
		{"Authorization": "Basic abc"},
		{"Authorization": "Sesame two tokens"},
		{"Authorization": "Sesame " + strings.Repeat("x", 300)},
		{"Authorization": "sesame lowercase"},
		{"Authorization": "Sesame " + strings.Repeat("a", 43)},
	}
	for _, route := range routes {
		for index, header := range headers {
			recorder := e.do(req{method: route.method, path: route.path, body: route.body, header: header, remote: "203.0.113." + string(rune('1'+index)) + ":9"})
			if route.method == http.MethodDelete && index == len(headers)-1 {
				expectStatus(t, recorder, http.StatusNoContent)
				continue
			}
			expectError(t, recorder, http.StatusUnauthorized, "not_authenticated")
		}
	}
}

func TestHeartbeatRejectsInvalidDetails(t *testing.T) {
	e := newEnv(t)
	o := e.createFirstOwner("Dana")
	token, _ := e.pairedDevice(o)
	bad := []map[string]any{
		{"protocolVersion": 0},
		{"protocolVersion": 101},
		{"protocolVersion": 1, "platform": strings.Repeat("p", 65)},
		{"protocolVersion": 1, "appVersion": "1\n2"},
		{"protocolVersion": 1, "updateChannel": "a\tb"},
	}
	for _, body := range bad {
		expectError(t, e.do(req{method: http.MethodPost, path: "/v1/desktop/heartbeat", header: bearer(token), body: body}), http.StatusBadRequest, "invalid_desktop_heartbeat")
	}
	unknown := e.do(req{method: http.MethodPost, path: "/v1/desktop/heartbeat", header: bearer(token), body: map[string]any{"protocolVersion": 1, "extra": true}})
	expectError(t, unknown, http.StatusBadRequest, "invalid_desktop_heartbeat")
}

func TestMemberDevicesAreRevokedWhenTheMemberIsDeleted(t *testing.T) {
	e := newEnv(t)
	o := e.createFirstOwner("Dana")
	created := o.do(req{method: http.MethodPost, path: "/v1/owner/members", body: map[string]string{"name": "Ana"}})
	expectStatus(t, created, http.StatusCreated)
	memberID := decodeBody(t, created)["member"].(map[string]any)["id"].(string)

	o.stepUp()
	issued := o.pairing(false, memberID)
	if issued["holder"].(map[string]any)["name"] != "Ana" {
		t.Fatalf("pairing holder = %v", issued["holder"])
	}
	linked := e.link(issued["code"].(string), "Ana's phone")
	expectStatus(t, linked, http.StatusCreated)
	token := decodeBody(t, linked)["accessToken"].(string)
	expectStatus(t, e.do(req{path: "/v1/desktop/status", header: bearer(token)}), http.StatusOK)

	listed := decodeBody(t, o.do(req{path: "/v1/owner/devices"}))["devices"].([]any)
	if len(listed) != 1 || listed[0].(map[string]any)["holder"].(map[string]any)["name"] != "Ana" {
		t.Fatalf("devices = %v", listed)
	}

	deleted := o.do(req{method: http.MethodDelete, path: "/v1/owner/members/" + memberID})
	expectStatus(t, deleted, http.StatusOK)
	if decodeBody(t, deleted)["revokedDevices"] != float64(1) {
		t.Fatalf("delete body = %s", deleted.Body.String())
	}
	expectError(t, e.do(req{path: "/v1/desktop/status", header: bearer(token)}), http.StatusUnauthorized, "not_authenticated")
	expectError(t, o.do(req{method: http.MethodDelete, path: "/v1/owner/members/" + memberID}), http.StatusNotFound, "not_found")
}

func TestPairingForAnotherPersonNeedsAKnownMember(t *testing.T) {
	e := newEnv(t)
	o := e.createFirstOwner("Dana")
	o.stepUp()
	expectError(t, o.do(req{method: http.MethodPost, path: "/v1/owner/pairings", body: map[string]any{"memberId": "mem9999"}}), http.StatusNotFound, "not_found")
	for _, body := range []map[string]any{
		{},
		{"self": true, "memberId": "mem0001"},
		{"memberId": "../x"},
		{"self": true, "deviceName": "bad\nname"},
	} {
		expectError(t, o.do(req{method: http.MethodPost, path: "/v1/owner/pairings", body: body}), http.StatusBadRequest, "invalid_pairing")
	}
}

func TestPairingLinkCarriesCodeAndFingerprintInTheFragment(t *testing.T) {
	e := newEnv(t, withPublicURL("https://sesame.example.net"))
	o := e.createFirstOwner("Dana")
	issued := o.pairing(true, "")
	instance := decodeBody(t, e.do(req{path: "/v1/instance"}))
	want := "https://sesame.example.net/pair#code=" + issued["code"].(string) + "&fp=" + instance["fingerprint"].(string)
	if issued["link"] != want {
		t.Fatalf("link = %v, want %s", issued["link"], want)
	}
	if _, err := time.Parse(time.RFC3339, issued["expiresAt"].(string)); err != nil {
		t.Fatal(err)
	}
	listed := decodeBody(t, o.do(req{path: "/v1/owner/pairings"}))["pairings"].([]any)
	if len(listed) != 1 {
		t.Fatalf("pairings = %v", listed)
	}
	if strings.Contains(o.do(req{path: "/v1/owner/pairings"}).Body.String(), issued["code"].(string)) {
		t.Fatal("the pairing list must not repeat the one-time code")
	}
}

func TestLinkingCanBeSwitchedOff(t *testing.T) {
	e := newEnv(t)
	o := e.createFirstOwner("Dana")
	code := o.pairing(true, "")["code"].(string)
	expectStatus(t, o.do(req{method: http.MethodPatch, path: "/v1/owner/flags/desktop_linking_enabled", body: map[string]bool{"enabled": false}}), http.StatusOK)
	expectError(t, e.link(code, "Laptop"), http.StatusServiceUnavailable, "desktop_linking_disabled")
	document := capabilityDocumentOf(t, e)
	if document["features"].(map[string]any)["desktopLinking"] != false {
		t.Fatalf("capabilities still advertise linking: %v", document)
	}
	expectStatus(t, o.do(req{method: http.MethodPatch, path: "/v1/owner/flags/desktop_linking_enabled", body: map[string]bool{"enabled": true}}), http.StatusOK)
	expectStatus(t, e.link(code, "Laptop"), http.StatusCreated)
}
