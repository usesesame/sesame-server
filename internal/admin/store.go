package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"usesesame.app/backend/internal/accounts"
)

var (
	ErrNotFound      = errors.New("admin record not found")
	ErrNotAllowed    = errors.New("admin action not allowed")
	ErrBootstrapDone = errors.New("a super admin already exists")
	ErrTOTPReplay    = errors.New("admin TOTP code was already used")
	// Fails closed like a wrong password; its own error tells the operator the key is wrong.
	ErrSecretUnreadable = errors.New("admin MFA secret cannot be decrypted with the configured key")

	// Grace for the begin-then-complete setup flow after the first read consumes the setup token.
	adminSetupGrace = 30 * time.Minute
)

type Store struct {
	db            *sql.DB
	encryptionKey []byte
}

func Open(ctx context.Context, databaseURL string, encryptionKey []byte) (*Store, error) {
	if len(encryptionKey) != 32 {
		return nil, errors.New("admin encryption key must be 32 bytes")
	}
	config, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		return nil, errors.New("database URL is invalid")
	}
	db := stdlib.OpenDB(*config)
	db.SetMaxOpenConns(6)
	db.SetMaxIdleConns(3)
	db.SetConnMaxLifetime(30 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, encryptionKey: append([]byte(nil), encryptionKey...)}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) UnreadableSecrets(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT email, totp_secret FROM sesame_admin_accounts ORDER BY email`)
	if err != nil {
		return nil, fmt.Errorf("list admin accounts: %w", err)
	}
	defer rows.Close()
	var unreadable []string
	for rows.Next() {
		var email string
		var encrypted []byte
		if err := rows.Scan(&email, &encrypted); err != nil {
			return nil, fmt.Errorf("read admin account: %w", err)
		}
		if _, err := decryptSecret(s.encryptionKey, encrypted); err != nil {
			unreadable = append(unreadable, email)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate admin accounts: %w", err)
	}
	return unreadable, nil
}

func (s *Store) SecretReadable(ctx context.Context, email string) (bool, error) {
	var encrypted []byte
	err := s.db.QueryRowContext(ctx, `SELECT totp_secret FROM sesame_admin_accounts WHERE email = $1`, normalizeEmail(email)).Scan(&encrypted)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, fmt.Errorf("read admin secret: %w", err)
	}
	_, decryptErr := decryptSecret(s.encryptionKey, encrypted)
	return decryptErr == nil, nil
}

func newID() (string, error) {
	token, _, err := NewToken()
	if err != nil {
		return "", err
	}
	return token[:32], nil
}

func normalizeEmail(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

func (s *Store) BootstrapSuper(ctx context.Context, email string, expiresAt time.Time) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sesame_admin_accounts`).Scan(&count); err != nil {
		return "", err
	}
	if count != 0 {
		return "", ErrBootstrapDone
	}
	id, err := newID()
	if err != nil {
		return "", err
	}
	placeholder, err := accounts.HashPassword("disabled-" + id)
	if err != nil {
		return "", err
	}
	secret, err := NewTOTPSecret()
	if err != nil {
		return "", err
	}
	encrypted, err := encryptSecret(s.encryptionKey, []byte(secret))
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO sesame_admin_accounts (id, email, password_hash, role, totp_secret) VALUES ($1, $2, $3, 'super', $4)`, id, normalizeEmail(email), placeholder, encrypted); err != nil {
		return "", err
	}
	token, tokenHash, err := NewToken()
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO sesame_admin_setup_tokens (token_hash, admin_id, expires_at) VALUES ($1, $2, $3)`, tokenHash, id, expiresAt); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return token, nil
}

// Available only to the local operator CLI.
func (s *Store) ResetSetup(ctx context.Context, email string, expiresAt time.Time) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	var id string
	var suspended bool
	if err := tx.QueryRowContext(ctx, `SELECT id, suspended FROM sesame_admin_accounts WHERE email = $1 FOR UPDATE`, normalizeEmail(email)).Scan(&id, &suspended); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	if suspended {
		return "", ErrNotAllowed
	}
	placeholder, err := accounts.HashPassword("disabled-" + id)
	if err != nil {
		return "", err
	}
	secret, err := NewTOTPSecret()
	if err != nil {
		return "", err
	}
	encrypted, err := encryptSecret(s.encryptionKey, []byte(secret))
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE sesame_admin_accounts
		SET password_hash = $2, totp_secret = $3, totp_verified = FALSE,
		    totp_last_used_counter = 0, last_login_at = NULL
		WHERE id = $1`, id, placeholder, encrypted); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sesame_admin_sessions WHERE admin_id = $1`, id); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sesame_admin_setup_tokens WHERE admin_id = $1`, id); err != nil {
		return "", err
	}
	token, tokenHash, err := NewToken()
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO sesame_admin_setup_tokens (token_hash, admin_id, expires_at) VALUES ($1, $2, $3)`, tokenHash, id, expiresAt); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO sesame_admin_audit_log (admin_id, admin_email, action, target_type, target_id, detail, ip_hash)
		VALUES (NULL, $1, 'admin.reset.requested', 'admin', $2, '{}'::jsonb, '')`, normalizeEmail(email), id); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return token, nil
}

// A captured setup link must not keep yielding the MFA secret for the full
// token lifetime: the first read consumes the token, and later reads stay
// readable only inside the grace window while the account is still unverified.
func (s *Store) SetupDetails(ctx context.Context, tokenHash []byte, now time.Time) (Account, string, error) {
	var account Account
	var encrypted []byte
	err := s.db.QueryRowContext(ctx, `
		UPDATE sesame_admin_setup_tokens AS setup
		SET used_at = COALESCE(setup.used_at, $2)
		FROM sesame_admin_accounts AS admin
		WHERE setup.token_hash = $1
		  AND setup.expires_at > $2
		  AND (setup.used_at IS NULL OR setup.used_at > $3)
		  AND admin.id = setup.admin_id
		  AND admin.suspended = FALSE
		  AND admin.totp_verified = FALSE
		RETURNING admin.id, admin.email, admin.role, admin.totp_verified, admin.suspended,
		          admin.created_at, COALESCE(admin.last_login_at, '0001-01-01'::timestamptz), admin.totp_secret
	`, tokenHash, now, now.Add(-adminSetupGrace)).Scan(&account.ID, &account.Email, &account.Role, &account.MFAVerified, &account.Suspended, &account.CreatedAt, &account.LastLoginAt, &encrypted)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, "", ErrNotFound
	}
	if err != nil {
		return Account{}, "", err
	}
	plain, err := decryptSecret(s.encryptionKey, encrypted)
	if err != nil {
		return Account{}, "", fmt.Errorf("%w: %w", ErrSecretUnreadable, err)
	}
	return account, string(plain), nil
}

func (s *Store) CompleteSetup(ctx context.Context, tokenHash []byte, passwordHash string, now time.Time, ipHash string, totpCounter int64) (Account, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Account{}, err
	}
	defer tx.Rollback()
	var adminID string
	err = tx.QueryRowContext(ctx, `
		UPDATE sesame_admin_setup_tokens AS setup
		SET used_at = $2
		FROM sesame_admin_accounts AS admin
		WHERE setup.token_hash = $1
		  AND setup.expires_at > $2
		  AND (setup.used_at IS NULL OR setup.used_at > $3)
		  AND admin.id = setup.admin_id
		  AND admin.totp_verified = FALSE
		RETURNING setup.admin_id`, tokenHash, now, now.Add(-adminSetupGrace)).Scan(&adminID)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	if err != nil {
		return Account{}, err
	}
	var account Account
	err = tx.QueryRowContext(ctx, `
		UPDATE sesame_admin_accounts
		SET password_hash = $2, totp_verified = TRUE,
		    totp_last_used_counter = GREATEST(totp_last_used_counter, $3)
		WHERE id = $1 AND suspended = FALSE
		RETURNING id, email, role, totp_verified, suspended, created_at,
		          COALESCE(last_login_at, '0001-01-01'::timestamptz)
	`, adminID, passwordHash, totpCounter).Scan(&account.ID, &account.Email, &account.Role, &account.MFAVerified, &account.Suspended, &account.CreatedAt, &account.LastLoginAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	if err != nil {
		return Account{}, err
	}
	if err := insertAudit(ctx, tx, account, "admin.setup.complete", "admin", account.ID, map[string]any{"role": account.Role, "totp_counter": totpCounter}, ipHash); err != nil {
		return Account{}, err
	}
	if err := tx.Commit(); err != nil {
		return Account{}, err
	}
	return account, nil
}

func (s *Store) FindByEmail(ctx context.Context, email string) (Account, string, string, int64, error) {
	var account Account
	var passwordHash string
	var encrypted []byte
	var lastUsedCounter int64
	err := s.db.QueryRowContext(ctx, `
		SELECT id, email, role, totp_verified, suspended, created_at,
			COALESCE(last_login_at, '0001-01-01'::timestamptz), password_hash, totp_secret, totp_last_used_counter
		FROM sesame_admin_accounts WHERE email = $1
	`, normalizeEmail(email)).Scan(&account.ID, &account.Email, &account.Role, &account.MFAVerified, &account.Suspended, &account.CreatedAt, &account.LastLoginAt, &passwordHash, &encrypted, &lastUsedCounter)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, "", "", 0, ErrNotFound
	}
	if err != nil {
		return Account{}, "", "", 0, err
	}
	plain, err := decryptSecret(s.encryptionKey, encrypted)
	if err != nil {
		return Account{}, "", "", 0, fmt.Errorf("%w: %w", ErrSecretUnreadable, err)
	}
	return account, passwordHash, string(plain), lastUsedCounter, nil
}

func (s *Store) CreateSession(ctx context.Context, actor Account, tokenHash []byte, ipHash, userAgent string, expiresAt time.Time, totpCounter int64) error {
	if len(userAgent) > 200 {
		userAgent = userAgent[:200]
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		UPDATE sesame_admin_accounts
		SET last_login_at = NOW(), totp_last_used_counter = $2
		WHERE id = $1 AND suspended = FALSE AND totp_last_used_counter < $2`,
		actor.ID, totpCounter)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated != 1 {
		return ErrTOTPReplay
	}
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO sesame_admin_sessions (token_hash, admin_id, ip_hash, user_agent, expires_at)
		VALUES ($1, $2, $3, $4, $5)
	`, tokenHash, actor.ID, ipHash, userAgent, expiresAt); err != nil {
		return err
	}
	if err = insertAudit(ctx, tx, actor, "admin.login", "admin", actor.ID, map[string]any{"totp_counter": totpCounter}, ipHash); err != nil {
		return err
	}
	return tx.Commit()
}

// CompleteSetup already consumed the setup token and TOTP counter atomically.
func (s *Store) CreateSessionAfterSetup(ctx context.Context, actor Account, tokenHash []byte, ipHash, userAgent string, expiresAt time.Time, totpCounter int64) error {
	if len(userAgent) > 200 {
		userAgent = userAgent[:200]
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		UPDATE sesame_admin_accounts
		SET last_login_at = NOW()
		WHERE id = $1 AND suspended = FALSE AND totp_last_used_counter = $2`,
		actor.ID, totpCounter)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated != 1 {
		return ErrTOTPReplay
	}
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO sesame_admin_sessions (token_hash, admin_id, ip_hash, user_agent, expires_at)
		VALUES ($1, $2, $3, $4, $5)
	`, tokenHash, actor.ID, ipHash, userAgent, expiresAt); err != nil {
		return err
	}
	if err = insertAudit(ctx, tx, actor, "admin.login", "admin", actor.ID, map[string]any{"totp_counter": totpCounter, "setup_session": true}, ipHash); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) AccountBySession(ctx context.Context, tokenHash []byte) (Account, error) {
	var account Account
	err := s.db.QueryRowContext(ctx, `
		SELECT admin.id, admin.email, admin.role, admin.totp_verified, admin.suspended,
			admin.created_at, COALESCE(admin.last_login_at, '0001-01-01'::timestamptz)
		FROM sesame_admin_sessions session
		JOIN sesame_admin_accounts admin ON admin.id = session.admin_id
		WHERE session.token_hash = $1 AND session.expires_at > NOW() AND admin.suspended = FALSE AND admin.totp_verified = TRUE
	`, tokenHash).Scan(&account.ID, &account.Email, &account.Role, &account.MFAVerified, &account.Suspended, &account.CreatedAt, &account.LastLoginAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	return account, err
}

func (s *Store) DeleteSession(ctx context.Context, actor Account, tokenHash []byte, ipHash string) error {
	return s.mutate(ctx, actor, "admin.logout", "admin", actor.ID, ipHash, map[string]any{}, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM sesame_admin_sessions WHERE token_hash = $1 AND admin_id = $2`, tokenHash, actor.ID)
		return err
	})
}

func (s *Store) Users(ctx context.Context, query string, page, size int) ([]UserSummary, int, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 25
	}
	needle := "%" + strings.ToLower(strings.TrimSpace(query)) + "%"
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sesame_accounts WHERE LOWER(email) LIKE $1 OR LOWER(id) LIKE $1`, needle).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT account.id, account.email, account.email_verified_at IS NOT NULL, account.beta_access,
			account.suspended_at, account.created_at,
			(SELECT COUNT(*) FROM sesame_sessions session WHERE session.account_id = account.id AND session.expires_at > NOW()),
			(SELECT COUNT(*) FROM sesame_desktop_connections device WHERE device.account_id = account.id AND device.expires_at > NOW())
		FROM sesame_accounts account
		WHERE LOWER(account.email) LIKE $1 OR LOWER(account.id) LIKE $1
		ORDER BY account.created_at DESC LIMIT $2 OFFSET $3
	`, needle, size, (page-1)*size)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	users := make([]UserSummary, 0)
	for rows.Next() {
		var user UserSummary
		if err := rows.Scan(&user.ID, &user.Email, &user.EmailVerified, &user.BetaAccess, &user.SuspendedAt, &user.CreatedAt, &user.SessionCount, &user.DeviceCount); err != nil {
			return nil, 0, err
		}
		users = append(users, user)
	}
	return users, total, rows.Err()
}

func (s *Store) User(ctx context.Context, id string) (UserDetail, error) {
	var user UserDetail
	err := s.db.QueryRowContext(ctx, `
		SELECT id, email, email_verified_at IS NOT NULL, beta_access, suspended_at,
			COALESCE(suspended_reason, ''), created_at
		FROM sesame_accounts WHERE id = $1
	`, id).Scan(&user.ID, &user.Email, &user.EmailVerified, &user.BetaAccess, &user.SuspendedAt, &user.SuspendedReason, &user.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return UserDetail{}, ErrNotFound
	}
	if err != nil {
		return UserDetail{}, err
	}
	user.Sessions = []Session{}
	rows, err := s.db.QueryContext(ctx, `SELECT id, label, created_at, last_seen_at, expires_at FROM sesame_sessions WHERE account_id = $1 AND expires_at > NOW() ORDER BY last_seen_at DESC`, id)
	if err != nil {
		return UserDetail{}, err
	}
	for rows.Next() {
		var session Session
		if err := rows.Scan(&session.ID, &session.Label, &session.CreatedAt, &session.LastSeenAt, &session.ExpiresAt); err != nil {
			rows.Close() //nolint:sqlclosecheck // deliberate early close, see above
			return UserDetail{}, err
		}
		user.Sessions = append(user.Sessions, session)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return UserDetail{}, err
	}
	if err := rows.Close(); err != nil {
		return UserDetail{}, err
	}
	user.SessionCount = len(user.Sessions)
	user.Devices = []Device{}
	devices, err := s.db.QueryContext(ctx, `SELECT device_id, device_name, created_at, expires_at FROM sesame_desktop_connections WHERE account_id = $1 AND expires_at > NOW() ORDER BY created_at DESC`, id)
	if err != nil {
		return UserDetail{}, err
	}
	defer devices.Close()
	for devices.Next() {
		var device Device
		if err := devices.Scan(&device.ID, &device.Name, &device.ConnectedAt, &device.ExpiresAt); err != nil {
			return UserDetail{}, err
		}
		user.Devices = append(user.Devices, device)
	}
	user.DeviceCount = len(user.Devices)
	return user, devices.Err()
}

func insertAudit(ctx context.Context, tx *sql.Tx, actor Account, action, targetType, targetID string, detail map[string]any, ipHash string) error {
	encoded, err := json.Marshal(detail)
	if err != nil || len(encoded) > 4096 {
		return errors.New("audit detail is invalid")
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO sesame_admin_audit_log (admin_id, admin_email, action, target_type, target_id, detail, ip_hash)
		VALUES (NULLIF($1, ''), $2, $3, $4, NULLIF($5, ''), $6, $7)
	`, actor.ID, actor.Email, action, targetType, targetID, encoded, ipHash)
	return err
}

func (s *Store) mutate(ctx context.Context, actor Account, action, targetType, targetID, ipHash string, detail map[string]any, change func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := change(tx); err != nil {
		return err
	}
	if err := insertAudit(ctx, tx, actor, action, targetType, targetID, detail, ipHash); err != nil {
		return err
	}
	return tx.Commit()
}

func affected(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) SetBeta(ctx context.Context, actor Account, accountID string, enabled bool, ipHash string) error {
	action := "user.beta.revoke"
	if enabled {
		action = "user.beta.grant"
	}
	return s.mutate(ctx, actor, action, "account", accountID, ipHash, map[string]any{"enabled": enabled}, func(tx *sql.Tx) error {
		if enabled {
			return affected(tx.ExecContext(ctx, `UPDATE sesame_accounts SET beta_access = TRUE, beta_granted_by = $2, beta_granted_at = NOW() WHERE id = $1`, accountID, actor.ID))
		}
		return affected(tx.ExecContext(ctx, `UPDATE sesame_accounts SET beta_access = FALSE, beta_granted_by = NULL, beta_granted_at = NULL WHERE id = $1`, accountID))
	})
}

func (s *Store) SetSuspended(ctx context.Context, actor Account, accountID string, suspended bool, reason, ipHash string) error {
	action := "user.unsuspend"
	if suspended {
		action = "user.suspend"
	}
	detail := map[string]any{"suspended": suspended}
	if suspended && reason != "" {
		detail["reason"] = reason
	}
	return s.mutate(ctx, actor, action, "account", accountID, ipHash, detail, func(tx *sql.Tx) error {
		if suspended {
			if err := affected(tx.ExecContext(ctx, `UPDATE sesame_accounts SET suspended_at = NOW(), suspended_reason = $2 WHERE id = $1`, accountID, reason)); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `DELETE FROM sesame_sessions WHERE account_id = $1`, accountID)
			return err
		}
		return affected(tx.ExecContext(ctx, `UPDATE sesame_accounts SET suspended_at = NULL, suspended_reason = NULL WHERE id = $1`, accountID))
	})
}

func (s *Store) RevokeUserSessions(ctx context.Context, actor Account, accountID, ipHash string) error {
	return s.mutate(ctx, actor, "user.sessions.revoke", "account", accountID, ipHash, map[string]any{}, func(tx *sql.Tx) error {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sesame_accounts WHERE id = $1)`, accountID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM sesame_sessions WHERE account_id = $1`, accountID)
		return err
	})
}

func (s *Store) RevokeUserDevice(ctx context.Context, actor Account, accountID, deviceID, ipHash string) error {
	return s.mutate(ctx, actor, "user.device.revoke", "device", deviceID, ipHash, map[string]any{"accountId": accountID}, func(tx *sql.Tx) error {
		return affected(tx.ExecContext(ctx, `DELETE FROM sesame_desktop_connections WHERE account_id = $1 AND device_id = $2`, accountID, deviceID))
	})
}

func (s *Store) DeleteUser(ctx context.Context, actor Account, accountID, ipHash string) error {
	return s.mutate(ctx, actor, "user.delete", "account", accountID, ipHash, map[string]any{}, func(tx *sql.Tx) error {
		return affected(tx.ExecContext(ctx, `DELETE FROM sesame_accounts WHERE id = $1`, accountID))
	})
}

func (s *Store) FeatureFlag(ctx context.Context, key string) (string, error) {
	var value string
	if err := s.db.QueryRowContext(ctx, `SELECT value FROM sesame_feature_flags WHERE key = $1`, key).Scan(&value); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	return value, nil
}

func (s *Store) FeatureFlags(ctx context.Context) ([]FeatureFlag, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value, updated_at FROM sesame_feature_flags ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	flags := []FeatureFlag{}
	for rows.Next() {
		var flag FeatureFlag
		if err := rows.Scan(&flag.Key, &flag.Value, &flag.UpdatedAt); err != nil {
			return nil, err
		}
		flags = append(flags, flag)
	}
	return flags, rows.Err()
}

func (s *Store) UpdateFeatureFlag(ctx context.Context, actor Account, key, value, ipHash string) error {
	return s.mutate(ctx, actor, "flag.update", "flag", key, ipHash, map[string]any{"value": value}, func(tx *sql.Tx) error {
		return affected(tx.ExecContext(ctx, `UPDATE sesame_feature_flags SET value = $2, updated_by = $3, updated_at = NOW() WHERE key = $1`, key, value, actor.ID))
	})
}

func (s *Store) Plans(ctx context.Context) ([]Plan, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, price, COALESCE(annual_price, ''), billing, description, available, includes, updated_at FROM sesame_product_plans ORDER BY CASE id WHEN 'free' THEN 1 WHEN 'sync' THEN 2 ELSE 3 END, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	plans := []Plan{}
	for rows.Next() {
		var plan Plan
		var includes []byte
		if err := rows.Scan(&plan.ID, &plan.Name, &plan.Price, &plan.AnnualPrice, &plan.Billing, &plan.Description, &plan.Available, &includes, &plan.UpdatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(includes, &plan.Includes); err != nil {
			return nil, err
		}
		plans = append(plans, plan)
	}
	return plans, rows.Err()
}

func (s *Store) UpdatePlan(ctx context.Context, actor Account, plan Plan, ipHash string) error {
	includes, err := json.Marshal(plan.Includes)
	if err != nil {
		return err
	}
	annualPrice := any(plan.AnnualPrice)
	if plan.AnnualPrice == "" {
		annualPrice = nil
	}
	return s.mutate(ctx, actor, "plan.update", "plan", plan.ID, ipHash, map[string]any{"name": plan.Name, "price": plan.Price, "annualPrice": plan.AnnualPrice, "available": plan.Available}, func(tx *sql.Tx) error {
		return affected(tx.ExecContext(ctx, `UPDATE sesame_product_plans SET name = $2, price = $3, annual_price = $4, billing = $5, description = $6, available = $7, includes = $8, updated_by = $9, updated_at = NOW() WHERE id = $1`, plan.ID, plan.Name, plan.Price, annualPrice, plan.Billing, plan.Description, plan.Available, includes, actor.ID))
	})
}
func (s *Store) Admins(ctx context.Context) ([]Account, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, email, role, totp_verified, suspended, created_at, COALESCE(last_login_at, '0001-01-01'::timestamptz) FROM sesame_admin_accounts ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	admins := []Account{}
	for rows.Next() {
		var account Account
		if err := rows.Scan(&account.ID, &account.Email, &account.Role, &account.MFAVerified, &account.Suspended, &account.CreatedAt, &account.LastLoginAt); err != nil {
			return nil, err
		}
		admins = append(admins, account)
	}
	return admins, rows.Err()
}

func (s *Store) InviteAdmin(ctx context.Context, actor Account, email string, role Role, expiresAt time.Time, ipHash string) (Account, string, error) {
	if !ValidRole(role) {
		return Account{}, "", errors.New("invalid role")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Account{}, "", err
	}
	defer tx.Rollback()
	id, err := newID()
	if err != nil {
		return Account{}, "", err
	}
	placeholder, err := accounts.HashPassword("disabled-" + id)
	if err != nil {
		return Account{}, "", err
	}
	secret, err := NewTOTPSecret()
	if err != nil {
		return Account{}, "", err
	}
	encrypted, err := encryptSecret(s.encryptionKey, []byte(secret))
	if err != nil {
		return Account{}, "", err
	}
	created := Account{ID: id, Email: normalizeEmail(email), Role: role, CreatedAt: time.Now().UTC()}
	if _, err := tx.ExecContext(ctx, `INSERT INTO sesame_admin_accounts (id, email, password_hash, role, totp_secret, created_by) VALUES ($1,$2,$3,$4,$5,$6)`, created.ID, created.Email, placeholder, created.Role, encrypted, actor.ID); err != nil {
		return Account{}, "", err
	}
	token, tokenHash, err := NewToken()
	if err != nil {
		return Account{}, "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO sesame_admin_setup_tokens (token_hash, admin_id, expires_at) VALUES ($1,$2,$3)`, tokenHash, id, expiresAt); err != nil {
		return Account{}, "", err
	}
	if err := insertAudit(ctx, tx, actor, "admin.create", "admin", id, map[string]any{"email": created.Email, "role": role}, ipHash); err != nil {
		return Account{}, "", err
	}
	if err := tx.Commit(); err != nil {
		return Account{}, "", err
	}
	return created, token, nil
}

func (s *Store) UpdateAdmin(ctx context.Context, actor Account, id string, role Role, suspended bool, ipHash string) error {
	if !ValidRole(role) || id == actor.ID && (role != RoleSuper || suspended) {
		return ErrNotAllowed
	}
	return s.mutate(ctx, actor, "admin.update", "admin", id, ipHash, map[string]any{"role": role, "suspended": suspended}, func(tx *sql.Tx) error {
		if err := ensureActiveSuperRemains(ctx, tx, id, role == RoleSuper && !suspended); err != nil {
			return err
		}
		if err := affected(tx.ExecContext(ctx, `UPDATE sesame_admin_accounts SET role = $2, suspended = $3 WHERE id = $1`, id, role, suspended)); err != nil {
			return err
		}
		if suspended {
			_, err := tx.ExecContext(ctx, `DELETE FROM sesame_admin_sessions WHERE admin_id = $1`, id)
			return err
		}
		return nil
	})
}

func (s *Store) DeleteAdmin(ctx context.Context, actor Account, id, ipHash string) error {
	if id == actor.ID {
		return ErrNotAllowed
	}
	return s.mutate(ctx, actor, "admin.delete", "admin", id, ipHash, map[string]any{}, func(tx *sql.Tx) error {
		if err := ensureActiveSuperRemains(ctx, tx, id, false); err != nil {
			return err
		}
		return affected(tx.ExecContext(ctx, `DELETE FROM sesame_admin_accounts WHERE id = $1`, id))
	})
}

func ensureActiveSuperRemains(ctx context.Context, tx *sql.Tx, targetID string, targetWillBeActiveSuper bool) error {
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('sesame-admin-super-set'))`); err != nil {
		return err
	}
	var currentRole Role
	var currentSuspended bool
	if err := tx.QueryRowContext(ctx, `SELECT role, suspended FROM sesame_admin_accounts WHERE id = $1 FOR UPDATE`, targetID).Scan(&currentRole, &currentSuspended); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if currentRole != RoleSuper || currentSuspended || targetWillBeActiveSuper {
		return nil
	}
	var activeSupers int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sesame_admin_accounts WHERE role = 'super' AND suspended = FALSE`).Scan(&activeSupers); err != nil {
		return err
	}
	if activeSupers <= 1 {
		return ErrNotAllowed
	}
	return nil
}

func (s *Store) Audit(ctx context.Context, actor Account, all bool, filter AuditFilter, page, size int) ([]AuditEntry, int, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 200 {
		size = 50
	}
	conditions := []string{"TRUE"}
	args := make([]any, 0, 5)
	appendCondition := func(column string, value any) {
		args = append(args, value)
		conditions = append(conditions, fmt.Sprintf("%s = $%d", column, len(args)))
	}
	if filter.Action != "" {
		appendCondition("action", filter.Action)
	}
	if all && filter.AdminID != "" {
		appendCondition("admin_id", filter.AdminID)
	}
	if !filter.From.IsZero() {
		args = append(args, filter.From)
		conditions = append(conditions, fmt.Sprintf("created_at >= $%d", len(args)))
	}
	if !filter.To.IsZero() {
		args = append(args, filter.To)
		conditions = append(conditions, fmt.Sprintf("created_at <= $%d", len(args)))
	}
	if !all {
		appendCondition("admin_id", actor.ID)
	}
	where := "WHERE " + strings.Join(conditions, " AND ")
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sesame_admin_audit_log `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, size, (page-1)*size)
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`SELECT id, admin_id, admin_email, action, target_type, target_id, detail, created_at FROM sesame_admin_audit_log %s ORDER BY created_at DESC LIMIT $%d OFFSET $%d`, where, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	entries := []AuditEntry{}
	for rows.Next() {
		var entry AuditEntry
		var detail []byte
		if err := rows.Scan(&entry.ID, &entry.AdminID, &entry.AdminEmail, &entry.Action, &entry.TargetType, &entry.TargetID, &detail, &entry.CreatedAt); err != nil {
			return nil, 0, err
		}
		if err := json.Unmarshal(detail, &entry.Detail); err != nil {
			return nil, 0, err
		}
		entries = append(entries, entry)
	}
	return entries, total, rows.Err()
}

func (s *Store) Tickets(ctx context.Context, filter TicketListFilter, page, size int) ([]TicketSummary, int, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 25
	}
	conditions := []string{"TRUE"}
	args := make([]any, 0, 5)
	if filter.Status != "" {
		args = append(args, filter.Status)
		conditions = append(conditions, fmt.Sprintf("t.status = $%d", len(args)))
	}
	if filter.Priority != "" {
		args = append(args, filter.Priority)
		conditions = append(conditions, fmt.Sprintf("t.priority = $%d", len(args)))
	}
	if filter.Category != "" {
		args = append(args, filter.Category)
		conditions = append(conditions, fmt.Sprintf("t.category = $%d", len(args)))
	}
	if filter.Assigned == "unassigned" {
		conditions = append(conditions, "t.assigned_admin_id IS NULL")
	} else if filter.Assigned != "" {
		args = append(args, filter.Assigned)
		conditions = append(conditions, fmt.Sprintf("t.assigned_admin_id = $%d", len(args)))
	}
	if filter.Query != "" {
		args = append(args, "%"+filter.Query+"%")
		conditions = append(conditions, fmt.Sprintf("(t.email ILIKE $%d OR t.subject ILIKE $%d)", len(args), len(args)))
	}
	where := strings.Join(conditions, " AND ")
	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sesame_support_requests t WHERE "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, size, (page-1)*size)
	query := fmt.Sprintf(`
		SELECT t.id, t.email, t.subject, t.status, t.priority, t.category, t.app_version, t.diagnostic_code, t.browser_integration, t.request_id,
		       t.assigned_admin_id, t.created_at, t.updated_at, t.first_response_at, t.closed_at,
		       (SELECT COUNT(*) FROM sesame_support_messages m WHERE m.ticket_id = t.id)
		FROM sesame_support_requests t
		WHERE %s
		ORDER BY
		  CASE t.priority WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'normal' THEN 2 WHEN 'low' THEN 3 END,
		  t.updated_at DESC
		LIMIT $%d OFFSET $%d
	`, where, len(args)-1, len(args))
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	tickets := []TicketSummary{}
	for rows.Next() {
		var t TicketSummary
		var assigned sql.NullString
		var firstResponse, closedAt sql.NullTime
		if err := rows.Scan(&t.ID, &t.Email, &t.Subject, &t.Status, &t.Priority, &t.Category, &t.AppVersion, &t.DiagnosticCode, &t.BrowserIntegration, &t.RequestID, &assigned, &t.CreatedAt, &t.UpdatedAt, &firstResponse, &closedAt, &t.MessageCount); err != nil {
			return nil, 0, err
		}
		if assigned.Valid {
			id := assigned.String
			t.AssignedAdminID = &id
		}
		if firstResponse.Valid {
			fr := firstResponse.Time
			t.FirstResponseAt = &fr
		}
		if closedAt.Valid {
			cl := closedAt.Time
			t.ClosedAt = &cl
		}
		t.SLADueAt = t.CreatedAt.Add(24 * time.Hour)
		t.SLABreached = t.FirstResponseAt == nil && time.Now().UTC().After(t.SLADueAt) && t.Status != TicketClosed
		if t.Status != TicketClosed {
			t.QueuePosition = len(tickets) + 1
		}
		tickets = append(tickets, t)
	}
	return tickets, total, rows.Err()
}

func (s *Store) Ticket(ctx context.Context, ticketID string) (TicketDetail, error) {
	var d TicketDetail
	var accountID, assigned sql.NullString
	var firstResponse, closedAt sql.NullTime
	err := s.db.QueryRowContext(ctx, `
		SELECT id, email, subject, status, priority, category, app_version, diagnostic_code, browser_integration, request_id,
		       account_id, assigned_admin_id, created_at, updated_at, first_response_at, closed_at
		FROM sesame_support_requests WHERE id = $1
	`, ticketID).Scan(&d.ID, &d.Email, &d.Subject, &d.Status, &d.Priority, &d.Category, &d.AppVersion, &d.DiagnosticCode, &d.BrowserIntegration, &d.RequestID, &accountID, &assigned, &d.CreatedAt, &d.UpdatedAt, &firstResponse, &closedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return TicketDetail{}, ErrNotFound
	}
	if err != nil {
		return TicketDetail{}, err
	}
	if accountID.Valid {
		d.AccountID = accountID.String
		devices, err := s.db.QueryContext(ctx, `SELECT device_id, device_name, created_at, expires_at FROM sesame_desktop_connections WHERE account_id = $1 AND expires_at > NOW() ORDER BY created_at DESC`, d.AccountID)
		if err != nil {
			return TicketDetail{}, err
		}
		defer devices.Close()
		d.LinkedDevices = []Device{}
		for devices.Next() {
			var device Device
			if err := devices.Scan(&device.ID, &device.Name, &device.ConnectedAt, &device.ExpiresAt); err != nil {
				return TicketDetail{}, err
			}
			d.LinkedDevices = append(d.LinkedDevices, device)
		}
		if err := devices.Err(); err != nil {
			return TicketDetail{}, err
		}
	}
	if assigned.Valid {
		id := assigned.String
		d.AssignedAdminID = &id
	}
	if firstResponse.Valid {
		fr := firstResponse.Time
		d.FirstResponseAt = &fr
	}
	if closedAt.Valid {
		cl := closedAt.Time
		d.ClosedAt = &cl
	}
	d.SLADueAt = d.CreatedAt.Add(24 * time.Hour)
	d.SLABreached = d.FirstResponseAt == nil && time.Now().UTC().After(d.SLADueAt) && d.Status != TicketClosed
	if d.Status != TicketClosed {
		if err := s.db.QueryRowContext(ctx, `
			SELECT COUNT(*) + 1 FROM sesame_support_requests queue
			WHERE queue.status IN ('open', 'in_progress', 'waiting')
			  AND (CASE queue.priority WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'normal' THEN 2 ELSE 3 END,
			       queue.updated_at, queue.id)
		      < (CASE $2 WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'normal' THEN 2 ELSE 3 END,
			         $3, $1)
		`, d.ID, d.Priority, d.UpdatedAt).Scan(&d.QueuePosition); err != nil {
			return TicketDetail{}, err
		}
	}

	msgRows, err := s.db.QueryContext(ctx, `
		SELECT message.id, message.author_role, message.admin_email, message.body, message.sent_via_email, message.created_at,
			COALESCE(email.status, ''), COALESCE(email.attempts, 0), email.next_attempt_at
		FROM sesame_support_messages message
		LEFT JOIN LATERAL (
			SELECT status, attempts, next_attempt_at FROM sesame_email_outbox
			WHERE support_message_id = message.id ORDER BY created_at DESC LIMIT 1
		) email ON TRUE
		WHERE message.ticket_id = $1 ORDER BY message.created_at
	`, ticketID)
	if err != nil {
		return TicketDetail{}, err
	}
	defer msgRows.Close()
	d.Messages = []TicketMessage{}
	for msgRows.Next() {
		var m TicketMessage
		var emailStatus sql.NullString
		var nextAttempt sql.NullTime
		if err := msgRows.Scan(&m.ID, &m.AuthorRole, &m.AdminEmail, &m.Body, &m.SentViaEmail, &m.CreatedAt, &emailStatus, &m.EmailAttempts, &nextAttempt); err != nil {
			return TicketDetail{}, err
		}
		if emailStatus.Valid && emailStatus.String != "" {
			m.EmailDeliveryStatus = emailStatus.String
		}
		if nextAttempt.Valid {
			value := nextAttempt.Time
			m.EmailNextAttemptAt = &value
		}
		d.Messages = append(d.Messages, m)
	}
	if err := msgRows.Err(); err != nil {
		return TicketDetail{}, err
	}

	noteRows, err := s.db.QueryContext(ctx, `
		SELECT id, admin_email, body, created_at
		FROM sesame_support_notes WHERE ticket_id = $1 ORDER BY created_at
	`, ticketID)
	if err != nil {
		return TicketDetail{}, err
	}
	defer noteRows.Close()
	d.Notes = []TicketNote{}
	for noteRows.Next() {
		var n TicketNote
		if err := noteRows.Scan(&n.ID, &n.AdminEmail, &n.Body, &n.CreatedAt); err != nil {
			return TicketDetail{}, err
		}
		d.Notes = append(d.Notes, n)
	}
	return d, noteRows.Err()
}

func (s *Store) ReplyTicket(ctx context.Context, actor Account, ticketID, body string, sendEmail bool, ipHash string) (TicketDetail, error) {
	msgID, err := newID()
	if err != nil {
		return TicketDetail{}, err
	}
	err = s.mutate(ctx, actor, "ticket.reply", "ticket", ticketID, ipHash, map[string]any{
		"sentViaEmail": sendEmail,
		"bodyLength":   len(body),
	}, func(tx *sql.Tx) error {
		var status string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM sesame_support_requests WHERE id = $1 FOR UPDATE`, ticketID).Scan(&status); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if status == string(TicketClosed) {
			return errors.New("this ticket is closed; reopen it before replying")
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO sesame_support_messages (id, ticket_id, author_role, admin_id, admin_email, body, sent_via_email)
			VALUES ($1, $2, 'staff', $3, $4, $5, $6)
		`, msgID, ticketID, actor.ID, actor.Email, body, sendEmail); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE sesame_support_requests
			SET status = 'waiting', updated_at = NOW(),
			    first_response_at = COALESCE(first_response_at, NOW())
			WHERE id = $1
		`, ticketID); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return TicketDetail{}, err
	}
	return s.Ticket(ctx, ticketID)
}

func (s *Store) AddTicketNote(ctx context.Context, actor Account, ticketID, body string, ipHash string) (TicketNote, error) {
	noteID, err := newID()
	if err != nil {
		return TicketNote{}, err
	}
	var note TicketNote
	err = s.mutate(ctx, actor, "ticket.note", "ticket", ticketID, ipHash, map[string]any{
		"bodyLength": len(body),
	}, func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sesame_support_requests WHERE id = $1`, ticketID).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return ErrNotFound
		}
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO sesame_support_notes (id, ticket_id, admin_id, admin_email, body)
			VALUES ($1, $2, $3, $4, $5)
			RETURNING id, admin_email, body, created_at
		`, noteID, ticketID, actor.ID, actor.Email, body).Scan(&note.ID, &note.AdminEmail, &note.Body, &note.CreatedAt); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return TicketNote{}, err
	}
	return note, nil
}

func (s *Store) AssignTicket(ctx context.Context, actor Account, ticketID, adminID string, ipHash string) error {
	return s.mutate(ctx, actor, "ticket.assign", "ticket", ticketID, ipHash, map[string]any{
		"to": adminID,
	}, func(tx *sql.Tx) error {
		if adminID == "" {
			if _, err := tx.ExecContext(ctx, `UPDATE sesame_support_requests SET assigned_admin_id = NULL, updated_at = NOW() WHERE id = $1`, ticketID); err != nil {
				return err
			}
			return nil
		}
		var role string
		if err := tx.QueryRowContext(ctx, `SELECT role FROM sesame_admin_accounts WHERE id = $1 AND suspended = FALSE`, adminID).Scan(&role); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return errors.New("the selected admin is not available")
			}
			return err
		}
		if role != string(RoleSuper) && role != string(RoleSupport) {
			return ErrNotAllowed
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE sesame_support_requests
			SET assigned_admin_id = $2, updated_at = NOW(),
			    status = CASE WHEN status = 'open' THEN 'in_progress' ELSE status END
			WHERE id = $1
		`, ticketID, adminID); err != nil {
			return err
		}
		return nil
	})
}

func (s *Store) SetTicketStatus(ctx context.Context, actor Account, ticketID string, status TicketStatus, ipHash string) error {
	return s.mutate(ctx, actor, "ticket.status", "ticket", ticketID, ipHash, map[string]any{
		"to": string(status),
	}, func(tx *sql.Tx) error {
		var oldStatus string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM sesame_support_requests WHERE id = $1 FOR UPDATE`, ticketID).Scan(&oldStatus); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if status == TicketClosed {
			if _, err := tx.ExecContext(ctx, `UPDATE sesame_support_requests SET status = $2, updated_at = NOW(), closed_at = NOW(), closed_by = $3 WHERE id = $1`, ticketID, string(status), actor.ID); err != nil {
				return err
			}
		} else {
			if _, err := tx.ExecContext(ctx, `UPDATE sesame_support_requests SET status = $2, updated_at = NOW(), closed_at = NULL, closed_by = NULL WHERE id = $1`, ticketID, string(status)); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) SetTicketPriority(ctx context.Context, actor Account, ticketID string, priority TicketPriority, ipHash string) error {
	return s.mutate(ctx, actor, "ticket.priority", "ticket", ticketID, ipHash, map[string]any{
		"to": string(priority),
	}, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE sesame_support_requests SET priority = $2, updated_at = NOW() WHERE id = $1`, ticketID, string(priority))
		if err != nil {
			return err
		}
		return affected(result, nil)
	})
}
