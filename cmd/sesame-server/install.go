package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"usesesame.app/backend/internal/selfhost/install"
)

type InstallRequest struct {
	Profile   string
	Version   string
	Host      string
	Directory string
}

func parseInstallArgs(args []string) (InstallRequest, CommandFlags, error) {
	var request InstallRequest
	var flags CommandFlags
	values := make(map[string]string)
	parsingFlags := true
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if parsingFlags && argument == "--" {
			parsingFlags = false
			continue
		}
		if !parsingFlags || argument == "-" || !strings.HasPrefix(argument, "-") {
			return InstallRequest{}, CommandFlags{}, errors.New("install: takes no arguments")
		}
		if argument == "-h" || argument == "-help" || argument == "--help" {
			return InstallRequest{}, CommandFlags{}, flag.ErrHelp
		}
		name, value, hasValue := strings.Cut(argument, "=")
		switch name {
		case "--profile", "--version", "--dir", "--host":
			if _, seen := values[name]; seen {
				return InstallRequest{}, CommandFlags{}, fmt.Errorf("install: %s was given more than once", name)
			}
			if !hasValue {
				if index+1 >= len(args) || args[index+1] == "" || strings.HasPrefix(args[index+1], "-") {
					return InstallRequest{}, CommandFlags{}, fmt.Errorf("install: %s needs a value", name)
				}
				value = args[index+1]
				index++
			}
			if value == "" {
				return InstallRequest{}, CommandFlags{}, fmt.Errorf("install: %s needs a value", name)
			}
			values[name] = value
		case "--json":
			if hasValue {
				return InstallRequest{}, CommandFlags{}, fmt.Errorf("install: --json takes no value")
			}
			if flags.JSON {
				return InstallRequest{}, CommandFlags{}, fmt.Errorf("install: --json was given more than once")
			}
			flags.JSON = true
		default:
			return InstallRequest{}, CommandFlags{}, fmt.Errorf("install: flag provided but not defined: %s", name)
		}
	}
	for _, name := range []string{"--profile", "--version", "--dir"} {
		if _, seen := values[name]; !seen {
			return InstallRequest{}, CommandFlags{}, fmt.Errorf("install: %s is required", name)
		}
	}
	request.Profile = values["--profile"]
	request.Version = values["--version"]
	request.Host = values["--host"]
	request.Directory = values["--dir"]
	return request, flags, nil
}

func runInstall(ctx context.Context, invocation Invocation) error {
	request := invocation.Install
	plan, err := install.Generate(ctx, install.Options{
		Profile:     request.Profile,
		Version:     request.Version,
		Host:        request.Host,
		Directory:   request.Directory,
		ToolVersion: invocation.Version,
	})
	if err != nil {
		return err
	}
	if invocation.Flags.JSON {
		document, err := json.Marshal(plan)
		if err != nil {
			return fmt.Errorf("the installation plan could not be encoded: %w", err)
		}
		_, err = invocation.Stdout.Write(append(document, '\n'))
		return err
	}
	return writeInstallSummary(invocation.Stdout, request.Directory, plan)
}

func writeInstallSummary(w io.Writer, directory string, plan install.Plan) error {
	if _, err := fmt.Fprintf(w, "Wrote the %s installation to %q.\n", plan.Profile, directory); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "The public URL is %s for image tag %s.\n", plan.PublicURL, plan.ImageVersion); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "Files:"); err != nil {
		return err
	}
	for _, file := range plan.Files {
		if _, err := fmt.Fprintf(w, "  %q mode %s\n", file.Path, file.Mode); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(w, "  \"plan.json\""); err != nil {
		return err
	}
	if len(plan.Prerequisites) > 0 {
		if _, err := fmt.Fprintln(w, "Host prerequisites are unknown:"); err != nil {
			return err
		}
		for _, prerequisite := range plan.Prerequisites {
			if _, err := fmt.Fprintf(w, "  %s: %s\n", prerequisite.ID, prerequisite.Message); err != nil {
				return err
			}
		}
	}
	_, err := fmt.Fprintf(w, "Inspect the files in %q and run `docker compose config` there before you start the server. Generation did not contact the network, pull an image or start a service.\n", directory)
	return err
}
