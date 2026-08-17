package accounts

import "context"

// Opt-in categories only; security notifications are deliberately absent because they are mandatory.
type NotificationPreferences struct {
	BetaReleases         bool `json:"betaReleases"`
	SupportReplies       bool `json:"supportReplies"`
	ProductAnnouncements bool `json:"productAnnouncements"`
}

type NotificationPreferencesStore interface {
	NotificationPreferences(context.Context, string) (NotificationPreferences, error)
	UpdateNotificationPreferences(context.Context, string, NotificationPreferences) error
}

var _ NotificationPreferencesStore = (*PostgresStore)(nil)

func (s *PostgresStore) NotificationPreferences(ctx context.Context, accountID string) (NotificationPreferences, error) {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO sesame_account_notification_preferences (account_id) VALUES ($1) ON CONFLICT (account_id) DO NOTHING`, accountID); err != nil {
		return NotificationPreferences{}, err
	}
	var preferences NotificationPreferences
	err := s.db.QueryRowContext(ctx, `
		SELECT beta_releases, support_replies, product_announcements
		FROM sesame_account_notification_preferences WHERE account_id = $1
	`, accountID).Scan(&preferences.BetaReleases, &preferences.SupportReplies, &preferences.ProductAnnouncements)
	return preferences, err
}

func (s *PostgresStore) UpdateNotificationPreferences(ctx context.Context, accountID string, preferences NotificationPreferences) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sesame_account_notification_preferences (account_id, beta_releases, support_replies, product_announcements)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (account_id) DO UPDATE SET beta_releases = EXCLUDED.beta_releases,
			support_replies = EXCLUDED.support_replies, product_announcements = EXCLUDED.product_announcements,
			updated_at = now()
	`, accountID, preferences.BetaReleases, preferences.SupportReplies, preferences.ProductAnnouncements)
	return err
}
