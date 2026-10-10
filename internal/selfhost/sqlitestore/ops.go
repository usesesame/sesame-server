package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"usesesame.app/backend/internal/selfhost"
)

const (
	pairingRetention = 24 * time.Hour
	deviceRetention  = 30 * 24 * time.Hour
	holderRetention  = 30 * 24 * time.Hour
)

func (s *Store) Maintain(ctx context.Context) (selfhost.MaintenanceReport, error) {
	var report selfhost.MaintenanceReport
	err := s.write(ctx, func(tx *sql.Tx) error {
		now := s.clock().Unix()
		deviceCutoff := now - int64(deviceRetention/time.Second)
		holderCutoff := now - int64(holderRetention/time.Second)
		pairingCutoff := now - int64(pairingRetention/time.Second)
		expiredDevices := `((revoked_at IS NOT NULL AND revoked_at < ?1) OR expires_at < ?1)`
		steps := []struct {
			target *int64
			query  string
			args   []any
		}{
			{&report.Sessions, `DELETE FROM sessions WHERE expires_at <= ?`, []any{now}},
			{&report.SetupTokens, `DELETE FROM setup_tokens WHERE expires_at <= ? OR used_at IS NOT NULL`, []any{now}},
			{&report.Pairings, `DELETE FROM pairings WHERE COALESCE(used_at, cancelled_at, expires_at) < ? OR device_id IN (SELECT id FROM devices WHERE ` + expiredDevices + `)`, []any{pairingCutoff}},
			{&report.Devices, `DELETE FROM devices WHERE ` + expiredDevices, []any{deviceCutoff}},
			{&report.RateLimits, `DELETE FROM rate_limits WHERE window_started_at + window_seconds <= ?`, []any{now}},
		}
		for _, step := range steps {
			result, err := tx.ExecContext(ctx, step.query, step.args...)
			if err != nil {
				return err
			}
			if *step.target, err = result.RowsAffected(); err != nil {
				return err
			}
		}
		for _, table := range []struct{ name, column string }{{"owners", "owner_id"}, {"members", "member_id"}} {
			result, err := tx.ExecContext(ctx, `
				DELETE FROM `+table.name+` WHERE removed_at IS NOT NULL AND removed_at < ?
				AND NOT EXISTS (SELECT 1 FROM devices WHERE devices.`+table.column+` = `+table.name+`.id)
				AND NOT EXISTS (SELECT 1 FROM pairings WHERE pairings.`+table.column+` = `+table.name+`.id)
			`, holderCutoff)
			if err != nil {
				return err
			}
			removed, err := result.RowsAffected()
			if err != nil {
				return err
			}
			report.Holders += removed
		}
		return nil
	})
	return report, err
}

func (s *Store) System(ctx context.Context) (selfhost.SystemInfo, error) {
	db, err := s.read()
	if err != nil {
		return selfhost.SystemInfo{}, err
	}
	var info selfhost.SystemInfo
	info.SchemaVersion, err = s.storedVersion(ctx, db)
	if err != nil {
		return selfhost.SystemInfo{}, err
	}
	now := s.clock().Unix()
	var lastBackup sql.NullInt64
	err = db.QueryRowContext(ctx, `
		SELECT (SELECT page_count * page_size FROM pragma_page_count(), pragma_page_size()),
			(SELECT last_backup_at FROM instance WHERE singleton = 1),
			(SELECT COUNT(*) FROM owners WHERE removed_at IS NULL AND password_hash IS NOT NULL),
			(SELECT COUNT(*) FROM members WHERE removed_at IS NULL),
			(SELECT COUNT(*) FROM devices d WHERE `+activeDevice+`),
			(SELECT COUNT(*) FROM pairings WHERE used_at IS NULL AND cancelled_at IS NULL AND expires_at > ?1)
	`, now).Scan(&info.DatabaseBytes, &lastBackup, &info.ActiveOwners, &info.Members, &info.ActiveDevices, &info.PendingPairing)
	if err != nil {
		return selfhost.SystemInfo{}, err
	}
	info.LastBackupAt = optionalTime(lastBackup)
	return info, nil
}

func (s *Store) Check(ctx context.Context) (selfhost.CheckReport, error) {
	db, err := s.read()
	if err != nil {
		return selfhost.CheckReport{}, err
	}
	var report selfhost.CheckReport
	if report.SchemaVersion, err = s.storedVersion(ctx, db); err != nil {
		return selfhost.CheckReport{}, err
	}
	report.IntegrityProblems, err = integrityProblems(ctx, db)
	if err != nil {
		return selfhost.CheckReport{}, err
	}
	report.ForeignKeyProblem, err = foreignKeyProblems(ctx, db)
	if err != nil {
		return selfhost.CheckReport{}, err
	}
	report.Audit, err = verifyAudit(ctx, db)
	if err != nil {
		return selfhost.CheckReport{}, err
	}
	return report, nil
}

func integrityProblems(ctx context.Context, db queryer) ([]string, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA integrity_check(20)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	problems := []string{}
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return nil, err
		}
		if line != "ok" {
			problems = append(problems, line)
		}
	}
	return problems, rows.Err()
}

func foreignKeyProblems(ctx context.Context, db queryer) (int, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
	}
	return count, rows.Err()
}

func (s *Store) Backup(ctx context.Context, destination string) error {
	if s.closed.Load() {
		return selfhost.ErrClosed
	}
	return s.backupTo(ctx, destination)
}

func (s *Store) backupTo(ctx context.Context, destination string) error {
	if destination == "" {
		return selfhost.ErrInvalidInput
	}
	directory := filepath.Dir(destination)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	if _, err := os.Lstat(destination); err == nil {
		return fmt.Errorf("%w: backup destination already exists", selfhost.ErrConflict)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	partial := destination + ".partial"
	if err := os.Remove(partial); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, partial); err != nil {
		_ = os.Remove(partial)
		return fmt.Errorf("sqlitestore: write backup: %w", err)
	}
	if err := finishBackup(partial, destination, directory); err != nil {
		_ = os.Remove(partial)
		return err
	}
	return nil
}

func finishBackup(partial, destination, directory string) error {
	if err := os.Chmod(partial, 0o600); err != nil {
		return err
	}
	file, err := os.OpenFile(partial, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Link(partial, destination); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%w: backup destination already exists", selfhost.ErrConflict)
		}
		return err
	}
	if err := os.Remove(partial); err != nil {
		return err
	}
	handle, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer handle.Close()
	return handle.Sync()
}

func (s *Store) RecordBackup(ctx context.Context, at time.Time) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE instance SET last_backup_at = ? WHERE singleton = 1`, at.UTC().Unix())
		return err
	})
}
