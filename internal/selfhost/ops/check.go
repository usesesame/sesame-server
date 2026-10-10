package ops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"usesesame.app/backend/internal/authkit"
	"usesesame.app/backend/internal/selfhost"
	"usesesame.app/backend/internal/selfhost/secrets"
	"usesesame.app/backend/internal/selfhost/sqlitestore"
)

var ownerTOTPContext = []byte("sesame-selfhost-owner-totp-v1")

type Report struct {
	Manifest        Manifest
	SchemaVersion   int
	Supported       int
	NeedsMigration  bool
	AuditRows       int64
	AuditHeadHash   string
	OwnersVerified  int
	SecretsVerified bool
	HasConfig       bool
}

func Check(ctx context.Context, file string, options ...Option) (Report, error) {
	cfg, err := newConfig(options)
	if err != nil {
		return Report{}, err
	}
	work, err := cfg.checkWorkDir(file)
	if err != nil {
		return Report{}, err
	}
	defer os.RemoveAll(work)
	_, report, err := verifyArchive(ctx, file, work, cfg)
	return report, err
}

func (c config) checkWorkDir(file string) (string, error) {
	candidates := []string{c.workDir}
	if c.workDir == "" {
		candidates = []string{filepath.Dir(file), os.TempDir()}
	}
	var last error
	for _, parent := range candidates {
		work, err := makePrivateDir(parent, ".check-")
		if err == nil {
			return work, nil
		}
		last = err
	}
	return "", fmt.Errorf("a working directory for the check cannot be created: %w", last)
}

func verifyArchive(ctx context.Context, file, staging string, cfg config) (Manifest, Report, error) {
	manifest, err := extractArchive(file, staging, cfg.limits)
	if err != nil {
		return Manifest{}, Report{}, err
	}
	report, err := inspect(ctx, staging, manifest)
	return manifest, report, err
}

func inspect(ctx context.Context, dir string, manifest Manifest) (Report, error) {
	report := Report{Manifest: manifest, SchemaVersion: manifest.SchemaVersion}
	for _, member := range manifest.Members {
		if member.Name == ConfigName {
			report.HasConfig = true
		}
	}
	supported, err := supportedSchema(ctx)
	if err != nil {
		return report, err
	}
	report.Supported = supported
	databasePath := filepath.Join(dir, DatabaseName)
	stored, err := storedSchemaVersion(ctx, databasePath)
	if err != nil {
		return report, fmt.Errorf("%w: database schema version cannot be read: %w", ErrDatabaseDamaged, err)
	}
	for _, version := range []int{manifest.SchemaVersion, stored} {
		if version > supported {
			return report, selfhost.SchemaTooNewError{Stored: version, Supported: supported}
		}
	}
	if stored != manifest.SchemaVersion {
		return report, invalid("manifest says schema version %d, but the database holds %d", manifest.SchemaVersion, stored)
	}
	if stored < supported {
		report.NeedsMigration = true
	}
	loaded, err := secrets.Load(dir)
	if err != nil {
		return report, invalid("secrets are not usable: %v", err)
	}
	adminKey := loaded.AdminEncryptionKey

	check, err := runStoreChecks(ctx, dir, databasePath, adminKey, report.NeedsMigration)
	if err != nil {
		return report, err
	}
	report.AuditRows = check.Audit.Rows
	report.AuditHeadHash = check.Audit.HeadHash
	if problems := describeProblems(check); problems != "" {
		return report, fmt.Errorf("%w: %s", ErrDatabaseDamaged, problems)
	}
	verified, err := verifyOwnerSecrets(ctx, databasePath, adminKey)
	if err != nil {
		return report, err
	}
	report.OwnersVerified = verified
	report.SecretsVerified = verified > 0
	return report, nil
}

func runStoreChecks(ctx context.Context, dir, databasePath string, adminKey []byte, migrate bool) (selfhost.CheckReport, error) {
	options := sqlitestore.Options{Path: databasePath, AdminKey: adminKey, ReadOnly: true}
	if migrate {
		scratch := filepath.Join(dir, ".inspect")
		if err := os.Mkdir(scratch, 0o700); err != nil {
			return selfhost.CheckReport{}, err
		}
		defer os.RemoveAll(scratch)
		copied := filepath.Join(scratch, DatabaseName)
		if err := copyFileLimited(databasePath, copied, 1<<62); err != nil {
			return selfhost.CheckReport{}, err
		}
		options.Path = copied
		options.ReadOnly = false
	}
	store, err := sqlitestore.Open(ctx, options)
	if err != nil {
		if errors.Is(err, selfhost.ErrSchemaTooNew) {
			return selfhost.CheckReport{}, err
		}
		return selfhost.CheckReport{}, fmt.Errorf("%w: database cannot be opened: %w", ErrDatabaseDamaged, err)
	}
	defer store.Close()
	check, err := store.Check(ctx)
	if err != nil {
		return selfhost.CheckReport{}, fmt.Errorf("%w: checks could not run: %w", ErrDatabaseDamaged, err)
	}
	return check, nil
}

func describeProblems(check selfhost.CheckReport) string {
	var parts []string
	if len(check.IntegrityProblems) > 0 {
		shown := check.IntegrityProblems
		if len(shown) > 5 {
			shown = shown[:5]
		}
		parts = append(parts, "integrity check reported: "+strings.Join(shown, "; "))
	}
	if check.ForeignKeyProblem > 0 {
		parts = append(parts, fmt.Sprintf("%d foreign key violations", check.ForeignKeyProblem))
	}
	if !check.Audit.OK {
		reason := "audit chain is broken"
		if check.Audit.FirstBreak != nil {
			reason = fmt.Sprintf("audit chain breaks at row %d: %s", check.Audit.FirstBreak.Seq, check.Audit.FirstBreak.Reason)
		}
		parts = append(parts, reason)
	}
	return strings.Join(parts, "; ")
}

func verifyOwnerSecrets(ctx context.Context, databasePath string, adminKey []byte) (int, error) {
	db, err := openRaw(databasePath, true, 0)
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrDatabaseDamaged, err)
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT id, totp_secret FROM owners WHERE removed_at IS NULL AND totp_secret IS NOT NULL ORDER BY created_at, id`)
	if err != nil {
		return 0, fmt.Errorf("%w: owners cannot be read: %w", ErrDatabaseDamaged, err)
	}
	defer rows.Close()
	verified := 0
	for rows.Next() {
		var id string
		var sealed []byte
		if err := rows.Scan(&id, &sealed); err != nil {
			return 0, fmt.Errorf("%w: %w", ErrDatabaseDamaged, err)
		}
		plain, err := authkit.Open(adminKey, sealed, ownerTOTPContext)
		if err != nil {
			return 0, fmt.Errorf("%w: the encryption key cannot decrypt the sign-in secret of owner %s, so the secrets directory belongs to a different instance", ErrSecretsMismatch, id)
		}
		clear(plain)
		verified++
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("%w: %w", ErrDatabaseDamaged, err)
	}
	return verified, nil
}
