package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/go-webauthn/webauthn/webauthn"
	"usesesame.app/backend/internal/accounts"
)

func TestPasskeyVerificationWithDatabaseSessions(t *testing.T) {
	databaseURL := os.Getenv("SESAME_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("SESAME_TEST_DATABASE_URL is required")
	}
	ctx := context.Background()
	store, err := accounts.Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	wa, err := webauthn.New(&webauthn.Config{RPID: passkeyTestRP, RPDisplayName: "Fictional account", RPOrigins: []string{passkeyTestOrigin}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		flags     byte
		oldPolicy bool
		expired   bool
		status    int
	}{
		{"verified", 5, false, false, http.StatusOK},
		{"unverified", 1, false, false, http.StatusUnauthorized},
		{"old policy", 5, true, false, http.StatusBadRequest},
		{"expired", 5, false, true, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handle := make([]byte, 16)
			if _, err := rand.Read(handle); err != nil {
				t.Fatal(err)
			}
			accountID := hex.EncodeToString(handle)
			_, err := store.DB().ExecContext(ctx, `INSERT INTO sesame_accounts (id, email, password_hash) VALUES ($1, $2, $3)`, accountID, accountID+"@example.invalid", "fictional-unused-hash")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if _, err := store.DB().ExecContext(ctx, `DELETE FROM sesame_accounts WHERE id = $1`, accountID); err != nil {
					t.Error(err)
				}
			})
			c := newPasskeyTestClient(t)
			c.handler = New(Config{AllowedOrigin: passkeyTestOrigin, Accounts: store, Passkeys: wa})
			c.store.credential.ID = handle
			credential, err := json.Marshal(c.store.credential)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.AddCredential(ctx, accountID, handle, credential, "Fictional passkey"); err != nil {
				t.Fatal(err)
			}
			begin := c.request("/v1/auth/passkey/login/begin", nil)
			if begin.Code != http.StatusOK {
				t.Fatalf("begin status %d", begin.Code)
			}
			cookie := c.cookies["sesame_wan"]
			if cookie == nil {
				t.Fatal("ceremony cookie missing")
			}
			ceremonyID, err := base64.RawURLEncoding.DecodeString(cookie.Value)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if _, err := store.DB().ExecContext(ctx, `DELETE FROM sesame_webauthn_sessions WHERE id = $1`, ceremonyID); err != nil {
					t.Error(err)
				}
			})
			var data []byte
			if err := store.DB().QueryRowContext(ctx, `SELECT data FROM sesame_webauthn_sessions WHERE id = $1`, ceremonyID).Scan(&data); err != nil {
				t.Fatal(err)
			}
			var session webauthn.SessionData
			if err := json.Unmarshal(data, &session); err != nil {
				t.Fatal(err)
			}
			if session.UserVerification != "required" {
				t.Fatal("stored ceremony must require user verification")
			}
			c.challenge = session.Challenge
			if tc.oldPolicy {
				session.UserVerification = "preferred"
				data, err = json.Marshal(session)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.DB().ExecContext(ctx, `UPDATE sesame_webauthn_sessions SET data = $2 WHERE id = $1`, ceremonyID, data); err != nil {
					t.Fatal(err)
				}
			}
			if tc.expired {
				if _, err := store.DB().ExecContext(ctx, `UPDATE sesame_webauthn_sessions SET expires_at = NOW() - INTERVAL '1 minute' WHERE id = $1`, ceremonyID); err != nil {
					t.Fatal(err)
				}
			}
			var body map[string]any
			if err := json.Unmarshal(c.body(false, tc.flags, passkeyTestOrigin, false), &body); err != nil {
				t.Fatal(err)
			}
			body["response"].(map[string]any)["userHandle"] = base64.RawURLEncoding.EncodeToString(handle)
			encoded, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			response := c.request("/v1/auth/passkey/login/finish", encoded)
			if response.Code != tc.status {
				t.Fatalf("finish status %d, want %d", response.Code, tc.status)
			}
			var sessions int
			if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM sesame_sessions WHERE account_id = $1`, accountID).Scan(&sessions); err != nil {
				t.Fatal(err)
			}
			want := 0
			if tc.status == http.StatusOK {
				want = 1
			}
			if sessions != want {
				t.Fatalf("stored %d account sessions, want %d", sessions, want)
			}
			if tc.status != http.StatusOK && c.cookies["sesame_session"] != nil {
				t.Fatal("rejected login set a session cookie")
			}
			if !tc.expired {
				if _, _, err := store.TakeWebAuthnSession(ctx, ceremonyID); err == nil {
					t.Fatal("ceremony survived its first use")
				}
			}
		})
	}
}
