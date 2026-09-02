package httpapi

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	adminstore "usesesame.app/backend/internal/admin"
	"usesesame.app/backend/internal/releases"
)

func (a *api) releaseCandidateIngest(response http.ResponseWriter, request *http.Request) {
	if !a.validReleaseCandidateToken(request) {
		writeError(response, http.StatusUnauthorized, "release_pipeline_unauthorized", "This endpoint accepts only the configured release pipeline credential.")
		return
	}
	if len(a.config.ReleaseCandidatePublicKey) != ed25519.PublicKeySize || a.config.ReleaseCandidateKeyID == "" || len(a.config.ReleaseCandidateTokenHash) != sha256.Size {
		writeError(response, http.StatusServiceUnavailable, "release_candidate_verification_unavailable", "Release candidate verification is not configured.")
		return
	}
	var candidate adminstore.ReleaseCandidate
	if !decodeAdminJSON(response, request, &candidate) {
		return
	}
	if reason := releaseCandidateValidationError(candidate); reason != "" {
		slog.Warn("release candidate validation failed", "reason", reason)
		writeError(response, http.StatusBadRequest, "invalid_release_candidate", "This release candidate did not pass cryptographic verification.")
		return
	}
	if !a.verifyReleaseCandidate(candidate) {
		slog.Warn("release candidate signature verification failed")
		writeError(response, http.StatusBadRequest, "invalid_release_candidate", "This release candidate did not pass cryptographic verification.")
		return
	}
	candidate.SigningPayload, _ = releaseCandidateSigningPayload(candidate)
	if a.config.ReleaseRegistry == nil {
		writeError(response, http.StatusServiceUnavailable, "release_candidate_verification_unavailable", "Release candidate storage is not configured.")
		return
	}
	release, err := a.config.ReleaseRegistry.AcceptReleaseCandidate(request.Context(), adminstore.Account{Email: "release-pipeline"}, candidate, a.adminIPHash(request))
	if err != nil {
		if errors.Is(err, adminstore.ErrReleaseCandidateConflict) {
			writeError(response, http.StatusConflict, "release_candidate_conflict", "This release tuple is already bound to different signed evidence.")
			return
		}
		adminStoreError(response, err)
		return
	}
	writeJSON(response, http.StatusCreated, map[string]any{"release": release})
}

func (a *api) validReleaseCandidateToken(request *http.Request) bool {
	const prefix = "Bearer "
	value := strings.TrimSpace(request.Header.Get("Authorization"))
	if !strings.HasPrefix(value, prefix) || len(a.config.ReleaseCandidateTokenHash) != sha256.Size {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(strings.TrimPrefix(value, prefix)))
	if err != nil || len(raw) != 32 {
		return false
	}
	digest := sha256.Sum256(raw)
	return subtle.ConstantTimeCompare(digest[:], a.config.ReleaseCandidateTokenHash) == 1
}

func releaseCandidateValidationError(candidate adminstore.ReleaseCandidate) string {
	validHTTPS := func(raw string) bool {
		parsed, err := url.Parse(raw)
		return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil && parsed.Fragment == ""
	}
	values := []string{candidate.Channel, candidate.Platform, candidate.Architecture, candidate.ReleaseNotesURL, candidate.SetDigest, candidate.CandidateSigningKeyID, candidate.CandidateSignature}
	for _, value := range values {
		if value == "" || len(value) > 16*1024 || strings.ContainsAny(value, "\r\n") {
			return "required-text"
		}
	}
	if candidate.SchemaVersion != 3 {
		return "schema-version"
	}
	if !releases.ValidVersion(candidate.Version) {
		return "version"
	}
	if candidate.Channel != "owner" && candidate.Channel != "beta" {
		return "channel"
	}
	if candidate.Platform != "windows" && candidate.Platform != "linux" {
		return "platform"
	}
	if candidate.Architecture != "x86_64" && candidate.Architecture != "aarch64" {
		return "platform"
	}
	if candidate.Platform == "windows" && candidate.SupportedWindows == "" {
		return "supported-windows"
	}
	if candidate.Platform == "linux" && candidate.SupportedWindows != "" {
		return "supported-windows"
	}
	if !validHTTPS(candidate.ReleaseNotesURL) || !sha256Pattern.MatchString(candidate.SetDigest) {
		return "https-url"
	}
	expectedFormats := map[string]bool{"nsis": true}
	if candidate.Platform == "linux" {
		expectedFormats = map[string]bool{"appimage": true, "deb": true, "rpm": true}
	}
	if len(candidate.Artifacts) != len(expectedFormats) {
		return "artifact-set"
	}
	seen := make(map[string]bool, len(candidate.Artifacts))
	for _, artifact := range candidate.Artifacts {
		key := artifact.Format + ":" + artifact.Architecture
		if seen[key] || !expectedFormats[artifact.Format] || artifact.Architecture != candidate.Architecture {
			return "artifact-set"
		}
		seen[key] = true
		artifactValues := []string{artifact.Format, artifact.Architecture, artifact.URL, artifact.ObjectKey, artifact.SHA256, artifact.DistributionClass, artifact.SigstoreIssuer, artifact.SigstoreIdentity, artifact.SigstoreBundleSHA256}
		for _, value := range artifactValues {
			if value == "" || len(value) > 16*1024 || strings.ContainsAny(value, "\r\n") {
				return "artifact-text"
			}
		}
		for _, value := range []string{artifact.UpdaterSignature, artifact.UpdaterSigningKeyID, artifact.AuthenticodeSubject, artifact.AuthenticodeThumbprint} {
			if len(value) > 16*1024 || strings.ContainsAny(value, "\r\n") {
				return "artifact-evidence-text"
			}
		}
		if !validHTTPS(artifact.URL) || !validArtifactObjectKey(artifact.ObjectKey) {
			return "artifact-location"
		}
		if !sha256Pattern.MatchString(artifact.SHA256) || artifact.Bytes <= 0 || artifact.Bytes > 8*1024*1024*1024 {
			return "artifact-integrity"
		}
		expectedUpdaterCapability := candidate.Platform == "windows" && artifact.Format == "nsis"
		if artifact.UpdaterCapable != expectedUpdaterCapability || (artifact.UpdaterCapable && (len(artifact.UpdaterSignature) < 64 || artifact.UpdaterSigningKeyID == "")) || (!artifact.UpdaterCapable && (artifact.UpdaterSignature != "" || artifact.UpdaterSigningKeyID != "")) {
			return "updater-capability"
		}
		if !validSigstoreCandidateEvidence(candidate, artifact) {
			return "sigstore-evidence"
		}
		if artifact.AuthenticodeVerified && (len(artifact.AuthenticodeEvidence) == 0 || artifact.AuthenticodeSubject == "" || artifact.AuthenticodeThumbprint == "") {
			return "authenticode-evidence"
		}
		if !releases.ArtifactEligible(artifact.DistributionClass, artifact.SigstoreVerified, artifact.AuthenticodeVerified) {
			return "distribution-eligibility"
		}
	}
	_, err := base64.RawURLEncoding.DecodeString(candidate.CandidateSignature)
	if err != nil {
		return "candidate-signature"
	}
	return ""
}

func validSigstoreCandidateEvidence(candidate adminstore.ReleaseCandidate, artifact adminstore.ReleaseArtifact) bool {
	expectedIdentity := "https://github.com/usesesame/sesame-desktop/.github/workflows/release-early-access.yml@refs/tags/v" + candidate.Version
	if !artifact.SigstoreVerified || artifact.SigstoreIssuer != "https://token.actions.githubusercontent.com" || artifact.SigstoreIdentity != expectedIdentity || !sha256Pattern.MatchString(artifact.SigstoreBundleSHA256) || len(artifact.SigstoreEvidence) == 0 {
		return false
	}
	evidence := artifact.SigstoreEvidence
	return evidence["schemaVersion"] == float64(1) &&
		evidence["verified"] == true &&
		evidence["transparencyLogVerified"] == true &&
		evidence["issuer"] == artifact.SigstoreIssuer &&
		evidence["certificateIdentity"] == artifact.SigstoreIdentity &&
		evidence["repository"] == "usesesame/sesame-desktop" &&
		evidence["workflow"] == ".github/workflows/release-early-access.yml" &&
		evidence["ref"] == "refs/tags/v"+candidate.Version &&
		evidence["artifactSha256"] == artifact.SHA256 &&
		evidence["artifactBundleSha256"] == artifact.SigstoreBundleSHA256
}

func (a *api) verifyReleaseCandidate(candidate adminstore.ReleaseCandidate) bool {
	if candidate.CandidateSigningKeyID != a.config.ReleaseCandidateKeyID {
		return false
	}
	signature, err := base64.RawURLEncoding.DecodeString(candidate.CandidateSignature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return false
	}
	payload, ok := releaseCandidateSigningPayload(candidate)
	if !ok {
		return false
	}
	return ed25519.Verify(a.config.ReleaseCandidatePublicKey, []byte(payload), signature)
}

// Binds every immutable artifact claim, including mandatory Sigstore evidence, to the pipeline receipt.
func releaseCandidateSigningPayload(candidate adminstore.ReleaseCandidate) (string, bool) {
	digest, ok := releaseSetDigest(candidate)
	if !ok || digest != candidate.SetDigest {
		return "", false
	}
	var updater adminstore.ReleaseArtifact
	for _, artifact := range candidate.Artifacts {
		if artifact.UpdaterCapable {
			updater = artifact
			break
		}
	}
	updaterBytes := ""
	if updater.UpdaterCapable {
		updaterBytes = strconv.FormatInt(updater.Bytes, 10)
	}
	return strings.Join([]string{
		"sesame-release-set-candidate-v1", candidate.Version, candidate.Channel, candidate.Platform,
		candidate.Architecture, candidate.SupportedWindows, candidate.ReleaseNotesURL, candidate.SetDigest,
		"updater", updater.Format, updater.Architecture, updater.URL, updater.ObjectKey, updater.SHA256,
		updaterBytes, updater.UpdaterSignature, updater.UpdaterSigningKeyID,
	}, "\n"), true
}

func releaseSetDigest(candidate adminstore.ReleaseCandidate) (string, bool) {
	artifacts := append([]adminstore.ReleaseArtifact(nil), candidate.Artifacts...)
	sort.Slice(artifacts, func(i, j int) bool {
		return artifacts[i].Format+":"+artifacts[i].Architecture < artifacts[j].Format+":"+artifacts[j].Architecture
	})
	lines := []string{"sesame-release-set-digest-v1", candidate.Version, candidate.Channel, candidate.Platform, candidate.Architecture, candidate.SupportedWindows, candidate.ReleaseNotesURL}
	for _, artifact := range artifacts {
		sigstoreEvidence, err := json.Marshal(artifact.SigstoreEvidence)
		if err != nil {
			return "", false
		}
		sigstoreDigest := sha256.Sum256(sigstoreEvidence)
		authenticodeDigest := ""
		if len(artifact.AuthenticodeEvidence) > 0 {
			evidence, err := json.Marshal(artifact.AuthenticodeEvidence)
			if err != nil {
				return "", false
			}
			digest := sha256.Sum256(evidence)
			authenticodeDigest = base64.RawURLEncoding.EncodeToString(digest[:])
		}
		lines = append(lines,
			"artifact", artifact.Format, artifact.Architecture, artifact.URL, artifact.ObjectKey,
			artifact.SHA256, strconv.FormatInt(artifact.Bytes, 10), strconv.FormatBool(artifact.UpdaterCapable),
			artifact.UpdaterSignature, artifact.UpdaterSigningKeyID, artifact.DistributionClass,
			strconv.FormatBool(artifact.SigstoreVerified), artifact.SigstoreIssuer, artifact.SigstoreIdentity,
			artifact.SigstoreBundleSHA256, base64.RawURLEncoding.EncodeToString(sigstoreDigest[:]),
			strconv.FormatBool(artifact.AuthenticodeVerified), artifact.AuthenticodeSubject,
			artifact.AuthenticodeThumbprint, authenticodeDigest,
		)
	}
	digest := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(digest[:]), true
}

func validArtifactObjectKey(value string) bool {
	if len(value) == 0 || len(value) > 1024 || strings.HasPrefix(value, "/") || strings.Contains(value, "//") {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	for _, character := range value {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || strings.ContainsRune("._/-", character)) {
			return false
		}
	}
	return true
}
