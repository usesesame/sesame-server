package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"usesesame.app/backend/internal/activity"
)

type fixedActivity struct {
	snapshot activity.Snapshot
	ok       bool
}

func (f fixedActivity) Snapshot() (activity.Snapshot, bool) {
	return f.snapshot, f.ok
}

func TestProjectActivityServesTheCachedSnapshot(t *testing.T) {
	snapshot := activity.Snapshot{
		GeneratedAt: time.Date(2026, 10, 4, 10, 0, 0, 0, time.UTC),
		WindowDays:  30,
		Repositories: []activity.Repository{
			{Name: "sesame-desktop", PushedAt: time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC), RecentCommits: 467},
		},
	}
	handler := New(Config{
		DeploymentProfile: DeploymentProfileProject,
		PublicSiteOrigin:  "https://site.example.invalid",
		ProjectActivity:   fixedActivity{snapshot: snapshot, ok: true},
	})
	request := httptest.NewRequest(http.MethodGet, "/v1/project/activity", nil)
	request.Header.Set("Origin", "https://site.example.invalid")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d %s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Access-Control-Allow-Origin"); got != "https://site.example.invalid" {
		t.Fatalf("Access-Control-Allow-Origin = %q", got)
	}
	if response.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Fatal("the public site read allows credentials")
	}
	if got := response.Header().Get("Cache-Control"); got != "public, max-age=300" {
		t.Fatalf("Cache-Control = %q", got)
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["generatedAt"] != "2026-10-04T10:00:00Z" || body["windowDays"] != float64(30) {
		t.Fatalf("body = %s", response.Body.String())
	}
	repositories := body["repositories"].([]any)
	first := repositories[0].(map[string]any)
	if len(repositories) != 1 || first["name"] != "sesame-desktop" || first["recentCommits"] != float64(467) || first["pushedAt"] != "2026-10-04T09:00:00Z" || len(first) != 3 {
		t.Fatalf("repositories = %v", repositories)
	}
}

func TestProjectActivityIsUnavailableUntilCounted(t *testing.T) {
	for name, source := range map[string]ProjectActivitySource{"no source": nil, "not counted": fixedActivity{}} {
		handler := New(Config{DeploymentProfile: DeploymentProfileProject, ProjectActivity: source})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/project/activity", nil))
		if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "project_activity_unavailable") {
			t.Fatalf("%s: %d %s", name, response.Code, response.Body.String())
		}
		if got := response.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("%s: Cache-Control = %q", name, got)
		}
	}
}

func TestProjectActivityRefusesMutation(t *testing.T) {
	handler := New(Config{DeploymentProfile: DeploymentProfileProject, ProjectActivity: fixedActivity{ok: true}})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/project/activity", strings.NewReader("{}")))
	if response.Code == http.StatusOK {
		t.Fatal("POST was served")
	}
}
