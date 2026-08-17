package accounts

import (
	"bytes"
	"context"
	"crypto/sha256"
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
	VerifyEmail(context.Context, []byte, time.Time) (User, error)
	CreatePasswordRecovery(context.Context, string, []byte, time.Time) (User, bool, error)
	ResetPasswordAndRotateSession(context.Context, TokenPasswordRotation) (User, error)
	CreateEmailChange(context.Context, string, string, []byte, time.Time) error
	ConfirmEmailChangeAndRotateSession(context.Context, TokenSessionRotation) (User, error)
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
}

var _ AccountSecurityStore = (*PostgresStore)(nil)

type Registration struct {
	Email                 string
	PasswordHash          string
	SessionTokenHash      []byte
	SessionExpiresAt      time.Time
	SessionLabel          string
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
}

type Licence struct {
	ID          string     `json:"id"`
	Product     string     `json:"product"`
	Status      string     `json:"status"`
	IssuedAt    time.Time  `json:"issuedAt"`
	ExpiresAt   *time.Time `json:"expiresAt,omitempty"`
	GraceEndsAt *time.Time `json:"graceEndsAt,omitempty"`
}

type Access struct {
	BetaAccess       bool      `json:"betaAccess"`
	EmailVerified    bool      `json:"emailVerified"`
	DownloadsAllowed bool      `json:"downloadsAllowed"`
	Licences         []Licence `json:"licences"`
}

type DownloadRelease struct {
	ID                string `json:"id"`
	Channel           string `json:"channel"`
	Platform          string `json:"platform"`
	Version           string `json:"version"`
	URL               string `json:"-"`
	ArtifactObjectKey string `json:"-"`
	SHA256            string `json:"sha256"`
	// Internal eligibility for a verified updater signature, not an Authenticode claim.
	Signed               bool      `json:"-"`
	UpdaterVerified      bool      `json:"updaterVerified"`
	DistributionClass    string    `json:"distributionClass"`
	SigstoreVerified     bool      `json:"sigstoreVerified"`
	SigstoreIdentity     string    `json:"sigstoreIdentity,omitempty"`
	AuthenticodeVerified bool      `json:"authenticodeVerified"`
	Signature            string    `json:"signature"`
	SigningKeyID         string    `json:"signingKeyId"`
	SupportedWindows     string    `json:"supportedWindows"`
	ReleaseNotesURL      string    `json:"releaseNotesUrl"`
	RollbackNotice       string    `json:"rollbackNotice,omitempty"`
	PublishedAt          time.Time `json:"publishedAt"`
}

// Fully validated at the HTTP boundary; ticket, idempotency key, and request fingerprint are hashes.
type DownloadTicketRequest struct {
	AccountID          string
	ReleaseID          string
	Platform           string
	ArtifactObjectKey  string
	ArtifactSHA256     string
	TokenHash          []byte
	IdempotencyKeyHash []byte
	RequestHash        []byte
	ExpiresAt          time.Time
}

type DownloadTicket struct {
	ReleaseID         string
	Platform          string
	ArtifactObjectKey string
	ArtifactSHA256    string
	ExpiresAt         time.Time
	Created           bool
}

type SupportRequest struct {
	AccountID          string
	Email              string
	Subject            string
	Message            string
	Category           string
	AppVersion         string
	DiagnosticCode     string
	BrowserIntegration string
	RequestID          string
}

type SupportTicketSummary struct {
	ID                 string     `json:"id"`
	Subject            string     `json:"subject"`
	Status             string     `json:"status"`
	Category           string     `json:"category"`
	AppVersion         string     `json:"appVersion"`
	DiagnosticCode     string     `json:"diagnosticCode"`
	BrowserIntegration string     `json:"browserIntegration"`
	RequestID          string     `json:"requestId"`
	MessageCount       int        `json:"messageCount"`
	UnreadCount        int        `json:"unreadCount"`
	CreatedAt          time.Time  `json:"createdAt"`
	UpdatedAt          time.Time  `json:"updatedAt"`
	ClosedAt           *time.Time `json:"closedAt,omitempty"`
	CanClose           bool       `json:"canClose"`
	CanReopen          bool       `json:"canReopen"`
}

type SupportTicketMessage struct {
	ID         string    `json:"id"`
	AuthorRole string    `json:"authorRole"`
	Body       string    `json:"body"`
	CreatedAt  time.Time `json:"createdAt"`
}

type SupportTicketDetail struct {
	SupportTicketSummary
	Messages []SupportTicketMessage `json:"messages"`
}

func (s *PostgresStore) RegisterEligible(ctx context.Context, input Registration) (User, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()

	eligible := input.AllowPublic
	revoked := false
	if !eligible {
		var status string
		err = tx.QueryRowContext(ctx, `SELECT status FROM sesame_beta_eligibility WHERE email = $1 FOR UPDATE`, input.Email).Scan(&status)
		eligible = err == nil && status == "eligible"
		revoked = err == nil && status == "revoked"
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return User{}, err
		}
	}
	if revoked {
		return User{}, ErrNotEligible
	}
	inviteUsed := false
	if !eligible && len(input.InviteHash) > 0 {
		var inviteEmail sql.NullString
		var uses, maxUses int
		err = tx.QueryRowContext(ctx, `
			SELECT email, uses, max_uses
			FROM sesame_beta_invites
			WHERE code_hash = $1 AND expires_at > NOW() AND revoked_at IS NULL AND uses < max_uses
			FOR UPDATE
		`, input.InviteHash).Scan(&inviteEmail, &uses, &maxUses)
		if err == nil && (!inviteEmail.Valid || inviteEmail.String == input.Email) {
			eligible = true
			inviteUsed = true
		} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return User{}, err
		}
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
	if err := insertSessionTx(ctx, tx, user.ID, input.SessionTokenHash, input.SessionExpiresAt, input.SessionLabel, time.Now().UTC()); err != nil {
		return User{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO sesame_account_tokens (token_hash, account_id, purpose, expires_at)
		VALUES ($1, $2, $3, $4)
	`, input.VerificationTokenHash, user.ID, TokenVerifyEmail, input.VerificationExpiresAt); err != nil {
		return User{}, err
	}
	_, _ = tx.ExecContext(ctx, `UPDATE sesame_beta_eligibility SET status = 'registered', registered_at = NOW() WHERE email = $1`, input.Email)
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
		RETURNING account.id, account.email, account.email_verified_at IS NOT NULL, account.beta_access,
			session.id, session.label, session.created_at, session.last_seen_at, session.authenticated_at, session.expires_at
	`, tokenHash).Scan(&user.ID, &user.Email, &user.EmailVerified, &user.BetaAccess,
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
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	return nil
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
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) RevokeAllSessions(ctx context.Context, accountID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sesame_sessions WHERE account_id = $1`, accountID)
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
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sesame_sessions WHERE account_id = $1`, input.AccountID); err != nil {
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

func (s *PostgresStore) VerifyEmail(ctx context.Context, tokenHash []byte, now time.Time) (User, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	accountID, _, err := consumeAccountToken(ctx, tx, tokenHash, TokenVerifyEmail, now)
	if err != nil {
		return User{}, err
	}
	var user User
	err = tx.QueryRowContext(ctx, `
		UPDATE sesame_accounts SET email_verified_at = COALESCE(email_verified_at, $2)
		WHERE id = $1
		RETURNING id, email, TRUE, beta_access
	`, accountID, now).Scan(&user.ID, &user.Email, &user.EmailVerified, &user.BetaAccess)
	if err != nil {
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
		return User{}, false, nil
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
	accountID, _, err := consumeAccountToken(ctx, tx, input.TokenHash, TokenRecoverPassword, input.AuthenticatedAt)
	if err != nil {
		return User{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE sesame_accounts SET password_hash = $2 WHERE id = $1 AND suspended_at IS NULL`, accountID, input.PasswordHash)
	if err != nil {
		return User{}, err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return User{}, ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sesame_sessions WHERE account_id = $1`, accountID); err != nil {
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

func (s *PostgresStore) CreateEmailChange(ctx context.Context, accountID, newEmail string, tokenHash []byte, expiresAt time.Time) error {
	var exists bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM sesame_accounts WHERE email = $1 AND id <> $2)`, newEmail, accountID).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return ErrEmailTaken
	}
	return s.replaceAccountToken(ctx, accountID, TokenChangeEmail, newEmail, tokenHash, expiresAt)
}

func (s *PostgresStore) ConfirmEmailChangeAndRotateSession(ctx context.Context, input TokenSessionRotation) (User, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	accountID, newEmail, err := consumeAccountToken(ctx, tx, input.TokenHash, TokenChangeEmail, input.AuthenticatedAt)
	if err != nil {
		return User{}, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE sesame_accounts SET email = $2, email_verified_at = $3 WHERE id = $1`, accountID, newEmail, input.AuthenticatedAt)
	if isUniqueViolation(err) {
		return User{}, ErrEmailTaken
	}
	if err != nil {
		return User{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sesame_sessions WHERE account_id = $1`, accountID); err != nil {
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

func (s *PostgresStore) AccountAccess(ctx context.Context, accountID string) (Access, error) {
	var access Access
	err := s.db.QueryRowContext(ctx, `SELECT beta_access, email_verified_at IS NOT NULL FROM sesame_accounts WHERE id = $1`, accountID).
		Scan(&access.BetaAccess, &access.EmailVerified)
	if errors.Is(err, sql.ErrNoRows) {
		return Access{}, ErrNotFound
	}
	if err != nil {
		return Access{}, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, product, status, issued_at, expires_at, grace_ends_at FROM sesame_licences WHERE account_id = $1 ORDER BY issued_at DESC`, accountID)
	if err != nil {
		return Access{}, err
	}
	defer rows.Close()
	access.Licences = make([]Licence, 0)
	for rows.Next() {
		var licence Licence
		var expires, graceEnds sql.NullTime
		if err := rows.Scan(&licence.ID, &licence.Product, &licence.Status, &licence.IssuedAt, &expires, &graceEnds); err != nil {
			return Access{}, err
		}
		if expires.Valid {
			value := expires.Time
			licence.ExpiresAt = &value
		}
		if graceEnds.Valid {
			value := graceEnds.Time
			licence.GraceEndsAt = &value
		}
		access.Licences = append(access.Licences, licence)
	}
	activeLicence := false
	for _, licence := range access.Licences {
		if licence.Status == "active" && (licence.ExpiresAt == nil || licence.ExpiresAt.After(time.Now().UTC())) {
			activeLicence = true
			break
		}
		if licence.Status == "grace_period" && licence.GraceEndsAt != nil && licence.GraceEndsAt.After(time.Now().UTC()) {
			activeLicence = true
			break
		}
	}
	access.DownloadsAllowed = access.EmailVerified && (access.BetaAccess || activeLicence)
	return access, rows.Err()
}

func (s *PostgresStore) SignedDownloads(ctx context.Context, accountID string) ([]DownloadRelease, error) {
	access, err := s.AccountAccess(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if !access.DownloadsAllowed {
		return []DownloadRelease{}, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT release.id, release.channel, release.platform, release.version, release.download_url, release.artifact_object_key, release.sha256, release.signature, release.signing_key_id,
			release.supported_windows, release.release_notes_url, release.rollback_notice, release.published_at, release.rollout_percent,
			COALESCE(artifact.distribution_class, 'lab'), COALESCE(artifact.sigstore_verified, FALSE), COALESCE(artifact.sigstore_identity, ''), COALESCE(artifact.authenticode_verified, FALSE)
		FROM sesame_releases AS release
		LEFT JOIN sesame_release_artifacts AS artifact ON artifact.release_id = release.id
		WHERE release.status = 'published' AND release.published_at IS NOT NULL AND release.update_enabled = TRUE AND release.kill_switch = FALSE
		ORDER BY published_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	releases := make([]DownloadRelease, 0)
	for rows.Next() {
		var release DownloadRelease
		var rolloutPercent int
		if err := rows.Scan(&release.ID, &release.Channel, &release.Platform, &release.Version, &release.URL, &release.ArtifactObjectKey,
			&release.SHA256, &release.Signature, &release.SigningKeyID, &release.SupportedWindows,
			&release.ReleaseNotesURL, &release.RollbackNotice, &release.PublishedAt, &rolloutPercent,
			&release.DistributionClass, &release.SigstoreVerified, &release.SigstoreIdentity, &release.AuthenticodeVerified); err != nil {
			return nil, err
		}
		if !rolloutIncludes(accountID, release.ID, rolloutPercent) {
			continue
		}
		release.Signed = release.Signature != "" && release.SigningKeyID != "" && release.ArtifactObjectKey != ""
		release.UpdaterVerified = release.Signed
		releases = append(releases, release)
	}
	return releases, rows.Err()
}

func rolloutIncludes(accountID, releaseID string, rolloutPercent int) bool {
	if rolloutPercent >= 100 {
		return true
	}
	if rolloutPercent <= 0 {
		return false
	}
	sum := sha256.Sum256([]byte(accountID + "\x00" + releaseID))
	return int(sum[0])%100 < rolloutPercent
}

func (s *PostgresStore) CreateOrRefreshDownloadTicket(ctx context.Context, input DownloadTicketRequest) (DownloadTicket, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DownloadTicket{}, err
	}
	defer tx.Rollback()

	var existingRequestHash []byte
	var redeemedAt sql.NullTime
	err = tx.QueryRowContext(ctx, `
		SELECT request_hash, redeemed_at
		FROM sesame_download_tickets
		WHERE account_id = $1 AND idempotency_key_hash = $2
		FOR UPDATE
	`, input.AccountID, input.IdempotencyKeyHash).Scan(&existingRequestHash, &redeemedAt)
	if err == nil {
		if !bytes.Equal(existingRequestHash, input.RequestHash) {
			return DownloadTicket{}, ErrIdempotencyConflict
		}
		if redeemedAt.Valid {
			return DownloadTicket{}, ErrDownloadTicketUsed
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE sesame_download_tickets
			SET token_hash = $3, expires_at = $4
			WHERE account_id = $1 AND idempotency_key_hash = $2
		`, input.AccountID, input.IdempotencyKeyHash, input.TokenHash, input.ExpiresAt); err != nil {
			return DownloadTicket{}, err
		}
		if err := tx.Commit(); err != nil {
			return DownloadTicket{}, err
		}
		return DownloadTicket{ReleaseID: input.ReleaseID, Platform: input.Platform, ArtifactObjectKey: input.ArtifactObjectKey, ArtifactSHA256: input.ArtifactSHA256, ExpiresAt: input.ExpiresAt}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return DownloadTicket{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO sesame_download_tickets (
			token_hash, account_id, release_id, platform, artifact_url, artifact_object_key, artifact_sha256,
			idempotency_key_hash, request_hash, expires_at
		) VALUES ($1,$2,$3,$4,'',$5,$6,$7,$8,$9)
	`, input.TokenHash, input.AccountID, input.ReleaseID, input.Platform, input.ArtifactObjectKey,
		input.ArtifactSHA256, input.IdempotencyKeyHash, input.RequestHash, input.ExpiresAt); err != nil {
		return DownloadTicket{}, err
	}
	if err := tx.Commit(); err != nil {
		return DownloadTicket{}, err
	}
	return DownloadTicket{ReleaseID: input.ReleaseID, Platform: input.Platform, ArtifactObjectKey: input.ArtifactObjectKey, ArtifactSHA256: input.ArtifactSHA256, ExpiresAt: input.ExpiresAt, Created: true}, nil
}

// Never consumes: a failed delivery must not burn a one-time link the user never used.
func (s *PostgresStore) PeekDownloadTicket(ctx context.Context, accountID string, tokenHash []byte, now time.Time) (DownloadTicket, error) {
	var ticket DownloadTicket
	err := s.db.QueryRowContext(ctx, `
		SELECT release_id, platform, artifact_object_key, artifact_sha256, expires_at
		FROM sesame_download_tickets
		WHERE token_hash = $1 AND account_id = $2 AND redeemed_at IS NULL AND expires_at > $3
	`, tokenHash, accountID, now).Scan(&ticket.ReleaseID, &ticket.Platform, &ticket.ArtifactObjectKey, &ticket.ArtifactSHA256, &ticket.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return DownloadTicket{}, ErrTokenExpired
	}
	return ticket, err
}

// Same WHERE clause as PeekDownloadTicket, so a concurrent redemption is reported, not silently double-redeemed.
func (s *PostgresStore) MarkDownloadTicketRedeemed(ctx context.Context, accountID string, tokenHash []byte, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE sesame_download_tickets
		SET redeemed_at = $3
		WHERE token_hash = $1 AND account_id = $2 AND redeemed_at IS NULL AND expires_at > $3
	`, tokenHash, accountID, now)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrTokenExpired
	}
	return nil
}

func (s *PostgresStore) CreateDesktopUpdateTicket(ctx context.Context, input DesktopUpdateTicketRequest) (DesktopUpdateTicket, error) {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sesame_desktop_update_tickets (token_hash, account_id, device_id, release_id, artifact_url, artifact_object_key, artifact_sha256, updater_signature, expires_at)
		VALUES ($1,$2,$3,$4,'',$5,$6,$7,$8)
	`, input.TokenHash, input.AccountID, input.DeviceID, input.ReleaseID, input.ArtifactObjectKey, input.ArtifactSHA256, input.UpdaterSignature, input.ExpiresAt.UTC())
	if err != nil {
		return DesktopUpdateTicket{}, err
	}
	return DesktopUpdateTicket{ReleaseID: input.ReleaseID, ArtifactObjectKey: input.ArtifactObjectKey, ArtifactSHA256: input.ArtifactSHA256, UpdaterSignature: input.UpdaterSignature, ExpiresAt: input.ExpiresAt.UTC()}, nil
}

func (s *PostgresStore) RedeemDesktopUpdateTicket(ctx context.Context, accountID, deviceID string, tokenHash []byte, now time.Time) (DesktopUpdateTicket, error) {
	var ticket DesktopUpdateTicket
	err := s.db.QueryRowContext(ctx, `
		UPDATE sesame_desktop_update_tickets
		SET last_redeemed_at = $4, redemption_count = redemption_count + 1
		WHERE token_hash = $1 AND account_id = $2 AND device_id = $3 AND expires_at > $4
		RETURNING release_id, artifact_object_key, artifact_sha256, updater_signature, expires_at
	`, tokenHash, accountID, deviceID, now.UTC()).Scan(&ticket.ReleaseID, &ticket.ArtifactObjectKey, &ticket.ArtifactSHA256, &ticket.UpdaterSignature, &ticket.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return DesktopUpdateTicket{}, ErrTokenExpired
	}
	return ticket, err
}

func (s *PostgresStore) CreateSupportRequest(ctx context.Context, input SupportRequest) (string, error) {
	id, err := newID()
	if err != nil {
		return "", err
	}
	messageID, err := newID()
	if err != nil {
		return "", err
	}
	var owner any
	if input.AccountID != "" {
		owner = input.AccountID
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	category := input.Category
	if category == "" {
		category = "general"
	}
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO sesame_support_requests (id, account_id, email, subject, message, category, app_version, diagnostic_code, browser_integration, request_id, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 'open')
	`, id, owner, input.Email, input.Subject, input.Message, category, input.AppVersion, input.DiagnosticCode, input.BrowserIntegration, input.RequestID); err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO sesame_support_messages (id, ticket_id, author_role, body)
		VALUES ($1, $2, 'user', $3)
	`, messageID, id, input.Message); err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return id, nil
}

func (s *PostgresStore) SupportTicketsForAccount(ctx context.Context, accountID string) ([]SupportTicketSummary, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT ticket.id, ticket.subject, ticket.status, ticket.category, ticket.app_version, ticket.diagnostic_code, ticket.browser_integration, ticket.request_id,
			ticket.created_at, ticket.updated_at, ticket.closed_at, ticket.account_reopen_until,
			(SELECT COUNT(*) FROM sesame_support_messages message WHERE message.ticket_id = ticket.id),
			(SELECT COUNT(*) FROM sesame_support_messages message
			 WHERE message.ticket_id = ticket.id AND message.author_role = 'staff'
			   AND message.created_at > ticket.account_last_read_at)
		FROM sesame_support_requests ticket
		WHERE ticket.account_id = $1
		ORDER BY ticket.updated_at DESC
	`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tickets := make([]SupportTicketSummary, 0)
	for rows.Next() {
		var ticket SupportTicketSummary
		var closedAt, reopenUntil sql.NullTime
		if err := rows.Scan(&ticket.ID, &ticket.Subject, &ticket.Status, &ticket.Category, &ticket.AppVersion, &ticket.DiagnosticCode, &ticket.BrowserIntegration, &ticket.RequestID, &ticket.CreatedAt, &ticket.UpdatedAt, &closedAt, &reopenUntil, &ticket.MessageCount, &ticket.UnreadCount); err != nil {
			return nil, err
		}
		if closedAt.Valid {
			value := closedAt.Time
			ticket.ClosedAt = &value
		}
		ticket.CanClose = ticket.Status != "closed"
		ticket.CanReopen = ticket.Status == "closed" && reopenUntil.Valid && reopenUntil.Time.After(time.Now().UTC())
		tickets = append(tickets, ticket)
	}
	return tickets, rows.Err()
}

func (s *PostgresStore) SupportTicketForAccount(ctx context.Context, accountID, ticketID string) (SupportTicketDetail, error) {
	var ticket SupportTicketDetail
	var closedAt, reopenUntil sql.NullTime
	err := s.db.QueryRowContext(ctx, `
		SELECT id, subject, status, category, app_version, diagnostic_code, browser_integration, request_id, created_at, updated_at, closed_at, account_reopen_until,
			(SELECT COUNT(*) FROM sesame_support_messages message WHERE message.ticket_id = sesame_support_requests.id),
			(SELECT COUNT(*) FROM sesame_support_messages message WHERE message.ticket_id = sesame_support_requests.id
			 AND message.author_role = 'staff' AND message.created_at > sesame_support_requests.account_last_read_at)
		FROM sesame_support_requests
		WHERE id = $1 AND account_id = $2
	`, ticketID, accountID).Scan(&ticket.ID, &ticket.Subject, &ticket.Status, &ticket.Category, &ticket.AppVersion, &ticket.DiagnosticCode, &ticket.BrowserIntegration, &ticket.RequestID, &ticket.CreatedAt, &ticket.UpdatedAt, &closedAt, &reopenUntil, &ticket.MessageCount, &ticket.UnreadCount)
	if errors.Is(err, sql.ErrNoRows) {
		return SupportTicketDetail{}, ErrNotFound
	}
	if err != nil {
		return SupportTicketDetail{}, err
	}
	if closedAt.Valid {
		value := closedAt.Time
		ticket.ClosedAt = &value
	}
	ticket.CanClose = ticket.Status != "closed"
	ticket.CanReopen = ticket.Status == "closed" && reopenUntil.Valid && reopenUntil.Time.After(time.Now().UTC())
	if _, err := s.db.ExecContext(ctx, `UPDATE sesame_support_requests SET account_last_read_at = NOW() WHERE id = $1 AND account_id = $2`, ticketID, accountID); err != nil {
		return SupportTicketDetail{}, err
	}
	ticket.UnreadCount = 0
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, author_role, body, created_at
		FROM sesame_support_messages
		WHERE ticket_id = $1
		ORDER BY created_at
	`, ticketID)
	if err != nil {
		return SupportTicketDetail{}, err
	}
	defer rows.Close()
	ticket.Messages = make([]SupportTicketMessage, 0)
	for rows.Next() {
		var message SupportTicketMessage
		if err := rows.Scan(&message.ID, &message.AuthorRole, &message.Body, &message.CreatedAt); err != nil {
			return SupportTicketDetail{}, err
		}
		ticket.Messages = append(ticket.Messages, message)
	}
	return ticket, rows.Err()
}

func (s *PostgresStore) CloseSupportTicket(ctx context.Context, accountID, ticketID string, now time.Time) (SupportTicketDetail, error) {
	result, err := s.db.ExecContext(ctx, `
		UPDATE sesame_support_requests
		SET status = 'closed', closed_at = $3, account_reopen_until = $3 + INTERVAL '30 days', updated_at = $3
		WHERE id = $1 AND account_id = $2 AND status <> 'closed'
	`, ticketID, accountID, now.UTC())
	if err != nil {
		return SupportTicketDetail{}, err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		var status string
		err := s.db.QueryRowContext(ctx, `SELECT status FROM sesame_support_requests WHERE id = $1 AND account_id = $2`, ticketID, accountID).Scan(&status)
		if errors.Is(err, sql.ErrNoRows) {
			return SupportTicketDetail{}, ErrNotFound
		}
		if err != nil {
			return SupportTicketDetail{}, err
		}
		return SupportTicketDetail{}, ErrSupportTicketClosed
	}
	return s.SupportTicketForAccount(ctx, accountID, ticketID)
}

func (s *PostgresStore) ReopenSupportTicket(ctx context.Context, accountID, ticketID string, now time.Time) (SupportTicketDetail, error) {
	result, err := s.db.ExecContext(ctx, `
		UPDATE sesame_support_requests
		SET status = 'open', closed_at = NULL, closed_by = NULL, account_reopen_until = NULL, updated_at = $3
		WHERE id = $1 AND account_id = $2 AND status = 'closed' AND account_reopen_until > $3
	`, ticketID, accountID, now.UTC())
	if err != nil {
		return SupportTicketDetail{}, err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		var exists bool
		if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sesame_support_requests WHERE id = $1 AND account_id = $2)`, ticketID, accountID).Scan(&exists); err != nil {
			return SupportTicketDetail{}, err
		}
		if !exists {
			return SupportTicketDetail{}, ErrNotFound
		}
		return SupportTicketDetail{}, ErrSupportTicketReopenExpired
	}
	return s.SupportTicketForAccount(ctx, accountID, ticketID)
}

func (s *PostgresStore) ReplyToSupportTicket(ctx context.Context, accountID, ticketID, body string) (SupportTicketDetail, error) {
	messageID, err := newID()
	if err != nil {
		return SupportTicketDetail{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SupportTicketDetail{}, err
	}
	defer tx.Rollback()
	var status string
	err = tx.QueryRowContext(ctx, `
		SELECT status FROM sesame_support_requests
		WHERE id = $1 AND account_id = $2
		FOR UPDATE
	`, ticketID, accountID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return SupportTicketDetail{}, ErrNotFound
	}
	if err != nil {
		return SupportTicketDetail{}, err
	}
	if status == "closed" {
		return SupportTicketDetail{}, ErrSupportTicketClosed
	}
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO sesame_support_messages (id, ticket_id, author_role, body)
		VALUES ($1, $2, 'user', $3)
	`, messageID, ticketID, body); err != nil {
		return SupportTicketDetail{}, err
	}
	if _, err = tx.ExecContext(ctx, `
		UPDATE sesame_support_requests
		SET status = CASE WHEN assigned_admin_id IS NULL THEN 'open' ELSE 'in_progress' END,
			updated_at = NOW()
		WHERE id = $1
	`, ticketID); err != nil {
		return SupportTicketDetail{}, err
	}
	if err = tx.Commit(); err != nil {
		return SupportTicketDetail{}, err
	}
	return s.SupportTicketForAccount(ctx, accountID, ticketID)
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
	if _, err := tx.ExecContext(ctx, `INSERT INTO sesame_account_tokens (token_hash, account_id, purpose, payload, expires_at) VALUES ($1, $2, $3, $4, $5)`, tokenHash, accountID, purpose, payload, expiresAt); err != nil {
		return err
	}
	return tx.Commit()
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
