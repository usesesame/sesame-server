package main

import (
	"context"

	"usesesame.app/backend/internal/selfhost/config"
	"usesesame.app/backend/internal/selfhost/secrets"
)

func wireCommands() Commands {
	return Commands{
		Serve:   ApplicationBuilderFunc(buildApplication),
		Install: RunnerFunc(runInstall),
		Backup:  RunnerFunc(runBackup),
		Restore: RunnerFunc(runRestore),
		Check:   RunnerFunc(runCheck),
		Export:  RunnerFunc(runExport),
		UpdatesReset: newUpdateSequenceReset(func(ctx context.Context, cfg config.Config, loaded *secrets.Secrets) (updateResetStore, error) {
			return openStore(ctx, cfg, loaded, storeMutation)
		}),
		OwnerReset: newOwnerReset(func(ctx context.Context, cfg config.Config, loaded *secrets.Secrets) (ownerResetStore, error) {
			return openStore(ctx, cfg, loaded, storeMutation)
		}),
		Recover: RunnerFunc(runRecover),
	}
}
