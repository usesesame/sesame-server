package releases

// ArtifactEligible is the single distribution-eligibility rule: early-access
// builds ship without Authenticode, production builds require it, and both
// require Sigstore verification.
func ArtifactEligible(distributionClass string, sigstoreVerified, authenticodeVerified bool) bool {
	if !sigstoreVerified {
		return false
	}
	switch distributionClass {
	case "early_access":
		return !authenticodeVerified
	case "production":
		return authenticodeVerified
	default:
		return false
	}
}
