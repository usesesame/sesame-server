package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"usesesame.app/backend/internal/selfhost"
	"usesesame.app/backend/internal/selfhost/config"
	"usesesame.app/backend/internal/selfhost/secrets"
)

type ownerResetStore interface {
	ResetOwner(ctx context.Context, by selfhost.Actor, name string) (selfhost.SetupIssue, error)
	Close() error
}

type ownerResetOpener func(ctx context.Context, cfg config.Config, loaded *secrets.Secrets) (ownerResetStore, error)

var hostActor = selfhost.Actor{Kind: selfhost.ActorSystem, ID: "host-command"}

func newOwnerReset(open ownerResetOpener) Runner {
	return RunnerFunc(func(ctx context.Context, invocation Invocation) error {
		name := strings.TrimSpace(invocation.Args[0])
		loaded, err := secrets.Load(invocation.Config.DataDir)
		if err != nil {
			return fmt.Errorf("the instance secrets in %s cannot be read, and an owner reset needs the original admin encryption key: %w", secrets.Dir(invocation.Config.DataDir), err)
		}
		store, err := open(ctx, invocation.Config, loaded)
		if err != nil {
			return err
		}
		defer store.Close()
		issue, err := store.ResetOwner(ctx, hostActor, name)
		switch {
		case errors.Is(err, selfhost.ErrNotFound):
			return fmt.Errorf("no active owner is named %q", name)
		case errors.Is(err, selfhost.ErrInvalidInput):
			return fmt.Errorf("%q is not a valid owner name", name)
		case err != nil:
			return err
		}
		_, err = fmt.Fprintf(invocation.Stdout, "Open %s/setup#token=%s to set a new password and authenticator for %s. The link works once, expires %s, and has ended every session for this owner.\n",
			invocation.Config.PublicURL.Origin, issue.Token, issue.Owner.Name, issue.ExpiresAt.UTC().Format("2006-01-02 15:04 UTC"))
		return err
	})
}
