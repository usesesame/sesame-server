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
	Version    string `json:"version"`
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

// A store record counts as published for the public only when the registry says
// so, the visibility flag is on, and the server holds the store id it needs to
// build the listing address. Anything less is reported as approved.
func publicExtensions(records []adminstore.ExtensionListing, visible bool, chromeID string) []publicExtension {
	extensions := []publicExtension{}
	for _, record := range records {
		if record.Status != "approved" && record.Status != "published" {
			continue
		}
		extension := publicExtension{Store: record.Store, Version: record.Version, Status: "approved"}
		if record.Status == "published" && visible {
			if address := extensionListingURL(record.Store, chromeID); address != "" {
				extension.Status = "published"
				extension.ListingURL = address
			}
		}
		extensions = append(extensions, extension)
	}
	return extensions
}

func extensionListingURL(store, chromeID string) string {
	if store == "chrome" && chromeExtensionID.MatchString(chromeID) {
		return chromeStoreListingBase + chromeID
	}
	return ""
}
