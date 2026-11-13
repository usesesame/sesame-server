package diagnostics

import (
	"errors"
	"math"
	"os"
)

const mebibyte = uint64(1 << 20)

var errSpaceUnavailable = errors.New("space is unavailable")

func (s *scanner) spaceCheck() Check {
	if !s.data.realDir {
		return spaceUnavailableCheck()
	}
	if !s.database.sizeKnown {
		return spaceDatabaseUnknownCheck()
	}
	dir, err := s.files.openDir(s.cfg.DataDir)
	if err != nil {
		return spaceUnavailableCheck()
	}
	defer dir.Close()
	opened, err := dir.Stat()
	if err != nil || !opened.IsDir() || !os.SameFile(s.data.info, opened) {
		return spaceUnavailableCheck()
	}
	available, err := s.files.availableSpace(dir)
	if err != nil {
		return spaceUnavailableCheck()
	}
	return spaceCheckResult(available, s.database.size)
}

func saturatingMul(first, second uint64) uint64 {
	if first == 0 || second == 0 {
		return 0
	}
	if first > math.MaxUint64/second {
		return math.MaxUint64
	}
	return first * second
}

func saturatingAdd(first, second uint64) uint64 {
	if first > math.MaxUint64-second {
		return math.MaxUint64
	}
	return first + second
}

func spaceCheckResult(available, databaseSize uint64) Check {
	critical := saturatingAdd(databaseSize, 256*mebibyte)
	tight := saturatingAdd(saturatingMul(databaseSize, 8), 256*mebibyte)
	switch {
	case available < critical:
		return spaceCriticalCheck()
	case available < tight:
		return spaceTightCheck()
	default:
		return spaceSufficientCheck()
	}
}

func spaceCriticalCheck() Check {
	return fixedCheck("space", StateAction, "space_critical",
		"Available space is below the database size plus 256 MiB, the critical planning bound, and this is an estimate rather than a reservation.",
		"Free space before the next write or backup.")
}

func spaceTightCheck() Check {
	return fixedCheck("space", StateUnknown, "space_tight",
		"Available space is below eight times the database size plus 256 MiB, the tight planning bound, and this is an estimate rather than a reservation.",
		"Free space before the database grows or a backup is written.")
}

func spaceSufficientCheck() Check {
	return fixedCheck("space", StateOK, "space_sufficient",
		"Available space meets the estimate of eight times the database size plus 256 MiB, but no space has been reserved.", "")
}

func spaceUnavailableCheck() Check {
	return fixedCheck("space", StateUnknown, "space_unavailable",
		"Available space could not be measured through the data directory.",
		"Check free space with the platform tools.")
}

func spaceDatabaseUnknownCheck() Check {
	return fixedCheck("space", StateUnknown, "space_database_unknown",
		"Space requirements could not be estimated because the database size was not observed.",
		"Check the database path and its metadata, then run the doctor again.")
}
