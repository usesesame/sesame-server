package ops

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
)

func CheckReader(ctx context.Context, input io.Reader, options ...Option) (Report, error) {
	cfg, err := newConfig(options)
	if err != nil {
		return Report{}, err
	}
	if input == nil {
		return Report{}, errors.New("ops: check input is required")
	}
	if cfg.workDir == "" {
		return Report{}, errors.New("ops: WithWorkDir is required to check an archive stream")
	}
	work, err := makePrivateDir(cfg.workDir, ".check-stream-")
	if err != nil {
		return Report{}, fmt.Errorf("a working directory for the check cannot be created: %w", err)
	}
	defer os.RemoveAll(work)
	archive, err := stageBackupInput(ctx, work, input, cfg.limits)
	if err != nil {
		return Report{}, err
	}
	_, report, err := verifyArchive(ctx, archive, work, cfg)
	return report, err
}
