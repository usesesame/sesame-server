package selfhost

import "time"

type RecoveryStatus struct {
	Pending     bool
	Generation  string
	StartedAt   time.Time
	CompletedAt *time.Time
}

type RecoverySelection struct {
	OwnerIDs  []string
	DeviceIDs []string
}

type RecoveryResult struct {
	RetainedOwners  int
	RemovedOwners   int
	RetainedDevices int
	RevokedDevices  int
	AlreadyComplete bool
}
