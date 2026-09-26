package httpapi

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"usesesame.app/backend/internal/accounts"
	adminstore "usesesame.app/backend/internal/admin"
)

var projectOnlyRoutes = []struct {
	method string
	path   string
}{
	{http.MethodGet, "/v1/admin/releases"},
	{http.MethodPost, "/v1/admin/releases/release-test/publish"},
	{http.MethodPost, "/v1/admin/releases/release-test/rollout"},
	{http.MethodPost, "/v1/admin/releases/release-test/emergency-stop"},
	{http.MethodPost, "/v1/admin/releases/release-test/withdraw"},
	{http.MethodGet, "/v1/admin/extension-publications"},
	{http.MethodPost, "/v1/admin/extension-publications"},
	{http.MethodPost, "/v1/admin/extension-publications/publication-test/transition"},
	{http.MethodGet, "/v1/admin/plans"},
	{http.MethodPatch, "/v1/admin/plans/free"},
	{http.MethodPost, "/v1/admin/users/account-test/owner-release"},
	{http.MethodDelete, "/v1/admin/users/account-test/owner-release"},
	{http.MethodPost, "/v1/release-candidates"},
}

func TestOperatorProfileDoesNotRegisterProjectOnlyRoutes(t *testing.T) {
	operator := New(Config{AdminOrigin: "https://admin.example.invalid"})
	for _, route := range projectOnlyRoutes {
		response := httptest.NewRecorder()
		operator.ServeHTTP(response, httptest.NewRequest(route.method, route.path, nil))
		if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), "not_found") {
			t.Fatalf("operator %s %s = %d %s, want the standard not-found response", route.method, route.path, response.Code, response.Body.String())
		}
	}
	project := New(Config{DeploymentProfile: DeploymentProfileProject, AdminOrigin: "https://admin.example.invalid"})
	for _, route := range projectOnlyRoutes {
		response := httptest.NewRecorder()
		project.ServeHTTP(response, httptest.NewRequest(route.method, route.path, nil))
		if response.Code == http.StatusNotFound {
			t.Fatalf("project %s %s = 404; the route must stay registered", route.method, route.path)
		}
	}
}

func TestOperatorProfileServesNoSesameCatalog(t *testing.T) {
	operator := New(Config{})

	plans := httptest.NewRecorder()
	operator.ServeHTTP(plans, httptest.NewRequest(http.MethodGet, "/v1/plans", nil))
	if plans.Code != http.StatusOK {
		t.Fatalf("operator plans status = %d: %s", plans.Code, plans.Body.String())
	}
	var planPayload struct {
		Plans []json.RawMessage `json:"plans"`
	}
	if err := json.Unmarshal(plans.Body.Bytes(), &planPayload); err != nil {
		t.Fatalf("decode operator plans: %v", err)
	}
	if len(planPayload.Plans) != 0 {
		t.Fatalf("operator plans = %s, want an empty list", plans.Body.String())
	}

	status := httptest.NewRecorder()
	operator.ServeHTTP(status, httptest.NewRequest(http.MethodGet, "/v1/product/status", nil))
	if status.Code != http.StatusOK {
		t.Fatalf("operator product status = %d: %s", status.Code, status.Body.String())
	}
	var statusPayload map[string]any
	if err := json.Unmarshal(status.Body.Bytes(), &statusPayload); err != nil {
		t.Fatalf("decode operator product status: %v", err)
	}
	for _, forbidden := range []string{"phase", "platforms", "accountRequired", "accountPurposes", "publicDownload"} {
		if _, present := statusPayload[forbidden]; present {
			t.Fatalf("operator product status exposed %q: %s", forbidden, status.Body.String())
		}
	}
	if strings.Contains(status.Body.String(), "Sesame Sync") || strings.Contains(status.Body.String(), "windows") {
		t.Fatalf("operator product status carried Sesame copy: %s", status.Body.String())
	}
	for _, required := range []string{"webSignInAvailable", "desktopConnectionAvailable", "registrationMode", "cloudSyncAvailable", "updated"} {
		if _, present := statusPayload[required]; !present {
			t.Fatalf("operator product status is missing %q: %s", required, status.Body.String())
		}
	}

	project := New(Config{DeploymentProfile: DeploymentProfileProject})
	projectPlans := httptest.NewRecorder()
	project.ServeHTTP(projectPlans, httptest.NewRequest(http.MethodGet, "/v1/plans", nil))
	if projectPlans.Code != http.StatusOK || !strings.Contains(projectPlans.Body.String(), "Sesame Sync") {
		t.Fatalf("project plans = %d %s, want the Sesame catalog", projectPlans.Code, projectPlans.Body.String())
	}
}

func TestOperatorProfileServesOneArtifactUnavailableResponse(t *testing.T) {
	handler := New(Config{
		AllowedOrigin:   "https://account.example.invalid",
		AdminOrigin:     "https://admin.example.invalid",
		SessionSecure:   false,
		AdminIPPepper:   "test-pepper",
		CapabilityTTL:   time.Minute,
		SessionDuration: time.Hour,
	})
	routes := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/v1/releases/latest"},
		{http.MethodGet, "/v1/desktop/updates"},
		{http.MethodGet, "/v1/desktop/update-tickets/fictional-ticket-value"},
		{http.MethodGet, "/v1/account/downloads"},
		{http.MethodPost, "/v1/account/download-tickets"},
		{http.MethodGet, "/v1/downloads/fictional-ticket-value"},
	}
	for _, route := range routes {
		request := httptest.NewRequest(route.method, route.path, nil)
		request.Header.Set("Origin", "https://account.example.invalid")
		request.Header.Set("X-Sesame-CSRF", "test-csrf")
		request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "test-csrf"})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), releaseArtifactsUnavailableCode) {
			t.Fatalf("operator %s %s = %d %s, want 503 %s", route.method, route.path, response.Code, response.Body.String(), releaseArtifactsUnavailableCode)
		}
	}
}

func TestOperatorProfileHidesArtifactFlags(t *testing.T) {
	operator := &api{config: Config{}}
	for _, key := range []string{"updater_enabled", "public_download"} {
		if operator.validFeatureFlag(key, "true") {
			t.Fatalf("operator accepted %s", key)
		}
	}
	if !operator.validFeatureFlag("cloud_sync_available", "true") {
		t.Fatal("operator refused an unrelated flag")
	}
	project := &api{config: Config{DeploymentProfile: DeploymentProfileProject}}
	if !project.validFeatureFlag("updater_enabled", "true") {
		t.Fatal("project refused updater_enabled")
	}

	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{4}, ed25519.SeedSize))
	features := func(handler http.Handler) map[string]bool {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/capabilities", nil))
		if response.Code != http.StatusOK {
			t.Fatalf("capabilities status = %d: %s", response.Code, response.Body.String())
		}
		var envelope struct {
			Payload string `json:"payload"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("decode capability envelope: %v", err)
		}
		payload, err := base64.RawURLEncoding.DecodeString(envelope.Payload)
		if err != nil {
			t.Fatalf("decode capability payload: %v", err)
		}
		var document struct {
			Features map[string]bool `json:"features"`
		}
		if err := json.Unmarshal(payload, &document); err != nil {
			t.Fatalf("decode capability document: %v", err)
		}
		return document.Features
	}
	operatorFeatures := features(New(Config{CapabilitySigningKey: key}))
	if operatorFeatures["downloads"] || operatorFeatures["updater"] {
		t.Fatalf("operator capabilities advertise artifact features: %v", operatorFeatures)
	}
	projectFeatures := features(New(Config{CapabilitySigningKey: key, DeploymentProfile: DeploymentProfileProject}))
	if !projectFeatures["downloads"] || !projectFeatures["updater"] {
		t.Fatalf("project capabilities hid artifact features: %v", projectFeatures)
	}
}

func TestOperatorProfileRefusesPublishingRoutesForASuperAdministrator(t *testing.T) {
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
	if _, err := accountStore.DB().ExecContext(ctx, `TRUNCATE sesame_admin_sessions, sesame_admin_accounts RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("clear admin tables: %v", err)
	}
	adminStore, err := adminstore.Open(ctx, databaseURL, bytes.Repeat([]byte{5}, 32))
	if err != nil {
		t.Fatalf("open admin store: %v", err)
	}
	t.Cleanup(func() { _ = adminStore.Close() })
	if _, err := accountStore.DB().ExecContext(ctx, `INSERT INTO sesame_admin_accounts (id, email, password_hash, role, totp_verified) VALUES ('super-profile', 'super@example.invalid', 'test', 'super', TRUE)`); err != nil {
		t.Fatalf("create super administrator: %v", err)
	}
	actor := adminstore.Account{ID: "super-profile", Email: "super@example.invalid", Role: adminstore.RoleSuper}
	operator := New(Config{Admin: adminStore, AdminOrigin: "https://admin.example.invalid"})

	for _, route := range projectOnlyRoutes {
		response := adminProfileRequest(t, operator, adminStore, actor, route.method, route.path, "")
		if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), "not_found") {
			t.Fatalf("operator super %s %s = %d %s, want the standard not-found response", route.method, route.path, response.Code, response.Body.String())
		}
	}

	operatorFlags := adminProfileRequest(t, operator, adminStore, actor, http.MethodGet, "/v1/admin/flags", "")
	if operatorFlags.Code != http.StatusOK || strings.Contains(operatorFlags.Body.String(), "updater_enabled") || strings.Contains(operatorFlags.Body.String(), "public_download") {
		t.Fatalf("operator flags = %d %s, want the artifact flags hidden", operatorFlags.Code, operatorFlags.Body.String())
	}
	for _, key := range []string{"updater_enabled", "public_download"} {
		response := adminProfileRequest(t, operator, adminStore, actor, http.MethodPatch, "/v1/admin/flags/"+key, `{"value":"true"}`)
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid_feature_flag") {
			t.Fatalf("operator PATCH %s = %d %s, want 400 invalid_feature_flag", key, response.Code, response.Body.String())
		}
	}
	operatorConfig := adminProfileRequest(t, operator, adminStore, actor, http.MethodGet, "/v1/admin/system/config", "")
	if operatorConfig.Code != http.StatusOK || !strings.Contains(operatorConfig.Body.String(), `"deploymentProfile":"operator"`) || strings.Contains(operatorConfig.Body.String(), "updater_enabled") {
		t.Fatalf("operator system config = %d %s", operatorConfig.Code, operatorConfig.Body.String())
	}

	project := New(Config{Admin: adminStore, AdminOrigin: "https://admin.example.invalid", DeploymentProfile: DeploymentProfileProject})
	projectReleases := adminProfileRequest(t, project, adminStore, actor, http.MethodGet, "/v1/admin/releases", "")
	if projectReleases.Code != http.StatusOK {
		t.Fatalf("project releases = %d %s", projectReleases.Code, projectReleases.Body.String())
	}
	projectMe := adminProfileRequest(t, project, adminStore, actor, http.MethodGet, "/v1/admin/auth/me", "")
	if projectMe.Code != http.StatusOK || !strings.Contains(projectMe.Body.String(), `"deploymentProfile":"project"`) {
		t.Fatalf("project me = %d %s", projectMe.Code, projectMe.Body.String())
	}
	projectFlags := adminProfileRequest(t, project, adminStore, actor, http.MethodGet, "/v1/admin/flags", "")
	if projectFlags.Code != http.StatusOK || !strings.Contains(projectFlags.Body.String(), "updater_enabled") {
		t.Fatalf("project flags = %d %s, want the artifact flags visible", projectFlags.Code, projectFlags.Body.String())
	}
	for _, key := range []string{"updater_enabled", "public_download"} {
		response := adminProfileRequest(t, project, adminStore, actor, http.MethodPatch, "/v1/admin/flags/"+key, `{"value":"true"}`)
		if response.Code != http.StatusNoContent {
			t.Fatalf("project PATCH %s = %d %s, want 204", key, response.Code, response.Body.String())
		}
	}
	projectConfig := adminProfileRequest(t, project, adminStore, actor, http.MethodGet, "/v1/admin/system/config", "")
	if projectConfig.Code != http.StatusOK || !strings.Contains(projectConfig.Body.String(), `"deploymentProfile":"project"`) || !strings.Contains(projectConfig.Body.String(), "updater_enabled") {
		t.Fatalf("project system config = %d %s", projectConfig.Code, projectConfig.Body.String())
	}
}

func adminProfileRequest(t *testing.T, handler http.Handler, store *adminstore.Store, actor adminstore.Account, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	token := fmt.Sprintf("profile-session-%s-%d", actor.ID, time.Now().UnixNano())
	if err := store.CreateSession(context.Background(), actor, adminstore.HashToken(token), "", "test", time.Now().UTC().Add(time.Hour), time.Now().UTC().UnixNano()); err != nil {
		t.Fatalf("create admin session: %v", err)
	}
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, path, reader)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Origin", "https://admin.example.invalid")
	request.Header.Set("X-Sesame-CSRF", "test-csrf")
	request.AddCookie(&http.Cookie{Name: adminSessionCookie, Value: token})
	request.AddCookie(&http.Cookie{Name: adminCSRFCookie, Value: "test-csrf"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
