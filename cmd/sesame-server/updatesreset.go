package main

import (
	"context"
	"fmt"

	"usesesame.app/backend/internal/selfhost"
	"usesesame.app/backend/internal/selfhost/config"
	"usesesame.app/backend/internal/selfhost/secrets"
)

type updateResetStore interface {
	ResetUpdateSequences(ctx context.Context, by selfhost.Actor) (int, error)
	Close() error
}

type updateResetOpener func(ctx context.Context, cfg config.Config, loaded *secrets.Secrets) (updateResetStore, error)

func newUpdateSequenceReset(open updateResetOpener) Runner {
	return RunnerFunc(func(ctx context.Context, invocation Invocation) error {
		loaded, err := secrets.Load(invocation.Config.DataDir)
		if err != nil {
			return fmt.Errorf("the instance secrets in %s cannot be read, and opening the database needs the original admin encryption key: %w", secrets.Dir(invocation.Config.DataDir), err)
		}
		store, err := open(ctx, invocation.Config, loaded)
		if err != nil {
			return err
		}
		defer store.Close()
		cleared, err := store.ResetUpdateSequences(ctx, hostActor)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(invocation.Stdout, "Forgot the highest accepted update feed sequence for %d signing keys. The next feed that verifies is accepted whatever its sequence number.\n", cleared)
		return err
	})
}
