package httpapi

import (
	"net/http"
	"regexp"

	adminstore "usesesame.app/backend/internal/admin"
)

const (
	browserExtensionVisibleFlag = "browser_extension_visible"
	chromeStoreListingBase      = "https://chromewebstore.google.com/detail/"
)

var chromeExtensionID = regexp.MustCompile(`^[a-p]{32}$`)

type publicExtension struct {
	Store      string `json:"store"`
	Version    string `json:"version,omitempty"`
	Status     string `json:"status"`
	ListingURL string `json:"listingUrl,omitempty"`
}

func (a *api) extensionListings(response http.ResponseWriter, request *http.Request) {
	extensions := []publicExtension{}
	if a.config.Admin != nil {
		records, err := a.config.Admin.PublicExtensionListings(request.Context())
		if err != nil {
			writeError(response, http.StatusServiceUnavailable, "extensions_unavailable", "Extension status is temporarily unavailable.")
			return
		}
		visible := a.runtimeFlagBool(request.Context(), browserExtensionVisibleFlag, false)
		extensions = publicExtensions(records, visible, a.config.ChromeExtensionID)
	}
	writeJSON(response, http.StatusOK, map[string]any{"extensions": extensions})
}

// The Chrome listing is public once the visibility flag is on and the server
// holds the store id it needs to build the address. A registry record only adds
// the version, and a withdrawn one keeps the listing hidden.
func publicExtensions(records []adminstore.ExtensionListing, visible bool, chromeID string) []publicExtension {
	extensions := []publicExtension{}
	chromeAddress := extensionListingURL("chrome", chromeID)
	chromeSeen := false
	for _, record := range records {
		if record.Store == "chrome" {
			chromeSeen = true
		}
		if record.Status != "approved" && record.Status != "published" {
			continue
		}
		extension := publicExtension{Store: record.Store, Version: record.Version, Status: "approved"}
		if record.Store == "chrome" && visible && chromeAddress != "" {
			extension.Status = "published"
			extension.ListingURL = chromeAddress
		}
		extensions = append(extensions, extension)
	}
	if !chromeSeen && visible && chromeAddress != "" {
		extensions = append(extensions, publicExtension{Store: "chrome", Status: "published", ListingURL: chromeAddress})
	}
	return extensions
}

func extensionListingURL(store, chromeID string) string {
	if store == "chrome" && chromeExtensionID.MatchString(chromeID) {
		return chromeStoreListingBase + chromeID
	}
	return ""
}
