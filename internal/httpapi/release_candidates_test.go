package httpapi

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	adminstore "usesesame.app/backend/internal/admin"
)

func TestReleaseCandidateIngest(t *testing.T) {
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize))
	token := bytes.Repeat([]byte{8}, 32)
	tokenHash := sha256.Sum256(token)
	registry := &testReleaseRegistry{}
	handler := New(Config{
		AllowedOrigin:             "https://account.example.invalid",
		AdminOrigin:               "https://admin.example.invalid",
		ReleaseRegistry:           registry,
		ReleaseCandidatePublicKey: privateKey.Public().(ed25519.PublicKey),
		ReleaseCandidateKeyID:     "test-key",
		ReleaseCandidateTokenHash: tokenHash[:],
		MinimumDesktopVersion:     "0.0.0",
		LatestDesktopVersion:      "0.0.0",
	})

	t.Run("rejects unauthorized and malformed requests", func(t *testing.T) {
		response := requestReleaseCandidate(t, handler, nil)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("missing token status = %d, want %d", response.Code, http.StatusUnauthorized)
		}
		response = httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/v1/release-candidates", strings.NewReader(`{"unsupported":true}`))
		request.Header.Set("Authorization", "Bearer "+base64.RawURLEncoding.EncodeToString(token))
		request.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid_admin_request") {
			t.Fatalf("unknown-field request = %d %s", response.Code, response.Body.String())
		}
		response = httptest.NewRecorder()
		request = httptest.NewRequest(http.MethodPost, "/v1/release-candidates", strings.NewReader(`{"vaultPassword":"fictional"}`))
		request.Header.Set("Authorization", "Bearer "+base64.RawURLEncoding.EncodeToString(token))
		request.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "vault_data_not_accepted") {
			t.Fatalf("vault-shaped request = %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("accepts signed candidates and reports conflicts", func(t *testing.T) {
		candidate := signedTestCandidate(t, privateKey)
		response := requestReleaseCandidate(t, handler, &candidate, token)
		if response.Code != http.StatusCreated {
			t.Fatalf("signed candidate status = %d: %s", response.Code, response.Body.String())
		}
		if registry.calls != 1 {
			t.Fatalf("registry calls = %d, want 1", registry.calls)
		}
		registry.err = adminstore.ErrReleaseCandidateConflict
		response = requestReleaseCandidate(t, handler, &candidate, token)
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "release_candidate_conflict") {
			t.Fatalf("conflict response = %d %s", response.Code, response.Body.String())
		}
	})
}

func requestReleaseCandidate(t *testing.T, handler http.Handler, candidate *adminstore.ReleaseCandidate, token ...[]byte) *httptest.ResponseRecorder {
	t.Helper()
	var body []byte
	if candidate != nil {
		var err error
		body, err = json.Marshal(candidate)
		if err != nil {
			t.Fatalf("marshal candidate: %v", err)
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/release-candidates", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if len(token) > 0 {
		request.Header.Set("Authorization", "Bearer "+base64.RawURLEncoding.EncodeToString(token[0]))
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func signedTestCandidate(t *testing.T, privateKey ed25519.PrivateKey) adminstore.ReleaseCandidate {
	t.Helper()
	digest := strings.Repeat("a", 64)
	bundleDigest := strings.Repeat("b", 64)
	identity := "https://github.com/usesesame/sesame-desktop/.github/workflows/release-early-access.yml@refs/tags/v0.2.3"
	candidate := adminstore.ReleaseCandidate{
		SchemaVersion:         2,
		Version:               "0.2.3",
		Channel:               "beta",
		Platform:              "windows",
		Architecture:          "x86_64",
		SupportedWindows:      "Windows 10",
		ReleaseNotesURL:       "https://example.invalid/releases/0.2.3",
		CandidateSigningKeyID: "test-key",
		Artifact: adminstore.ReleaseArtifact{
			URL:                  "https://downloads.example.invalid/Sesame.exe",
			ObjectKey:            "releases/0.2.3/Sesame.exe",
			SHA256:               digest,
			Bytes:                1,
			UpdaterSignature:     strings.Repeat("s", 64),
			UpdaterSigningKeyID:  "test-updater-key",
			DistributionClass:    "early_access",
			SigstoreVerified:     true,
			SigstoreIssuer:       "https://token.actions.githubusercontent.com",
			SigstoreIdentity:     identity,
			SigstoreBundleSHA256: bundleDigest,
			SigstoreEvidence: map[string]any{
				"schemaVersion":           1,
				"verified":                true,
				"transparencyLogVerified": true,
				"issuer":                  "https://token.actions.githubusercontent.com",
				"certificateIdentity":     identity,
				"repository":              "usesesame/sesame-desktop",
				"workflow":                ".github/workflows/release-early-access.yml",
				"ref":                     "refs/tags/v0.2.3",
				"artifactSha256":          digest,
				"artifactBundleSha256":    bundleDigest,
			},
		},
	}
	payload, ok := releaseCandidateSigningPayload(candidate)
	if !ok {
		t.Fatal("build signing payload")
	}
	candidate.CandidateSignature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, []byte(payload)))
	return candidate
}

type testReleaseRegistry struct {
	calls int
	err   error
}

func (r *testReleaseRegistry) AcceptReleaseCandidate(_ context.Context, _ adminstore.Account, candidate adminstore.ReleaseCandidate, _ string) (adminstore.Release, error) {
	r.calls++
	if r.err != nil {
		return adminstore.Release{}, r.err
	}
	return adminstore.Release{ID: "release-test", Channel: candidate.Channel, Platform: candidate.Platform, Architecture: candidate.Architecture, Version: candidate.Version}, nil
}

func (r *testReleaseRegistry) IsOwnerReleaseRingMember(context.Context, string) (bool, error) {
	return false, errors.New("not used")
}

func (r *testReleaseRegistry) LatestPublishedReleaseForChannel(context.Context, string, string, string) (adminstore.Release, error) {
	return adminstore.Release{}, errors.New("not used")
}

func (r *testReleaseRegistry) PublishedReleasesForUpdate(context.Context, string, string, bool) ([]adminstore.Release, error) {
	return nil, errors.New("not used")
}
