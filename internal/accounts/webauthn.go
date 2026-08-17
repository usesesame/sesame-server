package accounts

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
)

// Passkeys authenticate the website account only; they never involve the local vault.
type PasskeyStore interface {
	AddCredential(ctx context.Context, accountID string, credentialID, credential []byte, name string) error
	CredentialsForAccount(ctx context.Context, accountID string) ([]webauthn.Credential, error)
	UpdateCredential(ctx context.Context, credentialID, credential []byte) error
	ListCredentials(ctx context.Context, accountID string) ([]CredentialInfo, error)
	DeleteCredential(ctx context.Context, accountID string, credentialID []byte) (bool, error)
	SaveWebAuthnSession(ctx context.Context, id []byte, accountID string, data []byte, expiresAt time.Time) error
	TakeWebAuthnSession(ctx context.Context, id []byte) (accountID string, data []byte, err error)
}

// The non-secret description of a stored passkey shown to the account owner.
type CredentialInfo struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
}

// The handle is the account's own random id, so it never leaks the email address.
type WebAuthnUser struct {
	handle      []byte
	name        string
	credentials []webauthn.Credential
}

func NewWebAuthnUser(user User, credentials []webauthn.Credential) (*WebAuthnUser, error) {
	handle, err := hex.DecodeString(user.ID)
	if err != nil {
		return nil, errors.New("account id is not a valid handle")
	}
	return &WebAuthnUser{handle: handle, name: user.Email, credentials: credentials}, nil
}

func (u *WebAuthnUser) WebAuthnID() []byte                         { return u.handle }
func (u *WebAuthnUser) WebAuthnName() string                       { return u.name }
func (u *WebAuthnUser) WebAuthnDisplayName() string                { return u.name }
func (u *WebAuthnUser) WebAuthnCredentials() []webauthn.Credential { return u.credentials }

func AccountIDForHandle(handle []byte) string {
	return hex.EncodeToString(handle)
}

func (s *PostgresStore) AddCredential(ctx context.Context, accountID string, credentialID, credential []byte, name string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sesame_webauthn_credentials (credential_id, account_id, credential, name)
		VALUES ($1, $2, $3, $4)
	`, credentialID, accountID, credential, name)
	return err
}

func (s *PostgresStore) CredentialsForAccount(ctx context.Context, accountID string) ([]webauthn.Credential, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT credential FROM sesame_webauthn_credentials WHERE account_id = $1`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var credentials []webauthn.Credential
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var credential webauthn.Credential
		if err := json.Unmarshal(raw, &credential); err != nil {
			return nil, err
		}
		credentials = append(credentials, credential)
	}
	return credentials, rows.Err()
}

func (s *PostgresStore) UpdateCredential(ctx context.Context, credentialID, credential []byte) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sesame_webauthn_credentials SET credential = $2 WHERE credential_id = $1`, credentialID, credential)
	return err
}

func (s *PostgresStore) ListCredentials(ctx context.Context, accountID string) ([]CredentialInfo, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT credential_id, name, created_at
		FROM sesame_webauthn_credentials
		WHERE account_id = $1
		ORDER BY created_at
	`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []CredentialInfo
	for rows.Next() {
		var id []byte
		var info CredentialInfo
		if err := rows.Scan(&id, &info.Name, &info.CreatedAt); err != nil {
			return nil, err
		}
		info.ID = hex.EncodeToString(id)
		list = append(list, info)
	}
	return list, rows.Err()
}

func (s *PostgresStore) DeleteCredential(ctx context.Context, accountID string, credentialID []byte) (bool, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM sesame_webauthn_credentials WHERE account_id = $1 AND credential_id = $2`, accountID, credentialID)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected > 0, err
}

func (s *PostgresStore) SaveWebAuthnSession(ctx context.Context, id []byte, accountID string, data []byte, expiresAt time.Time) error {
	var owner any
	if accountID != "" {
		owner = accountID
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sesame_webauthn_sessions (id, account_id, data, expires_at)
		VALUES ($1, $2, $3, $4)
	`, id, owner, data, expiresAt)
	return err
}

func (s *PostgresStore) TakeWebAuthnSession(ctx context.Context, id []byte) (string, []byte, error) {
	var accountID sql.NullString
	var data []byte
	err := s.db.QueryRowContext(ctx, `
		DELETE FROM sesame_webauthn_sessions
		WHERE id = $1 AND expires_at > NOW()
		RETURNING account_id, data
	`, id).Scan(&accountID, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, ErrNotFound
	}
	if err != nil {
		return "", nil, err
	}
	return accountID.String, data, nil
}
