package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"usesesame.app/backend/internal/buildinfo"
	"usesesame.app/backend/internal/selfhost/config"
)

const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

type Environment struct {
	Lookup   config.Lookup
	Stdin    io.Reader
	Stdout   io.Writer
	Stderr   io.Writer
	Commands Commands
	Version  string
	Commit   string
}

func defaultEnvironment(lookup config.Lookup, stdin io.Reader, stdout, stderr io.Writer) Environment {
	return Environment{
		Lookup:   lookup,
		Stdin:    stdin,
		Stdout:   stdout,
		Stderr:   stderr,
		Commands: wireCommands(),
		Version:  buildinfo.Version,
		Commit:   buildinfo.Commit,
	}
}

type parsed struct {
	name    string
	args    []string
	flags   CommandFlags
	install InstallRequest
}

func run(ctx context.Context, args []string, env Environment) int {
	command, err := parseCommand(args)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprint(env.Stdout, usage)
		return exitOK
	}
	if err != nil {
		fmt.Fprintf(env.Stderr, "sesame-server: %v\n\n%s", err, usage)
		return exitUsage
	}
	if command.name == "version" {
		fmt.Fprintf(env.Stdout, "sesame-server %s (%s)\n", env.Version, env.Commit)
		return exitOK
	}
	if command.name == "help" {
		fmt.Fprint(env.Stdout, usage)
		return exitOK
	}
	if command.name == "install" {
		invocation := Invocation{Install: command.install, Flags: command.flags, Stdin: env.Stdin, Stdout: env.Stdout, Stderr: env.Stderr, Version: env.Version}
		if err := env.Commands.Install.Run(ctx, invocation); err != nil {
			fmt.Fprintf(env.Stderr, "sesame-server: %v\n", err)
			return exitFailure
		}
		return exitOK
	}
	cfg, err := config.Load(env.Lookup)
	if err != nil {
		fmt.Fprintf(env.Stderr, "sesame-server: configuration is invalid:\n%v\n", err)
		return exitFailure
	}
	logger := newLogger(env.Stderr, cfg)
	invocation := Invocation{Config: cfg, Args: command.args, Flags: command.flags, Stdin: env.Stdin, Stdout: env.Stdout, Stderr: env.Stderr, Logger: logger, Version: env.Version}
	if err := execute(ctx, command, env, invocation); err != nil {
		fmt.Fprintf(env.Stderr, "sesame-server: %v\n", err)
		return exitFailure
	}
	return exitOK
}

func newLogger(w io.Writer, cfg config.Config) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: cfg.SlogLevel()}))
}

func execute(ctx context.Context, command parsed, env Environment, invocation Invocation) error {
	switch command.name {
	case "serve":
		return serve(ctx, env, invocation)
	case "backup":
		return env.Commands.Backup.Run(ctx, invocation)
	case "restore":
		return env.Commands.Restore.Run(ctx, invocation)
	case "check":
		return env.Commands.Check.Run(ctx, invocation)
	case "owner reset":
		return env.Commands.OwnerReset.Run(ctx, invocation)
	case "updates reset-sequence":
		return env.Commands.UpdatesReset.Run(ctx, invocation)
	case "export":
		return env.Commands.Export.Run(ctx, invocation)
	case "recover":
		return env.Commands.Recover.Run(ctx, invocation)
	case "healthcheck":
		return healthcheck(ctx, invocation)
	}
	return fmt.Errorf("unknown command %q", command.name)
}

func parseCommand(args []string) (parsed, error) {
	if len(args) == 0 {
		return parsed{name: "serve"}, nil
	}
	switch args[0] {
	case "-h", "-help", "--help", "help":
		return parsed{}, flag.ErrHelp
	case "-version", "--version", "version":
		if len(args) > 1 {
			return parsed{}, errors.New("version takes no arguments")
		}
		return parsed{name: "version"}, nil
	}
	name, rest := args[0], args[1:]
	if name == "owner" {
		if len(rest) == 0 || rest[0] != "reset" {
			return parsed{}, errors.New("owner needs the subcommand reset, as in: sesame-server owner reset <name>")
		}
		name, rest = "owner reset", rest[1:]
	}
	if name == "updates" {
		if len(rest) == 0 || rest[0] != "reset-sequence" {
			return parsed{}, errors.New("updates needs the subcommand reset-sequence, as in: sesame-server updates reset-sequence")
		}
		name, rest = "updates reset-sequence", rest[1:]
	}
	if name == "recover" {
		if _, err := parseRecoverArgs(rest); err != nil {
			return parsed{}, err
		}
		return parsed{name: name, args: rest}, nil
	}
	if name == "check" {
		positional, flags, err := parseCheckArgs(rest)
		if err != nil {
			return parsed{}, err
		}
		return parsed{name: name, args: positional, flags: flags}, nil
	}
	if name == "install" {
		request, flags, err := parseInstallArgs(rest)
		if err != nil {
			return parsed{}, err
		}
		return parsed{name: name, flags: flags, install: request}, nil
	}
	var minArgs, maxArgs int
	switch name {
	case "serve", "healthcheck", "updates reset-sequence":
	case "backup", "export":
		maxArgs = 1
	case "restore", "owner reset":
		minArgs, maxArgs = 1, 1
	default:
		return parsed{}, fmt.Errorf("unknown command %q", args[0])
	}
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var archiveStdout bool
	if name == "backup" {
		flags.BoolVar(&archiveStdout, "stdout", false, "")
	}
	if err := flags.Parse(rest); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return parsed{}, err
		}
		return parsed{}, fmt.Errorf("%s: %w", name, err)
	}
	positional := flags.Args()
	if len(positional) < minArgs || len(positional) > maxArgs {
		return parsed{}, fmt.Errorf("%s: %s", name, argumentCount(minArgs, maxArgs, len(positional)))
	}
	if archiveStdout && len(positional) > 0 {
		return parsed{}, errors.New("backup: --stdout accepts no output file")
	}
	return parsed{name: name, args: positional, flags: CommandFlags{Stdout: archiveStdout}}, nil
}

func parseCheckArgs(args []string) ([]string, CommandFlags, error) {
	var flags CommandFlags
	var positional []string
	parsingFlags := true
	for _, arg := range args {
		if parsingFlags && arg == "--" {
			parsingFlags = false
			continue
		}
		if parsingFlags {
			switch arg {
			case "-h", "-help", "--help":
				return nil, CommandFlags{}, flag.ErrHelp
			case "--stdin":
				flags.Stdin = true
				continue
			case "--json":
				flags.JSON = true
				continue
			}
			if strings.HasPrefix(arg, "-") && arg != "-" {
				return nil, CommandFlags{}, fmt.Errorf("check: flag provided but not defined: %s", arg)
			}
		}
		positional = append(positional, arg)
	}
	if flags.Stdin && len(positional) > 0 {
		return nil, CommandFlags{}, errors.New("check: --stdin does not take a backup file")
	}
	if len(positional) > 1 {
		return nil, CommandFlags{}, fmt.Errorf("check: %s", argumentCount(0, 1, len(positional)))
	}
	if flags.JSON && !flags.Stdin && len(positional) == 0 {
		return nil, CommandFlags{}, errors.New("check: --json needs a backup file or --stdin")
	}
	return positional, flags, nil
}

func argumentCount(minArgs, maxArgs, got int) string {
	switch {
	case maxArgs == 0:
		return fmt.Sprintf("takes no arguments but got %d", got)
	case minArgs == maxArgs:
		return fmt.Sprintf("needs exactly %d argument but got %d", minArgs, got)
	}
	return fmt.Sprintf("takes at most %d argument but got %d", maxArgs, got)
}
