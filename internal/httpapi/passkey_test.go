package httpapi

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"usesesame.app/backend/internal/accounts"
)

const passkeyTestOrigin = "https://account.example.invalid"
const passkeyTestRP = "account.example.invalid"

type passkeyTestStore struct {
	accounts.Store
	accounts.PasskeyStore
	accounts.AccountSecurityStore
	credential    webauthn.Credential
	ceremony      []byte
	ceremonyID    []byte
	ceremonyOwner string
	expires       time.Time
	sessions      int
	registrations int
	updateError   error
	suspended     bool
}

func (s *passkeyTestStore) SaveWebAuthnSession(_ context.Context, id []byte, owner string, data []byte, expires time.Time) error {
	s.ceremony, s.ceremonyID, s.ceremonyOwner, s.expires = data, id, owner, expires
	return nil
}

func (s *passkeyTestStore) TakeWebAuthnSession(_ context.Context, id []byte) (string, []byte, error) {
	if !bytes.Equal(s.ceremonyID, id) || s.ceremony == nil || time.Now().After(s.expires) {
		return "", nil, accounts.ErrNotFound
	}
	data := s.ceremony
	s.ceremony = nil
	return s.ceremonyOwner, data, nil
}

func (s *passkeyTestStore) FindByID(context.Context, string) (accounts.User, string, error) {
	return accounts.User{ID: "01020304", Email: "fictional@example.invalid", Suspended: s.suspended}, "", nil
}

func (s *passkeyTestStore) SessionForToken(ctx context.Context, _ []byte) (accounts.User, accounts.SessionInfo, error) {
	user, _, err := s.FindByID(ctx, "01020304")
	return user, accounts.SessionInfo{AuthenticatedAt: time.Now()}, err
}

func (s *passkeyTestStore) CredentialsForAccount(context.Context, string) ([]webauthn.Credential, error) {
	return []webauthn.Credential{s.credential}, nil
}

func (s *passkeyTestStore) UpdateCredential(_ context.Context, _ []byte, data []byte) error {
	if s.updateError != nil {
		return s.updateError
	}
	return json.Unmarshal(data, &s.credential)
}

func (s *passkeyTestStore) AddCredential(_ context.Context, _ string, _ []byte, data []byte, _ string) error {
	s.registrations++
	return json.Unmarshal(data, &s.credential)
}

func (s *passkeyTestStore) CreateSession(context.Context, string, []byte, time.Time) error {
	s.sessions++
	return nil
}

type passkeyTestClient struct {
	t         *testing.T
	handler   http.Handler
	store     *passkeyTestStore
	private   ed25519.PrivateKey
	cookies   map[string]*http.Cookie
	challenge string
	peer      string
}

func newPasskeyTestClient(t *testing.T) *passkeyTestClient {
	t.Helper()
	wa, err := webauthn.New(&webauthn.Config{
		RPID: passkeyTestRP, RPDisplayName: "Fictional account", RPOrigins: []string{passkeyTestOrigin},
		AuthenticatorSelection: protocol.AuthenticatorSelection{UserVerification: protocol.VerificationPreferred},
	})
	if err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := cbor.Marshal(map[int]any{1: 1, 3: -8, -1: 6, -2: []byte(public)})
	if err != nil {
		t.Fatal(err)
	}
	store := &passkeyTestStore{credential: webauthn.Credential{ID: []byte("fictional-credential"), PublicKey: key, AttestationType: "none"}}
	address := [16]byte{0x20, 0x01, 0x0d, 0xb8}
	copy(address[4:], public[:12])
	return &passkeyTestClient{t: t, handler: New(Config{AllowedOrigin: passkeyTestOrigin, Accounts: store, Passkeys: wa}), store: store, private: private, cookies: map[string]*http.Cookie{}, peer: netip.AddrPortFrom(netip.AddrFrom16(address), 12345).String()}
}

func (c *passkeyTestClient) request(path string, body []byte) *httptest.ResponseRecorder {
	c.t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	request.RemoteAddr = c.peer
	request.Header.Set("Origin", passkeyTestOrigin)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Sesame-CSRF", "fictional-csrf")
	request.AddCookie(&http.Cookie{Name: "sesame_csrf", Value: "fictional-csrf"})
	for _, cookie := range c.cookies {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	c.handler.ServeHTTP(response, request)
	for _, cookie := range response.Result().Cookies() {
		if cookie.MaxAge < 0 {
			delete(c.cookies, cookie.Name)
		} else {
			c.cookies[cookie.Name] = cookie
		}
	}
	return response
}

func (c *passkeyTestClient) begin(registration bool) {
	c.t.Helper()
	path := "/v1/auth/passkey/login/begin"
	if registration {
		path = "/v1/account/passkey/register/begin"
		c.cookies["sesame_session"] = &http.Cookie{Name: "sesame_session", Value: "fictional-session"}
	}
	response := c.request(path, nil)
	if response.Code != http.StatusOK {
		c.t.Fatalf("begin status %d", response.Code)
	}
	var session webauthn.SessionData
	if err := json.Unmarshal(c.store.ceremony, &session); err != nil {
		c.t.Fatal(err)
	}
	if session.UserVerification != protocol.VerificationRequired {
		c.t.Fatal("ceremony must require user verification")
	}
	c.challenge = session.Challenge
}

func (c *passkeyTestClient) body(registration bool, flags byte, origin string, invalidSignature bool) []byte {
	c.t.Helper()
	ceremonyType := "webauthn.get"
	if registration {
		ceremonyType = "webauthn.create"
	}
	client, err := json.Marshal(map[string]any{"type": ceremonyType, "challenge": c.challenge, "origin": origin, "crossOrigin": false})
	if err != nil {
		c.t.Fatal(err)
	}
	rpHash := sha256.Sum256([]byte(passkeyTestRP))
	auth := append([]byte(nil), rpHash[:]...)
	auth = append(auth, flags)
	auth = binary.BigEndian.AppendUint32(auth, 1)
	encode := base64.RawURLEncoding.EncodeToString
	response := map[string]any{"clientDataJSON": encode(client)}
	if registration {
		auth = append(auth, make([]byte, 16)...)
		auth = binary.BigEndian.AppendUint16(auth, uint16(len(c.store.credential.ID)))
		auth = append(auth, c.store.credential.ID...)
		auth = append(auth, c.store.credential.PublicKey...)
		attestation, err := cbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": auth})
		if err != nil {
			c.t.Fatal(err)
		}
		response["attestationObject"] = encode(attestation)
	} else {
		clientHash := sha256.Sum256(client)
		signed := append(append([]byte(nil), auth...), clientHash[:]...)
		signature := ed25519.Sign(c.private, signed)
		if invalidSignature {
			signature[0] ^= 1
		}
		response["authenticatorData"] = encode(auth)
		response["signature"] = encode(signature)
		response["userHandle"] = encode([]byte{1, 2, 3, 4})
	}
	body, err := json.Marshal(map[string]any{"id": encode(c.store.credential.ID), "rawId": encode(c.store.credential.ID), "type": "public-key", "response": response})
	if err != nil {
		c.t.Fatal(err)
	}
	return body
}

func TestPasskeyLoginRequiresCurrentUserVerification(t *testing.T) {
	for _, tc := range []struct {
		name             string
		flags            byte
		origin           string
		invalidSignature bool
		setup            func(*passkeyTestClient)
		status           int
	}{
		{name: "verified", flags: 5, status: http.StatusOK},
		{name: "unverified", flags: 1, status: http.StatusUnauthorized},
		{name: "previously verified credential", flags: 1, setup: func(c *passkeyTestClient) { c.store.credential.Flags.UserVerified = true }, status: http.StatusUnauthorized},
		{name: "missing presence", flags: 4, status: http.StatusUnauthorized},
		{name: "invalid signature", flags: 5, invalidSignature: true, status: http.StatusUnauthorized},
		{name: "wrong origin", flags: 5, origin: "https://other.example.invalid", status: http.StatusUnauthorized},
		{name: "expired ceremony", flags: 5, setup: func(c *passkeyTestClient) { c.store.expires = time.Now().Add(-time.Minute) }, status: http.StatusBadRequest},
		{name: "old preferred ceremony", flags: 5, setup: func(c *passkeyTestClient) { c.changeStoredPolicy(protocol.VerificationPreferred) }, status: http.StatusBadRequest},
		{name: "malformed ceremony", flags: 5, setup: func(c *passkeyTestClient) { c.store.ceremony = []byte("invalid") }, status: http.StatusBadRequest},
		{name: "persistence failed", flags: 5, setup: func(c *passkeyTestClient) { c.store.updateError = errors.New("fictional storage failure") }, status: http.StatusServiceUnavailable},
		{name: "suspended", flags: 5, setup: func(c *passkeyTestClient) { c.store.suspended = true }, status: http.StatusLocked},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newPasskeyTestClient(t)
			c.begin(false)
			if tc.setup != nil {
				tc.setup(c)
			}
			origin := tc.origin
			if origin == "" {
				origin = passkeyTestOrigin
			}
			response := c.request("/v1/auth/passkey/login/finish", c.body(false, tc.flags, origin, tc.invalidSignature))
			if response.Code != tc.status {
				t.Fatalf("status %d, want %d", response.Code, tc.status)
			}
			if tc.status == http.StatusOK {
				if c.store.sessions != 1 || c.cookies["sesame_session"] == nil {
					t.Fatal("verified login must create a session")
				}
			} else if c.store.sessions != 0 || c.cookies["sesame_session"] != nil {
				t.Fatal("rejected login created a session")
			}
		})
	}
}

func (c *passkeyTestClient) changeStoredPolicy(policy protocol.UserVerificationRequirement) {
	c.t.Helper()
	var session webauthn.SessionData
	if err := json.Unmarshal(c.store.ceremony, &session); err != nil {
		c.t.Fatal(err)
	}
	session.UserVerification = policy
	data, err := json.Marshal(session)
	if err != nil {
		c.t.Fatal(err)
	}
	c.store.ceremony = data
}

func TestPasskeyLoginConsumesCeremonyOnSuccessAndFailure(t *testing.T) {
	for _, flags := range []byte{1, 5} {
		c := newPasskeyTestClient(t)
		c.begin(false)
		cookie := c.cookies["sesame_wan"]
		body := c.body(false, flags, passkeyTestOrigin, false)
		c.request("/v1/auth/passkey/login/finish", body)
		count := c.store.sessions
		c.cookies["sesame_wan"] = cookie
		response := c.request("/v1/auth/passkey/login/finish", body)
		if response.Code != http.StatusBadRequest || c.store.sessions != count {
			t.Fatal("consumed ceremony was accepted")
		}
	}
}

func TestPasskeyRegistrationRequiresUserVerification(t *testing.T) {
	for _, tc := range []struct {
		name      string
		flags     byte
		oldPolicy bool
		status    int
	}{
		{"verified", 0x45, false, http.StatusCreated},
		{"unverified", 0x41, false, http.StatusBadRequest},
		{"missing presence", 0x44, false, http.StatusBadRequest},
		{"old preferred ceremony", 0x45, true, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newPasskeyTestClient(t)
			c.begin(true)
			if tc.oldPolicy {
				c.changeStoredPolicy(protocol.VerificationPreferred)
			}
			response := c.request("/v1/account/passkey/register/finish", c.body(true, tc.flags, passkeyTestOrigin, false))
			if response.Code != tc.status {
				t.Fatalf("status %d, want %d", response.Code, tc.status)
			}
			want := 0
			if tc.status == http.StatusCreated {
				want = 1
			}
			if c.store.registrations != want {
				t.Fatalf("saved %d credentials, want %d", c.store.registrations, want)
			}
		})
	}
}
