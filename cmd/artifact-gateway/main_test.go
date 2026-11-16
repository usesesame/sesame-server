package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

var testSigningKey = func() []byte {
	key, err := decodeKey(testKeyRaw)
	if err != nil {
		panic(err)
	}
	return key
}()

const testKeyRaw = "YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE"

func signedPath(objectKey string, expires int64) string {
	mac := hmac.New(sha256.New, testSigningKey)
	mac.Write([]byte(macPrefix + objectKey + "\n" + strconv.FormatInt(expires, 10)))
	return "/" + objectKey + "?expires=" + strconv.FormatInt(expires, 10) + "&signature=" + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func gatewayFor(t *testing.T, key string, files map[string]string) (http.Handler, string) {
	t.Helper()
	root := t.TempDir()
	for name, bytes := range files {
		full := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(bytes), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	handler, ready, err := handler(key, root)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if ready != (strings.TrimSpace(key) != "") {
		t.Fatalf("ready = %v, want %v", ready, strings.TrimSpace(key) != "")
	}
	return handler, root
}

func doGet(handler http.Handler, target string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "http://gateway"+target, nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestServesSignedUnexpiredRequests(t *testing.T) {
	expires := time.Now().Unix() + 300
	handler, _ := gatewayFor(t, testKeyRaw, map[string]string{"releases/0.2.5/app.exe": "package-bytes"})
	response := doGet(handler, signedPath("releases/0.2.5/app.exe", expires))
	if response.Code != http.StatusOK {
		t.Fatalf("signed request status = %d, want 200", response.Code)
	}
	if body := response.Body.String(); body != "package-bytes" {
		t.Fatalf("body = %q", body)
	}
	head := httptest.NewRequest(http.MethodHead, "http://gateway"+signedPath("releases/0.2.5/app.exe", expires), nil)
	headRecorder := httptest.NewRecorder()
	handler.ServeHTTP(headRecorder, head)
	if headRecorder.Code != http.StatusOK {
		t.Fatalf("head request status = %d, want 200", headRecorder.Code)
	}
}

func TestRefusalMatrix(t *testing.T) {
	expires := time.Now().Unix() + 300
	expired := time.Now().Unix() - 10
	cases := []struct {
		name       string
		target     string
		statusCode int
	}{
		{"unsigned", "/releases/x.exe?expires=" + strconv.FormatInt(expires, 10), http.StatusForbidden},
		{"expired", "/releases/x.exe?expires=" + strconv.FormatInt(expired, 10) + "&signature=" + signedPath("releases/x.exe", expired)[strings.Index(signedPath("releases/x.exe", expired), "&signature=")+len("&signature="):], http.StatusForbidden},
		{"tampered signature", "/releases/x.exe?expires=" + strconv.FormatInt(expires, 10) + "&signature=" + strings.Repeat("A", 43), http.StatusForbidden},
		{"traversal", "/../etc/passwd", http.StatusNotFound},
		{"absolute traversal", "/releases/../../etc/passwd", http.StatusNotFound},
		{"double slash", "//releases/x.exe", http.StatusNotFound},
		{"oversized key", "/" + strings.Repeat("a", 1025), http.StatusNotFound},
		{"illegal characters", "/releases/with%20space.exe", http.StatusNotFound},
		{"missing file", signedPath("releases/missing.exe", expires), http.StatusNotFound},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			handler, _ := gatewayFor(t, testKeyRaw, map[string]string{"releases/x.exe": "package-bytes"})
			response := doGet(handler, item.target)
			if response.Code != item.statusCode {
				t.Fatalf("status = %d, want %d", response.Code, item.statusCode)
			}
		})
	}
}

func TestDisabledGatewayRefusesArtifactsAndServesLivez(t *testing.T) {
	artifact, _, err := handler("", "")
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	handler := livezHandler(false, artifact)
	response := doGet(handler, "/releases/x.exe?expires=1&signature=x")
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled artifact status = %d, want 503", response.Code)
	}
	live := doGet(handler, "/livez")
	if live.Code != http.StatusOK {
		t.Fatalf("livez status = %d, want 200", live.Code)
	}
}

func TestRejectsSymlinkedObjectKeys(t *testing.T) {
	handler, root := gatewayFor(t, testKeyRaw, map[string]string{"releases/x.exe": "package-bytes"})
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "releases", "link.exe")); err != nil {
		t.Fatal(err)
	}
	response := doGet(handler, signedPath("releases/link.exe", time.Now().Unix()+300))
	if response.Code != http.StatusNotFound {
		t.Fatalf("symlink status = %d, want 404", response.Code)
	}
}

func TestMethodRestriction(t *testing.T) {
	handler, _ := gatewayFor(t, testKeyRaw, map[string]string{"releases/x.exe": "package-bytes"})
	request := httptest.NewRequest(http.MethodPost, "http://gateway/releases/x.exe", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("post status = %d, want 405", recorder.Code)
	}
}

func TestDecodeKey(t *testing.T) {
	key, err := decodeKey(testKeyRaw)
	if err != nil || len(key) != 32 {
		t.Fatalf("decode valid key: len=%d err=%v", len(key), err)
	}
	if _, err := decodeKey("short"); err == nil {
		t.Fatal("a short key must be rejected")
	}
	if _, err := decodeKey("!!!not-base64url!!!"); err == nil {
		t.Fatal("a malformed key must be rejected")
	}
}

func TestValidObjectKey(t *testing.T) {
	cases := map[string]bool{
		"releases/0.2.5/Sesame.exe": true,
		"a":                         true,
		"":                          false,
		"/releases/x.exe":           false,
		"releases//x.exe":           false,
		"releases/../x.exe":         false,
		"releases/./x.exe":          false,
		"releases/x.exe ":           false,
		"releases/x.exe\n":          false,
	}
	for value, want := range cases {
		if got := validObjectKey(value); got != want {
			t.Errorf("validObjectKey(%q) = %v, want %v", value, got, want)
		}
	}
}

func TestDownloadWriteTimeScalesWithTheFileSize(t *testing.T) {
	cases := []struct {
		size int64
		want time.Duration
	}{
		{-1, minimumDownloadWriteTime},
		{0, minimumDownloadWriteTime},
		{1 << 20, minimumDownloadWriteTime},
		{80 << 20, 640 * time.Second},
		{200 << 20, 1600 * time.Second},
		{300 << 20, maximumDownloadWriteTime},
		{1 << 40, maximumDownloadWriteTime},
		{1<<63 - 1, maximumDownloadWriteTime},
	}
	for _, tc := range cases {
		if got := downloadWriteTime(tc.size); got != tc.want {
			t.Errorf("downloadWriteTime(%d) = %s, want %s", tc.size, got, tc.want)
		}
	}
}

func TestServerKeepsTheShortWriteTimeout(t *testing.T) {
	server := newServer("127.0.0.1:0", http.NotFoundHandler())
	if server.WriteTimeout != 30*time.Second || server.ReadTimeout != 10*time.Second || server.ReadHeaderTimeout != 5*time.Second {
		t.Fatalf("server timeouts = write %s read %s header %s", server.WriteTimeout, server.ReadTimeout, server.ReadHeaderTimeout)
	}
}

func TestSlowClientReceivesTheWholeFile(t *testing.T) {
	size := 32 << 20
	gateway, _ := gatewayFor(t, testKeyRaw, map[string]string{"desktop/installer.bin": strings.Repeat("s", size)})
	server := httptest.NewUnstartedServer(gateway)
	server.Config.WriteTimeout = 100 * time.Millisecond
	server.Start()
	t.Cleanup(server.Close)

	response, err := http.Get(server.URL + signedPath("desktop/installer.bin", time.Now().Add(time.Hour).Unix()))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	first := make([]byte, 1024)
	if _, err := io.ReadFull(response.Body, first); err != nil {
		t.Fatalf("read the first bytes: %v", err)
	}
	time.Sleep(500 * time.Millisecond)
	rest, err := io.Copy(io.Discard, response.Body)
	if err != nil || int(rest)+len(first) != size {
		t.Fatalf("read %d of %d bytes, error %v", int(rest)+len(first), size, err)
	}
}

type deadlineRecorder struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
}

func (r *deadlineRecorder) SetWriteDeadline(deadline time.Time) error {
	r.deadlines = append(r.deadlines, deadline)
	return nil
}

func TestOnlyAServedFileExtendsTheWriteDeadline(t *testing.T) {
	gateway, root := gatewayFor(t, testKeyRaw, map[string]string{"desktop/installer.bin": "fictional installer"})
	if err := os.Symlink(filepath.Join(root, "desktop", "installer.bin"), filepath.Join(root, "desktop", "linked.bin")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	valid := time.Now().Add(time.Hour).Unix()
	refused := []struct {
		method string
		target string
	}{
		{http.MethodGet, "/desktop/installer.bin"},
		{http.MethodGet, "/desktop/installer.bin?expires=1&signature=invalid"},
		{http.MethodGet, signedPath("desktop/installer.bin", time.Now().Add(-time.Minute).Unix())},
		{http.MethodGet, signedPath("desktop/missing.bin", valid)},
		{http.MethodGet, signedPath("desktop/linked.bin", valid)},
		{http.MethodGet, signedPath("desktop", valid)},
		{http.MethodGet, "/desktop/../secret.bin?expires=1&signature=x"},
		{http.MethodPost, signedPath("desktop/installer.bin", valid)},
	}
	for _, tc := range refused {
		recorder := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
		gateway.ServeHTTP(recorder, httptest.NewRequest(tc.method, "http://gateway"+tc.target, nil))
		if recorder.Code == http.StatusOK {
			t.Fatalf("%s %s was served", tc.method, tc.target)
		}
		if len(recorder.deadlines) != 0 {
			t.Fatalf("%s %s extended the write deadline before refusing", tc.method, tc.target)
		}
	}
	recorder := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	before := time.Now()
	gateway.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://gateway"+signedPath("desktop/installer.bin", valid), nil))
	if recorder.Code != http.StatusOK || len(recorder.deadlines) != 1 {
		t.Fatalf("status %d with %d deadline changes, want 200 and one", recorder.Code, len(recorder.deadlines))
	}
	if extended := recorder.deadlines[0].Sub(before); extended < minimumDownloadWriteTime || extended > minimumDownloadWriteTime+time.Minute {
		t.Fatalf("write deadline extended by %s, want about %s", extended, minimumDownloadWriteTime)
	}
}
