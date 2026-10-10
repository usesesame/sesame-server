package updates

import "usesesame.app/backend/internal/releases"

func Newer(candidate, current string) bool {
	next, err := releases.ParseVersion(candidate)
	if err != nil {
		return false
	}
	installed, err := releases.ParseVersion(current)
	if err != nil {
		return false
	}
	return next.Compare(installed) > 0
}

func Offered(release Release, channel string) bool {
	version, err := releases.ParseVersion(release.Version)
	if err != nil {
		return false
	}
	if channel == ChannelStable && len(version.Prerelease) > 0 {
		return false
	}
	return true
}
