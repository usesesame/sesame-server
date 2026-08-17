package accounts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"golang.org/x/crypto/argon2"
)

const (
	argonMemory      = 19 * 1024
	argonIterations  = 2
	argonParallelism = 1
	argonKeyLength   = 32
)

var (
	ErrEmailTaken                 = errors.New("email already registered")
	ErrNotFound                   = errors.New("account not found")
	ErrNotEligible                = errors.New("account is not eligible for registration")
	ErrTokenExpired               = errors.New("account action token is invalid or expired")
	ErrRecentAuthRequired         = errors.New("recent authentication is required")
	ErrSupportTicketClosed        = errors.New("support ticket is closed")
	ErrSupportTicketReopenExpired = errors.New("support ticket can no longer be reopened")
	ErrIdempotencyConflict        = errors.New("idempotency key does not match this request")
	ErrDownloadTicketUsed         = errors.New("download ticket has already been redeemed")
)

type User struct {
	ID            string `json:"id"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"emailVerified"`
	BetaAccess    bool   `json:"betaAccess"`
	Suspended     bool   `json:"-"`
}

type Store interface {
	Register(context.Context, string, string, []byte, time.Time) (User, error)
	FindByEmail(context.Context, string) (User, string, error)
	FindByID(context.Context, string) (User, string, error)
	CreateSession(context.Context, string, []byte, time.Time) error
	UserBySession(context.Context, []byte) (User, error)
	DeleteSession(context.Context, []byte) error
	UpdatePassword(context.Context, string, string) error
	DeleteAccountSessions(context.Context, string) error
	DeleteAccount(context.Context, string) error
	Ping(context.Context) error
	Close() error
}

type DesktopStore interface {
	CreateDesktopLink(context.Context, string, []byte, time.Time) error
	RedeemDesktopLink(context.Context, []byte, string, []byte, time.Time) (DesktopConnection, error)
	DesktopConnectionForToken(context.Context, []byte) (DesktopConnection, error)
	RevokeDesktopConnection(context.Context, []byte) error
	DesktopConnectionsForAccount(context.Context, string) ([]DesktopConnection, error)
	RevokeDesktopConnectionForAccount(context.Context, string, string) error
}

type RateLimitStore interface {
	ConsumeRateLimit(context.Context, string, int, time.Duration) (bool, time.Duration, error)
}

type MaintenanceStore interface {
	PurgeExpired(context.Context) error
}

type DesktopConnection struct {
	AccountID                   string     `json:"-"`
	DeviceID                    string     `json:"deviceId"`
	DeviceName                  string     `json:"deviceName"`
	ConnectedAt                 time.Time  `json:"connectedAt"`
	ExpiresAt                   time.Time  `json:"expiresAt"`
	AppVersion                  string     `json:"appVersion,omitempty"`
	Platform                    string     `json:"platform,omitempty"`
	Architecture                string     `json:"architecture,omitempty"`
	UpdateChannel               string     `json:"updateChannel,omitempty"`
	LastSeenAt                  time.Time  `json:"lastSeenAt"`
	ProtocolVersion             int        `json:"protocolVersion"`
	BrowserHelperCapable        bool       `json:"browserHelperCapable"`
	BrowserHelperLastObservedAt *time.Time `json:"browserHelperLastObservedAt,omitempty"`
	AccountSuspended            bool       `json:"-"`
}

type DesktopHeartbeat struct {
	AppVersion            string
	Platform              string
	Architecture          string
	UpdateChannel         string
	ProtocolVersion       int
	BrowserHelperCapable  bool
	BrowserHelperObserved bool
}

type DesktopConnectionManager interface {
	RenameDesktopConnection(context.Context, string, string, string) error
	HeartbeatDesktopConnection(context.Context, []byte, DesktopHeartbeat) (DesktopConnection, error)
}

type DesktopUpdateTicketStore interface {
	CreateDesktopUpdateTicket(context.Context, DesktopUpdateTicketRequest) (DesktopUpdateTicket, error)
	RedeemDesktopUpdateTicket(context.Context, string, string, []byte, time.Time) (DesktopUpdateTicket, error)
}

type DesktopUpdateTicketRequest struct {
	AccountID         string
	DeviceID          string
	ReleaseID         string
	ArtifactObjectKey string
	ArtifactSHA256    string
	UpdaterSignature  string
	TokenHash         []byte
	ExpiresAt         time.Time
}

type DesktopUpdateTicket struct {
	ReleaseID         string
	ArtifactObjectKey string
	ArtifactSHA256    string
	UpdaterSignature  string
	ExpiresAt         time.Time
}

func scanDesktopConnection(scan func(...any) error, withSuspended bool, withAccount bool) (DesktopConnection, error) {
	var connection DesktopConnection
	var helperLastObserved sql.NullTime
	values := []any{
		&connection.DeviceID, &connection.DeviceName, &connection.ConnectedAt, &connection.ExpiresAt,
		&connection.AppVersion, &connection.Platform, &connection.Architecture, &connection.UpdateChannel,
		&connection.LastSeenAt, &connection.ProtocolVersion, &connection.BrowserHelperCapable, &helperLastObserved,
	}
	if withSuspended {
		values = append(values, &connection.AccountSuspended)
	}
	if withAccount {
		values = append(values, &connection.AccountID)
	}
	if err := scan(values...); err != nil {
		return DesktopConnection{}, err
	}
	if helperLastObserved.Valid {
		value := helperLastObserved.Time
		connection.BrowserHelperLastObservedAt = &value
	}
	return connection, nil
}

type PostgresStore struct {
	db *sql.DB
}

func Open(ctx context.Context, databaseURL string) (*PostgresStore, error) {
	store, err := OpenWithoutMigrate(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	if err := store.Migrate(ctx); err != nil {
		store.Close()
		return nil, err
	}
	return store, nil
}

func OpenWithoutMigrate(ctx context.Context, databaseURL string) (*PostgresStore, error) {
	if strings.TrimSpace(databaseURL) == "" {
		return nil, errors.New("database URL is required")
	}
	config, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		return nil, errors.New("database URL is invalid")
	}
	db := stdlib.OpenDB(*config)
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)
	store := &PostgresStore{db: db}
	if err := store.Ping(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *PostgresStore) Register(ctx context.Context, email, passwordHash string, tokenHash []byte, expiresAt time.Time) (User, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	accountID, err := newID()
	if err != nil {
		return User{}, err
	}
	user := User{ID: accountID, Email: email}
	if _, err = tx.ExecContext(ctx, `INSERT INTO sesame_accounts (id, email, password_hash) VALUES ($1, $2, $3)`, user.ID, user.Email, passwordHash); err != nil {
		if isUniqueViolation(err) {
			return User{}, ErrEmailTaken
		}
		return User{}, err
	}
	sessionID, err := newID()
	if err != nil {
		return User{}, err
	}
	now := time.Now().UTC()
	if _, err = tx.ExecContext(ctx, `INSERT INTO sesame_sessions (id, token_hash, account_id, expires_at, label, authenticated_at, last_seen_at) VALUES ($1, $2, $3, $4, 'Browser', $5, $5)`, sessionID, tokenHash, user.ID, expiresAt, now); err != nil {
		return User{}, err
	}
	if err = tx.Commit(); err != nil {
		return User{}, err
	}
	return user, nil
}

func (s *PostgresStore) FindByEmail(ctx context.Context, email string) (User, string, error) {
	var user User
	var passwordHash string
	err := s.db.QueryRowContext(ctx, `SELECT id, email, password_hash, email_verified_at IS NOT NULL, beta_access, suspended_at IS NOT NULL FROM sesame_accounts WHERE email = $1`, email).Scan(&user.ID, &user.Email, &passwordHash, &user.EmailVerified, &user.BetaAccess, &user.Suspended)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, "", ErrNotFound
	}
	return user, passwordHash, err
}

func (s *PostgresStore) FindByID(ctx context.Context, accountID string) (User, string, error) {
	var user User
	var passwordHash string
	err := s.db.QueryRowContext(ctx, `SELECT id, email, password_hash, email_verified_at IS NOT NULL, beta_access, suspended_at IS NOT NULL FROM sesame_accounts WHERE id = $1`, accountID).Scan(&user.ID, &user.Email, &passwordHash, &user.EmailVerified, &user.BetaAccess, &user.Suspended)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, "", ErrNotFound
	}
	return user, passwordHash, err
}

func (s *PostgresStore) CreateSession(ctx context.Context, accountID string, tokenHash []byte, expiresAt time.Time) error {
	sessionID, err := newID()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	_, err = s.db.ExecContext(ctx, `INSERT INTO sesame_sessions (id, token_hash, account_id, expires_at, label, authenticated_at, last_seen_at) VALUES ($1, $2, $3, $4, 'Browser', $5, $5)`, sessionID, tokenHash, accountID, expiresAt, now)
	return err
}

func (s *PostgresStore) UpdatePassword(ctx context.Context, accountID, passwordHash string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE sesame_accounts SET password_hash = $1 WHERE id = $2`, passwordHash, accountID)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err == nil && affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) DeleteAccountSessions(ctx context.Context, accountID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sesame_sessions WHERE account_id = $1`, accountID)
	return err
}

func (s *PostgresStore) DeleteAccount(ctx context.Context, accountID string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM sesame_accounts WHERE id = $1`, accountID)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err == nil && affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) UserBySession(ctx context.Context, tokenHash []byte) (User, error) {
	var user User
	err := s.db.QueryRowContext(ctx, `
		SELECT account.id, account.email, account.email_verified_at IS NOT NULL, account.beta_access
		FROM sesame_sessions AS session
		JOIN sesame_accounts AS account ON account.id = session.account_id
		WHERE session.token_hash = $1 AND session.expires_at > NOW() AND account.suspended_at IS NULL
	`, tokenHash).Scan(&user.ID, &user.Email, &user.EmailVerified, &user.BetaAccess)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return user, err
}

func (s *PostgresStore) DeleteSession(ctx context.Context, tokenHash []byte) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sesame_sessions WHERE token_hash = $1`, tokenHash)
	return err
}

func (s *PostgresStore) CreateDesktopLink(ctx context.Context, accountID string, codeHash []byte, expiresAt time.Time) error {
	id, err := newID()
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO sesame_desktop_link_codes (id, code_hash, account_id, expires_at) VALUES ($1, $2, $3, $4)`, id, codeHash, accountID, expiresAt)
	return err
}

func (s *PostgresStore) RedeemDesktopLink(ctx context.Context, codeHash []byte, deviceName string, tokenHash []byte, expiresAt time.Time) (DesktopConnection, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DesktopConnection{}, err
	}
	defer tx.Rollback()
	var accountID, linkID string
	err = tx.QueryRowContext(ctx, `
		UPDATE sesame_desktop_link_codes
		SET used_at = NOW()
		WHERE code_hash = $1 AND expires_at > NOW() AND used_at IS NULL AND cancelled_at IS NULL
		RETURNING account_id, id
	`, codeHash).Scan(&accountID, &linkID)
	if errors.Is(err, sql.ErrNoRows) {
		return DesktopConnection{}, ErrNotFound
	}
	if err != nil {
		return DesktopConnection{}, err
	}
	deviceID, err := newID()
	if err != nil {
		return DesktopConnection{}, err
	}
	now := time.Now().UTC()
	connection := DesktopConnection{AccountID: accountID, DeviceID: deviceID, DeviceName: deviceName, ConnectedAt: now, LastSeenAt: now, ExpiresAt: expiresAt.UTC(), ProtocolVersion: 1}
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO sesame_desktop_connections (token_hash, account_id, device_id, device_name, expires_at, last_seen_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, tokenHash, accountID, connection.DeviceID, connection.DeviceName, expiresAt, now); err != nil {
		return DesktopConnection{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE sesame_desktop_link_codes SET connected_device_id = $2 WHERE id = $1`, linkID, connection.DeviceID); err != nil {
		return DesktopConnection{}, err
	}
	if err = tx.Commit(); err != nil {
		return DesktopConnection{}, err
	}
	return connection, nil
}

func (s *PostgresStore) DesktopConnectionForToken(ctx context.Context, tokenHash []byte) (DesktopConnection, error) {
	connection, err := scanDesktopConnection(s.db.QueryRowContext(ctx, `
		SELECT connection.device_id, connection.device_name, connection.created_at, connection.expires_at,
			connection.app_version, connection.platform, connection.architecture, connection.update_channel,
			connection.last_seen_at, connection.protocol_version, connection.browser_helper_capable, connection.browser_helper_last_observed_at,
			account.suspended_at IS NOT NULL, connection.account_id
		FROM sesame_desktop_connections connection
		JOIN sesame_accounts account ON account.id = connection.account_id
		WHERE connection.token_hash = $1 AND connection.expires_at > NOW()
	`, tokenHash).Scan, true, true)
	if errors.Is(err, sql.ErrNoRows) {
		return DesktopConnection{}, ErrNotFound
	}
	return connection, err
}

func (s *PostgresStore) RevokeDesktopConnection(ctx context.Context, tokenHash []byte) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sesame_desktop_connections WHERE token_hash = $1`, tokenHash)
	return err
}

func (s *PostgresStore) DesktopConnectionsForAccount(ctx context.Context, accountID string) ([]DesktopConnection, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT device_id, device_name, created_at, expires_at,
			app_version, platform, architecture, update_channel,
			last_seen_at, protocol_version, browser_helper_capable, browser_helper_last_observed_at
		FROM sesame_desktop_connections
		WHERE account_id = $1 AND expires_at > NOW()
		ORDER BY created_at DESC
	`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	connections := make([]DesktopConnection, 0)
	for rows.Next() {
		connection, err := scanDesktopConnection(rows.Scan, false, false)
		if err != nil {
			return nil, err
		}
		connections = append(connections, connection)
	}
	return connections, rows.Err()
}

func (s *PostgresStore) RevokeDesktopConnectionForAccount(ctx context.Context, accountID, deviceID string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM sesame_desktop_connections WHERE account_id = $1 AND device_id = $2`, accountID, deviceID)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err == nil && affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) RenameDesktopConnection(ctx context.Context, accountID, deviceID, deviceName string) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE sesame_desktop_connections SET device_name = $3
		WHERE account_id = $1 AND device_id = $2 AND expires_at > NOW()
	`, accountID, deviceID, deviceName)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) HeartbeatDesktopConnection(ctx context.Context, tokenHash []byte, heartbeat DesktopHeartbeat) (DesktopConnection, error) {
	connection, err := scanDesktopConnection(s.db.QueryRowContext(ctx, `
		UPDATE sesame_desktop_connections
		SET app_version = $2, platform = $3, architecture = $4, update_channel = $5,
			protocol_version = $6, browser_helper_capable = $7,
			browser_helper_last_observed_at = CASE WHEN $8 THEN NOW() ELSE browser_helper_last_observed_at END,
			last_seen_at = NOW()
		WHERE token_hash = $1 AND expires_at > NOW()
		RETURNING device_id, device_name, created_at, expires_at,
			app_version, platform, architecture, update_channel,
			last_seen_at, protocol_version, browser_helper_capable, browser_helper_last_observed_at
	`, tokenHash, heartbeat.AppVersion, heartbeat.Platform, heartbeat.Architecture, heartbeat.UpdateChannel,
		heartbeat.ProtocolVersion, heartbeat.BrowserHelperCapable, heartbeat.BrowserHelperObserved).Scan, false, false)
	if errors.Is(err, sql.ErrNoRows) {
		return DesktopConnection{}, ErrNotFound
	}
	return connection, err
}

func (s *PostgresStore) ConsumeRateLimit(ctx context.Context, key string, limit int, window time.Duration) (bool, time.Duration, error) {
	if key == "" || limit <= 0 || window <= 0 {
		return false, 0, errors.New("invalid rate limit")
	}
	now := time.Now().UTC()
	cutoff := now.Add(-window)
	var attempts int
	var windowStarted time.Time
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO sesame_rate_limits (key, window_started_at, attempts, updated_at)
		VALUES ($1, $2, 1, $2)
		ON CONFLICT (key) DO UPDATE SET
			window_started_at = CASE WHEN sesame_rate_limits.window_started_at <= $3 THEN $2 ELSE sesame_rate_limits.window_started_at END,
			attempts = CASE WHEN sesame_rate_limits.window_started_at <= $3 THEN 1 ELSE sesame_rate_limits.attempts + 1 END,
			updated_at = $2
		RETURNING attempts, window_started_at
	`, key, now, cutoff).Scan(&attempts, &windowStarted)
	if err != nil {
		return false, 0, err
	}
	retryAfter := window - now.Sub(windowStarted)
	if retryAfter < time.Second {
		retryAfter = time.Second
	}
	return attempts <= limit, retryAfter, nil
}

func (s *PostgresStore) PurgeExpired(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM sesame_sessions WHERE expires_at <= NOW();
		DELETE FROM sesame_desktop_link_codes WHERE created_at <= NOW() - INTERVAL '1 day';
		DELETE FROM sesame_desktop_connections WHERE expires_at <= NOW();
		DELETE FROM sesame_webauthn_sessions WHERE expires_at <= NOW();
		DELETE FROM sesame_account_tokens WHERE expires_at <= NOW() OR used_at IS NOT NULL;
		DELETE FROM sesame_download_tickets WHERE expires_at <= NOW() OR redeemed_at IS NOT NULL;
		DELETE FROM sesame_account_events WHERE expires_at <= NOW();
		DELETE FROM sesame_rate_limits WHERE updated_at <= NOW() - INTERVAL '1 day';
		DELETE FROM sesame_admin_sessions WHERE expires_at <= NOW();
		DELETE FROM sesame_admin_setup_tokens WHERE expires_at <= NOW() OR used_at IS NOT NULL;
	`)
	return err
}

func (s *PostgresStore) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

func (s *PostgresStore) Close() error {
	return s.db.Close()
}

func (s *PostgresStore) DB() *sql.DB {
	return s.db
}

func HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hash := argon2.IDKey([]byte(password), salt, argonIterations, argonMemory, argonParallelism, argonKeyLength)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argonMemory, argonIterations, argonParallelism, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(hash)), nil
}

func VerifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false
	}
	var memory, iterations, parallelism uint32
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism); err != nil {
		return false
	}
	if memory == 0 || iterations == 0 || parallelism == 0 {
		return false
	}
	salt, saltErr := base64.RawStdEncoding.DecodeString(parts[4])
	expected, hashErr := base64.RawStdEncoding.DecodeString(parts[5])
	if saltErr != nil || hashErr != nil || len(salt) != 16 || len(expected) != argonKeyLength {
		return false
	}
	actual := argon2.IDKey([]byte(password), salt, iterations, memory, uint8(parallelism), argonKeyLength)
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

func DummyVerifyPassword() {
	dummySalt := make([]byte, 16)
	_, _ = rand.Read(dummySalt)
	_ = argon2.IDKey([]byte("dummy-password"), dummySalt, argonIterations, argonMemory, argonParallelism, argonKeyLength)
}

func NeedsRehash(encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return true
	}
	return parts[3] != fmt.Sprintf("m=%d,t=%d,p=%d", argonMemory, argonIterations, argonParallelism)
}

func NewSessionToken() (string, []byte, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(bytes)
	return token, HashSessionToken(token), nil
}

func HashSessionToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func newID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func isUniqueViolation(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && postgresError.Code == "23505"
}
