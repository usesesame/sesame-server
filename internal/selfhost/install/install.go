package install

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
)

type generator struct {
	hook func(string) error
}

func Generate(ctx context.Context, options Options) (Plan, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	return generator{}.generate(ctx, options)
}

func (g generator) generate(ctx context.Context, options Options) (plan Plan, err error) {
	if err := ctx.Err(); err != nil {
		return Plan{}, fmt.Errorf("install: %w", err)
	}
	spec, err := validateOptions(options)
	if err != nil {
		return Plan{}, err
	}
	generated, err := renderFiles(spec)
	if err != nil {
		return Plan{}, err
	}
	plan = buildPlan(spec, generated)

	if err := g.step(ctx, "create-directory"); err != nil {
		return Plan{}, err
	}
	target, err := createTarget(spec.directory)
	if err != nil {
		return Plan{}, err
	}
	created := make([]createdFile, 0, len(generated)+1)
	published := false
	committed := false
	defer func() {
		defer closePins(created)
		if committed || published {
			target.close()
			return
		}
		if cleanupErr := target.cleanup(created); cleanupErr != nil {
			err = errors.Join(err, cleanupErr)
		}
	}()
	if err := g.step(ctx, "sync-parent"); err != nil {
		return Plan{}, err
	}
	if err := syncDirectory(target.parent); err != nil {
		return Plan{}, fmt.Errorf("install: parent directory cannot be synced: %w", err)
	}
	for _, file := range generated {
		if err := g.step(ctx, "write:"+file.name); err != nil {
			return Plan{}, err
		}
		if err := g.writeNewFile(ctx, target.root, file.name, file.data, &created); err != nil {
			return Plan{}, fmt.Errorf("install: %s cannot be written: %w", file.name, err)
		}
	}

	if err := g.step(ctx, "publish"); err != nil {
		return Plan{}, err
	}
	encoded, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return Plan{}, fmt.Errorf("install: plan cannot be encoded: %w", err)
	}
	encoded = append(encoded, '\n')
	tempName, err := planTempName()
	if err != nil {
		return Plan{}, fmt.Errorf("install: plan temporary name cannot be created: %w", err)
	}
	if err := g.writeNewFile(ctx, target.root, tempName, encoded, &created); err != nil {
		return Plan{}, fmt.Errorf("install: plan temporary file cannot be written: %w", err)
	}
	if err := g.step(ctx, "link-plan"); err != nil {
		return Plan{}, err
	}
	if err := target.checkIdentity(); err != nil {
		return Plan{}, err
	}
	planInput := created[len(created)-1]
	if err := target.checkFileIdentity(planInput); err != nil {
		return Plan{}, err
	}
	if err := target.root.Link(tempName, planName); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return Plan{}, errors.New("install: plan.json already exists and was not replaced")
		}
		return Plan{}, fmt.Errorf("install: plan.json cannot be published: %w", err)
	}
	published = true
	publishedPlan := createdFile{name: planName, info: planInput.info}
	if err := target.checkFileIdentity(publishedPlan); err != nil {
		return Plan{}, err
	}
	if err := removeIfSame(target.root, tempName, planInput.info); err != nil {
		return Plan{}, fmt.Errorf("install: plan temporary file cannot be removed: %w", err)
	}
	if err := g.step(ctx, "sync-directory"); err != nil {
		return Plan{}, err
	}
	if err := syncDirectory(target.root); err != nil {
		return Plan{}, fmt.Errorf("install: generated directory cannot be synced: %w", err)
	}
	if err := target.checkIdentity(); err != nil {
		return Plan{}, err
	}
	if err := target.checkFileIdentity(publishedPlan); err != nil {
		return Plan{}, err
	}
	committed = true
	return plan, nil
}

func (g generator) step(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("install: %w", err)
	}
	if g.hook != nil {
		if err := g.hook(name); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("install: %w", err)
	}
	return nil
}
