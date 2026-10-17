package memstore_test

import (
	"testing"

	"usesesame.app/backend/internal/syncsvc"
	"usesesame.app/backend/internal/syncsvc/memstore"
	"usesesame.app/backend/internal/syncsvc/servicetest"
)

func TestServiceBehaviourOnTheMemoryStore(t *testing.T) {
	servicetest.Run(t, func(t *testing.T) syncsvc.Storage { return memstore.New() })
}
