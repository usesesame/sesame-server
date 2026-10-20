package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	adminstore "usesesame.app/backend/internal/admin"
)

const testChromeExtensionID = "abcdefghijklmnopabcdefghijklmnop"

func TestPublicExtensionsPublishChromeOnceTheFlagAndStoreIDAreSet(t *testing.T) {
	published := []adminstore.ExtensionListing{{Store: "chrome", Version: "0.2.0", Status: "published"}}
	approved := []adminstore.ExtensionListing{{Store: "chrome", Version: "0.2.0", Status: "approved"}}
	cases := []struct {
		name    string
		records []adminstore.ExtensionListing
		visible bool
		id      string
		status  string
		address string
	}{
		{"published, visible and linked", published, true, testChromeExtensionID, "published", chromeStoreListingBase + testChromeExtensionID},
		{"approved, visible and linked", approved, true, testChromeExtensionID, "published", chromeStoreListingBase + testChromeExtensionID},
		{"published but hidden", published, false, testChromeExtensionID, "approved", ""},
		{"published and visible without a store id", published, true, "", "approved", ""},
		{"published with a malformed store id", published, true, "../evil", "approved", ""},
		{"published with an uppercase store id", published, true, strings.ToUpper(testChromeExtensionID), "approved", ""},
	}
	for _, test := range cases {
		got := publicExtensions(test.records, test.visible, test.id)
		if len(got) != 1 || got[0].Status != test.status || got[0].ListingURL != test.address || got[0].Version != "0.2.0" {
			t.Fatalf("%s: got %+v, want status %q and address %q", test.name, got, test.status, test.address)
		}
	}
}

func TestPublicExtensionsPublishChromeWithoutARegistryRecord(t *testing.T) {
	got := publicExtensions(nil, true, testChromeExtensionID)
	if len(got) != 1 || got[0].Store != "chrome" || got[0].Status != "published" || got[0].ListingURL != chromeStoreListingBase+testChromeExtensionID || got[0].Version != "" {
		t.Fatalf("got %+v, want one published chrome entry with the listing and no version", got)
	}
	for _, test := range []struct {
		name    string
		visible bool
		id      string
	}{
		{"hidden", false, testChromeExtensionID},
		{"no store id", true, ""},
		{"malformed store id", true, "https://example.invalid/x"},
	} {
		if got := publicExtensions(nil, test.visible, test.id); len(got) != 0 {
			t.Fatalf("%s: got %+v, want nothing", test.name, got)
		}
	}
}

func TestPublicExtensionsOmitWithdrawnAndUnknownStoreAddresses(t *testing.T) {
	got := publicExtensions([]adminstore.ExtensionListing{
		{Store: "chrome", Version: "0.1.0", Status: "withdrawn"},
		{Store: "firefox", Version: "0.2.0", Status: "published"},
	}, true, testChromeExtensionID)
	if len(got) != 1 || got[0].Store != "firefox" || got[0].Status != "approved" || got[0].ListingURL != "" {
		t.Fatalf("got %+v, want only the firefox record reported as approved with no address", got)
	}
}

func TestExtensionsRouteReturnsAnEmptyListWithoutAnAdminStore(t *testing.T) {
	project := New(Config{DeploymentProfile: DeploymentProfileProject, AdminOrigin: "https://admin.example.invalid"})
	response := httptest.NewRecorder()
	project.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/extensions", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"extensions":[]`) {
		t.Fatalf("GET /v1/extensions = %d %s, want an empty list", response.Code, response.Body.String())
	}
}

func TestVisibilityFlagIsAnAllowedAdminFlagOnlyInTheProjectProfile(t *testing.T) {
	project := &api{config: Config{DeploymentProfile: DeploymentProfileProject}}
	operator := &api{config: Config{}}
	if !project.validFeatureFlag(browserExtensionVisibleFlag, "true") || project.validFeatureFlag(browserExtensionVisibleFlag, "yes") {
		t.Fatal("the project profile must accept true or false and nothing else")
	}
	if operator.validFeatureFlag(browserExtensionVisibleFlag, "true") {
		t.Fatal("the operator profile must not accept the extension visibility flag")
	}
}
