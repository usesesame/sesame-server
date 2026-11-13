package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"usesesame.app/backend/internal/selfhost/ops"
	"usesesame.app/backend/internal/selfhost/secrets"
)

const manualStampFormat = "20060102T150405Z"

func opsOptions(invocation Invocation) []ops.Option {
	return []ops.Option{ops.WithVersion(invocation.Version), ops.WithLogger(invocation.Logger)}
}

func runBackup(ctx context.Context, invocation Invocation) error {
	cfg := invocation.Config
	if invocation.Flags.Stdout {
		if len(invocation.Args) != 0 {
			return errors.New("backup --stdout accepts no output file")
		}
		if invocation.Stdout == nil {
			return errors.New("backup --stdout needs standard output")
		}
		suppressSIGPIPE()
		output, finish := interruptibleStdout(ctx, invocation.Stdout)
		manifest, backupErr := ops.BackupTo(ctx, cfg.DataDir, output, opsOptions(invocation)...)
		finishErr := finish()
		if backupErr != nil {
			return backupErr
		}
		if finishErr != nil {
			return fmt.Errorf("the backup stream could not be written: %w", finishErr)
		}
		_, err := fmt.Fprintf(invocation.Stderr, "Backup streamed to standard output with the database at schema %d and the instance secrets. Keep it private, because it holds the keys that unlock owner sign-in.\n", manifest.SchemaVersion)
		return err
	}
	var path string
	if len(invocation.Args) == 1 {
		path = invocation.Args[0]
	} else {
		path = filepath.Join(ops.BackupDir(cfg.DataDir), "sesame-manual-"+time.Now().UTC().Format(manualStampFormat)+".tar")
	}
	manifest, err := ops.Backup(ctx, cfg.DataDir, path, opsOptions(invocation)...)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(invocation.Stdout, "Backup written to %s with the database at schema %d and the instance secrets. Keep it private, because it holds the keys that unlock owner sign-in.\n", path, manifest.SchemaVersion)
	return err
}

func runRestore(ctx context.Context, invocation Invocation) error {
	source := invocation.Args[0]
	var result ops.RestoreResult
	var err error
	if source == "-" {
		result, err = ops.RestoreReader(ctx, invocation.Stdin, invocation.Config.DataDir, opsOptions(invocation)...)
	} else {
		result, err = ops.Restore(ctx, source, invocation.Config.DataDir, opsOptions(invocation)...)
	}
	if result.Recovered != "" {
		fmt.Fprintf(invocation.Stderr, "An interrupted restore was finished first: %s\n", result.Recovered)
	}
	if err != nil {
		if errors.Is(err, ops.ErrInstanceRunning) {
			return fmt.Errorf("stop the running server before restoring: %w", err)
		}
		return err
	}
	label := source
	if source == "-" {
		label = "the archive from standard input"
	}
	_, err = fmt.Fprintf(invocation.Stdout, "Restored %s into %s at schema %d and installed schema %d.\n", label, invocation.Config.DataDir, result.Report.SchemaVersion, result.InstalledSchema)
	if err == nil {
		_, err = fmt.Fprintf(invocation.Stdout, "The restored database is waiting for local recovery approval. Run sesame-server recover --status to see the generation and choose the owners and devices to keep.\n")
	}
	if err == nil && result.AsideDir != "" {
		_, err = fmt.Fprintf(invocation.Stdout, "The previous contents were moved to %s.\n", result.AsideDir)
	}
	return err
}

func runCheck(ctx context.Context, invocation Invocation) error {
	if invocation.Flags.Stdin {
		if invocation.Stdin == nil {
			return errors.New("check --stdin needs standard input")
		}
		options := append(opsOptions(invocation), ops.WithWorkDir(invocation.Config.DataDir))
		report, err := ops.CheckReader(ctx, invocation.Stdin, options...)
		if err != nil {
			return fmt.Errorf("the backup from standard input did not pass: %w", err)
		}
		if invocation.Flags.JSON {
			return writeCheckReportJSON(invocation.Stdout, report)
		}
		return writeArchiveReport(invocation.Stdout, "The backup from standard input", report)
	}
	if len(invocation.Args) == 1 {
		return checkBackup(ctx, invocation)
	}
	if invocation.Flags.JSON {
		return errors.New("check --json needs a backup file or --stdin")
	}
	return checkLive(ctx, invocation)
}

func checkBackup(ctx context.Context, invocation Invocation) error {
	file := invocation.Args[0]
	report, err := ops.Check(ctx, file, opsOptions(invocation)...)
	if err != nil {
		return fmt.Errorf("%s did not pass: %w", file, err)
	}
	if invocation.Flags.JSON {
		return writeCheckReportJSON(invocation.Stdout, report)
	}
	return writeArchiveReport(invocation.Stdout, file, report)
}

func writeArchiveReport(w io.Writer, label string, report ops.Report) error {
	migration := ""
	if report.NeedsMigration {
		migration = " and needs a migration before use"
	}
	_, err := fmt.Fprintf(w, "%s is intact. It was made by server %s at %s with schema %d of %d supported%s. It holds %d audit rows, %d verified owners, and the secrets match the database: %t.\n",
		label, report.Manifest.ServerVersion, report.Manifest.CreatedAt.UTC().Format(time.RFC3339), report.SchemaVersion, report.Supported, migration, report.AuditRows, report.OwnersVerified, report.SecretsVerified)
	return err
}

type checkReportJSON struct {
	SchemaVersion   int    `json:"schemaVersion"`
	Supported       int    `json:"supported"`
	NeedsMigration  bool   `json:"needsMigration"`
	AuditRows       int64  `json:"auditRows"`
	AuditHeadHash   string `json:"auditHeadHash"`
	OwnersVerified  int    `json:"ownersVerified"`
	SecretsVerified bool   `json:"secretsVerified"`
	HasConfig       bool   `json:"hasConfig"`
}

func writeCheckReportJSON(w io.Writer, report ops.Report) error {
	return json.NewEncoder(w).Encode(checkReportJSON{
		SchemaVersion:   report.SchemaVersion,
		Supported:       report.Supported,
		NeedsMigration:  report.NeedsMigration,
		AuditRows:       report.AuditRows,
		AuditHeadHash:   report.AuditHeadHash,
		OwnersVerified:  report.OwnersVerified,
		SecretsVerified: report.SecretsVerified,
		HasConfig:       report.HasConfig,
	})
}

func checkLive(ctx context.Context, invocation Invocation) error {
	cfg := invocation.Config
	loaded, err := secrets.Load(cfg.DataDir)
	if err != nil {
		return err
	}
	store, err := openStore(ctx, cfg, loaded, storeReadOnly)
	if err != nil {
		return err
	}
	defer store.Close()
	report, err := store.Check(ctx)
	if err != nil {
		return err
	}
	if !report.OK() {
		for _, problem := range report.IntegrityProblems {
			fmt.Fprintf(invocation.Stderr, "database problem: %s\n", problem)
		}
		if report.ForeignKeyProblem > 0 {
			fmt.Fprintf(invocation.Stderr, "%d rows break a foreign key\n", report.ForeignKeyProblem)
		}
		if !report.Audit.OK && report.Audit.FirstBreak != nil {
			fmt.Fprintf(invocation.Stderr, "the audit chain breaks at row %d\n", report.Audit.FirstBreak.Seq)
		}
		return fmt.Errorf("%s did not pass its checks", databasePath(cfg))
	}
	_, err = fmt.Fprintf(invocation.Stdout, "%s passed its checks at schema %d with %d audit rows.\n", databasePath(cfg), report.SchemaVersion, report.Audit.Rows)
	return err
}

func runExport(ctx context.Context, invocation Invocation) error {
	cfg := invocation.Config
	loaded, err := secrets.Load(cfg.DataDir)
	if err != nil {
		return err
	}
	store, err := openStore(ctx, cfg, loaded, storeReadOnly)
	if err != nil {
		return err
	}
	defer store.Close()
	out := io.Writer(invocation.Stdout)
	var file *os.File
	path := ""
	if len(invocation.Args) == 1 && invocation.Args[0] != "-" {
		path = invocation.Args[0]
		file, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%s already exists and export never overwrites a file", path)
		}
		if err != nil {
			return err
		}
		out = file
	}
	exportErr := ops.Export(ctx, store, out, opsOptions(invocation)...)
	if file != nil {
		if closeErr := file.Close(); exportErr == nil {
			exportErr = closeErr
		}
		if exportErr != nil {
			_ = os.Remove(path)
		}
	}
	if exportErr != nil {
		return exportErr
	}
	if path != "" {
		_, err = fmt.Fprintf(invocation.Stdout, "Export written to %s.\n", path)
	}
	return err
}
