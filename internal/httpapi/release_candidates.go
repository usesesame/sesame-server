package httpapi

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	adminstore "usesesame.app/backend/internal/admin"
	"usesesame.app/backend/internal/releases"
)

func (a *api) releaseCandidateIngest(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodPost) {
		return
	}
	if !a.validReleaseCandidateToken(request) {
		writeError(response, http.StatusUnauthorized, "release_pipeline_unauthorized", "This endpoint accepts only the configured release pipeline credential.")
		return
	}
	if len(a.config.ReleaseCandidatePublicKey) != ed25519.PublicKeySize || a.config.ReleaseCandidateKeyID == "" || len(a.config.ReleaseCandidateTokenHash) != sha256.Size {
		writeError(response, http.StatusServiceUnavailable, "release_candidate_verification_unavailable", "Release candidate verification is not configured.")
		return
	}
	var candidate adminstore.ReleaseCandidate
	if !decodeAdminJSON(response, request, &candidate) || !validReleaseCandidate(candidate) || !a.verifyReleaseCandidate(candidate) {
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

func validReleaseCandidate(candidate adminstore.ReleaseCandidate) bool {
	validHTTPS := func(raw string) bool {
		parsed, err := url.Parse(raw)
		return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil && parsed.Fragment == ""
	}
	values := []string{candidate.Channel, candidate.Platform, candidate.Architecture, candidate.SupportedWindows, candidate.ReleaseNotesURL, candidate.Artifact.ObjectKey, candidate.Artifact.SHA256, candidate.Artifact.UpdaterSignature, candidate.Artifact.UpdaterSigningKeyID, candidate.Artifact.DistributionClass, candidate.Artifact.SigstoreIssuer, candidate.Artifact.SigstoreIdentity, candidate.Artifact.SigstoreBundleSHA256, candidate.CandidateSigningKeyID, candidate.CandidateSignature}
	for _, value := range values {
		if value == "" || len(value) > 16*1024 || strings.ContainsAny(value, "\r\n") {
			return false
		}
	}
	for _, value := range []string{candidate.Artifact.AuthenticodeSubject, candidate.Artifact.AuthenticodeThumbprint} {
		if len(value) > 16*1024 || strings.ContainsAny(value, "\r\n") {
			return false
		}
	}
	if candidate.SchemaVersion != 2 || !releases.ValidVersion(candidate.Version) || (candidate.Channel != "owner" && candidate.Channel != "beta") || candidate.Platform != "windows" || (candidate.Architecture != "x86_64" && candidate.Architecture != "aarch64") || !validHTTPS(candidate.ReleaseNotesURL) || !validArtifactObjectKey(candidate.Artifact.ObjectKey) || !sha256Pattern.MatchString(candidate.Artifact.SHA256) || candidate.Artifact.Bytes <= 0 || candidate.Artifact.Bytes > 8*1024*1024*1024 {
		return false
	}
	if !validSigstoreCandidateEvidence(candidate) {
		return false
	}
	if candidate.Artifact.AuthenticodeVerified && (len(candidate.Artifact.AuthenticodeEvidence) == 0 || candidate.Artifact.AuthenticodeSubject == "" || candidate.Artifact.AuthenticodeThumbprint == "") {
		return false
	}
	if (candidate.Artifact.DistributionClass == "early_access" && candidate.Artifact.AuthenticodeVerified) || (candidate.Artifact.DistributionClass == "production" && !candidate.Artifact.AuthenticodeVerified) || (candidate.Artifact.DistributionClass != "early_access" && candidate.Artifact.DistributionClass != "production") {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(candidate.CandidateSignature)
	return err == nil
}

func validSigstoreCandidateEvidence(candidate adminstore.ReleaseCandidate) bool {
	artifact := candidate.Artifact
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
	sigstoreEvidence, err := json.Marshal(candidate.Artifact.SigstoreEvidence)
	if err != nil {
		return "", false
	}
	sigstoreDigest := sha256.Sum256(sigstoreEvidence)
	evidenceDigest := ""
	if len(candidate.Artifact.AuthenticodeEvidence) > 0 {
		evidence, err := json.Marshal(candidate.Artifact.AuthenticodeEvidence)
		if err != nil {
			return "", false
		}
		digest := sha256.Sum256(evidence)
		evidenceDigest = base64.RawURLEncoding.EncodeToString(digest[:])
	}
	return strings.Join([]string{
		"sesame-release-candidate-v2", candidate.Version, candidate.Channel, candidate.Platform, candidate.Architecture,
		candidate.SupportedWindows, candidate.ReleaseNotesURL, candidate.Artifact.ObjectKey, candidate.Artifact.SHA256,
		strconv.FormatInt(candidate.Artifact.Bytes, 10), candidate.Artifact.UpdaterSignature, candidate.Artifact.UpdaterSigningKeyID,
		candidate.Artifact.DistributionClass, strconv.FormatBool(candidate.Artifact.SigstoreVerified), candidate.Artifact.SigstoreIssuer,
		candidate.Artifact.SigstoreIdentity, candidate.Artifact.SigstoreBundleSHA256, base64.RawURLEncoding.EncodeToString(sigstoreDigest[:]),
		strconv.FormatBool(candidate.Artifact.AuthenticodeVerified), candidate.Artifact.AuthenticodeSubject,
		candidate.Artifact.AuthenticodeThumbprint, evidenceDigest,
	}, "\n"), true
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
