package releases

import "testing"

func TestParseVersionAcceptsCanonicalSemver(t *testing.T) {
	for _, raw := range []string{
		"0.0.0",
		"0.2.5",
		"1.2.3",
		"1.2.3-alpha",
		"1.2.3-alpha.1",
		"1.2.3-alpha.beta.1",
		"1.2.3-0",
		"1.2.3-x-y-z.7",
		"18446744073709551615.0.0",
	} {
		version, err := ParseVersion(raw)
		if err != nil {
			t.Errorf("ParseVersion(%q) = %v, want a canonical version", raw, err)
			continue
		}
		if version.Compare(version) != 0 {
			t.Errorf("%q does not compare equal to itself", raw)
		}
	}
}

func TestParseVersionRejectsNonCanonicalInput(t *testing.T) {
	for _, raw := range []string{
		"",
		" 1.2.3",
		"1.2.3 ",
		"v1.2.3",
		"1.2",
		"1.2.3.4",
		"01.2.3",
		"1.02.3",
		"1.2.03",
		"1.2.3-",
		"1.2.3-alpha..1",
		"1.2.3-alpha_1",
		"1.2.3-01",
		"1.2.3-alpha+build",
		"1.2.3+build",
		"1.2.3-alpha+build.5",
		"1.2.3-rélease",
		"1.2.-1",
		"1.2.3.4-alpha",
		"18446744073709551616.0.0",
		"latest",
	} {
		if ValidVersion(raw) {
			t.Errorf("ValidVersion(%q) = true, want rejection", raw)
		}
	}
}

func TestVersionOrderingFollowsSemverPrecedence(t *testing.T) {
	ordered := []string{
		"0.9.9",
		"1.0.0-alpha",
		"1.0.0-alpha.1",
		"1.0.0-alpha.beta",
		"1.0.0-beta",
		"1.0.0-beta.2",
		"1.0.0-beta.11",
		"1.0.0-rc.1",
		"1.0.0",
		"1.0.1",
		"1.1.0",
		"1.9.0",
		"1.10.0",
		"2.0.0",
	}
	parsed := make([]Version, 0, len(ordered))
	for _, raw := range ordered {
		version, err := ParseVersion(raw)
		if err != nil {
			t.Fatalf("ParseVersion(%q): %v", raw, err)
		}
		parsed = append(parsed, version)
	}
	for index := 0; index+1 < len(parsed); index++ {
		lower, higher := parsed[index], parsed[index+1]
		if lower.Compare(higher) != -1 {
			t.Errorf("%s should sort below %s", ordered[index], ordered[index+1])
		}
		if higher.Compare(lower) != 1 {
			t.Errorf("%s should sort above %s", ordered[index+1], ordered[index])
		}
	}
}

func TestVersionOrderingDistinguishesNumericAndTextIdentifiers(t *testing.T) {
	for _, pair := range []struct {
		lower  string
		higher string
	}{
		{"1.0.0-alpha.2", "1.0.0-alpha.10"},
		{"1.0.0-1", "1.0.0-alpha"},
		{"1.0.0-alpha", "1.0.0-alpha.1"},
		{"1.0.0-2", "1.0.0-10"},
	} {
		lower, err := ParseVersion(pair.lower)
		if err != nil {
			t.Fatalf("ParseVersion(%q): %v", pair.lower, err)
		}
		higher, err := ParseVersion(pair.higher)
		if err != nil {
			t.Fatalf("ParseVersion(%q): %v", pair.higher, err)
		}
		if lower.Compare(higher) != -1 {
			t.Errorf("%s should sort below %s", pair.lower, pair.higher)
		}
	}
}
