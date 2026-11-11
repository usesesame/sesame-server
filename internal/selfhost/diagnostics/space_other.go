//go:build !linux

package diagnostics

import "os"

func openDirectory(string) (*os.File, error) {
	return nil, errSpaceUnavailable
}

func directoryAvailableSpace(*os.File) (uint64, error) {
	return 0, errSpaceUnavailable
}
