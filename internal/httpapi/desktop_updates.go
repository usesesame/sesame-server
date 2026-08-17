package httpapi

import (
	"crypto/sha256"
	"errors"
	"net/http"
	"strings"
	"time"

	"usesesame.app/backend/internal/accounts"
	adminstore "usesesame.app/backend/internal/admin"
	"usesesame.app/backend/internal/releases"
)

const desktopUpdateTicketTTL = 30 * time.Minute

func (a *api) desktopUpdate(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodGet) || !a.requireAccounts(response) {
		return
	}
	if !a.capabilityEnabled(request.Context(), "updater_enabled") {
		writeError(response, http.StatusServiceUnavailable, "updater_unavailable", "Desktop updates are temporarily unavailable.")
		return
	}
	connection, ok := a.desktopConnectionForRequest(response, request)
	if !ok {
		return
	}
	current, err := releases.ParseVersion(request.URL.Query().Get("currentVersion"))
	if err != nil {
		writeError(response, http.StatusBadRequest, "invalid_desktop_version", "The desktop version is invalid.")
		return
	}
	if connection.Platform != "" && connection.Platform != "windows" {
		a.noDesktopUpdate(response, request, "")
		return
	}
	architecture := connection.Architecture
	if architecture == "" {
		architecture = "x86_64"
	}
	registry := a.config.ReleaseRegistry
	if registry == nil {
		writeError(response, http.StatusServiceUnavailable, "updater_unavailable", "Desktop updates are temporarily unavailable.")
		return
	}
	owner, err := registry.IsOwnerReleaseRingMember(request.Context(), connection.AccountID)
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "updater_unavailable", "Desktop updates are temporarily unavailable.")
		return
	}
	candidates, err := registry.PublishedReleasesForUpdate(request.Context(), "windows", architecture, owner)
	if errors.Is(err, accounts.ErrNotFound) || errors.Is(err, adminstore.ErrNotFound) || len(candidates) == 0 {
		a.noDesktopUpdate(response, request, "beta")
		return
	}
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "updater_unavailable", "Desktop updates are temporarily unavailable.")
		return
	}
	if request.URL.Query().Get("format") == "tauri" && a.config.DesktopUpdateBaseURL == "" {
		writeError(response, http.StatusServiceUnavailable, "updater_unavailable", "Desktop update delivery is not configured.")
		return
	}
	release, found := highestEligibleDesktopRelease(candidates, current, connection.AccountID)
	if !found {
		a.noDesktopUpdate(response, request, "beta")
		return
	}
	if !distributableReleaseArtifact(release.Artifact) {
		a.noDesktopUpdate(response, request, "beta")
		return
	}
	if request.URL.Query().Get("format") == "tauri" && (release.Artifact == nil || release.Artifact.CandidatePayload == "" || release.Artifact.CandidateSigningKeyID == "" || release.Artifact.CandidateSignature == "") {
		writeError(response, http.StatusServiceUnavailable, "updater_unavailable", "Desktop update verification is not available for this release.")
		return
	}
	store, ok := a.config.Accounts.(accounts.DesktopUpdateTicketStore)
	if !ok {
		writeError(response, http.StatusServiceUnavailable, "updater_unavailable", "Desktop updates are temporarily unavailable.")
		return
	}
	ticket, tokenHash, err := accounts.NewSessionToken()
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "updater_unavailable", "Desktop updates are temporarily unavailable.")
		return
	}
	expiresAt := time.Now().UTC().Add(desktopUpdateTicketTTL)
	if _, err := store.CreateDesktopUpdateTicket(request.Context(), accounts.DesktopUpdateTicketRequest{AccountID: connection.AccountID, DeviceID: connection.DeviceID, ReleaseID: release.ID, ArtifactObjectKey: release.ArtifactObjectKey, ArtifactSHA256: release.SHA256, UpdaterSignature: release.Signature, TokenHash: tokenHash, ExpiresAt: expiresAt}); err != nil {
		writeError(response, http.StatusServiceUnavailable, "updater_unavailable", "Desktop updates are temporarily unavailable.")
		return
	}
	ticketPath := "/v1/desktop/update-tickets/" + ticket
	if request.URL.Query().Get("format") == "tauri" {
		response.Header().Set("Content-Type", "application/json; charset=utf-8")
		writeJSON(response, http.StatusOK, tauriUpdateResponse{
			Version:   release.Version,
			Notes:     "See the release notes for details.",
			Published: release.PublishedAt,
			URL:       a.config.DesktopUpdateBaseURL + ticketPath,
			Signature: release.Signature,
			CandidateReceipt: candidateReceipt{
				Payload:      release.Artifact.CandidatePayload,
				SigningKeyID: release.Artifact.CandidateSigningKeyID,
				Signature:    release.Artifact.CandidateSignature,
			},
		})
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"available": true, "channel": release.Channel, "version": release.Version, "sha256": release.SHA256, "signature": release.Signature, "ticket": ticket, "ticketPath": ticketPath, "expiresAt": expiresAt, "releaseNotesUrl": release.ReleaseNotesURL})
}

func distributableReleaseArtifact(artifact *adminstore.ReleaseArtifact) bool {
	if artifact == nil || !artifact.SigstoreVerified {
		return false
	}
	if artifact.DistributionClass == "early_access" {
		return !artifact.AuthenticodeVerified
	}
	return artifact.DistributionClass == "production" && artifact.AuthenticodeVerified
}

func highestEligibleDesktopRelease(candidates []adminstore.Release, current releases.Version, accountID string) (adminstore.Release, bool) {
	var selected adminstore.Release
	var selectedVersion releases.Version
	found := false
	for _, candidate := range candidates {
		version, err := releases.ParseVersion(candidate.Version)
		if err != nil || version.Compare(current) <= 0 || !includedInRollout(candidate.ID, accountID, candidate.RolloutPercent) {
			continue
		}
		if !found || version.Compare(selectedVersion) > 0 {
			selected, selectedVersion, found = candidate, version, true
		}
	}
	return selected, found
}

// url and signature must be top-level fields; Tauri does not parse the static-manifest shape here.
type tauriUpdateResponse struct {
	Version          string           `json:"version"`
	Notes            string           `json:"notes"`
	Published        *time.Time       `json:"pub_date,omitempty"`
	URL              string           `json:"url"`
	Signature        string           `json:"signature"`
	CandidateReceipt candidateReceipt `json:"candidateReceipt"`
}

type candidateReceipt struct {
	Payload      string `json:"payload"`
	SigningKeyID string `json:"signingKeyId"`
	Signature    string `json:"signature"`
}

func (a *api) noDesktopUpdate(response http.ResponseWriter, request *http.Request, channel string) {
	if request.URL.Query().Get("format") == "tauri" {
		response.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"available": false, "channel": channel})
}

// Server-owned cohort assignment, so rollout cannot be bypassed by changing local device state.
func includedInRollout(releaseID, accountID string, percent int) bool {
	if percent >= 100 {
		return true
	}
	if percent <= 0 || releaseID == "" || accountID == "" {
		return false
	}
	digest := sha256.Sum256([]byte("sesame-release-rollout-v1\x00" + releaseID + "\x00" + accountID))
	cohort := (uint16(digest[0])<<8 | uint16(digest[1])) % 100
	return int(cohort) < percent
}

func (a *api) redeemDesktopUpdateTicket(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodGet) || !a.requireAccounts(response) {
		return
	}
	connection, ok := a.desktopConnectionForRequest(response, request)
	if !ok {
		return
	}
	token := strings.TrimPrefix(request.URL.Path, "/v1/desktop/update-tickets/")
	if token == "" || strings.Contains(token, "/") {
		a.notFound(response, request)
		return
	}
	store, ok := a.config.Accounts.(accounts.DesktopUpdateTicketStore)
	if !ok {
		writeError(response, http.StatusServiceUnavailable, "updater_unavailable", "Desktop updates are temporarily unavailable.")
		return
	}
	ticket, err := store.RedeemDesktopUpdateTicket(request.Context(), connection.AccountID, connection.DeviceID, accounts.HashSessionToken(token), time.Now().UTC())
	if errors.Is(err, accounts.ErrTokenExpired) {
		writeError(response, http.StatusGone, "update_ticket_expired", "This update ticket has expired. Check for updates again.")
		return
	}
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "updater_unavailable", "Desktop updates are temporarily unavailable.")
		return
	}
	if a.config.ArtifactDelivery == nil || !validArtifactObjectKey(ticket.ArtifactObjectKey) {
		writeError(response, http.StatusServiceUnavailable, "updater_unavailable", "Desktop updates are temporarily unavailable.")
		return
	}
	artifactURL, err := a.config.ArtifactDelivery.SignedURL(request.Context(), ticket.ArtifactObjectKey, minArtifactURLExpiry(ticket.ExpiresAt))
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "updater_unavailable", "Desktop updates are temporarily unavailable.")
		return
	}
	response.Header().Set("X-Sesame-Artifact-SHA256", ticket.ArtifactSHA256)
	response.Header().Set("X-Sesame-Updater-Signature", ticket.UpdaterSignature)
	http.Redirect(response, request, artifactURL, http.StatusTemporaryRedirect)
}
