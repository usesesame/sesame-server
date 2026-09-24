package accounts

import (
	"context"
	"errors"
	"testing"
)

func TestUpdateCredentialRefusesAConcurrentDeletion(t *testing.T) {
	store, db := lifecycleTestStore(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `INSERT INTO sesame_accounts (id, email, password_hash) VALUES ('acct-passkey', 'passkey-user@example.invalid', 'test')`); err != nil {
		t.Fatalf("create account: %v", err)
	}
	credentialID := []byte("fictional-credential-id")
	if _, err := db.ExecContext(ctx, `INSERT INTO sesame_webauthn_credentials (credential_id, account_id, credential, name) VALUES ($1, 'acct-passkey', '{}'::jsonb, 'Fictional key')`, credentialID); err != nil {
		t.Fatalf("create credential: %v", err)
	}
	if err := store.UpdateCredential(ctx, credentialID, []byte(`{"counter":1}`)); err != nil {
		t.Fatalf("update a live credential: %v", err)
	}
	deleted, err := store.DeleteCredential(ctx, "acct-passkey", credentialID)
	if err != nil || !deleted {
		t.Fatalf("delete credential: deleted=%v err=%v", deleted, err)
	}
	if err := store.UpdateCredential(ctx, credentialID, []byte(`{"counter":2}`)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update after deletion = %v, want ErrNotFound so the login fails", err)
	}
}
