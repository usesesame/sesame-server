package selfhost

import (
	"errors"
	"fmt"
)

var (
	ErrNotFound           = errors.New("selfhost: not found")
	ErrConflict           = errors.New("selfhost: conflict")
	ErrInvalidInput       = errors.New("selfhost: invalid input")
	ErrInvalidCredentials = errors.New("selfhost: invalid credentials")
	ErrReplayedCode       = errors.New("selfhost: authentication code already used")
	ErrSessionInvalid     = errors.New("selfhost: session invalid or expired")
	ErrSetupTokenInvalid  = errors.New("selfhost: setup token invalid or expired")
	ErrPairingInvalid     = errors.New("selfhost: pairing code invalid, used, cancelled or expired")
	ErrDeviceInvalid      = errors.New("selfhost: device token invalid, revoked or expired")
	ErrLastOwner          = errors.New("selfhost: the last active owner cannot be removed")
	ErrSchemaTooNew       = errors.New("selfhost: database schema is newer than this server")
	ErrClosed             = errors.New("selfhost: store closed")
)

type SchemaTooNewError struct {
	Stored    int
	Supported int
}

func (e SchemaTooNewError) Error() string {
	return fmt.Sprintf("database schema version %d is newer than the supported version %d, so this server refuses to open it", e.Stored, e.Supported)
}

func (e SchemaTooNewError) Is(target error) bool {
	return target == ErrSchemaTooNew
}
