package httpapi

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/go-webauthn/webauthn/webauthn"
	"usesesame.app/backend/internal/accounts"
)

const preHijackOrigin = passkeyTestOrigin

type preHijackEmails struct {
	mu    sync.Mutex
	links map[string]string
}

func (e *preHijackEmails) SendAccountEmail(_ context.Context, message AccountEmail) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.links[message.Kind+"\x00"+strings.ToLower(message.To)] = message.ActionURL
	return nil
}

func (e *preHijackEmails) token(t *testing.T, kind, address string) string {
	t.Helper()
	e.mu.Lock()
	link := e.links[kind+"\x00"+strings.ToLower(address)]
	e.mu.Unlock()
	if link == "" {
		t.Fatalf("no %s mail for %s", kind, address)
	}
	_, fragment, found := strings.Cut(link, "#token=")
	if !found {
		t.Fatalf("%s mail has no token fragment: %s", kind, link)
	}
	token, err := url.QueryUnescape(fragment)
	if err != nil {
		t.Fatalf("decode %s token: %v", kind, err)
	}
	return token
}

type preHijackEnv struct {
	handler   http.Handler
	store     *accounts.PostgresStore
	db        *sql.DB
	emails    *preHijackEmails
	ipPrefix  string
	ipCounter int64
}

func newPreHijackEnv(t *testing.T) *preHijackEnv {
	t.Helper()
	databaseURL := os.Getenv("SESAME_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("SESAME_TEST_DATABASE_URL is required")
	}
	ctx := context.Background()
	store, err := accounts.Open(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open account store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	lockDatabaseTests(t, store.DB())
	wa, err := webauthn.New(&webauthn.Config{RPID: passkeyTestRP, RPDisplayName: "Fictional account", RPOrigins: []string{preHijackOrigin}})
	if err != nil {
		t.Fatalf("configure passkeys: %v", err)
	}
	emails := &preHijackEmails{links: map[string]string{}}
	seed := time.Now().UnixNano()
	env := &preHijackEnv{
		store:    store,
		db:       store.DB(),
		emails:   emails,
		ipPrefix: fmt.Sprintf("10.%d.%d.", byte(seed>>16), byte(seed>>8)),
	}
	env.handler = New(Config{
		Accounts: store, AllowedOrigin: preHijackOrigin, WebBaseURL: preHijackOrigin,
		RegistrationMode: "public", EmailSender: emails, Passkeys: wa,
		SessionDuration: time.Hour, RecentAuthDuration: 10 * time.Minute,
	})
	t.Cleanup(func() {
		if _, err := env.db.ExecContext(context.Background(), `DELETE FROM sesame_rate_limits WHERE key LIKE $1`, "%"+env.ipPrefix+"%"); err != nil {
			t.Error(err)
		}
	})
	return env
}

func (e *preHijackEnv) nextAddress() string {
	return e.ipPrefix + strconv.Itoa(int(atomic.AddInt64(&e.ipCounter, 1))) + ":40000"
}

func (e *preHijackEnv) uniqueEmail(label string) string {
	return fmt.Sprintf("victim-%s-%d@example.invalid", label, time.Now().UnixNano())
}

func (e *preHijackEnv) countRows(t *testing.T, query, accountID string) int {
	t.Helper()
	var count int
	if err := e.db.QueryRowContext(context.Background(), query, accountID).Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	return count
}

func (e *preHijackEnv) deleteAccountOnCleanup(t *testing.T, accountID string) {
	t.Helper()
	t.Cleanup(func() {
		if _, err := e.db.ExecContext(context.Background(), `DELETE FROM sesame_accounts WHERE id = $1`, accountID); err != nil {
			t.Error(err)
		}
	})
}

type preHijackClient struct {
	t       *testing.T
	env     *preHijackEnv
	cookies map[string]*http.Cookie
}

func newPreHijackClient(t *testing.T, env *preHijackEnv) *preHijackClient {
	t.Helper()
	return &preHijackClient{t: t, env: env, cookies: map[string]*http.Cookie{}}
}

func (c *preHijackClient) request(method, path string, body any) *httptest.ResponseRecorder {
	c.t.Helper()
	var request *http.Request
	if body == nil {
		request = httptest.NewRequest(method, path, nil)
	} else {
		encoded, err := json.Marshal(body)
		if err != nil {
			c.t.Fatalf("marshal request: %v", err)
		}
		request = httptest.NewRequest(method, path, bytes.NewReader(encoded))
		request.Header.Set("Content-Type", "application/json")
	}
	request.RemoteAddr = c.env.nextAddress()
	request.Header.Set("Origin", preHijackOrigin)
	request.Header.Set("X-Sesame-CSRF", "pre-hijack-csrf")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "pre-hijack-csrf"})
	for _, cookie := range c.cookies {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	c.env.handler.ServeHTTP(response, request)
	for _, cookie := range response.Result().Cookies() {
		if cookie.MaxAge < 0 {
			delete(c.cookies, cookie.Name)
		} else {
			c.cookies[cookie.Name] = cookie
		}
	}
	return response
}

func (c *preHijackClient) desktopRequest(method, path, token string, body any) *httptest.ResponseRecorder {
	c.t.Helper()
	var request *http.Request
	if body == nil {
		request = httptest.NewRequest(method, path, nil)
	} else {
		encoded, err := json.Marshal(body)
		if err != nil {
			c.t.Fatalf("marshal desktop request: %v", err)
		}
		request = httptest.NewRequest(method, path, bytes.NewReader(encoded))
		request.Header.Set("Content-Type", "application/json")
	}
	request.RemoteAddr = c.env.nextAddress()
	if token != "" {
		request.Header.Set("Authorization", "Sesame "+token)
	}
	response := httptest.NewRecorder()
	c.env.handler.ServeHTTP(response, request)
	return response
}

type preHijackUser struct {
	ID            string `json:"id"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"emailVerified"`
	BetaAccess    bool   `json:"betaAccess"`
}

func decodeResponseUser(t *testing.T, response *httptest.ResponseRecorder) preHijackUser {
	t.Helper()
	var payload struct {
		User preHijackUser `json:"user"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response user %q: %v", response.Body.String(), err)
	}
	return payload.User
}

func (e *preHijackEnv) registerAccount(t *testing.T, client *preHijackClient, email string) (string, string) {
	t.Helper()
	response := client.request(http.MethodPost, "/v1/auth/register", map[string]any{
		"email": email, "password": "fictional-attacker-password",
		"termsAccepted": true, "termsVersion": termsVersion,
		"privacyAcknowledged": true, "privacyVersion": privacyVersion,
	})
	if response.Code != http.StatusCreated {
		t.Fatalf("registration status = %d: %s", response.Code, response.Body.String())
	}
	user := decodeResponseUser(t, response)
	if user.ID == "" || user.EmailVerified {
		t.Fatalf("registered user = %+v, want a new unverified account", user)
	}
	session := client.cookies[sessionCookieName]
	if session == nil {
		t.Fatal("registration did not issue a browser session")
	}
	e.deleteAccountOnCleanup(t, user.ID)
	return user.ID, session.Value
}

func (e *preHijackEnv) seedPasskey(t *testing.T, accountID string) {
	t.Helper()
	credentialID := make([]byte, 16)
	if _, err := rand.Read(credentialID); err != nil {
		t.Fatal(err)
	}
	credential, err := json.Marshal(webauthn.Credential{ID: credentialID, AttestationType: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.store.AddCredential(context.Background(), accountID, credentialID, credential, "Attacker passkey"); err != nil {
		t.Fatalf("seed passkey: %v", err)
	}
}

func (e *preHijackEnv) seedDesktopConnection(t *testing.T, accountID string) string {
	t.Helper()
	_, codeHash, err := accounts.NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.CreateOrReplaceDesktopLink(context.Background(), accountID, codeHash, time.Now().UTC().Add(10*time.Minute)); err != nil {
		t.Fatalf("seed desktop link: %v", err)
	}
	token, tokenHash, err := accounts.NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.RedeemDesktopLink(context.Background(), codeHash, "Attacker desktop", tokenHash, time.Now().UTC().Add(90*24*time.Hour)); err != nil {
		t.Fatalf("seed desktop connection: %v", err)
	}
	return token
}

func (e *preHijackEnv) seedDesktopLinkCode(t *testing.T, accountID string) string {
	t.Helper()
	code, codeHash, err := accounts.NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.CreateOrReplaceDesktopLink(context.Background(), accountID, codeHash, time.Now().UTC().Add(10*time.Minute)); err != nil {
		t.Fatalf("seed desktop link code: %v", err)
	}
	return code
}

func (e *preHijackEnv) registerPasskey(t *testing.T, client *preHijackClient, accountID string) {
	t.Helper()
	begin := client.request(http.MethodPost, "/v1/account/passkey/register/begin", nil)
	if begin.Code != http.StatusOK {
		t.Fatalf("passkey begin status = %d: %s", begin.Code, begin.Body.String())
	}
	ceremony := client.cookies[passkeyCeremonyCookie]
	if ceremony == nil {
		t.Fatal("passkey begin did not set a ceremony cookie")
	}
	ceremonyID, err := base64.RawURLEncoding.DecodeString(ceremony.Value)
	if err != nil {
		t.Fatalf("decode ceremony cookie: %v", err)
	}
	var data []byte
	if err := e.db.QueryRowContext(context.Background(), `SELECT data FROM sesame_webauthn_sessions WHERE id = $1`, ceremonyID).Scan(&data); err != nil {
		t.Fatalf("read passkey ceremony: %v", err)
	}
	var session webauthn.SessionData
	if err := json.Unmarshal(data, &session); err != nil {
		t.Fatalf("decode passkey ceremony: %v", err)
	}
	credentialID := make([]byte, 16)
	if _, err := rand.Read(credentialID); err != nil {
		t.Fatal(err)
	}
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := cbor.Marshal(map[int]any{1: 1, 3: -8, -1: 6, -2: []byte(public)})
	if err != nil {
		t.Fatal(err)
	}
	clientData, err := json.Marshal(map[string]any{"type": "webauthn.create", "challenge": session.Challenge, "origin": preHijackOrigin, "crossOrigin": false})
	if err != nil {
		t.Fatal(err)
	}
	rpHash := sha256.Sum256([]byte(passkeyTestRP))
	authenticatorData := append([]byte(nil), rpHash[:]...)
	authenticatorData = append(authenticatorData, 0x45)
	authenticatorData = binary.BigEndian.AppendUint32(authenticatorData, 1)
	authenticatorData = append(authenticatorData, make([]byte, 16)...)
	authenticatorData = binary.BigEndian.AppendUint16(authenticatorData, uint16(len(credentialID)))
	authenticatorData = append(authenticatorData, credentialID...)
	authenticatorData = append(authenticatorData, key...)
	attestation, err := cbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": authenticatorData})
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{
		"id":    base64.RawURLEncoding.EncodeToString(credentialID),
		"rawId": base64.RawURLEncoding.EncodeToString(credentialID),
		"type":  "public-key",
		"response": map[string]any{
			"clientDataJSON":    base64.RawURLEncoding.EncodeToString(clientData),
			"attestationObject": base64.RawURLEncoding.EncodeToString(attestation),
		},
	}
	finish := client.request(http.MethodPost, "/v1/account/passkey/register/finish?name=Fictional+passkey", body)
	if finish.Code != http.StatusCreated {
		t.Fatalf("passkey finish status = %d: %s", finish.Code, finish.Body.String())
	}
	if _, err := e.db.ExecContext(context.Background(), `DELETE FROM sesame_webauthn_sessions WHERE account_id = $1`, accountID); err != nil {
		t.Error(err)
	}
}

func TestPreHijackVerificationRevokesUnverifiedCredentials(t *testing.T) {
	env := newPreHijackEnv(t)
	email := env.uniqueEmail("verify")
	attacker := newPreHijackClient(t, env)
	accountID, attackerSession := env.registerAccount(t, attacker, email)
	env.seedPasskey(t, accountID)
	attackerDesktop := env.seedDesktopConnection(t, accountID)
	pendingCode := env.seedDesktopLinkCode(t, accountID)

	denied := attacker.request(http.MethodPost, "/v1/account/desktop-link", nil)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("unverified desktop link status = %d: %s", denied.Code, denied.Body.String())
	}

	verified := attacker.request(http.MethodPost, "/v1/auth/email/verification/confirm", map[string]any{"token": env.emails.token(t, "verify-email", email)})
	if verified.Code != http.StatusOK {
		t.Fatalf("verification status = %d: %s", verified.Code, verified.Body.String())
	}
	user := decodeResponseUser(t, verified)
	if user.ID != accountID || !user.EmailVerified {
		t.Fatalf("verified user = %+v", user)
	}
	replacement := attacker.cookies[sessionCookieName]
	if replacement == nil || replacement.Value == attackerSession {
		t.Fatal("verification did not issue a replacement browser session")
	}
	if me := attacker.request(http.MethodGet, "/v1/auth/me", nil); me.Code != http.StatusOK {
		t.Fatalf("replacement session status = %d: %s", me.Code, me.Body.String())
	}
	old := newPreHijackClient(t, env)
	old.cookies[sessionCookieName] = &http.Cookie{Name: sessionCookieName, Value: attackerSession}
	if stale := old.request(http.MethodGet, "/v1/auth/me", nil); stale.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session status = %d: %s", stale.Code, stale.Body.String())
	}
	if count := env.countRows(t, `SELECT COUNT(*) FROM sesame_webauthn_credentials WHERE account_id = $1`, accountID); count != 0 {
		t.Fatalf("passkeys after verification = %d, want 0", count)
	}
	if count := env.countRows(t, `SELECT COUNT(*) FROM sesame_desktop_connections WHERE account_id = $1`, accountID); count != 0 {
		t.Fatalf("desktop connections after verification = %d, want 0", count)
	}
	if status := attacker.desktopRequest(http.MethodGet, "/v1/desktop/status", attackerDesktop, nil); status.Code != http.StatusUnauthorized {
		t.Fatalf("revoked desktop status = %d: %s", status.Code, status.Body.String())
	}
	late := attacker.desktopRequest(http.MethodPost, "/v1/desktop/link", "", map[string]any{"code": pendingCode, "deviceName": "Late desktop"})
	if late.Code != http.StatusUnauthorized {
		t.Fatalf("cancelled link code status = %d: %s", late.Code, late.Body.String())
	}
	env.registerPasskey(t, attacker, accountID)
	if count := env.countRows(t, `SELECT COUNT(*) FROM sesame_webauthn_credentials WHERE account_id = $1`, accountID); count != 1 {
		t.Fatalf("passkeys after post-verification registration = %d, want 1", count)
	}
}

func TestPreHijackRecoveryRevokesUnverifiedCredentials(t *testing.T) {
	env := newPreHijackEnv(t)
	email := env.uniqueEmail("recover")
	attacker := newPreHijackClient(t, env)
	accountID, attackerSession := env.registerAccount(t, attacker, email)
	env.seedPasskey(t, accountID)
	attackerDesktop := env.seedDesktopConnection(t, accountID)
	pendingCode := env.seedDesktopLinkCode(t, accountID)

	requested := attacker.request(http.MethodPost, "/v1/auth/password/recovery/request", map[string]any{"email": email})
	if requested.Code != http.StatusAccepted {
		t.Fatalf("recovery request status = %d: %s", requested.Code, requested.Body.String())
	}
	confirmed := attacker.request(http.MethodPost, "/v1/auth/password/recovery/confirm", map[string]any{
		"token": env.emails.token(t, "recover-password", email), "newPassword": "fictional-victim-password",
	})
	if confirmed.Code != http.StatusOK {
		t.Fatalf("recovery status = %d: %s", confirmed.Code, confirmed.Body.String())
	}
	user := decodeResponseUser(t, confirmed)
	if user.ID != accountID {
		t.Fatalf("recovered user = %+v", user)
	}
	replacement := attacker.cookies[sessionCookieName]
	if replacement == nil || replacement.Value == attackerSession {
		t.Fatal("recovery did not issue a replacement browser session")
	}
	if me := attacker.request(http.MethodGet, "/v1/auth/me", nil); me.Code != http.StatusOK {
		t.Fatalf("replacement session status = %d: %s", me.Code, me.Body.String())
	}
	old := newPreHijackClient(t, env)
	old.cookies[sessionCookieName] = &http.Cookie{Name: sessionCookieName, Value: attackerSession}
	if stale := old.request(http.MethodGet, "/v1/auth/me", nil); stale.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session status = %d: %s", stale.Code, stale.Body.String())
	}
	if count := env.countRows(t, `SELECT COUNT(*) FROM sesame_webauthn_credentials WHERE account_id = $1`, accountID); count != 0 {
		t.Fatalf("passkeys after recovery = %d, want 0", count)
	}
	if count := env.countRows(t, `SELECT COUNT(*) FROM sesame_desktop_connections WHERE account_id = $1`, accountID); count != 0 {
		t.Fatalf("desktop connections after recovery = %d, want 0", count)
	}
	if status := attacker.desktopRequest(http.MethodGet, "/v1/desktop/status", attackerDesktop, nil); status.Code != http.StatusUnauthorized {
		t.Fatalf("revoked desktop status = %d: %s", status.Code, status.Body.String())
	}
	late := attacker.desktopRequest(http.MethodPost, "/v1/desktop/link", "", map[string]any{"code": pendingCode, "deviceName": "Late desktop"})
	if late.Code != http.StatusUnauthorized {
		t.Fatalf("cancelled link code status = %d: %s", late.Code, late.Body.String())
	}
	login := newPreHijackClient(t, env)
	signedIn := login.request(http.MethodPost, "/v1/auth/login", map[string]any{"email": email, "password": "fictional-victim-password"})
	if signedIn.Code != http.StatusOK {
		t.Fatalf("login with the recovered password status = %d: %s", signedIn.Code, signedIn.Body.String())
	}
}

func TestVerifiedAccountKeepsPasskeyRegisteredAfterVerification(t *testing.T) {
	env := newPreHijackEnv(t)
	email := env.uniqueEmail("keep")
	client := newPreHijackClient(t, env)
	accountID, _ := env.registerAccount(t, client, email)
	verified := client.request(http.MethodPost, "/v1/auth/email/verification/confirm", map[string]any{"token": env.emails.token(t, "verify-email", email)})
	if verified.Code != http.StatusOK {
		t.Fatalf("verification status = %d: %s", verified.Code, verified.Body.String())
	}
	env.registerPasskey(t, client, accountID)
	if count := env.countRows(t, `SELECT COUNT(*) FROM sesame_webauthn_credentials WHERE account_id = $1`, accountID); count != 1 {
		t.Fatalf("passkeys after verification = %d, want 1", count)
	}
	requested := client.request(http.MethodPost, "/v1/auth/password/recovery/request", map[string]any{"email": email})
	if requested.Code != http.StatusAccepted {
		t.Fatalf("recovery request status = %d: %s", requested.Code, requested.Body.String())
	}
	confirmed := client.request(http.MethodPost, "/v1/auth/password/recovery/confirm", map[string]any{
		"token": env.emails.token(t, "recover-password", email), "newPassword": "fictional-recovered-password",
	})
	if confirmed.Code != http.StatusOK {
		t.Fatalf("recovery status = %d: %s", confirmed.Code, confirmed.Body.String())
	}
	if count := env.countRows(t, `SELECT COUNT(*) FROM sesame_webauthn_credentials WHERE account_id = $1`, accountID); count != 1 {
		t.Fatalf("passkeys after recovery = %d, want the account's own post-verification passkey", count)
	}
}

func TestEmailChangeThenRecoveryKeepsVerifiedCredentials(t *testing.T) {
	env := newPreHijackEnv(t)
	originalEmail := env.uniqueEmail("before-change")
	changedEmail := env.uniqueEmail("after-change")
	client := newPreHijackClient(t, env)
	accountID, _ := env.registerAccount(t, client, originalEmail)
	verified := client.request(http.MethodPost, "/v1/auth/email/verification/confirm", map[string]any{"token": env.emails.token(t, "verify-email", originalEmail)})
	if verified.Code != http.StatusOK {
		t.Fatalf("verification status = %d: %s", verified.Code, verified.Body.String())
	}
	env.registerPasskey(t, client, accountID)
	desktopToken := env.seedDesktopConnection(t, accountID)

	requested := client.request(http.MethodPost, "/v1/account/email/change/request", map[string]any{"newEmail": changedEmail})
	if requested.Code != http.StatusAccepted {
		t.Fatalf("email change request status = %d: %s", requested.Code, requested.Body.String())
	}
	changed := client.request(http.MethodPost, "/v1/account/email/change/confirm", map[string]any{"token": env.emails.token(t, "change-email", changedEmail)})
	if changed.Code != http.StatusOK {
		t.Fatalf("email change status = %d: %s", changed.Code, changed.Body.String())
	}

	recoveryRequested := client.request(http.MethodPost, "/v1/auth/password/recovery/request", map[string]any{"email": changedEmail})
	if recoveryRequested.Code != http.StatusAccepted {
		t.Fatalf("recovery request status = %d: %s", recoveryRequested.Code, recoveryRequested.Body.String())
	}
	recovered := client.request(http.MethodPost, "/v1/auth/password/recovery/confirm", map[string]any{
		"token": env.emails.token(t, "recover-password", changedEmail), "newPassword": "fictional-changed-password",
	})
	if recovered.Code != http.StatusOK {
		t.Fatalf("recovery status = %d: %s", recovered.Code, recovered.Body.String())
	}
	if count := env.countRows(t, `SELECT COUNT(*) FROM sesame_webauthn_credentials WHERE account_id = $1`, accountID); count != 1 {
		t.Fatalf("passkeys after email change and recovery = %d, want the verified account's passkey", count)
	}
	if count := env.countRows(t, `SELECT COUNT(*) FROM sesame_desktop_connections WHERE account_id = $1`, accountID); count != 1 {
		t.Fatalf("desktop connections after email change and recovery = %d, want the verified account's connection", count)
	}
	if status := client.desktopRequest(http.MethodGet, "/v1/desktop/status", desktopToken, nil); status.Code != http.StatusOK {
		t.Fatalf("desktop status after email change and recovery = %d: %s", status.Code, status.Body.String())
	}
}
