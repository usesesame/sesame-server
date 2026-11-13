package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"usesesame.app/backend/internal/selfhost"
)

const (
	recoveryGenerationLength = 32
	maxRecoveryIDLength      = 256
)

func validRecoveryGeneration(value string) bool {
	if len(value) != recoveryGenerationLength {
		return false
	}
	for _, character := range value {
		if character >= '0' && character <= '9' || character >= 'a' && character <= 'f' {
			continue
		}
		return false
	}
	return true
}

func readRecoveryStatus(ctx context.Context, db queryer) (selfhost.RecoveryStatus, error) {
	var exists int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'recovery'`).Scan(&exists); err != nil {
		return selfhost.RecoveryStatus{}, fmt.Errorf("%w: %w", selfhost.ErrRecoveryStateInvalid, err)
	}
	if exists == 0 {
		return selfhost.RecoveryStatus{}, fmt.Errorf("%w: the recovery row is missing", selfhost.ErrRecoveryStateInvalid)
	}
	var pending int
	var generation string
	var started int64
	var completed sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT pending, generation, started_at, completed_at FROM recovery WHERE singleton = 1`).
		Scan(&pending, &generation, &started, &completed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return selfhost.RecoveryStatus{}, fmt.Errorf("%w: the recovery row is missing", selfhost.ErrRecoveryStateInvalid)
		}
		return selfhost.RecoveryStatus{}, fmt.Errorf("%w: %w", selfhost.ErrRecoveryStateInvalid, err)
	}
	switch pending {
	case 0:
		if generation == "" && started == 0 && !completed.Valid {
			return selfhost.RecoveryStatus{}, nil
		}
		if !validRecoveryGeneration(generation) || started <= 0 || !completed.Valid || completed.Int64 < started {
			return selfhost.RecoveryStatus{}, fmt.Errorf("%w: the completed recovery record is inconsistent", selfhost.ErrRecoveryStateInvalid)
		}
		completedAt := unixTime(completed.Int64)
		return selfhost.RecoveryStatus{
			Generation:  generation,
			StartedAt:   unixTime(started),
			CompletedAt: &completedAt,
		}, nil
	case 1:
		if !validRecoveryGeneration(generation) || started <= 0 || completed.Valid {
			return selfhost.RecoveryStatus{}, fmt.Errorf("%w: the pending recovery record is inconsistent", selfhost.ErrRecoveryStateInvalid)
		}
		return selfhost.RecoveryStatus{
			Pending:    true,
			Generation: generation,
			StartedAt:  unixTime(started),
		}, nil
	default:
		return selfhost.RecoveryStatus{}, fmt.Errorf("%w: the recovery pending value is not recognized", selfhost.ErrRecoveryStateInvalid)
	}
}

func (s *Store) RecoveryStatus(ctx context.Context) (selfhost.RecoveryStatus, error) {
	db, err := s.read()
	if err != nil {
		return selfhost.RecoveryStatus{}, err
	}
	return readRecoveryStatus(ctx, db)
}

func (s *Store) requireRecovered(ctx context.Context) error {
	status, err := s.RecoveryStatus(ctx)
	if err != nil {
		return err
	}
	if status.Pending {
		return fmt.Errorf("%w: this database was restored and needs local approval", selfhost.ErrRecoveryPending)
	}
	return nil
}

func readSingleInstanceID(ctx context.Context, db queryer) (string, error) {
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM instance`).Scan(&count); err != nil {
		return "", fmt.Errorf("%w: %w", selfhost.ErrRecoveryStateInvalid, err)
	}
	if count != 1 {
		return "", fmt.Errorf("%w: the database must contain exactly one instance row", selfhost.ErrRecoveryStateInvalid)
	}
	var id string
	if err := db.QueryRowContext(ctx, `SELECT id FROM instance WHERE singleton = 1`).Scan(&id); err != nil {
		return "", fmt.Errorf("%w: %w", selfhost.ErrRecoveryStateInvalid, err)
	}
	if id == "" {
		return "", fmt.Errorf("%w: the instance identity is empty", selfhost.ErrRecoveryStateInvalid)
	}
	return id, nil
}

func PrepareRestore(ctx context.Context, options Options) (int, error) {
	if options.Path == "" {
		return 0, errors.New("sqlitestore: database path is required")
	}
	info, err := os.Stat(options.Path)
	if err != nil {
		return 0, fmt.Errorf("sqlitestore: staged database cannot be opened: %w", err)
	}
	if !info.Mode().IsRegular() {
		return 0, errors.New("sqlitestore: staged database is not a regular file")
	}
	options.Preserve = true
	options.ReadOnly = false
	options.RequireRecovered = false
	store, err := Open(ctx, options)
	if err != nil {
		return 0, err
	}
	installed, err := store.storedVersion(ctx, store.db)
	if err != nil {
		return 0, closePreparedStore(store, fmt.Errorf("sqlitestore: read installed schema: %w", err))
	}
	if err := store.prepareRecovery(ctx); err != nil {
		return 0, closePreparedStore(store, err)
	}
	if err := store.checkpointRecovery(ctx); err != nil {
		return 0, closePreparedStore(store, err)
	}
	if err := store.Close(); err != nil {
		return 0, fmt.Errorf("sqlitestore: close prepared database: %w", err)
	}
	return installed, nil
}

func closePreparedStore(store *Store, cause error) error {
	if err := store.Close(); err != nil {
		return errors.Join(cause, fmt.Errorf("sqlitestore: close prepared database: %w", err))
	}
	return cause
}

func (s *Store) prepareRecovery(ctx context.Context) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		instanceID, err := readSingleInstanceID(ctx, tx)
		if err != nil {
			return err
		}
		if _, err := readRecoveryStatus(ctx, tx); err != nil {
			return err
		}
		generation, err := newID()
		if err != nil {
			return err
		}
		now := s.clock()
		updated, err := tx.ExecContext(ctx, `
			UPDATE recovery SET pending = 1, generation = ?, started_at = ?, completed_at = NULL
			WHERE singleton = 1
		`, generation, now.Unix())
		if err != nil {
			return err
		}
		if changed, err := updated.RowsAffected(); err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return fmt.Errorf("%w: the recovery row could not be marked pending", selfhost.ErrRecoveryStateInvalid)
		}
		sessions, err := execCount(ctx, tx, `DELETE FROM sessions`)
		if err != nil {
			return err
		}
		setupTokens, err := execCount(ctx, tx, `DELETE FROM setup_tokens`)
		if err != nil {
			return err
		}
		pairings, err := execCount(ctx, tx, `
			UPDATE pairings SET cancelled_at = ? WHERE used_at IS NULL AND cancelled_at IS NULL
		`, now.Unix())
		if err != nil {
			return err
		}
		syncPairings, err := execCount(ctx, tx, `DELETE FROM sync_pairings`)
		if err != nil {
			return err
		}
		detail := map[string]string{
			"generation":   generation,
			"sessions":     fmt.Sprint(sessions),
			"setupTokens":  fmt.Sprint(setupTokens),
			"pairings":     fmt.Sprint(pairings),
			"syncPairings": fmt.Sprint(syncPairings),
		}
		if err := s.audit(ctx, tx, selfhost.SystemActor, "recovery.pending", instanceID, detail); err != nil {
			return err
		}
		status, err := readRecoveryStatus(ctx, tx)
		if err != nil {
			return err
		}
		if !status.Pending || status.Generation != generation || status.CompletedAt != nil || !status.StartedAt.Equal(now) {
			return fmt.Errorf("%w: the pending recovery state did not read back as written", selfhost.ErrRecoveryStateInvalid)
		}
		return requireNoRecoveryTransients(ctx, tx)
	})
}

func (s *Store) checkpointRecovery(ctx context.Context) error {
	var busy, logFrames, checkpointed int64
	if err := s.db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &checkpointed); err != nil {
		return fmt.Errorf("sqlitestore: checkpoint the restored database: %w", err)
	}
	if busy != 0 {
		return errors.New("sqlitestore: checkpoint the restored database: the checkpoint is busy")
	}
	if checkpointed != logFrames {
		return fmt.Errorf("sqlitestore: checkpoint the restored database: only %d of %d frames were checkpointed", checkpointed, logFrames)
	}
	return nil
}

type recoveryOwnerState struct {
	complete bool
	removed  bool
}

type recoveryDeviceState struct {
	ownerID  sql.NullString
	memberID sql.NullString
	expires  int64
	revoked  sql.NullInt64
}

func (s *Store) CompleteRecovery(ctx context.Context, by selfhost.Actor, generation string, selection selfhost.RecoverySelection) (selfhost.RecoveryResult, error) {
	var result selfhost.RecoveryResult
	if !validRecoveryGeneration(generation) {
		return result, fmt.Errorf("%w: the generation is not 32 lowercase hexadecimal characters", selfhost.ErrRecoveryGenerationMismatch)
	}
	owners, err := recoveryIDSet("owner", selection.OwnerIDs)
	if err != nil {
		return result, err
	}
	devices, err := recoveryIDSet("device", selection.DeviceIDs)
	if err != nil {
		return result, err
	}
	ownerIDs := sortedRecoveryIDs(owners)
	deviceIDs := sortedRecoveryIDs(devices)
	err = s.write(ctx, func(tx *sql.Tx) error {
		instanceID, err := readSingleInstanceID(ctx, tx)
		if err != nil {
			return err
		}
		status, err := readRecoveryStatus(ctx, tx)
		if err != nil {
			return err
		}
		if status.Generation != generation {
			return fmt.Errorf("%w: the request does not match the current restore generation", selfhost.ErrRecoveryGenerationMismatch)
		}
		if !status.Pending {
			if status.CompletedAt == nil {
				return fmt.Errorf("%w: the recovery is not pending", selfhost.ErrRecoveryGenerationMismatch)
			}
			result = selfhost.RecoveryResult{AlreadyComplete: true}
			return nil
		}
		ownerStates, err := loadRecoveryOwners(ctx, tx)
		if err != nil {
			return err
		}
		completeOwners := 0
		for _, state := range ownerStates {
			if !state.removed && state.complete {
				completeOwners++
			}
		}
		if completeOwners > 0 && len(owners) == 0 {
			return fmt.Errorf("%w: this restore contains complete owners", selfhost.ErrRecoveryOwnerRequired)
		}
		for id := range owners {
			state, found := ownerStates[id]
			if !found || state.removed || !state.complete {
				return fmt.Errorf("%w: owner %q is unknown, removed or incomplete", selfhost.ErrRecoverySelectionInvalid, id)
			}
		}
		memberStates, err := loadRecoveryMembers(ctx, tx)
		if err != nil {
			return err
		}
		deviceStates, err := loadRecoveryDevices(ctx, tx)
		if err != nil {
			return err
		}
		now := s.clock()
		for id := range devices {
			state, found := deviceStates[id]
			if !found {
				return fmt.Errorf("%w: device %q is unknown", selfhost.ErrRecoverySelectionInvalid, id)
			}
			if state.revoked.Valid || state.expires <= now.Unix() {
				return fmt.Errorf("%w: device %q is revoked or expired", selfhost.ErrRecoverySelectionInvalid, id)
			}
			if state.ownerID.Valid {
				if _, retained := owners[state.ownerID.String]; !retained {
					return fmt.Errorf("%w: device %q belongs to an owner that is not retained", selfhost.ErrRecoverySelectionInvalid, id)
				}
				continue
			}
			if !state.memberID.Valid {
				return fmt.Errorf("%w: device %q has no holder", selfhost.ErrRecoverySelectionInvalid, id)
			}
			memberRemoved, found := memberStates[state.memberID.String]
			if !found || memberRemoved {
				return fmt.Errorf("%w: device %q belongs to a removed member", selfhost.ErrRecoverySelectionInvalid, id)
			}
		}
		removedOwners, err := removeUnretainedOwners(ctx, tx, now.Unix(), ownerIDs)
		if err != nil {
			return err
		}
		revokedDevices, err := revokeUnretainedDevices(ctx, tx, now.Unix(), deviceIDs)
		if err != nil {
			return err
		}
		if _, err := execCount(ctx, tx, `DELETE FROM sessions`); err != nil {
			return err
		}
		if _, err := execCount(ctx, tx, `DELETE FROM setup_tokens`); err != nil {
			return err
		}
		if _, err := execCount(ctx, tx, `
			UPDATE pairings SET cancelled_at = ? WHERE used_at IS NULL AND cancelled_at IS NULL
		`, now.Unix()); err != nil {
			return err
		}
		if _, err := execCount(ctx, tx, `DELETE FROM sync_pairings`); err != nil {
			return err
		}
		detail := map[string]string{
			"generation":      generation,
			"retainedOwners":  fmt.Sprint(len(ownerIDs)),
			"removedOwners":   fmt.Sprint(removedOwners),
			"retainedDevices": fmt.Sprint(len(deviceIDs)),
			"revokedDevices":  fmt.Sprint(revokedDevices),
		}
		if err := s.audit(ctx, tx, by, "recovery.completed", instanceID, detail); err != nil {
			return err
		}
		completedAt := now
		if status.StartedAt.After(completedAt) {
			completedAt = status.StartedAt
		}
		updated, err := tx.ExecContext(ctx, `
			UPDATE recovery SET pending = 0, completed_at = ?
			WHERE singleton = 1 AND pending = 1 AND generation = ?
		`, completedAt.Unix(), generation)
		if err != nil {
			return err
		}
		if changed, err := updated.RowsAffected(); err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return fmt.Errorf("%w: the recovery was completed by another transaction", selfhost.ErrRecoveryGenerationMismatch)
		}
		completed, err := readRecoveryStatus(ctx, tx)
		if err != nil {
			return err
		}
		if completed.Pending || completed.Generation != generation || completed.CompletedAt == nil || !completed.CompletedAt.Equal(completedAt) {
			return fmt.Errorf("%w: the completed recovery state did not read back as written", selfhost.ErrRecoveryStateInvalid)
		}
		if err := requireRetainedOwners(ctx, tx, ownerIDs); err != nil {
			return err
		}
		if err := requireRetainedDevices(ctx, tx, deviceIDs); err != nil {
			return err
		}
		if err := requireNoRecoveryTransients(ctx, tx); err != nil {
			return err
		}
		result = selfhost.RecoveryResult{
			RetainedOwners:  len(ownerIDs),
			RemovedOwners:   removedOwners,
			RetainedDevices: len(deviceIDs),
			RevokedDevices:  revokedDevices,
		}
		return nil
	})
	if err != nil {
		return selfhost.RecoveryResult{}, err
	}
	return result, nil
}

func recoveryIDSet(kind string, ids []string) (map[string]struct{}, error) {
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id == "" || len(id) > maxRecoveryIDLength {
			return nil, fmt.Errorf("%w: an %s ID is empty or too long", selfhost.ErrRecoverySelectionInvalid, kind)
		}
		if _, duplicate := set[id]; duplicate {
			return nil, fmt.Errorf("%w: %s %q is selected twice", selfhost.ErrRecoverySelectionInvalid, kind, id)
		}
		set[id] = struct{}{}
	}
	return set, nil
}

func sortedRecoveryIDs(set map[string]struct{}) []string {
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func loadRecoveryOwners(ctx context.Context, db queryer) (map[string]recoveryOwnerState, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, password_hash IS NOT NULL, removed_at IS NOT NULL FROM owners`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	owners := map[string]recoveryOwnerState{}
	for rows.Next() {
		var id string
		var complete, removed int
		if err := rows.Scan(&id, &complete, &removed); err != nil {
			return nil, err
		}
		owners[id] = recoveryOwnerState{complete: complete == 1, removed: removed == 1}
	}
	return owners, rows.Err()
}

func loadRecoveryMembers(ctx context.Context, db queryer) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, removed_at IS NOT NULL FROM members`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	members := map[string]bool{}
	for rows.Next() {
		var id string
		var removed int
		if err := rows.Scan(&id, &removed); err != nil {
			return nil, err
		}
		members[id] = removed == 1
	}
	return members, rows.Err()
}

func loadRecoveryDevices(ctx context.Context, db queryer) (map[string]recoveryDeviceState, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, owner_id, member_id, expires_at, revoked_at FROM devices`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	devices := map[string]recoveryDeviceState{}
	for rows.Next() {
		var id string
		var state recoveryDeviceState
		if err := rows.Scan(&id, &state.ownerID, &state.memberID, &state.expires, &state.revoked); err != nil {
			return nil, err
		}
		devices[id] = state
	}
	return devices, rows.Err()
}

func removeUnretainedOwners(ctx context.Context, tx *sql.Tx, now int64, retained []string) (int, error) {
	query := `UPDATE owners SET removed_at = ?, password_hash = NULL, totp_secret = NULL WHERE removed_at IS NULL`
	args := []any{now}
	if len(retained) > 0 {
		query += ` AND id NOT IN (` + placeholders(len(retained)) + `)`
		for _, id := range retained {
			args = append(args, id)
		}
	}
	return execCount(ctx, tx, query, args...)
}

func revokeUnretainedDevices(ctx context.Context, tx *sql.Tx, now int64, retained []string) (int, error) {
	query := `UPDATE devices SET revoked_at = ? WHERE revoked_at IS NULL`
	args := []any{now}
	if len(retained) > 0 {
		query += ` AND id NOT IN (` + placeholders(len(retained)) + `)`
		for _, id := range retained {
			args = append(args, id)
		}
	}
	return execCount(ctx, tx, query, args...)
}

func requireRetainedOwners(ctx context.Context, db queryer, retained []string) error {
	if len(retained) == 0 {
		count, err := countQuery(ctx, db, `SELECT COUNT(*) FROM owners WHERE removed_at IS NULL`)
		if err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("%w: an owner remains active without being retained", selfhost.ErrRecoveryStateInvalid)
		}
		return nil
	}
	count, err := countQuery(ctx, db, `
		SELECT COUNT(*) FROM owners
		WHERE id IN (`+placeholders(len(retained))+`) AND removed_at IS NULL AND password_hash IS NOT NULL AND totp_secret IS NOT NULL
	`, stringArgs(retained)...)
	if err != nil {
		return err
	}
	if count != len(retained) {
		return fmt.Errorf("%w: a retained owner is missing or lost credentials", selfhost.ErrRecoveryStateInvalid)
	}
	leftover, err := countQuery(ctx, db, `
		SELECT COUNT(*) FROM owners WHERE removed_at IS NULL AND id NOT IN (`+placeholders(len(retained))+`)
	`, stringArgs(retained)...)
	if err != nil {
		return err
	}
	if leftover != 0 {
		return fmt.Errorf("%w: an owner remains active without being retained", selfhost.ErrRecoveryStateInvalid)
	}
	return nil
}

func requireRetainedDevices(ctx context.Context, db queryer, retained []string) error {
	if len(retained) == 0 {
		count, err := countQuery(ctx, db, `SELECT COUNT(*) FROM devices WHERE revoked_at IS NULL`)
		if err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("%w: a device remains active without being retained", selfhost.ErrRecoveryStateInvalid)
		}
		return nil
	}
	count, err := countQuery(ctx, db, `
		SELECT COUNT(*) FROM devices WHERE id IN (`+placeholders(len(retained))+`) AND revoked_at IS NULL
	`, stringArgs(retained)...)
	if err != nil {
		return err
	}
	if count != len(retained) {
		return fmt.Errorf("%w: a retained device is missing or revoked", selfhost.ErrRecoveryStateInvalid)
	}
	leftover, err := countQuery(ctx, db, `
		SELECT COUNT(*) FROM devices WHERE revoked_at IS NULL AND id NOT IN (`+placeholders(len(retained))+`)
	`, stringArgs(retained)...)
	if err != nil {
		return err
	}
	if leftover != 0 {
		return fmt.Errorf("%w: a device remains active without being retained", selfhost.ErrRecoveryStateInvalid)
	}
	return nil
}

func requireNoRecoveryTransients(ctx context.Context, db queryer) error {
	checks := []struct {
		name  string
		query string
	}{
		{"sessions", `SELECT COUNT(*) FROM sessions`},
		{"setup tokens", `SELECT COUNT(*) FROM setup_tokens`},
		{"desktop pairings", `SELECT COUNT(*) FROM pairings WHERE used_at IS NULL AND cancelled_at IS NULL`},
		{"Sync pairings", `SELECT COUNT(*) FROM sync_pairings`},
	}
	for _, check := range checks {
		count, err := countQuery(ctx, db, check.query)
		if err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("%w: %d %s remain", selfhost.ErrRecoveryStateInvalid, count, check.name)
		}
	}
	return nil
}

func execCount(ctx context.Context, db queryer, query string, args ...any) (int, error) {
	result, err := db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(changed), nil
}

func countQuery(ctx context.Context, db queryer, query string, args ...any) (int, error) {
	var count int
	if err := db.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func placeholders(count int) string {
	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}

func stringArgs(values []string) []any {
	args := make([]any, len(values))
	for index, value := range values {
		args[index] = value
	}
	return args
}
