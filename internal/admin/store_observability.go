package admin

import "context"

func (s *Store) Overview(ctx context.Context) (Overview, error) {
	var overview Overview
	err := s.db.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM sesame_accounts),
			(SELECT COUNT(*) FROM sesame_accounts WHERE created_at >= NOW() - INTERVAL '7 days'),
			(SELECT COUNT(*) FROM sesame_accounts WHERE beta_access = TRUE),
			(SELECT COUNT(*) FROM sesame_accounts WHERE email_verified_at IS NULL),
			(SELECT COUNT(*) FROM sesame_accounts WHERE suspended_at IS NOT NULL),
			(SELECT COUNT(*) FROM sesame_admin_sessions WHERE expires_at > NOW()),
			(SELECT COUNT(*) FROM sesame_support_requests WHERE status IN ('open', 'in_progress', 'waiting')),
			(SELECT COUNT(*) FROM sesame_support_requests WHERE status IN ('open', 'in_progress', 'waiting') AND assigned_admin_id IS NULL),
			(SELECT COUNT(*) FROM sesame_support_requests WHERE status IN ('open', 'in_progress', 'waiting') AND priority = 'urgent')
	`).Scan(&overview.Users, &overview.NewUsersThisWeek, &overview.BetaUsers, &overview.UnverifiedUsers, &overview.SuspendedUsers, &overview.ActiveAdminSessions, &overview.OpenTickets, &overview.UnassignedTickets, &overview.UrgentTickets)
	return overview, err
}

func (s *Store) RateLimitMetrics(ctx context.Context) ([]RateLimitMetric, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT split_part(key, ':', 1), COUNT(*), SUM(attempts), MAX(updated_at) FROM sesame_rate_limits GROUP BY split_part(key, ':', 1) ORDER BY MAX(updated_at) DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	metrics := []RateLimitMetric{}
	for rows.Next() {
		var metric RateLimitMetric
		if err := rows.Scan(&metric.Operation, &metric.Buckets, &metric.Attempts, &metric.UpdatedAt); err != nil {
			return nil, err
		}
		metrics = append(metrics, metric)
	}
	return metrics, rows.Err()
}

func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }
