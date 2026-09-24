package accounts

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// PendingMigrations reports the migration versions embedded in this binary that
// the database has not applied. The API uses it to refuse to serve on a stale
// schema instead of failing later on a missing column or table.
func (s *PostgresStore) PendingMigrations(ctx context.Context) ([]string, error) {
	migrations, err := loadMigrations()
	if err != nil {
		return nil, err
	}
	applied := make(map[string]bool, len(migrations))
	rows, err := s.db.QueryContext(ctx, `SELECT version FROM sesame_schema_migrations`)
	if err != nil {
		var postgresError *pgconn.PgError
		// A database that never ran the migrate job has no tracking table yet.
		// That is the most stale schema possible, not an internal error.
		if !errors.As(err, &postgresError) || postgresError.Code != "42P01" {
			return nil, err
		}
		return missingMigrationVersions(migrations, applied), nil
	}
	defer rows.Close()
	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			return nil, err
		}
		applied[version] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return missingMigrationVersions(migrations, applied), nil
}

func missingMigrationVersions(migrations []migration, applied map[string]bool) []string {
	pending := make([]string, 0)
	for _, m := range migrations {
		if !applied[m.version] {
			pending = append(pending, m.version)
		}
	}
	return pending
}
