package releases

import "testing"

func TestArtifactEligibleFollowsDistributionClass(t *testing.T) {
	cases := []struct {
		name                 string
		distributionClass    string
		sigstoreVerified     bool
		authenticodeVerified bool
		eligible             bool
	}{
		{"early access without authenticode", "early_access", true, false, true},
		{"early access with authenticode", "early_access", true, true, false},
		{"production with authenticode", "production", true, true, true},
		{"production without authenticode", "production", true, false, false},
		{"early access without sigstore", "early_access", false, false, false},
		{"production without sigstore", "production", false, true, false},
		{"unknown class with both checks", "staging", true, true, false},
		{"empty class with both checks", "", true, true, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := ArtifactEligible(testCase.distributionClass, testCase.sigstoreVerified, testCase.authenticodeVerified)
			if got != testCase.eligible {
				t.Fatalf("ArtifactEligible(%q, %t, %t) = %t, want %t",
					testCase.distributionClass, testCase.sigstoreVerified, testCase.authenticodeVerified, got, testCase.eligible)
			}
		})
	}
}
