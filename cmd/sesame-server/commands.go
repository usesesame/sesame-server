package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"

	"usesesame.app/backend/internal/selfhost/config"
	"usesesame.app/backend/internal/selfhost/secrets"
)

type Invocation struct {
	Config  config.Config
	Args    []string
	Stdout  io.Writer
	Stderr  io.Writer
	Logger  *slog.Logger
	Version string
}

type Runner interface {
	Run(ctx context.Context, invocation Invocation) error
}

type RunnerFunc func(ctx context.Context, invocation Invocation) error

func (f RunnerFunc) Run(ctx context.Context, invocation Invocation) error {
	return f(ctx, invocation)
}

type BuildInput struct {
	Config  config.Config
	Secrets *secrets.Secrets
	Logger  *slog.Logger
	Version string
	Commit  string
}

type Application struct {
	Handler http.Handler
	Jobs    []func(ctx context.Context)
	Close   func() error
}

type ApplicationBuilder interface {
	Build(ctx context.Context, input BuildInput) (*Application, error)
}

type ApplicationBuilderFunc func(ctx context.Context, input BuildInput) (*Application, error)

func (f ApplicationBuilderFunc) Build(ctx context.Context, input BuildInput) (*Application, error) {
	return f(ctx, input)
}

type Commands struct {
	Serve        ApplicationBuilder
	Backup       Runner
	Restore      Runner
	Check        Runner
	OwnerReset   Runner
	UpdatesReset Runner
	Export       Runner
}
