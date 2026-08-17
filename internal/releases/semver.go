// Package releases contains release-pipeline validation shared by the admin
// control plane and update endpoints. Loose version strings are not accepted:
// update ordering must never depend on lexical comparison.
package releases

import (
	"errors"
	"strconv"
	"strings"
)

type Version struct {
	Major      uint64
	Minor      uint64
	Patch      uint64
	Prerelease []identifier
}

type identifier struct {
	raw     string
	numeric bool
	number  uint64
}

func ParseVersion(raw string) (Version, error) {
	if raw == "" || strings.TrimSpace(raw) != raw || strings.Contains(raw, "+") {
		return Version{}, errors.New("version must be canonical SemVer without build metadata")
	}
	core, prerelease, hasPrerelease := strings.Cut(raw, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return Version{}, errors.New("version must be canonical SemVer")
	}
	parsed := Version{}
	values := []*uint64{&parsed.Major, &parsed.Minor, &parsed.Patch}
	for index, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return Version{}, errors.New("version must be canonical SemVer")
		}
		value, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return Version{}, errors.New("version must be canonical SemVer")
		}
		*values[index] = value
	}
	if !hasPrerelease {
		return parsed, nil
	}
	if prerelease == "" {
		return Version{}, errors.New("version prerelease is invalid")
	}
	for _, part := range strings.Split(prerelease, ".") {
		if part == "" || !validIdentifier(part) {
			return Version{}, errors.New("version prerelease is invalid")
		}
		entry := identifier{raw: part}
		if allDigits(part) {
			if len(part) > 1 && part[0] == '0' {
				return Version{}, errors.New("version prerelease is invalid")
			}
			entry.numeric = true
			entry.number, _ = strconv.ParseUint(part, 10, 64)
		}
		parsed.Prerelease = append(parsed.Prerelease, entry)
	}
	return parsed, nil
}

func ValidVersion(raw string) bool {
	_, err := ParseVersion(raw)
	return err == nil
}

func (version Version) Compare(other Version) int {
	for _, pair := range [][2]uint64{{version.Major, other.Major}, {version.Minor, other.Minor}, {version.Patch, other.Patch}} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	if len(version.Prerelease) == 0 && len(other.Prerelease) > 0 {
		return 1
	}
	if len(version.Prerelease) > 0 && len(other.Prerelease) == 0 {
		return -1
	}
	for index := 0; index < len(version.Prerelease) && index < len(other.Prerelease); index++ {
		left, right := version.Prerelease[index], other.Prerelease[index]
		if left.numeric && right.numeric {
			if left.number < right.number {
				return -1
			}
			if left.number > right.number {
				return 1
			}
			continue
		}
		if left.numeric != right.numeric {
			if left.numeric {
				return -1
			}
			return 1
		}
		if left.raw < right.raw {
			return -1
		}
		if left.raw > right.raw {
			return 1
		}
	}
	if len(version.Prerelease) < len(other.Prerelease) {
		return -1
	}
	if len(version.Prerelease) > len(other.Prerelease) {
		return 1
	}
	return 0
}

func validIdentifier(value string) bool {
	for _, character := range value {
		if !(character >= '0' && character <= '9') && !(character >= 'a' && character <= 'z') && !(character >= 'A' && character <= 'Z') && character != '-' {
			return false
		}
	}
	return true
}

func allDigits(value string) bool {
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}
