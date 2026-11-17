package accounts

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const (
	TokenVerifyEmail     = "verify_email"
	TokenRecoverPassword = "recover_password"
	TokenChangeEmail     = "change_email"
)

// Website-account lifecycle and browser-session security; no vault-data methods.
type AccountSecurityStore interface {
	RegisterEligible(context.Context, Registration) (User, error)
	SessionForToken(context.Context, []byte) (User, SessionInfo, error)
	MarkSessionAuthenticated(context.Context, []byte, time.Time) error
	SessionsForAccount(context.Context, string) ([]SessionInfo, error)
	DeleteSessionForAccount(context.Context, string, string) error
	RevokeAllSessions(context.Context, string) error
	ChangePasswordAndRotateSession(context.Context, PasswordRotation) error
	CreateEmailVerification(context.Context, string, []byte, time.Time) error
	VerifyEmail(context.Context, TokenSessionRotation) (User, error)
	CreatePasswordRecovery(context.Context, string, []byte, time.Time) (User, bool, error)
	ResetPasswordAndRotateSession(context.Context, TokenPasswordRotation) (User, error)
	CreateEmailChange(context.Context, string, int64, string, []byte, time.Time) error
	EnrollPassword(context.Context, string, int64, string) error
	CredentialSetupRequired(context.Context, string) (bool, error)
	ConfirmEmailChangeAndRotateSession(context.Context, TokenSessionRotation) (EmailChangeResult, error)
	AccountAccess(context.Context, string) (Access, error)
	SignedDownloads(context.Context, string) ([]DownloadRelease, error)
	CreateOrRefreshDownloadTicket(context.Context, DownloadTicketRequest) (DownloadTicket, error)
	// Reads without consuming, so delivery can be validated before the one-time redemption.
	PeekDownloadTicket(context.Context, string, []byte, time.Time) (DownloadTicket, error)
	MarkDownloadTicketRedeemed(context.Context, string, []byte, time.Time) error
	CreateSupportRequest(context.Context, SupportRequest) (string, error)
	SupportTicketsForAccount(context.Context, string) ([]SupportTicketSummary, error)
	SupportTicketForAccount(context.Context, string, string) (SupportTicketDetail, error)
	ReplyToSupportTicket(context.Context, string, string, string) (SupportTicketDetail, error)
	CloseSupportTicket(context.Context, string, string, time.Time) (SupportTicketDetail, error)
	ReopenSupportTicket(context.Context, string, string, time.Time) (SupportTicketDetail, error)
	SupportTicketForAccessToken(context.Context, []byte) (SupportTicketDetail, error)
	ReplyToSupportTicketWithAccessToken(context.Context, []byte, string) (SupportTicketDetail, error)
	AttachSupportTicket(context.Context, string, string) (SupportTicketDetail, error)
}

var _ AccountSecurityStore = (*PostgresStore)(nil)

type Registration struct {
	Email                 string
	PasswordHash          string
	VerificationTokenHash []byte
	VerificationExpiresAt time.Time
	InviteHash            []byte
	AllowPublic           bool
	TermsAcceptedAt       time.Time
	TermsVersion          string
	PrivacyAcknowledgedAt time.Time
	PrivacyVersion        string
}

type SessionInfo struct {
	ID              string    `json:"id"`
	Label           string    `json:"label"`
	CreatedAt       time.Time `json:"createdAt"`
	LastSeenAt      time.Time `json:"lastSeenAt"`
	AuthenticatedAt time.Time `json:"authenticatedAt"`
	ExpiresAt       time.Time `json:"expiresAt"`
	Current         bool      `json:"current"`

	SecurityGeneration int64 `json:"-"`
}

type PasswordRotation struct {
	AccountID            string
	ExpectedPasswordHash string
	PasswordHash         string
	SessionTokenHash     []byte
	SessionExpiresAt     time.Time
	SessionLabel         string
	AuthenticatedAt      time.Time
}

type TokenPasswordRotation struct {
	TokenHash        []byte
	PasswordHash     string
	SessionTokenHash []byte
	SessionExpiresAt time.Time
	SessionLabel     string
	AuthenticatedAt  time.Time
}

type TokenSessionRotation struct {
	TokenHash        []byte
	SessionTokenHash []byte
	SessionExpiresAt time.Time
	SessionLabel     string
	AuthenticatedAt  time.Time
	// ConfirmPassword is read only when an email address is verified for the first time. Empty clears the registration password.
	ConfirmPassword string
}

type EmailChangeResult struct {
	User          User
	PreviousEmail string
}

func (s *PostgresStore) RegisterEligible(ctx context.Context, input Registration) (User, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()

	eligible := input.AllowPublic
	uniform := false
	if !eligible {
		var status string
		err = tx.QueryRowContext(ctx, `SELECT status FROM sesame_beta_eligibility WHERE email = $1 FOR UPDATE`, input.Email).Scan(&status)
		switch {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return User{}, err
		case status == "registered":
			uniform = true
		case status == "revoked":
			return User{}, ErrNotEligible
		default:
			eligible = status == "eligible"
		}
	}
	inviteUsed := false
	if !eligible && !uniform && len(input.InviteHash) > 0 {
		var inviteEmail sql.NullString
		var exhausted, expired, revoked bool
		err = tx.QueryRowContext(ctx, `
			SELECT email, uses >= max_uses, expires_at <= NOW(), revoked_at IS NOT NULL
			FROM sesame_beta_invites
			WHERE code_hash = $1
			FOR UPDATE
		`, input.InviteHash).Scan(&inviteEmail, &exhausted, &expired, &revoked)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return User{}, ErrNotEligible
		case err != nil:
			return User{}, err
		case expired || revoked:
			return User{}, ErrNotEligible
		case exhausted:
			uniform = true
		case !inviteEmail.Valid || inviteEmail.String == input.Email:
			eligible = true
			inviteUsed = true
		default:
			return User{}, ErrNotEligible
		}
	}
	if uniform {
		return User{}, ErrRegistrationNotCreated
	}
	if !eligible {
		return User{}, ErrNotEligible
	}

	accountID, err := newID()
	if err != nil {
		return User{}, err
	}
	user := User{ID: accountID, Email: input.Email, BetaAccess: true}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO sesame_accounts (
			id, email, password_hash, beta_access,
			terms_accepted_at, terms_version,
			privacy_acknowledged_at, privacy_version
		)
		VALUES ($1, $2, $3, TRUE, $4, $5, $6, $7)
	`, user.ID, user.Email, input.PasswordHash,
		input.TermsAcceptedAt, input.TermsVersion,
		input.PrivacyAcknowledgedAt, input.PrivacyVersion)
	if isUniqueViolation(err) {
		return User{}, ErrEmailTaken
	}
	if err != nil {
		return User{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO sesame_account_tokens (token_hash, account_id, purpose, expires_at)
		VALUES ($1, $2, $3, $4)
	`, input.VerificationTokenHash, user.ID, TokenVerifyEmail, input.VerificationExpiresAt); err != nil {
		return User{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sesame_beta_eligibility SET status = 'registered', registered_at = NOW() WHERE email = $1`, input.Email); err != nil {
		return User{}, err
	}
	if inviteUsed {
		if _, err := tx.ExecContext(ctx, `UPDATE sesame_beta_invites SET uses = uses + 1 WHERE code_hash = $1`, input.InviteHash); err != nil {
			return User{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return User{}, err
	}
	return user, nil
}

func (s *PostgresStore) SessionForToken(ctx context.Context, tokenHash []byte) (User, SessionInfo, error) {
	var user User
	var session SessionInfo
	err := s.db.QueryRowContext(ctx, `
		UPDATE sesame_sessions AS session
		SET last_seen_at = NOW()
		FROM sesame_accounts AS account
		WHERE session.token_hash = $1 AND session.expires_at > NOW() AND account.id = session.account_id
			AND account.suspended_at IS NULL
		RETURNING account.id, account.email, account.email_verified_at IS NOT NULL, account.beta_access, account.security_generation,
			session.id, session.label, session.created_at, session.last_seen_at, session.authenticated_at, session.expires_at
	`, tokenHash).Scan(&user.ID, &user.Email, &user.EmailVerified, &user.BetaAccess, &session.SecurityGeneration,
		&session.ID, &session.Label, &session.CreatedAt, &session.LastSeenAt, &session.AuthenticatedAt, &session.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, SessionInfo{}, ErrNotFound
	}
	return user, session, err
}

func (s *PostgresStore) MarkSessionAuthenticated(ctx context.Context, tokenHash []byte, authenticatedAt time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE sesame_sessions SET authenticated_at = $2, last_seen_at = $2 WHERE token_hash = $1 AND expires_at > $2`, tokenHash, authenticatedAt)
	if err != nil {
		return err
	}
	return affectedOrNotFound(result)
}

func (s *PostgresStore) SessionsForAccount(ctx context.Context, accountID string) ([]SessionInfo, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, label, created_at, last_seen_at, authenticated_at, expires_at
		FROM sesame_sessions WHERE account_id = $1 AND expires_at > NOW()
		ORDER BY last_seen_at DESC
	`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sessions := make([]SessionInfo, 0)
	for rows.Next() {
		var session SessionInfo
		if err := rows.Scan(&session.ID, &session.Label, &session.CreatedAt, &session.LastSeenAt, &session.AuthenticatedAt, &session.ExpiresAt); err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}

func (s *PostgresStore) DeleteSessionForAccount(ctx context.Context, accountID, sessionID string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM sesame_sessions WHERE account_id = $1 AND id = $2`, accountID, sessionID)
	if err != nil {
		return err
	}
	return affectedOrNotFound(result)
}

func (s *PostgresStore) RevokeAllSessions(ctx context.Context, accountID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := invalidatePendingCapabilitiesTx(ctx, tx, accountID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sesame_sessions WHERE account_id = $1`, accountID); err != nil {
		return err
	}
	if err := deleteDesktopConnectionsTx(ctx, tx, accountID); err != nil {
		return err
	}
	return tx.Commit()
}

func invalidatePendingCapabilitiesTx(ctx context.Context, tx *sql.Tx, accountID string) error {
	result, err := tx.ExecContext(ctx, `UPDATE sesame_accounts SET security_generation = security_generation + 1 WHERE id = $1`, accountID)
	if err != nil {
		return err
	}
	if err := affectedOrNotFound(result); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE sesame_desktop_link_codes SET cancelled_at = NOW()
		WHERE account_id = $1 AND used_at IS NULL AND cancelled_at IS NULL
	`, accountID); err != nil {
		return err
	}
	return deletePendingAccountTokens(ctx, tx, accountID, TokenChangeEmail)
}

func lockSecurityGenerationTx(ctx context.Context, tx *sql.Tx, accountID string, expectedGeneration int64) error {
	var generation int64
	err := tx.QueryRowContext(ctx, `SELECT security_generation FROM sesame_accounts WHERE id = $1 FOR NO KEY UPDATE`, accountID).Scan(&generation)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if generation != expectedGeneration {
		return ErrSecurityStateChanged
	}
	return nil
}

func deleteDesktopConnectionsTx(ctx context.Context, tx *sql.Tx, accountID string) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM sesame_desktop_connections WHERE account_id = $1`, accountID)
	return err
}

func (s *PostgresStore) ChangePasswordAndRotateSession(ctx context.Context, input PasswordRotation) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE sesame_accounts SET password_hash = $2 WHERE id = $1 AND password_hash = $3`, input.AccountID, input.PasswordHash, input.ExpectedPasswordHash)
	if err != nil {
		return err
	}
	if err := affectedOrNotFound(result); err != nil {
		return err
	}
	if err := invalidatePendingCapabilitiesTx(ctx, tx, input.AccountID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sesame_sessions WHERE account_id = $1`, input.AccountID); err != nil {
		return err
	}
	if err := deleteDesktopConnectionsTx(ctx, tx, input.AccountID); err != nil {
		return err
	}
	if err := insertSessionTx(ctx, tx, input.AccountID, input.SessionTokenHash, input.SessionExpiresAt, input.SessionLabel, input.AuthenticatedAt); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *PostgresStore) CreateEmailVerification(ctx context.Context, accountID string, tokenHash []byte, expiresAt time.Time) error {
	return s.replaceAccountToken(ctx, accountID, TokenVerifyEmail, "", tokenHash, expiresAt)
}

func (s *PostgresStore) VerifyEmail(ctx context.Context, input TokenSessionRotation) (User, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	accountID, err := lockAccountForToken(ctx, tx, input.TokenHash, TokenVerifyEmail, input.AuthenticatedAt)
	if err != nil {
		return User{}, err
	}
	if _, _, err := consumeAccountToken(ctx, tx, input.TokenHash, TokenVerifyEmail, input.AuthenticatedAt); err != nil {
		return User{}, err
	}
	var wasVerified bool
	var registrationHash string
	if err := tx.QueryRowContext(ctx, `SELECT email_verified_at IS NOT NULL, password_hash FROM sesame_accounts WHERE id = $1 FOR NO KEY UPDATE`, accountID).Scan(&wasVerified, &registrationHash); err != nil {
		return User{}, err
	}
	// The mailbox proves the address, not that its owner chose the password.
	// Only a verifier who also knows the password keeps it; the error rolls the token back.
	keepPassword := false
	if !wasVerified && input.ConfirmPassword != "" {
		if registrationHash == "" {
			if err := DummyVerifyPasswordContext(ctx); err != nil {
				return User{}, err
			}
			return User{}, ErrPasswordIncorrect
		}
		matched, err := VerifyPasswordContext(ctx, registrationHash, input.ConfirmPassword)
		if err != nil {
			return User{}, err
		}
		if !matched {
			return User{}, ErrPasswordIncorrect
		}
		keepPassword = true
	}
	var user User
	err = tx.QueryRowContext(ctx, `
		UPDATE sesame_accounts SET email_verified_at = COALESCE(email_verified_at, NOW())
		WHERE id = $1
		RETURNING id, email, TRUE, beta_access
	`, accountID).Scan(&user.ID, &user.Email, &user.EmailVerified, &user.BetaAccess)
	if err != nil {
		return User{}, err
	}
	if !wasVerified {
		if err := invalidatePendingCapabilitiesTx(ctx, tx, accountID); err != nil {
			return User{}, err
		}
	}
	if err := revokePreVerificationCredentials(ctx, tx, accountID, wasVerified); err != nil {
		return User{}, err
	}
	if !wasVerified && !keepPassword {
		if _, err := tx.ExecContext(ctx, `UPDATE sesame_accounts SET password_hash = '' WHERE id = $1`, accountID); err != nil {
			return User{}, err
		}
	}
	if err := insertSessionTx(ctx, tx, accountID, input.SessionTokenHash, input.SessionExpiresAt, input.SessionLabel, input.AuthenticatedAt); err != nil {
		return User{}, err
	}
	if err := tx.Commit(); err != nil {
		return User{}, err
	}
	return user, nil
}

func (s *PostgresStore) CreatePasswordRecovery(ctx context.Context, email string, tokenHash []byte, expiresAt time.Time) (User, bool, error) {
	var user User
	err := s.db.QueryRowContext(ctx, `SELECT id, email, email_verified_at IS NOT NULL, beta_access FROM sesame_accounts WHERE email = $1 AND suspended_at IS NULL`, email).
		Scan(&user.ID, &user.Email, &user.EmailVerified, &user.BetaAccess)
	if errors.Is(err, sql.ErrNoRows) {
		throwawayID, idErr := newID()
		if idErr != nil {
			return User{}, false, idErr
		}
		if err := s.replaceAccountToken(ctx, throwawayID, TokenRecoverPassword, "", tokenHash, expiresAt); err != nil {
			return User{}, false, err
		}
		return User{Email: email}, false, nil
	}
	if err != nil {
		return User{}, false, err
	}
	if err := s.replaceAccountToken(ctx, user.ID, TokenRecoverPassword, "", tokenHash, expiresAt); err != nil {
		return User{}, false, err
	}
	return user, true, nil
}

func (s *PostgresStore) ResetPasswordAndRotateSession(ctx context.Context, input TokenPasswordRotation) (User, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	accountID, err := lockAccountForToken(ctx, tx, input.TokenHash, TokenRecoverPassword, input.AuthenticatedAt)
	if err != nil {
		return User{}, err
	}
	if _, _, err := consumeAccountToken(ctx, tx, input.TokenHash, TokenRecoverPassword, input.AuthenticatedAt); err != nil {
		return User{}, err
	}
	var wasVerified bool
	if err := tx.QueryRowContext(ctx, `SELECT email_verified_at IS NOT NULL FROM sesame_accounts WHERE id = $1`, accountID).Scan(&wasVerified); err != nil {
		return User{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE sesame_accounts SET password_hash = $2 WHERE id = $1 AND suspended_at IS NULL`, accountID, input.PasswordHash)
	if err != nil {
		return User{}, err
	}
	if err := affectedOrNotFound(result); err != nil {
		return User{}, err
	}
	if err := invalidatePendingCapabilitiesTx(ctx, tx, accountID); err != nil {
		return User{}, err
	}
	if err := revokePreVerificationCredentials(ctx, tx, accountID, wasVerified); err != nil {
		return User{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sesame_desktop_connections WHERE account_id = $1`, accountID); err != nil {
		return User{}, err
	}
	if err := deleteDesktopConnectionsTx(ctx, tx, accountID); err != nil {
		return User{}, err
	}
	if err := insertSessionTx(ctx, tx, accountID, input.SessionTokenHash, input.SessionExpiresAt, input.SessionLabel, input.AuthenticatedAt); err != nil {
		return User{}, err
	}
	user, err := userByIDTx(ctx, tx, accountID)
	if err != nil {
		return User{}, err
	}
	if err := tx.Commit(); err != nil {
		return User{}, err
	}
	return user, nil
}

func revokePreVerificationCredentials(ctx context.Context, tx *sql.Tx, accountID string, accountWasVerified bool) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM sesame_sessions WHERE account_id = $1`, accountID); err != nil {
		return err
	}
	if accountWasVerified {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM sesame_webauthn_credentials
		WHERE account_id = $1 AND created_at < COALESCE((SELECT email_verified_at FROM sesame_accounts WHERE id = $1), NOW())
	`, accountID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM sesame_desktop_connections
		WHERE account_id = $1 AND created_at < COALESCE((SELECT email_verified_at FROM sesame_accounts WHERE id = $1), NOW())
	`, accountID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE sesame_desktop_link_codes SET cancelled_at = NOW()
		WHERE account_id = $1 AND used_at IS NULL AND cancelled_at IS NULL
			AND created_at < COALESCE((SELECT email_verified_at FROM sesame_accounts WHERE id = $1), NOW())
	`, accountID); err != nil {
		return err
	}
	return nil
}

func (s *PostgresStore) CreateEmailChange(ctx context.Context, accountID string, expectedGeneration int64, newEmail string, tokenHash []byte, expiresAt time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := lockSecurityGenerationTx(ctx, tx, accountID, expectedGeneration); err != nil {
		return err
	}
	var verified bool
	if err := tx.QueryRowContext(ctx, `SELECT email_verified_at IS NOT NULL FROM sesame_accounts WHERE id = $1`, accountID).Scan(&verified); err != nil {
		return err
	}
	if !verified {
		return ErrEmailUnverified
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM sesame_accounts WHERE email = $1 AND id <> $2)`, newEmail, accountID).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return ErrEmailTaken
	}
	if err := deletePendingAccountTokens(ctx, tx, accountID, TokenChangeEmail); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO sesame_account_tokens (token_hash, account_id, purpose, payload, expires_at, security_generation)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, tokenHash, accountID, TokenChangeEmail, newEmail, expiresAt, expectedGeneration); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *PostgresStore) ConfirmEmailChangeAndRotateSession(ctx context.Context, input TokenSessionRotation) (EmailChangeResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return EmailChangeResult{}, err
	}
	defer tx.Rollback()
	accountID, err := lockAccountForToken(ctx, tx, input.TokenHash, TokenChangeEmail, input.AuthenticatedAt)
	if err != nil {
		return EmailChangeResult{}, err
	}
	var previousEmail string
	var accountGeneration int64
	var verified bool
	if err := tx.QueryRowContext(ctx, `
		SELECT email, security_generation, email_verified_at IS NOT NULL FROM sesame_accounts WHERE id = $1
	`, accountID).Scan(&previousEmail, &accountGeneration, &verified); err != nil {
		return EmailChangeResult{}, err
	}
	var tokenGeneration int64
	if err := tx.QueryRowContext(ctx, `
		SELECT security_generation FROM sesame_account_tokens
		WHERE token_hash = $1 AND purpose = $2 AND used_at IS NULL AND expires_at > $3
	`, input.TokenHash, TokenChangeEmail, input.AuthenticatedAt).Scan(&tokenGeneration); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return EmailChangeResult{}, ErrTokenExpired
		}
		return EmailChangeResult{}, err
	}
	if !verified || accountGeneration != tokenGeneration {
		return EmailChangeResult{}, ErrTokenExpired
	}
	_, newEmail, err := consumeAccountToken(ctx, tx, input.TokenHash, TokenChangeEmail, input.AuthenticatedAt)
	if err != nil {
		return EmailChangeResult{}, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE sesame_accounts SET email = $2, email_verified_at = $3 WHERE id = $1`, accountID, newEmail, input.AuthenticatedAt)
	if isUniqueViolation(err) {
		return EmailChangeResult{}, ErrEmailTaken
	}
	if err != nil {
		return EmailChangeResult{}, err
	}
	if err := invalidatePendingCapabilitiesTx(ctx, tx, accountID); err != nil {
		return EmailChangeResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sesame_sessions WHERE account_id = $1`, accountID); err != nil {
		return EmailChangeResult{}, err
	}
	if err := deletePendingAccountTokens(ctx, tx, accountID, TokenRecoverPassword); err != nil {
		return EmailChangeResult{}, err
	}
	if err := insertSessionTx(ctx, tx, accountID, input.SessionTokenHash, input.SessionExpiresAt, input.SessionLabel, input.AuthenticatedAt); err != nil {
		return EmailChangeResult{}, err
	}
	user, err := userByIDTx(ctx, tx, accountID)
	if err != nil {
		return EmailChangeResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return EmailChangeResult{}, err
	}
	return EmailChangeResult{User: user, PreviousEmail: previousEmail}, nil
}

func (s *PostgresStore) EnrollPassword(ctx context.Context, accountID string, expectedGeneration int64, passwordHash string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := lockSecurityGenerationTx(ctx, tx, accountID, expectedGeneration); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE sesame_accounts SET password_hash = $2
		WHERE id = $1 AND password_hash = '' AND email_verified_at IS NOT NULL AND suspended_at IS NULL
	`, accountID, passwordHash)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected > 0 {
		if err := invalidatePendingCapabilitiesTx(ctx, tx, accountID); err != nil {
			return err
		}
		return tx.Commit()
	}
	var verified bool
	var hasPassword bool
	err = tx.QueryRowContext(ctx, `SELECT email_verified_at IS NOT NULL, password_hash <> '' FROM sesame_accounts WHERE id = $1 AND suspended_at IS NULL`, accountID).Scan(&verified, &hasPassword)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if !verified {
		return ErrEmailUnverified
	}
	if hasPassword {
		return ErrPasswordAlreadySet
	}
	return ErrNotFound
}

func (s *PostgresStore) CredentialSetupRequired(ctx context.Context, accountID string) (bool, error) {
	var required bool
	err := s.db.QueryRowContext(ctx, `
		SELECT account.email_verified_at IS NOT NULL AND account.password_hash = ''
			AND NOT EXISTS (SELECT 1 FROM sesame_webauthn_credentials WHERE account_id = account.id)
		FROM sesame_accounts AS account WHERE account.id = $1
	`, accountID).Scan(&required)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	return required, err
}

func (s *PostgresStore) replaceAccountToken(ctx context.Context, accountID, purpose, payload string, tokenHash []byte, expiresAt time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM sesame_account_tokens WHERE account_id = $1 AND purpose = $2 AND used_at IS NULL`, accountID, purpose); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO sesame_account_tokens (token_hash, account_id, purpose, payload, expires_at)
		SELECT $1, $2, $3, $4, $5
		WHERE EXISTS (SELECT 1 FROM sesame_accounts WHERE id = $2)
	`, tokenHash, accountID, purpose, payload, expiresAt); err != nil {
		return err
	}
	return tx.Commit()
}

func deletePendingAccountTokens(ctx context.Context, tx *sql.Tx, accountID, purpose string) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM sesame_account_tokens WHERE account_id = $1 AND purpose = $2 AND used_at IS NULL`, accountID, purpose)
	return err
}

func lockAccountForToken(ctx context.Context, tx *sql.Tx, tokenHash []byte, purpose string, now time.Time) (string, error) {
	var accountID string
	err := tx.QueryRowContext(ctx, `
		SELECT account_id FROM sesame_account_tokens
		WHERE token_hash = $1 AND purpose = $2 AND used_at IS NULL AND expires_at > $3
	`, tokenHash, purpose, now).Scan(&accountID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrTokenExpired
	}
	if err != nil {
		return "", err
	}
	var locked string
	err = tx.QueryRowContext(ctx, `SELECT id FROM sesame_accounts WHERE id = $1 FOR NO KEY UPDATE`, accountID).Scan(&locked)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrTokenExpired
	}
	if err != nil {
		return "", err
	}
	return locked, nil
}

func consumeAccountToken(ctx context.Context, tx *sql.Tx, tokenHash []byte, purpose string, now time.Time) (string, string, error) {
	var accountID, payload string
	err := tx.QueryRowContext(ctx, `
		UPDATE sesame_account_tokens SET used_at = $3
		WHERE token_hash = $1 AND purpose = $2 AND used_at IS NULL AND expires_at > $3
		RETURNING account_id, payload
	`, tokenHash, purpose, now).Scan(&accountID, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrTokenExpired
	}
	return accountID, payload, err
}

func insertSessionTx(ctx context.Context, tx *sql.Tx, accountID string, tokenHash []byte, expiresAt time.Time, label string, authenticatedAt time.Time) error {
	id, err := newID()
	if err != nil {
		return err
	}
	if label == "" {
		label = "Browser"
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO sesame_sessions (id, token_hash, account_id, expires_at, label, authenticated_at, last_seen_at)
		VALUES ($1, $2, $3, $4, $5, $6, $6)
	`, id, tokenHash, accountID, expiresAt, label, authenticatedAt)
	return err
}

func userByIDTx(ctx context.Context, tx *sql.Tx, accountID string) (User, error) {
	var user User
	err := tx.QueryRowContext(ctx, `SELECT id, email, email_verified_at IS NOT NULL, beta_access FROM sesame_accounts WHERE id = $1`, accountID).
		Scan(&user.ID, &user.Email, &user.EmailVerified, &user.BetaAccess)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return user, err
}
