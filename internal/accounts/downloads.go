package accounts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"time"
)

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
