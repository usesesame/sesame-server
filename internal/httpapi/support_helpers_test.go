package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"usesesame.app/backend/internal/accounts"
	adminstore "usesesame.app/backend/internal/admin"
)

const (
	supportTestOrigin      = "https://account.example.invalid"
	supportTestAdminOrigin = "https://admin.example.invalid"
	supportTestOwnerEmail  = "support-owner@example.invalid"
	supportTestOtherEmail  = "support-other@example.invalid"
)

type supportTestEnv struct {
	handler      http.Handler
	accountStore *accounts.PostgresStore
	adminStore   *adminstore.Store
	db           *sql.DB
	ipPrefix     string
	ipCounter    int64
}

func newSupportTestEnv(t *testing.T, options ...func(*Config)) *supportTestEnv {
	t.Helper()
	databaseURL := os.Getenv("SESAME_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("SESAME_TEST_DATABASE_URL is required")
	}
	ctx := context.Background()
	accountStore, err := accounts.Open(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open account store: %v", err)
	}
	t.Cleanup(func() { _ = accountStore.Close() })
	db := accountStore.DB()
	lockDatabaseTests(t, db)
	if _, err := db.ExecContext(ctx, `
		TRUNCATE sesame_support_requests, sesame_support_messages, sesame_support_notes,
		         sesame_admin_audit_log, sesame_admin_sessions, sesame_admin_accounts
		RESTART IDENTITY CASCADE
	`); err != nil {
		t.Fatalf("clear support tables: %v", err)
	}
	adminStore, err := adminstore.Open(ctx, databaseURL, bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatalf("open admin store: %v", err)
	}
	t.Cleanup(func() { _ = adminStore.Close() })
	seed := time.Now().UnixNano()
	config := Config{
		Accounts:      accountStore,
		Admin:         adminStore,
		AllowedOrigin: supportTestOrigin,
		AdminOrigin:   supportTestAdminOrigin,
		WebBaseURL:    supportTestOrigin,
	}
	for _, option := range options {
		option(&config)
	}
	env := &supportTestEnv{
		handler:      New(config),
		accountStore: accountStore,
		adminStore:   adminStore,
		db:           db,
		ipPrefix:     fmt.Sprintf("10.%d.%d.", byte(seed>>16), byte(seed>>8)),
	}
	t.Cleanup(func() {
		if _, err := env.db.ExecContext(context.Background(), `DELETE FROM sesame_rate_limits WHERE key LIKE $1`, "%"+env.ipPrefix+"%"); err != nil {
			t.Error(err)
		}
	})
	return env
}

func (e *supportTestEnv) nextIP() string {
	return e.ipPrefix + fmt.Sprintf("%d", atomic.AddInt64(&e.ipCounter, 1))
}

func (e *supportTestEnv) request(method, path string, body any, options ...func(*http.Request)) *httptest.ResponseRecorder {
	var reader *bytes.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			panic(err)
		}
		reader = bytes.NewReader(encoded)
	}
	var request *http.Request
	if reader == nil {
		request = httptest.NewRequest(method, path, nil)
	} else {
		request = httptest.NewRequest(method, path, reader)
		request.Header.Set("Content-Type", "application/json")
	}
	request.RemoteAddr = e.nextIP() + ":40000"
	for _, option := range options {
		option(request)
	}
	response := httptest.NewRecorder()
	e.handler.ServeHTTP(response, request)
	return response
}

func supportSession(token string) func(*http.Request) {
	return func(request *http.Request) {
		request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	}
}

func supportWebMutation() func(*http.Request) {
	return func(request *http.Request) {
		request.Header.Set("Origin", supportTestOrigin)
		request.Header.Set("X-Sesame-CSRF", "test-csrf")
		request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "test-csrf"})
	}
}

func supportGuestIntakeEmail() string {
	return "guest-intake@example.invalid"
}

func (e *supportTestEnv) seedAccount(t *testing.T, id, email string) string {
	t.Helper()
	ctx := context.Background()
	if _, err := e.db.ExecContext(ctx, `DELETE FROM sesame_accounts WHERE id = $1`, id); err != nil {
		t.Fatalf("clear account %s: %v", id, err)
	}
	if _, err := e.db.ExecContext(ctx, `INSERT INTO sesame_accounts (id, email, password_hash) VALUES ($1, $2, 'fictional-test-hash')`, id, email); err != nil {
		t.Fatalf("seed account %s: %v", id, err)
	}
	t.Cleanup(func() {
		if _, err := e.db.ExecContext(context.Background(), `DELETE FROM sesame_accounts WHERE id = $1`, id); err != nil {
			t.Error(err)
		}
	})
	token := id + "-session-token"
	if err := e.accountStore.CreateSession(ctx, id, accounts.HashSessionToken(token), time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatalf("create session for %s: %v", id, err)
	}
	return token
}

func (e *supportTestEnv) seedAdmin(t *testing.T, id, email string, role adminstore.Role) adminstore.Account {
	t.Helper()
	if _, err := e.db.ExecContext(context.Background(), `DELETE FROM sesame_admin_accounts WHERE id = $1`, id); err != nil {
		t.Fatalf("clear admin %s: %v", id, err)
	}
	if _, err := e.db.ExecContext(context.Background(), `INSERT INTO sesame_admin_accounts (id, email, password_hash, role, totp_verified) VALUES ($1, $2, 'fictional-test-hash', $3, TRUE)`, id, email, string(role)); err != nil {
		t.Fatalf("seed admin %s: %v", id, err)
	}
	t.Cleanup(func() {
		if _, err := e.db.ExecContext(context.Background(), `DELETE FROM sesame_admin_accounts WHERE id = $1`, id); err != nil {
			t.Error(err)
		}
	})
	return adminstore.Account{ID: id, Email: email, Role: role}
}

func (e *supportTestEnv) seedTicket(t *testing.T, id, accountID, email string) {
	t.Helper()
	ctx := context.Background()
	var owner any
	if accountID != "" {
		owner = accountID
	}
	if _, err := e.db.ExecContext(ctx, `
		INSERT INTO sesame_support_requests (id, account_id, email, subject, message, category, status)
		VALUES ($1, $2, $3, 'Support test subject', 'Support test message body', 'general', 'open')`, id, owner, email); err != nil {
		t.Fatalf("seed ticket %s: %v", id, err)
	}
	if _, err := e.db.ExecContext(ctx, `INSERT INTO sesame_support_messages (id, ticket_id, author_role, body) VALUES ($1 || '-message', $1, 'user', 'Support test message body')`, id); err != nil {
		t.Fatalf("seed ticket message %s: %v", id, err)
	}
	t.Cleanup(func() {
		if _, err := e.db.ExecContext(context.Background(), `DELETE FROM sesame_support_requests WHERE id = $1`, id); err != nil {
			t.Error(err)
		}
	})
}

func (e *supportTestEnv) adminRequest(t *testing.T, actor adminstore.Account, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	token := fmt.Sprintf("test-admin-session-%s-%d", actor.ID, time.Now().UnixNano())
	if err := e.adminStore.CreateSession(context.Background(), actor, adminstore.HashToken(token), "", "support-test", time.Now().UTC().Add(time.Hour), time.Now().UTC().UnixNano()); err != nil {
		t.Fatalf("create admin session: %v", err)
	}
	var reader *bytes.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal admin request: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	var request *http.Request
	if reader == nil {
		request = httptest.NewRequest(method, path, nil)
	} else {
		request = httptest.NewRequest(method, path, reader)
		request.Header.Set("Content-Type", "application/json")
	}
	request.RemoteAddr = e.nextIP() + ":40000"
	request.Header.Set("Origin", supportTestAdminOrigin)
	request.Header.Set("X-Sesame-CSRF", "test-csrf")
	request.AddCookie(&http.Cookie{Name: adminSessionCookie, Value: token})
	request.AddCookie(&http.Cookie{Name: adminCSRFCookie, Value: "test-csrf"})
	response := httptest.NewRecorder()
	e.handler.ServeHTTP(response, request)
	return response
}

func errorCode(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode error body %q: %v", response.Body.String(), err)
	}
	return payload.Error.Code
}

func ticketStatus(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	return decodeTicket(t, response).Status
}

type supportTicketView struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	CanClose  bool   `json:"canClose"`
	CanReopen bool   `json:"canReopen"`
	Messages  []struct {
		AuthorRole string `json:"authorRole"`
		Body       string `json:"body"`
	} `json:"messages"`
}

func decodeTicket(t *testing.T, response *httptest.ResponseRecorder) supportTicketView {
	t.Helper()
	var payload struct {
		Ticket supportTicketView `json:"ticket"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode ticket body %q: %v", response.Body.String(), err)
	}
	return payload.Ticket
}
