package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"usesesame.app/backend/internal/selfhost/diagnostics"
)

const doctorWriteFailure = "sesame-server: the doctor report could not be written\n"
const doctorInterruptedOutputTimeout = time.Second

func parseDoctorArgs(args []string) (CommandFlags, error) {
	var flags CommandFlags
	parsingFlags := true
	help := false
	for _, argument := range args {
		if parsingFlags && argument == "--" {
			parsingFlags = false
			continue
		}
		if !parsingFlags {
			return CommandFlags{}, errors.New("doctor: takes no arguments")
		}
		switch argument {
		case "-h", "-help", "--help":
			if help {
				return CommandFlags{}, errors.New("doctor: help was given more than once")
			}
			help = true
		case "--json":
			if flags.JSON {
				return CommandFlags{}, errors.New("doctor: --json was given more than once")
			}
			flags.JSON = true
		case "--network":
			return CommandFlags{}, errors.New("doctor: --network is not supported")
		default:
			name, _, hasValue := strings.Cut(argument, "=")
			switch {
			case name == "--json" && hasValue:
				return CommandFlags{}, errors.New("doctor: --json takes no value")
			case name == "--network" && hasValue:
				return CommandFlags{}, errors.New("doctor: --network is not supported")
			case strings.HasPrefix(argument, "-") && argument != "-":
				return CommandFlags{}, fmt.Errorf("doctor: flag provided but not defined: %s", name)
			default:
				return CommandFlags{}, errors.New("doctor: takes no arguments")
			}
		}
	}
	if help {
		return CommandFlags{}, flag.ErrHelp
	}
	return flags, nil
}

func runDoctor(ctx context.Context, env Environment, jsonOutput bool) int {
	if ctx == nil {
		ctx = context.Background()
	}
	report := diagnostics.Inspect(ctx, diagnostics.Options{
		Lookup:      env.Lookup,
		ToolVersion: env.Version,
	})
	var err error
	if env.Stdout == nil {
		err = errors.New("standard output is unavailable")
	} else {
		outputContext, cancelOutput := doctorOutputContext(ctx)
		defer cancelOutput()
		output, finish := interruptibleStdout(outputContext, env.Stdout)
		if jsonOutput {
			err = writeDoctorJSON(output, report)
		} else {
			err = writeDoctorHuman(output, report)
		}
		finishError := finish()
		if err == nil {
			err = finishError
		}
	}
	if err != nil {
		if env.Stderr != nil {
			writeDoctorFailure(ctx, env.Stderr)
		}
		return exitFailure
	}
	return doctorExitCode(report.Overall)
}

func doctorOutputContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx.Err() != nil {
		return context.WithTimeout(context.WithoutCancel(ctx), doctorInterruptedOutputTimeout)
	}
	return ctx, func() {}
}

func writeDoctorFailure(ctx context.Context, destination io.Writer) {
	outputContext, cancelOutput := doctorOutputContext(ctx)
	defer cancelOutput()
	output, finish := interruptibleStdout(outputContext, destination)
	_, _ = io.WriteString(output, doctorWriteFailure)
	_ = finish()
}

func writeDoctorJSON(destination io.Writer, report diagnostics.Report) error {
	document, err := json.Marshal(report)
	if err != nil {
		return err
	}
	document = append(document, '\n')
	written, err := destination.Write(document)
	if err != nil {
		return err
	}
	if written != len(document) {
		return io.ErrShortWrite
	}
	return nil
}

func writeDoctorHuman(destination io.Writer, report diagnostics.Report) error {
	var document strings.Builder
	fmt.Fprintf(&document, "Overall: %s\n", doctorOverallLabel(report.Overall))
	for _, check := range report.Checks {
		fmt.Fprintf(&document, "\n%s: %s\n%s\n", strings.ReplaceAll(check.ID, "_", " "), check.State, check.Summary)
		if check.Next != nil && *check.Next != "" {
			fmt.Fprintf(&document, "Next: %s\n", *check.Next)
		}
	}
	if len(report.Limitations) > 0 {
		fmt.Fprintln(&document, "\nDatabase contents, recovery state and key contents were not inspected. This report does not test network access, clock accuracy, port availability, host port mappings, proxy behavior or write access, and it does not prove that this process matches the running service.")
	}
	fmt.Fprintf(&document, "\n%s\n", doctorOverallSentence(report.Overall))
	written, err := destination.Write([]byte(document.String()))
	if err != nil {
		return err
	}
	if written != document.Len() {
		return io.ErrShortWrite
	}
	return nil
}

func doctorExitCode(overall string) int {
	switch overall {
	case "ok":
		return exitOK
	case "action":
		return exitFailure
	default:
		return exitUnknown
	}
}

func doctorOverallLabel(overall string) string {
	switch overall {
	case "ok", "action", "unknown":
		return overall
	default:
		return "unknown"
	}
}

func doctorOverallSentence(overall string) string {
	switch overall {
	case "ok":
		return "Every performed check is ok."
	case "action":
		return "At least one check needs action."
	default:
		return "Some facts were not tested, so this is not a passing result."
	}
}
