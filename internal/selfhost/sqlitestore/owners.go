package sqlitestore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"usesesame.app/backend/internal/authkit"
	"usesesame.app/backend/internal/selfhost"
)

var totpSealContext = []byte("sesame-selfhost-owner-totp-v1")

const (
	setupFirst  = "first"
	setupInvite = "invite"
	setupReset  = "reset"
	maxAgent    = 256
)

type credentials struct {
	id          string
	name        string
	hash        string
	secret      []byte
	lastCounter int64
}

type setupToken struct {
	id        string
	kind      string
	ownerID   sql.NullString
	ownerName sql.NullString
	secret    []byte
	expiresAt int64
}

func csrfToken(sessionToken string) string {
	digest := sha256.Sum256([]byte("sesame-selfhost-csrf-v1\n" + sessionToken))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func countActiveOwners(ctx context.Context, db queryer, excludeID string) (int, error) {
	var count int
	err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM owners
		WHERE removed_at IS NULL AND password_hash IS NOT NULL AND id <> ?
	`, excludeID).Scan(&count)
	return count, err
}

func scanOwner(scan func(...any) error) (selfhost.Owner, error) {
	var owner selfhost.Owner
	var created int64
	var lastLogin sql.NullInt64
	if err := scan(&owner.ID, &owner.Name, &created, &lastLogin, &owner.SetupPending); err != nil {
		return selfhost.Owner{}, err
	}
	owner.CreatedAt = unixTime(created)
	owner.LastLoginAt = optionalTime(lastLogin)
	return owner, nil
}

const ownerColumns = `id, name, created_at, last_login_at, password_hash IS NULL`

func readOwner(ctx context.Context, db queryer, id string) (selfhost.Owner, error) {
	owner, err := scanOwner(db.QueryRowContext(ctx, `SELECT `+ownerColumns+` FROM owners WHERE id = ? AND removed_at IS NULL`, id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return selfhost.Owner{}, selfhost.ErrNotFound
	}
	return owner, err
}

func (s *Store) StartFirstSetup(ctx context.Context) (selfhost.SetupIssue, error) {
	var issue selfhost.SetupIssue
	err := s.write(ctx, func(tx *sql.Tx) error {
		active, err := countActiveOwners(ctx, tx, "")
		if err != nil {
			return err
		}
		if active > 0 {
			return selfhost.ErrConflict
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM setup_tokens WHERE kind = ? AND used_at IS NULL`, setupFirst); err != nil {
			return err
		}
		issue, err = s.insertSetupToken(ctx, tx, setupFirst, "", selfhost.SystemActor)
		if err != nil {
			return err
		}
		return s.audit(ctx, tx, selfhost.SystemActor, "setup.started", "instance", nil)
	})
	return issue, err
}

func (s *Store) insertSetupToken(ctx context.Context, tx *sql.Tx, kind, ownerID string, by selfhost.Actor) (selfhost.SetupIssue, error) {
	token, hash, err := authkit.NewToken()
	if err != nil {
		return selfhost.SetupIssue{}, err
	}
	id, err := newID()
	if err != nil {
		return selfhost.SetupIssue{}, err
	}
	now := s.clock()
	expires := now.Add(selfhost.SetupTokenTTL)
	var owner any
	if ownerID != "" {
		owner = ownerID
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO setup_tokens (id, token_hash, kind, owner_id, created_by, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, id, hash, kind, owner, by.String(), now.Unix(), expires.Unix()); err != nil {
		return selfhost.SetupIssue{}, err
	}
	issue := selfhost.SetupIssue{Token: token, ExpiresAt: expires}
	if ownerID != "" {
		issue.Owner, err = readOwner(ctx, tx, ownerID)
		if err != nil {
			return selfhost.SetupIssue{}, err
		}
	}
	return issue, nil
}

func (s *Store) InviteOwner(ctx context.Context, by selfhost.Actor, name string) (selfhost.SetupIssue, error) {
	normalized, ok := selfhost.NormalizeName(name)
	if !ok {
		return selfhost.SetupIssue{}, selfhost.ErrInvalidInput
	}
	var issue selfhost.SetupIssue
	err := s.write(ctx, func(tx *sql.Tx) error {
		id, err := newID()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO owners (id, name, name_key, created_at) VALUES (?, ?, ?, ?)`,
			id, normalized, selfhost.NameKey(normalized), s.clock().Unix()); err != nil {
			if isUnique(err) {
				return selfhost.ErrConflict
			}
			return err
		}
		issue, err = s.insertSetupToken(ctx, tx, setupInvite, id, by)
		if err != nil {
			return err
		}
		return s.audit(ctx, tx, by, "owner.invited", id, map[string]string{"name": normalized})
	})
	return issue, err
}

func (s *Store) ResetOwner(ctx context.Context, by selfhost.Actor, name string) (selfhost.SetupIssue, error) {
	normalized, ok := selfhost.NormalizeName(name)
	if !ok {
		return selfhost.SetupIssue{}, selfhost.ErrInvalidInput
	}
	var issue selfhost.SetupIssue
	err := s.write(ctx, func(tx *sql.Tx) error {
		var id string
		err := tx.QueryRowContext(ctx, `SELECT id FROM owners WHERE name_key = ? AND removed_at IS NULL`, selfhost.NameKey(normalized)).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return selfhost.ErrNotFound
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE owners SET password_hash = NULL, totp_secret = NULL WHERE id = ?`, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE owner_id = ?`, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM setup_tokens WHERE owner_id = ? AND used_at IS NULL`, id); err != nil {
			return err
		}
		now := s.clock().Unix()
		revoked, err := revokeDevicesWhere(ctx, tx, now, "owner_id", id)
		if err != nil {
			return err
		}
		if err := cancelPairingsWhere(ctx, tx, now, "owner_id", id); err != nil {
			return err
		}
		issue, err = s.insertSetupToken(ctx, tx, setupReset, id, by)
		if err != nil {
			return err
		}
		return s.audit(ctx, tx, by, "owner.reset", id, map[string]string{"name": normalized, "revokedDevices": fmt.Sprint(revoked)})
	})
	return issue, err
}

func (s *Store) findSetupToken(ctx context.Context, db queryer, token string) (setupToken, error) {
	var found setupToken
	err := db.QueryRowContext(ctx, `
		SELECT t.id, t.kind, t.owner_id, o.name, t.totp_secret, t.expires_at
		FROM setup_tokens t LEFT JOIN owners o ON o.id = t.owner_id
		WHERE t.token_hash = ? AND t.used_at IS NULL AND t.expires_at > ?
		AND (t.owner_id IS NULL OR (o.removed_at IS NULL AND o.password_hash IS NULL))
	`, authkit.HashToken(token), s.clock().Unix()).Scan(&found.id, &found.kind, &found.ownerID, &found.ownerName, &found.secret, &found.expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return setupToken{}, selfhost.ErrSetupTokenInvalid
	}
	if err != nil {
		return setupToken{}, err
	}
	if found.kind == setupFirst {
		active, err := countActiveOwners(ctx, db, "")
		if err != nil {
			return setupToken{}, err
		}
		if active > 0 {
			return setupToken{}, selfhost.ErrSetupTokenInvalid
		}
	}
	return found, nil
}

func (s *Store) SetupDetails(ctx context.Context, token string) (selfhost.SetupDetails, error) {
	if token == "" || len(token) > 256 {
		return selfhost.SetupDetails{}, selfhost.ErrSetupTokenInvalid
	}
	var details selfhost.SetupDetails
	err := s.write(ctx, func(tx *sql.Tx) error {
		found, err := s.findSetupToken(ctx, tx, token)
		if err != nil {
			return err
		}
		secret := ""
		if found.secret == nil {
			secret, err = authkit.NewTOTPSecret()
			if err != nil {
				return err
			}
			sealed, err := authkit.Seal(s.adminKey, []byte(secret), totpSealContext)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE setup_tokens SET totp_secret = ? WHERE id = ?`, sealed, found.id); err != nil {
				return err
			}
		} else {
			plain, err := authkit.Open(s.adminKey, found.secret, totpSealContext)
			if err != nil {
				return fmt.Errorf("sqlitestore: setup secret cannot be decrypted: %w", err)
			}
			secret = string(plain)
		}
		details = selfhost.SetupDetails{
			TOTPSecret: secret,
			OwnerName:  found.ownerName.String,
			FirstOwner: found.kind == setupFirst,
			ExpiresAt:  unixTime(found.expiresAt),
		}
		return nil
	})
	return details, err
}

func (s *Store) CompleteSetup(ctx context.Context, input selfhost.CompleteSetupInput) (selfhost.OwnerLogin, error) {
	if input.Token == "" || len(input.Token) > 256 {
		return selfhost.OwnerLogin{}, selfhost.ErrSetupTokenInvalid
	}
	if !selfhost.ValidPassword(input.Password) {
		return selfhost.OwnerLogin{}, selfhost.ErrInvalidInput
	}
	db, err := s.read()
	if err != nil {
		return selfhost.OwnerLogin{}, err
	}
	found, err := s.findSetupToken(ctx, db, input.Token)
	if err != nil {
		return selfhost.OwnerLogin{}, err
	}
	name, err := setupOwnerName(found, input.Name)
	if err != nil {
		return selfhost.OwnerLogin{}, err
	}
	if found.secret == nil {
		return selfhost.OwnerLogin{}, selfhost.ErrInvalidCredentials
	}
	secret, err := authkit.Open(s.adminKey, found.secret, totpSealContext)
	if err != nil {
		return selfhost.OwnerLogin{}, fmt.Errorf("sqlitestore: setup secret cannot be decrypted: %w", err)
	}
	counter, ok := authkit.VerifyTOTP(string(secret), input.Code, s.clock())
	if !ok {
		return selfhost.OwnerLogin{}, selfhost.ErrInvalidCredentials
	}
	passwordHash, err := authkit.HashPassword(input.Password)
	if err != nil {
		return selfhost.OwnerLogin{}, err
	}
	var login selfhost.OwnerLogin
	err = s.write(ctx, func(tx *sql.Tx) error {
		now := s.clock()
		used, err := tx.ExecContext(ctx, `UPDATE setup_tokens SET used_at = ? WHERE id = ? AND used_at IS NULL AND expires_at > ?`, now.Unix(), found.id, now.Unix())
		if err != nil {
			return err
		}
		if changed, err := used.RowsAffected(); err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return selfhost.ErrSetupTokenInvalid
		}
		ownerID := found.ownerID.String
		if found.kind == setupFirst {
			active, err := countActiveOwners(ctx, tx, "")
			if err != nil {
				return err
			}
			if active > 0 {
				return selfhost.ErrSetupTokenInvalid
			}
			ownerID, err = newID()
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO owners (id, name, name_key, password_hash, totp_secret, totp_last_counter, created_at, last_login_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			`, ownerID, name, selfhost.NameKey(name), passwordHash, found.secret, counter, now.Unix(), now.Unix()); err != nil {
				if isUnique(err) {
					return selfhost.ErrConflict
				}
				return err
			}
		} else {
			updated, err := tx.ExecContext(ctx, `
				UPDATE owners SET password_hash = ?, totp_secret = ?, totp_last_counter = ?, last_login_at = ?
				WHERE id = ? AND removed_at IS NULL AND password_hash IS NULL
			`, passwordHash, found.secret, counter, now.Unix(), ownerID)
			if err != nil {
				return err
			}
			if changed, err := updated.RowsAffected(); err != nil || changed != 1 {
				if err != nil {
					return err
				}
				return selfhost.ErrSetupTokenInvalid
			}
		}
		login, err = s.createSession(ctx, tx, ownerID, input.UserAgent)
		if err != nil {
			return err
		}
		actor := selfhost.Actor{Kind: selfhost.ActorOwner, ID: ownerID}
		if err := s.audit(ctx, tx, actor, "owner.setup_completed", ownerID, map[string]string{"name": name, "kind": found.kind}); err != nil {
			return err
		}
		if found.kind == setupFirst && input.UpdateChecks != nil {
			return s.chooseUpdatesAtSetup(ctx, tx, actor, *input.UpdateChecks)
		}
		return nil
	})
	return login, err
}

func setupOwnerName(found setupToken, requested string) (string, error) {
	if found.kind == setupFirst {
		name, ok := selfhost.NormalizeName(requested)
		if !ok {
			return "", selfhost.ErrInvalidInput
		}
		return name, nil
	}
	if strings.TrimSpace(requested) != "" && selfhost.NameKey(strings.TrimSpace(requested)) != selfhost.NameKey(found.ownerName.String) {
		return "", selfhost.ErrInvalidInput
	}
	return found.ownerName.String, nil
}

func (s *Store) ownerCredentials(ctx context.Context, db queryer, where string, arg any) (credentials, error) {
	var found credentials
	err := db.QueryRowContext(ctx, `
		SELECT id, name, password_hash, totp_secret, totp_last_counter FROM owners
		WHERE removed_at IS NULL AND password_hash IS NOT NULL AND `+where, arg).Scan(&found.id, &found.name, &found.hash, &found.secret, &found.lastCounter)
	if errors.Is(err, sql.ErrNoRows) {
		return credentials{}, selfhost.ErrNotFound
	}
	return found, err
}

func (s *Store) verifyCredentials(found credentials, password, code string) (int64, error) {
	passwordOK := authkit.VerifyPassword(found.hash, password)
	plain, err := authkit.Open(s.adminKey, found.secret, totpSealContext)
	if err != nil {
		return 0, fmt.Errorf("sqlitestore: owner secret cannot be decrypted: %w", err)
	}
	counter, codeOK := authkit.VerifyTOTP(string(plain), code, s.clock())
	if !passwordOK || !codeOK {
		return 0, selfhost.ErrInvalidCredentials
	}
	if counter <= found.lastCounter {
		return 0, selfhost.ErrReplayedCode
	}
	return counter, nil
}

func (s *Store) consumeCounter(ctx context.Context, tx *sql.Tx, found credentials, counter int64, login bool) error {
	query := `UPDATE owners SET totp_last_counter = ? WHERE id = ? AND totp_last_counter < ? AND password_hash = ? AND removed_at IS NULL`
	args := []any{counter, found.id, counter, found.hash}
	if login {
		query = `UPDATE owners SET totp_last_counter = ?, last_login_at = ? WHERE id = ? AND totp_last_counter < ? AND password_hash = ? AND removed_at IS NULL`
		args = []any{counter, s.clock().Unix(), found.id, counter, found.hash}
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return selfhost.ErrReplayedCode
	}
	return nil
}

func (s *Store) Login(ctx context.Context, input selfhost.LoginInput) (selfhost.OwnerLogin, error) {
	name, ok := selfhost.NormalizeName(input.Name)
	if !ok || len(input.Password) > selfhost.MaxPasswordLength || len(input.Code) > 16 {
		authkit.DummyVerifyPassword()
		return selfhost.OwnerLogin{}, selfhost.ErrInvalidCredentials
	}
	db, err := s.read()
	if err != nil {
		return selfhost.OwnerLogin{}, err
	}
	found, err := s.ownerCredentials(ctx, db, `name_key = ?`, selfhost.NameKey(name))
	if errors.Is(err, selfhost.ErrNotFound) {
		authkit.DummyVerifyPassword()
		return selfhost.OwnerLogin{}, selfhost.ErrInvalidCredentials
	}
	if err != nil {
		return selfhost.OwnerLogin{}, err
	}
	counter, err := s.verifyCredentials(found, input.Password, input.Code)
	if err != nil {
		return selfhost.OwnerLogin{}, err
	}
	var login selfhost.OwnerLogin
	err = s.write(ctx, func(tx *sql.Tx) error {
		if err := s.consumeCounter(ctx, tx, found, counter, true); err != nil {
			return err
		}
		login, err = s.createSession(ctx, tx, found.id, input.UserAgent)
		if err != nil {
			return err
		}
		return s.audit(ctx, tx, selfhost.Actor{Kind: selfhost.ActorOwner, ID: found.id}, "owner.login", found.id, nil)
	})
	return login, err
}

func (s *Store) StepUp(ctx context.Context, input selfhost.StepUpInput) (selfhost.OwnerSession, error) {
	if len(input.Password) > selfhost.MaxPasswordLength || len(input.Code) > 16 {
		return selfhost.OwnerSession{}, selfhost.ErrInvalidCredentials
	}
	current, err := s.Session(ctx, input.SessionToken)
	if err != nil {
		return selfhost.OwnerSession{}, err
	}
	db, err := s.read()
	if err != nil {
		return selfhost.OwnerSession{}, err
	}
	found, err := s.ownerCredentials(ctx, db, `id = ?`, current.Owner.ID)
	if errors.Is(err, selfhost.ErrNotFound) {
		return selfhost.OwnerSession{}, selfhost.ErrSessionInvalid
	}
	if err != nil {
		return selfhost.OwnerSession{}, err
	}
	counter, err := s.verifyCredentials(found, input.Password, input.Code)
	if err != nil {
		return selfhost.OwnerSession{}, err
	}
	err = s.write(ctx, func(tx *sql.Tx) error {
		if err := s.consumeCounter(ctx, tx, found, counter, false); err != nil {
			return err
		}
		now := s.clock().Unix()
		result, err := tx.ExecContext(ctx, `UPDATE sessions SET recent_auth_at = ? WHERE id = ? AND expires_at > ?`, now, current.ID, now)
		if err != nil {
			return err
		}
		if changed, err := result.RowsAffected(); err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return selfhost.ErrSessionInvalid
		}
		return s.audit(ctx, tx, selfhost.Actor{Kind: selfhost.ActorOwner, ID: found.id}, "owner.step_up", found.id, nil)
	})
	if err != nil {
		return selfhost.OwnerSession{}, err
	}
	return s.Session(ctx, input.SessionToken)
}

func (s *Store) createSession(ctx context.Context, tx *sql.Tx, ownerID, userAgent string) (selfhost.OwnerLogin, error) {
	token, hash, err := authkit.NewToken()
	if err != nil {
		return selfhost.OwnerLogin{}, err
	}
	id, err := newID()
	if err != nil {
		return selfhost.OwnerLogin{}, err
	}
	if len(userAgent) > maxAgent {
		userAgent = userAgent[:maxAgent]
	}
	userAgent = strings.Map(func(character rune) rune {
		if character < 0x20 || character == 0x7f {
			return -1
		}
		return character
	}, userAgent)
	now := s.clock()
	expires := now.Add(s.sessionTTL)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO sessions (id, token_hash, owner_id, user_agent, created_at, expires_at, recent_auth_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, id, hash, ownerID, userAgent, now.Unix(), expires.Unix(), now.Unix()); err != nil {
		return selfhost.OwnerLogin{}, err
	}
	owner, err := readOwner(ctx, tx, ownerID)
	if err != nil {
		return selfhost.OwnerLogin{}, err
	}
	return selfhost.OwnerLogin{Token: token, Session: selfhost.OwnerSession{
		ID: id, Owner: owner, CreatedAt: now, ExpiresAt: expires, RecentAuthAt: now, CSRFToken: csrfToken(token),
	}}, nil
}

func (s *Store) Session(ctx context.Context, token string) (selfhost.OwnerSession, error) {
	if token == "" || len(token) > 256 {
		return selfhost.OwnerSession{}, selfhost.ErrSessionInvalid
	}
	db, err := s.read()
	if err != nil {
		return selfhost.OwnerSession{}, err
	}
	var session selfhost.OwnerSession
	var created, expires, recent, ownerCreated int64
	var lastLogin sql.NullInt64
	err = db.QueryRowContext(ctx, `
		SELECT s.id, s.created_at, s.expires_at, s.recent_auth_at, o.id, o.name, o.created_at, o.last_login_at
		FROM sessions s JOIN owners o ON o.id = s.owner_id
		WHERE s.token_hash = ? AND s.expires_at > ? AND o.removed_at IS NULL AND o.password_hash IS NOT NULL
	`, authkit.HashToken(token), s.clock().Unix()).Scan(&session.ID, &created, &expires, &recent,
		&session.Owner.ID, &session.Owner.Name, &ownerCreated, &lastLogin)
	if errors.Is(err, sql.ErrNoRows) {
		return selfhost.OwnerSession{}, selfhost.ErrSessionInvalid
	}
	if err != nil {
		return selfhost.OwnerSession{}, err
	}
	session.CreatedAt = unixTime(created)
	session.ExpiresAt = unixTime(expires)
	session.RecentAuthAt = unixTime(recent)
	session.Owner.CreatedAt = unixTime(ownerCreated)
	session.Owner.LastLoginAt = optionalTime(lastLogin)
	session.CSRFToken = csrfToken(token)
	return session, nil
}

func (s *Store) Logout(ctx context.Context, token string) error {
	if token == "" || len(token) > 256 {
		return nil
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, authkit.HashToken(token))
		return err
	})
}

func (s *Store) ListOwners(ctx context.Context) ([]selfhost.Owner, error) {
	db, err := s.read()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT `+ownerColumns+` FROM owners WHERE removed_at IS NULL ORDER BY created_at, name_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	owners := []selfhost.Owner{}
	for rows.Next() {
		owner, err := scanOwner(rows.Scan)
		if err != nil {
			return nil, err
		}
		owners = append(owners, owner)
	}
	return owners, rows.Err()
}

func (s *Store) RemoveOwner(ctx context.Context, by selfhost.Actor, id string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		var pending bool
		err := tx.QueryRowContext(ctx, `SELECT password_hash IS NULL FROM owners WHERE id = ? AND removed_at IS NULL`, id).Scan(&pending)
		if errors.Is(err, sql.ErrNoRows) {
			return selfhost.ErrNotFound
		}
		if err != nil {
			return err
		}
		if !pending {
			remaining, err := countActiveOwners(ctx, tx, id)
			if err != nil {
				return err
			}
			if remaining == 0 {
				return selfhost.ErrLastOwner
			}
		}
		now := s.clock().Unix()
		if _, err := tx.ExecContext(ctx, `UPDATE owners SET removed_at = ?, password_hash = NULL, totp_secret = NULL WHERE id = ?`, now, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE owner_id = ?`, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM setup_tokens WHERE owner_id = ? AND used_at IS NULL`, id); err != nil {
			return err
		}
		revoked, err := revokeDevicesWhere(ctx, tx, now, "owner_id", id)
		if err != nil {
			return err
		}
		if err := cancelPairingsWhere(ctx, tx, now, "owner_id", id); err != nil {
			return err
		}
		return s.audit(ctx, tx, by, "owner.removed", id, map[string]string{"revokedDevices": fmt.Sprint(revoked)})
	})
}
