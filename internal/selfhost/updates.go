package selfhost

import (
	"context"
	"time"
)

type UpdateChoice string

const (
	UpdatesUnset UpdateChoice = "unset"
	UpdatesOn    UpdateChoice = "on"
	UpdatesOff   UpdateChoice = "off"
)

const (
	UpdateErrorUnreachable = "feed_unreachable"
	UpdateErrorInvalid     = "feed_invalid"
	UpdateErrorRollback    = "feed_rollback"
	UpdateErrorExpired     = "feed_expired"

	DefaultUpdateChannel = "stable"
	maxUpdateChannel     = 32
)

func ValidUpdateChannel(value string) bool {
	if value == "" || len(value) > maxUpdateChannel || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if !(character >= 'a' && character <= 'z') && !(character >= '0' && character <= '9') && character != '-' {
			return false
		}
	}
	return true
}

func ValidUpdateError(code string) bool {
	switch code {
	case UpdateErrorUnreachable, UpdateErrorInvalid, UpdateErrorRollback, UpdateErrorExpired:
		return true
	}
	return false
}

type UpdateSettings struct {
	Choice    UpdateChoice
	Channel   string
	Sequences map[string]int64
	Feed      []byte
	CheckedAt *time.Time
	LastError string
}

type UpdateChange struct {
	Enabled *bool
	Channel *string
}

type UpdateFeed struct {
	Raw       []byte
	KeyID     string
	Sequence  int64
	CheckedAt time.Time
}

type UpdateStore interface {
	UpdateSettings(ctx context.Context) (UpdateSettings, error)
	ConfigureUpdates(ctx context.Context, by Actor, change UpdateChange) (UpdateSettings, error)
	RecordUpdateFeed(ctx context.Context, feed UpdateFeed) error
	RecordUpdateFailure(ctx context.Context, code string, at time.Time) error
	ResetUpdateSequences(ctx context.Context, by Actor) (int, error)
}
