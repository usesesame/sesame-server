package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"usesesame.app/backend/internal/accounts"
	adminstore "usesesame.app/backend/internal/admin"
)

func TestHealthRoutesReportBuildIdentity(t *testing.T) {
	handler := New(Config{Version: "1.2.3", Commit: "0123456789abcdef0123456789abcdef01234567"})
	for _, path := range []string{"/livez", "/readyz"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d", path, response.Code)
		}
		body := response.Body.Bytes()
		if !bytes.Contains(body, []byte(`"version":"1.2.3"`)) || !bytes.Contains(body, []byte(`"commit":"0123456789abcdef0123456789abcdef01234567"`)) {
			t.Fatalf("%s identity = %s", path, body)
		}
	}
}

func TestLatestReleaseMessageMatchesTheArtifactEvidence(t *testing.T) {
	databaseURL := os.Getenv("SESAME_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("SESAME_TEST_DATABASE_URL is required")
	}
	ctx := context.Background()
	accountStore, err := accounts.Open(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open account store: %v", err)
	}
	t.Cleanup(func() { _ = accountStore.Close() })
	lockDatabaseTests(t, accountStore.DB())
	if _, err := accountStore.DB().ExecContext(ctx, `TRUNCATE sesame_releases, sesame_release_artifacts, sesame_feature_flags RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("clear release tables: %v", err)
	}
	adminStore, err := adminstore.Open(ctx, databaseURL, bytes.Repeat([]byte{2}, 32))
	if err != nil {
		t.Fatalf("open admin store: %v", err)
	}
	t.Cleanup(func() { _ = adminStore.Close() })
	if _, err := accountStore.DB().ExecContext(ctx, `INSERT INTO sesame_feature_flags (key, value) VALUES ('public_download', 'true') ON CONFLICT (key) DO UPDATE SET value = 'true'`); err != nil {
		t.Fatalf("enable public download: %v", err)
	}
	if _, err := accountStore.DB().ExecContext(ctx, `INSERT INTO sesame_releases (id, channel, platform, architecture, version, download_url, artifact_object_key, sha256, signature, signing_key_id, supported_windows, release_notes_url, rollback_notice, status, rollout_percent, update_enabled, kill_switch, release_set_digest, release_set_verified_at, published_at) VALUES ('release-linux-test', 'beta', 'linux', 'x86_64', '0.2.5', 'https://downloads.example.invalid/Sesame.AppImage', 'linux/0.2.5/appimage', repeat('a', 64), '', '', '', 'https://example.invalid/releases/0.2.5', '', 'published', 100, TRUE, FALSE, repeat('b', 64), NOW(), NOW())`); err != nil {
		t.Fatalf("create Linux release: %v", err)
	}
	for _, format := range []string{"appimage", "deb", "rpm"} {
		if _, err := accountStore.DB().ExecContext(ctx, `INSERT INTO sesame_release_artifacts (id, release_id, format, architecture, artifact_url, artifact_object_key, artifact_sha256, artifact_bytes, updater_capable, updater_signature, updater_signing_key_id, distribution_class, sigstore_evidence, sigstore_verified, sigstore_issuer, sigstore_identity, sigstore_bundle_sha256) VALUES ($1, 'release-linux-test', $2, 'x86_64', 'https://downloads.example.invalid/0.2.5/' || $2, 'linux/0.2.5/' || $2, repeat('a', 64), 1, FALSE, '', '', 'early_access', '{"verified":true}', TRUE, 'https://token.actions.githubusercontent.com', 'linux-test-identity', repeat('c', 64))`, "artifact-"+format, format); err != nil {
			t.Fatalf("create Linux artifact %s: %v", format, err)
		}
	}
	handler := New(Config{Admin: adminStore})

	request := httptest.NewRequest(http.MethodGet, "/v1/releases/latest?platform=linux", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("latest Linux release status = %d: %s", response.Code, response.Body.String())
	}
	var payload struct {
		Available bool   `json:"available"`
		Signed    bool   `json:"signed"`
		Message   string `json:"message"`
		Version   string `json:"version"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode latest Linux release: %v", err)
	}
	if !payload.Available || payload.Version != "0.2.5" {
		t.Fatalf("latest Linux release = %+v, want available 0.2.5", payload)
	}
	if payload.Signed {
		t.Fatal("Linux package reports an updater signature it does not carry")
	}
	if strings.Contains(payload.Message, "updater signature") && !strings.Contains(payload.Message, "no updater signature") {
		t.Fatalf("Linux message claims an updater signature: %q", payload.Message)
	}

	windowsRequest := httptest.NewRequest(http.MethodGet, "/v1/releases/latest?platform=windows", nil)
	windowsResponse := httptest.NewRecorder()
	handler.ServeHTTP(windowsResponse, windowsRequest)
	if windowsResponse.Code != http.StatusOK {
		t.Fatalf("latest Windows release status = %d: %s", windowsResponse.Code, windowsResponse.Body.String())
	}
	var windowsPayload struct {
		Available bool   `json:"available"`
		Message   string `json:"message"`
	}
	if err := json.Unmarshal(windowsResponse.Body.Bytes(), &windowsPayload); err != nil {
		t.Fatalf("decode latest Windows release: %v", err)
	}
	if windowsPayload.Available {
		t.Fatal("windows release should stay unavailable without a published row")
	}
}
