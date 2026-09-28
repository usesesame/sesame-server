package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"usesesame.app/backend/internal/accounts"
	adminstore "usesesame.app/backend/internal/admin"
	"usesesame.app/backend/internal/support"
)

type supportQueuedEmail struct {
	db *sql.DB
}

func (s supportQueuedEmail) SendAccountEmail(ctx context.Context, message AccountEmail) error {
	return s.insert(ctx, s.db, message)
}

func (s supportQueuedEmail) SendAccountEmailTx(ctx context.Context, tx *sql.Tx, message AccountEmail) error {
	return s.insert(ctx, tx, message)
}

type supportOutboxInserter interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func (s supportQueuedEmail) insert(ctx context.Context, exec supportOutboxInserter, message AccountEmail) error {
	_, err := exec.ExecContext(ctx, `
		INSERT INTO sesame_email_outbox (kind, to_email, action_url, expires_at, subject, body, support_message_id)
		VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, ''))
	`, message.Kind, message.To, message.ActionURL, message.ExpiresAt.UTC(), message.Subject, message.Body, message.SupportMessageID)
	return err
}

type supportFailingEmail struct{}

func (supportFailingEmail) SendAccountEmail(context.Context, AccountEmail) error {
	return errors.New("fictional delivery is off")
}

func (supportFailingEmail) SendAccountEmailTx(context.Context, *sql.Tx, AccountEmail) error {
	return errors.New("fictional enqueue failure")
}

func (e *supportTestEnv) emailHandler(sender EmailSender) http.Handler {
	return New(Config{
		Accounts:      e.accountStore,
		Admin:         e.adminStore,
		EmailSender:   sender,
		AllowedOrigin: supportTestOrigin,
		AdminOrigin:   supportTestAdminOrigin,
		WebBaseURL:    supportTestOrigin,
	})
}

func requestOn(t *testing.T, handler http.Handler, env *supportTestEnv, method, path string, body any, options ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
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
	request.RemoteAddr = env.nextIP() + ":40000"
	for _, option := range options {
		option(request)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func adminRequestOn(t *testing.T, handler http.Handler, env *supportTestEnv, actor adminstore.Account, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	token := actor.ID + "-admin-link-session-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	if err := env.adminStore.CreateSession(context.Background(), actor, adminstore.HashToken(token), "", "support-link-test", time.Now().UTC().Add(time.Hour), time.Now().UnixNano()); err != nil {
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
	request.RemoteAddr = env.nextIP() + ":40000"
	request.Header.Set("Origin", supportTestAdminOrigin)
	request.Header.Set("X-Sesame-CSRF", "test-csrf")
	request.AddCookie(&http.Cookie{Name: adminSessionCookie, Value: token})
	request.AddCookie(&http.Cookie{Name: adminCSRFCookie, Value: "test-csrf"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func verifySupportAccount(t *testing.T, env *supportTestEnv, accountID string) {
	t.Helper()
	if _, err := env.db.ExecContext(context.Background(), `UPDATE sesame_accounts SET email_verified_at = NOW() WHERE id = $1`, accountID); err != nil {
		t.Fatalf("verify account %s: %v", accountID, err)
	}
}

func issueSupportLink(t *testing.T, env *supportTestEnv, ticketID, email string, expiresAt time.Time) (string, []byte) {
	t.Helper()
	token, tokenHash, err := accounts.NewSessionToken()
	if err != nil {
		t.Fatalf("generate link token: %v", err)
	}
	if err := support.IssueAccessLink(context.Background(), env.db, ticketID, email, tokenHash, expiresAt, time.Now().UTC()); err != nil {
		t.Fatalf("issue link for %s: %v", ticketID, err)
	}
	return token, tokenHash
}

func supportLinkFailureBody() string {
	return `{"error":{"code":"support_link_invalid","message":"` + supportLinkInvalidMessage + `"}}` + "\n"
}

func supportOutboxLinkToken(t *testing.T, db *sql.DB, ticketID string, newest bool) string {
	t.Helper()
	order := "ASC"
	if newest {
		order = "DESC"
	}
	var actionURL string
	if err := db.QueryRowContext(context.Background(), `
		SELECT email.action_url FROM sesame_email_outbox email
		JOIN sesame_support_messages message ON message.id = email.support_message_id
		WHERE message.ticket_id = $1 AND message.author_role = 'staff'
		ORDER BY message.created_at `+order+` LIMIT 1
	`, ticketID).Scan(&actionURL); err != nil {
		t.Fatalf("read link email for %s: %v", ticketID, err)
	}
	const marker = "#token="
	index := strings.Index(actionURL, marker)
	if index < 0 {
		t.Fatalf("link email action URL has no fragment token: %q", actionURL)
	}
	return actionURL[index+len(marker):]
}

func TestSupportAccessLinkMigrationOnFreshDatabase(t *testing.T) {
	conn := newSupportMigrationSchema(t, supportEmailDatabase(t))
	applySupportMigrationsThrough(t, conn, "0041")
	ctx := context.Background()

	var columns int
	if err := conn.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'sesame_support_access_links'
	`).Scan(&columns); err != nil {
		t.Fatalf("count link columns: %v", err)
	}
	if columns != 7 {
		t.Fatalf("link columns = %d, want 7", columns)
	}
	var indexes int
	if err := conn.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM pg_indexes
		WHERE schemaname = current_schema() AND tablename = 'sesame_support_access_links'
	`).Scan(&indexes); err != nil {
		t.Fatalf("count link indexes: %v", err)
	}
	if indexes != 3 {
		t.Fatalf("link indexes = %d, want the primary key and both named indexes", indexes)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO sesame_support_requests (id, email, subject, message) VALUES ('ticket-cascade', 'guest@example.invalid', 'Subject', 'Body')`); err != nil {
		t.Fatalf("seed cascade ticket: %v", err)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO sesame_support_access_links (token_hash, ticket_id, requester_email, expires_at) VALUES ($1, 'ticket-cascade', 'guest@example.invalid', NOW() + INTERVAL '7 days')`, bytes.Repeat([]byte{7}, 32)); err != nil {
		t.Fatalf("seed cascade link: %v", err)
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM sesame_support_requests WHERE id = 'ticket-cascade'`); err != nil {
		t.Fatalf("delete cascade ticket: %v", err)
	}
	var links int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM sesame_support_access_links`).Scan(&links); err != nil {
		t.Fatalf("count links after cascade: %v", err)
	}
	if links != 0 {
		t.Fatalf("links after ticket delete = %d, want cascade delete", links)
	}
}

func TestSupportAccessLinkMigrationUpgradesAnExistingSchema(t *testing.T) {
	conn := newSupportMigrationSchema(t, supportEmailDatabase(t))
	applySupportMigrationsThrough(t, conn, "0040")
	body, err := os.ReadFile(filepath.Join("..", "accounts", "migrations", "0041_support_access_links.sql"))
	if err != nil {
		t.Fatalf("read migration 0041: %v", err)
	}
	if _, err := conn.ExecContext(context.Background(), string(body)); err != nil {
		t.Fatalf("apply migration 0041 to an existing schema: %v", err)
	}
	if _, err := conn.ExecContext(context.Background(), `INSERT INTO sesame_support_requests (id, email, subject, message) VALUES ('ticket-upgrade', 'guest@example.invalid', 'Subject', 'Body')`); err != nil {
		t.Fatalf("seed upgrade ticket: %v", err)
	}
	if _, err := conn.ExecContext(context.Background(), `INSERT INTO sesame_support_access_links (token_hash, ticket_id, requester_email, expires_at) VALUES ($1, 'ticket-upgrade', 'guest@example.invalid', NOW() + INTERVAL '7 days')`, bytes.Repeat([]byte{8}, 32)); err != nil {
		t.Fatalf("insert link after upgrade: %v", err)
	}
}

func TestSupportGuestLinkJourneyQueuesEmailAndAttaches(t *testing.T) {
	env := newSupportTestEnv(t)
	handler := env.emailHandler(supportQueuedEmail{db: env.db})
	staff := env.seedAdmin(t, "admin-guest-link", "staff-guest-link@example.invalid", adminstore.RoleSupport)
	guestEmail := "guest-journey-" + strconv.FormatInt(time.Now().UnixNano(), 10) + "@example.invalid"
	const (
		guestBody = "The fictional journey message body."
		staffBody = "A fictional staff answer."
	)

	response := requestOn(t, handler, env, http.MethodPost, "/v1/support/requests", map[string]any{
		"email":   guestEmail,
		"subject": "Fictional journey subject",
		"message": guestBody,
	}, supportWebMutation())
	if response.Code != http.StatusAccepted {
		t.Fatalf("intake = %d: %s", response.Code, response.Body.String())
	}
	var receipt struct {
		RequestID string `json:"requestId"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &receipt); err != nil || receipt.RequestID == "" {
		t.Fatalf("decode intake receipt %q: %v", response.Body.String(), err)
	}
	reference := receipt.RequestID

	response = adminRequestOn(t, handler, env, staff, http.MethodPost, "/v1/admin/support/"+reference+"/reply", map[string]any{"body": staffBody})
	if response.Code != http.StatusOK {
		t.Fatalf("staff reply = %d: %s", response.Code, response.Body.String())
	}

	var kind, actionURL, subject, body string
	if err := env.db.QueryRowContext(context.Background(), `
		SELECT email.kind, email.action_url, email.subject, email.body
		FROM sesame_email_outbox email
		JOIN sesame_support_messages message ON message.id = email.support_message_id
		WHERE message.ticket_id = $1 AND message.author_role = 'staff'
	`, reference).Scan(&kind, &actionURL, &subject, &body); err != nil {
		t.Fatalf("read queued link email: %v", err)
	}
	if kind != "support-reply" {
		t.Fatalf("link email kind = %q, want support-reply", kind)
	}
	if !strings.HasPrefix(actionURL, supportTestOrigin+"/support/request#token=") {
		t.Fatalf("link action URL = %q, want the guest page fragment", actionURL)
	}
	for _, forbidden := range []string{staffBody, guestBody, "Fictional journey subject", reference} {
		if strings.Contains(body, forbidden) || strings.Contains(subject, forbidden) {
			t.Fatalf("link email leaked %q in subject %q body %q", forbidden, subject, body)
		}
	}
	token := strings.TrimPrefix(actionURL, supportTestOrigin+"/support/request#token=")
	if len(token) != 43 {
		t.Fatalf("link token length = %d, want 43", len(token))
	}

	var requesterEmail string
	var revokedAt sql.NullTime
	var expiresAt time.Time
	if err := env.db.QueryRowContext(context.Background(), `
		SELECT requester_email, revoked_at, expires_at FROM sesame_support_access_links WHERE ticket_id = $1
	`, reference).Scan(&requesterEmail, &revokedAt, &expiresAt); err != nil {
		t.Fatalf("read link row: %v", err)
	}
	if requesterEmail != guestEmail || revokedAt.Valid {
		t.Fatalf("link row = %q revoked %v, want the live guest address", requesterEmail, revokedAt)
	}
	if until := time.Until(expiresAt); until < 6*24*time.Hour || until > 8*24*time.Hour {
		t.Fatalf("link expiry = %v, want about 7 days", expiresAt)
	}

	for attempt := 1; attempt <= 2; attempt++ {
		response = env.request(http.MethodPost, "/v1/support/access", map[string]any{"token": token}, supportWebMutation())
		if response.Code != http.StatusOK {
			t.Fatalf("guest read %d = %d: %s", attempt, response.Code, response.Body.String())
		}
		ticket := decodeTicket(t, response)
		if ticket.CanClose || ticket.CanReopen {
			t.Fatal("a guest read must not offer close or reopen")
		}
		if !strings.Contains(response.Body.String(), staffBody) {
			t.Fatalf("guest read %d did not include the staff reply", attempt)
		}
	}

	response = env.request(http.MethodPost, "/v1/support/access/reply", map[string]any{"token": token, "message": "Fictional guest follow-up."}, supportWebMutation())
	if response.Code != http.StatusCreated {
		t.Fatalf("guest reply = %d: %s", response.Code, response.Body.String())
	}
	response = env.adminRequest(t, staff, http.MethodGet, "/v1/admin/support/"+reference, nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Fictional guest follow-up.") {
		t.Fatalf("staff view = %d: %s", response.Code, response.Body.String())
	}

	accountToken := env.seedAccount(t, "acct-journey", guestEmail)
	verifySupportAccount(t, env, "acct-journey")
	response = env.request(http.MethodPost, "/v1/account/support/"+reference+"/attach", nil, supportSession(accountToken), supportWebMutation())
	if response.Code != http.StatusOK || decodeTicket(t, response).ID != reference {
		t.Fatalf("attach = %d: %s", response.Code, response.Body.String())
	}
	var owner sql.NullString
	if err := env.db.QueryRowContext(context.Background(), `SELECT account_id FROM sesame_support_requests WHERE id = $1`, reference).Scan(&owner); err != nil {
		t.Fatalf("read ticket owner: %v", err)
	}
	if !owner.Valid || owner.String != "acct-journey" {
		t.Fatalf("ticket owner = %v, want acct-journey", owner)
	}
	var liveLinks int
	if err := env.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM sesame_support_access_links WHERE ticket_id = $1 AND revoked_at IS NULL`, reference).Scan(&liveLinks); err != nil {
		t.Fatalf("count live links: %v", err)
	}
	if liveLinks != 0 {
		t.Fatalf("live links after attach = %d, want 0", liveLinks)
	}
	response = env.request(http.MethodPost, "/v1/support/access", map[string]any{"token": token}, supportWebMutation())
	if response.Code != http.StatusBadRequest || response.Body.String() != supportLinkFailureBody() {
		t.Fatalf("guest read after attach = %d %q", response.Code, response.Body.String())
	}
	response = env.request(http.MethodGet, "/v1/account/support", nil, supportSession(accountToken))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), reference) {
		t.Fatalf("account list after attach = %d: %s", response.Code, response.Body.String())
	}
}

func TestSupportStaffReplyReplacesTheLiveLink(t *testing.T) {
	env := newSupportTestEnv(t)
	handler := env.emailHandler(supportQueuedEmail{db: env.db})
	staff := env.seedAdmin(t, "admin-link-replace", "staff-link-replace@example.invalid", adminstore.RoleSupport)
	env.seedTicket(t, "ticket-link-replace", "", "replace@example.invalid")

	for _, body := range []string{"First fictional staff answer.", "Second fictional staff answer."} {
		response := adminRequestOn(t, handler, env, staff, http.MethodPost, "/v1/admin/support/ticket-link-replace/reply", map[string]any{"body": body})
		if response.Code != http.StatusOK {
			t.Fatalf("staff reply = %d: %s", response.Code, response.Body.String())
		}
	}
	firstToken := supportOutboxLinkToken(t, env.db, "ticket-link-replace", false)
	var firstRevoked sql.NullTime
	if err := env.db.QueryRowContext(context.Background(), `
		SELECT revoked_at FROM sesame_support_access_links WHERE ticket_id = 'ticket-link-replace' AND revoked_at IS NOT NULL
	`).Scan(&firstRevoked); err != nil {
		t.Fatalf("read replaced link: %v", err)
	}
	response := env.request(http.MethodPost, "/v1/support/access", map[string]any{"token": firstToken}, supportWebMutation())
	if response.Code != http.StatusBadRequest || response.Body.String() != supportLinkFailureBody() {
		t.Fatalf("superseded link = %d %q", response.Code, response.Body.String())
	}
	response = env.request(http.MethodPost, "/v1/support/access", map[string]any{"token": supportOutboxLinkToken(t, env.db, "ticket-link-replace", true)}, supportWebMutation())
	if response.Code != http.StatusOK {
		t.Fatalf("newest link = %d: %s", response.Code, response.Body.String())
	}
}

func TestSupportAccessLinkFailureModesAreIndistinguishable(t *testing.T) {
	env := newSupportTestEnv(t)
	baseline := env.request(http.MethodPost, "/v1/support/access", map[string]any{"token": supportAccessToken(21)}, supportWebMutation())
	if baseline.Code != http.StatusBadRequest || baseline.Body.String() != supportLinkFailureBody() {
		t.Fatalf("baseline unknown link = %d %q", baseline.Code, baseline.Body.String())
	}
	ctx := context.Background()
	now := time.Now().UTC()

	cases := []struct {
		name   string
		mutate func(t *testing.T, ticketID string)
	}{
		{name: "expired", mutate: func(t *testing.T, ticketID string) {
			if _, err := env.db.ExecContext(ctx, `UPDATE sesame_support_access_links SET expires_at = NOW() - INTERVAL '1 minute' WHERE ticket_id = $1`, ticketID); err != nil {
				t.Fatalf("expire link: %v", err)
			}
		}},
		{name: "revoked", mutate: func(t *testing.T, ticketID string) {
			if _, err := support.RevokeAccessLinks(ctx, env.db, ticketID, now); err != nil {
				t.Fatalf("revoke link: %v", err)
			}
		}},
		{name: "closed and reopened", mutate: func(t *testing.T, ticketID string) {
			if _, err := support.Close(ctx, env.db, ticketID, "", now, ""); err != nil {
				t.Fatalf("close ticket: %v", err)
			}
			if _, err := support.SetOpenStatus(ctx, env.db, ticketID, "open", now, ""); err != nil {
				t.Fatalf("reopen ticket: %v", err)
			}
		}},
		{name: "attached", mutate: func(t *testing.T, ticketID string) {
			env.seedAccount(t, "acct-mismatch-owner", "mismatch-owner@example.invalid")
			if _, err := env.db.ExecContext(ctx, `UPDATE sesame_support_requests SET account_id = 'acct-mismatch-owner' WHERE id = $1`, ticketID); err != nil {
				t.Fatalf("attach ticket: %v", err)
			}
		}},
		{name: "address mismatch", mutate: func(t *testing.T, ticketID string) {
			if _, err := env.db.ExecContext(ctx, `UPDATE sesame_support_requests SET email = 'moved-away@example.invalid' WHERE id = $1`, ticketID); err != nil {
				t.Fatalf("change address: %v", err)
			}
		}},
	}
	for index, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			ticketID := "ticket-failure-" + strings.ReplaceAll(testCase.name, " ", "-")
			env.seedTicket(t, ticketID, "", "failure-"+string(rune('a'+index))+"@example.invalid")
			token, _ := issueSupportLink(t, env, ticketID, "failure-"+string(rune('a'+index))+"@example.invalid", now.Add(7*24*time.Hour))
			testCase.mutate(t, ticketID)
			response := env.request(http.MethodPost, "/v1/support/access", map[string]any{"token": token}, supportWebMutation())
			if response.Code != http.StatusBadRequest || response.Body.String() != baseline.Body.String() {
				t.Fatalf("stale link = %d %q, want the generic body %q", response.Code, response.Body.String(), baseline.Body.String())
			}
			response = env.request(http.MethodPost, "/v1/support/access/reply", map[string]any{"token": token, "message": "A fictional follow-up."}, supportWebMutation())
			if response.Code != http.StatusBadRequest || response.Body.String() != baseline.Body.String() {
				t.Fatalf("stale reply = %d %q, want the generic body %q", response.Code, response.Body.String(), baseline.Body.String())
			}
		})
	}
}

func TestSupportAccessNeverResolvesForeignRequests(t *testing.T) {
	env := newSupportTestEnv(t)
	ctx := context.Background()
	env.seedTicket(t, "ticket-foreign-a", "", "foreign-a@example.invalid")
	if _, err := env.db.ExecContext(ctx, `INSERT INTO sesame_support_requests (id, email, subject, message) VALUES ('ticket-foreign-b', 'foreign-b@example.invalid', 'Foreign B subject', 'Foreign B message')`); err != nil {
		t.Fatalf("seed foreign ticket: %v", err)
	}
	if _, err := env.db.ExecContext(ctx, `INSERT INTO sesame_support_messages (id, ticket_id, author_role, body) VALUES ('ticket-foreign-b-message', 'ticket-foreign-b', 'user', 'Foreign B message')`); err != nil {
		t.Fatalf("seed foreign message: %v", err)
	}
	token, _ := issueSupportLink(t, env, "ticket-foreign-a", "foreign-a@example.invalid", time.Now().UTC().Add(7*24*time.Hour))

	response := env.request(http.MethodPost, "/v1/support/access", map[string]any{"token": token, "requestId": "ticket-foreign-b"}, supportWebMutation())
	if response.Code != http.StatusBadRequest || response.Body.String() != supportLinkFailureBody() {
		t.Fatalf("foreign field = %d %q", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "ticket-foreign-b") || strings.Contains(response.Body.String(), "Foreign B subject") {
		t.Fatal("a rejected request named the other request")
	}
	response = env.request(http.MethodPost, "/v1/support/access", map[string]any{"token": token}, supportWebMutation())
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "ticket-foreign-a") {
		t.Fatalf("own request = %d: %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "ticket-foreign-b") || strings.Contains(response.Body.String(), "Foreign B subject") {
		t.Fatal("a token for one request returned another request")
	}

	for _, body := range []map[string]any{{"token": "ticket-foreign-b"}, {}} {
		response = env.request(http.MethodPost, "/v1/support/access", body, supportWebMutation())
		if response.Code != http.StatusBadRequest || response.Body.String() != supportLinkFailureBody() {
			t.Fatalf("reference as token = %d %q", response.Code, response.Body.String())
		}
	}
	otherToken := env.seedAccount(t, "acct-foreign-other", "foreign-other@example.invalid")
	verifySupportAccount(t, env, "acct-foreign-other")
	response = env.request(http.MethodPost, "/v1/account/support/ticket-foreign-b/attach", nil, supportSession(otherToken), supportWebMutation())
	if response.Code != http.StatusNotFound || errorCode(t, response) != "support_request_not_found" {
		t.Fatalf("foreign attach = %d %q", response.Code, errorCode(t, response))
	}
}

func TestSupportAccessReplyRulesAndSecretGuard(t *testing.T) {
	env := newSupportTestEnv(t)
	env.seedTicket(t, "ticket-reply-rules", "", "reply-rules@example.invalid")
	token, _ := issueSupportLink(t, env, "ticket-reply-rules", "reply-rules@example.invalid", time.Now().UTC().Add(7*24*time.Hour))

	response := env.request(http.MethodPost, "/v1/support/access/reply", map[string]any{"token": token, "message": "My password = hunter2 and it fails."}, supportWebMutation())
	if response.Code != http.StatusBadRequest || errorCode(t, response) != "secret_shaped_content" {
		t.Fatalf("secret reply = %d %q", response.Code, errorCode(t, response))
	}
	var messages int
	if err := env.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM sesame_support_messages WHERE ticket_id = 'ticket-reply-rules'`).Scan(&messages); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if messages != 1 {
		t.Fatalf("messages after rejected secret = %d, want the original only", messages)
	}
	response = env.request(http.MethodPost, "/v1/support/access/reply", map[string]any{"token": token, "message": "x"}, supportWebMutation())
	if response.Code != http.StatusBadRequest || errorCode(t, response) != "invalid_support_reply" {
		t.Fatalf("short reply = %d %q", response.Code, errorCode(t, response))
	}
	response = env.request(http.MethodPost, "/v1/support/access/reply", map[string]any{"token": token, "message": "The fictional fix worked."}, supportWebMutation())
	if response.Code != http.StatusCreated {
		t.Fatalf("valid reply = %d: %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "hunter2") {
		t.Fatal("a rejected secret appeared in a later response")
	}
}

func TestSupportAccessDatabaseBursts(t *testing.T) {
	env := newSupportTestEnv(t)
	env.seedTicket(t, "ticket-burst", "", "burst-link@example.invalid")
	token, _ := issueSupportLink(t, env, "ticket-burst", "burst-link@example.invalid", time.Now().UTC().Add(7*24*time.Hour))
	readIP := env.ipPrefix + "240"
	readOptions := func(request *http.Request) {
		request.RemoteAddr = readIP + ":40000"
		request.Header.Set("Origin", supportTestOrigin)
		request.Header.Set("X-Sesame-CSRF", "test-csrf")
		request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "test-csrf"})
	}
	for attempt := 1; attempt <= 60; attempt++ {
		response := env.request(http.MethodPost, "/v1/support/access", map[string]any{"token": supportAccessToken(31)}, readOptions)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("read attempt %d = %d, want 400", attempt, response.Code)
		}
	}
	response := env.request(http.MethodPost, "/v1/support/access", map[string]any{"token": supportAccessToken(31)}, readOptions)
	if response.Code != http.StatusTooManyRequests || errorCode(t, response) != "too_many_attempts" || response.Header().Get("Retry-After") == "" {
		t.Fatalf("read burst = %d %q retry %q", response.Code, errorCode(t, response), response.Header().Get("Retry-After"))
	}
	response = env.request(http.MethodPost, "/v1/support/access", map[string]any{"token": token}, readOptions)
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("valid token after the read burst = %d, want 429", response.Code)
	}

	replyIP := env.ipPrefix + "241"
	for attempt := 1; attempt <= 12; attempt++ {
		response := env.request(http.MethodPost, "/v1/support/access/reply", map[string]any{"token": token, "message": "x"}, func(request *http.Request) {
			request.RemoteAddr = replyIP + ":40000"
		}, supportWebMutation())
		if response.Code != http.StatusBadRequest {
			t.Fatalf("reply attempt %d = %d, want 400", attempt, response.Code)
		}
	}
	response = env.request(http.MethodPost, "/v1/support/access/reply", map[string]any{"token": token, "message": "x"}, func(request *http.Request) {
		request.RemoteAddr = replyIP + ":40000"
	}, supportWebMutation())
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("reply client burst = %d, want 429", response.Code)
	}

	perLinkToken, _ := issueSupportLink(t, env, "ticket-burst", "burst-link@example.invalid", time.Now().UTC().Add(7*24*time.Hour))
	for attempt := 1; attempt <= 20; attempt++ {
		response := env.request(http.MethodPost, "/v1/support/access/reply", map[string]any{"token": perLinkToken, "message": "x"}, func(request *http.Request) {
			request.RemoteAddr = env.ipPrefix + strconv.Itoa(100+attempt) + ":40000"
		}, supportWebMutation())
		if response.Code != http.StatusBadRequest {
			t.Fatalf("per-link attempt %d = %d, want 400", attempt, response.Code)
		}
	}
	response = env.request(http.MethodPost, "/v1/support/access/reply", map[string]any{"token": perLinkToken, "message": "x"}, func(request *http.Request) {
		request.RemoteAddr = env.ipPrefix + "199:40000"
	}, supportWebMutation())
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("per-link burst = %d, want 429", response.Code)
	}
}

func TestSupportAttachRules(t *testing.T) {
	env := newSupportTestEnv(t)
	env.seedTicket(t, "ticket-attach", "", "attach-guest@example.invalid")
	token, _ := issueSupportLink(t, env, "ticket-attach", "attach-guest@example.invalid", time.Now().UTC().Add(7*24*time.Hour))

	response := env.request(http.MethodPost, "/v1/account/support/ticket-attach/attach", nil, supportWebMutation())
	if response.Code != http.StatusUnauthorized || errorCode(t, response) != "not_authenticated" {
		t.Fatalf("attach without a session = %d %q", response.Code, errorCode(t, response))
	}
	unverifiedToken := env.seedAccount(t, "acct-attach-unverified", "attach-guest@example.invalid")
	response = env.request(http.MethodPost, "/v1/account/support/ticket-attach/attach", nil, supportSession(unverifiedToken), supportWebMutation())
	if response.Code != http.StatusForbidden || errorCode(t, response) != "email_unverified" {
		t.Fatalf("attach with an unverified account = %d %q", response.Code, errorCode(t, response))
	}
	otherToken := env.seedAccount(t, "acct-attach-other", "attach-other@example.invalid")
	verifySupportAccount(t, env, "acct-attach-other")
	response = env.request(http.MethodPost, "/v1/account/support/ticket-attach/attach", nil, supportSession(otherToken), supportWebMutation())
	if response.Code != http.StatusNotFound || errorCode(t, response) != "support_request_not_found" {
		t.Fatalf("attach with another address = %d %q", response.Code, errorCode(t, response))
	}
	verifySupportAccount(t, env, "acct-attach-unverified")
	response = env.request(http.MethodPost, "/v1/account/support/ticket-attach/attach", nil, supportSession(unverifiedToken), supportWebMutation())
	if response.Code != http.StatusOK || decodeTicket(t, response).ID != "ticket-attach" {
		t.Fatalf("attach = %d: %s", response.Code, response.Body.String())
	}
	var liveLinks int
	if err := env.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM sesame_support_access_links WHERE ticket_id = 'ticket-attach' AND revoked_at IS NULL`).Scan(&liveLinks); err != nil {
		t.Fatalf("count live links: %v", err)
	}
	if liveLinks != 0 {
		t.Fatalf("live links after attach = %d, want 0", liveLinks)
	}
	response = env.request(http.MethodPost, "/v1/support/access", map[string]any{"token": token}, supportWebMutation())
	if response.Code != http.StatusBadRequest || response.Body.String() != supportLinkFailureBody() {
		t.Fatalf("guest link after attach = %d %q", response.Code, response.Body.String())
	}
	response = env.request(http.MethodPost, "/v1/account/support/ticket-attach/attach", nil, supportSession(otherToken), supportWebMutation())
	if response.Code != http.StatusNotFound {
		t.Fatalf("second attach = %d, want 404", response.Code)
	}

	env.seedTicket(t, "ticket-attach-closed", "", "attach-guest@example.invalid")
	if _, err := support.Close(context.Background(), env.db, "ticket-attach-closed", "", time.Now().UTC(), ""); err != nil {
		t.Fatalf("close guest ticket: %v", err)
	}
	response = env.request(http.MethodPost, "/v1/account/support/ticket-attach-closed/attach", nil, supportSession(unverifiedToken), supportWebMutation())
	if response.Code != http.StatusOK || decodeTicket(t, response).Status != "closed" {
		t.Fatalf("attach a closed guest request = %d: %s", response.Code, response.Body.String())
	}
	response = env.request(http.MethodPost, "/v1/account/support/ticket-attach-closed/reopen", nil, supportSession(unverifiedToken), supportWebMutation())
	if response.Code != http.StatusOK || decodeTicket(t, response).Status != "open" {
		t.Fatalf("reopen after attach = %d: %s", response.Code, response.Body.String())
	}
}

func TestSupportAttachRaceKeepsOneOwner(t *testing.T) {
	env := newSupportTestEnv(t)
	env.seedTicket(t, "ticket-attach-race", "", "attach-race@example.invalid")
	accountToken := env.seedAccount(t, "acct-attach-race", "attach-race@example.invalid")
	verifySupportAccount(t, env, "acct-attach-race")
	handler := env.handler

	attach := func() int {
		request := httptest.NewRequest(http.MethodPost, "/v1/account/support/ticket-attach-race/attach", nil)
		request.RemoteAddr = env.nextIP() + ":40000"
		request.Header.Set("Origin", supportTestOrigin)
		request.Header.Set("X-Sesame-CSRF", "test-csrf")
		request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: accountToken})
		request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "test-csrf"})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response.Code
	}
	start := make(chan struct{})
	results := make(chan int, 2)
	var wait sync.WaitGroup
	for attempt := 0; attempt < 2; attempt++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			results <- attach()
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	codes := map[int]int{}
	for code := range results {
		codes[code]++
	}
	if codes[http.StatusOK] != 1 || codes[http.StatusNotFound] != 1 {
		t.Fatalf("attach race codes = %v, want one 200 and one 404", codes)
	}
}

func TestSupportCloseRaceLeavesNoLiveLink(t *testing.T) {
	env := newSupportTestEnv(t)
	env.seedTicket(t, "ticket-close-race", "", "close-race@example.invalid")
	token, _ := issueSupportLink(t, env, "ticket-close-race", "close-race@example.invalid", time.Now().UTC().Add(7*24*time.Hour))

	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		_, _ = support.Close(context.Background(), env.db, "ticket-close-race", "", time.Now().UTC(), "")
	}()
	go func() {
		defer wait.Done()
		request := httptest.NewRequest(http.MethodPost, "/v1/support/access", strings.NewReader(`{"token":"`+token+`"}`))
		request.Header.Set("Content-Type", "application/json")
		request.RemoteAddr = env.nextIP() + ":40000"
		request.Header.Set("Origin", supportTestOrigin)
		request.Header.Set("X-Sesame-CSRF", "test-csrf")
		request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "test-csrf"})
		response := httptest.NewRecorder()
		env.handler.ServeHTTP(response, request)
		if code := response.Code; code != http.StatusOK && code != http.StatusBadRequest {
			t.Errorf("racing redemption = %d, want 200 or the generic 400", code)
		}
	}()
	wait.Wait()
	var liveLinks int
	if err := env.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM sesame_support_access_links WHERE ticket_id = 'ticket-close-race' AND revoked_at IS NULL`).Scan(&liveLinks); err != nil {
		t.Fatalf("count live links: %v", err)
	}
	if liveLinks != 0 {
		t.Fatalf("live links after the close race = %d, want 0", liveLinks)
	}
	response := env.request(http.MethodPost, "/v1/support/access", map[string]any{"token": token}, supportWebMutation())
	if response.Code != http.StatusBadRequest || response.Body.String() != supportLinkFailureBody() {
		t.Fatalf("redemption after the close race = %d %q", response.Code, response.Body.String())
	}
}

func TestSupportStaffReplyWithoutMailIsUndeliveredAndIssuesNoLink(t *testing.T) {
	env := newSupportTestEnv(t)
	staff := env.seedAdmin(t, "admin-link-off", "staff-link-off@example.invalid", adminstore.RoleSupport)
	env.seedTicket(t, "ticket-link-off", "", "link-off@example.invalid")

	response := env.adminRequest(t, staff, http.MethodPost, "/v1/admin/support/ticket-link-off/reply", map[string]any{"body": "A fictional undelivered answer."})
	if response.Code != http.StatusOK {
		t.Fatalf("reply with mail off = %d: %s", response.Code, response.Body.String())
	}
	var sentViaEmail bool
	if err := env.db.QueryRowContext(context.Background(), `SELECT sent_via_email FROM sesame_support_messages WHERE ticket_id = 'ticket-link-off' AND author_role = 'staff'`).Scan(&sentViaEmail); err != nil {
		t.Fatalf("read staff message: %v", err)
	}
	if sentViaEmail {
		t.Fatal("a reply with no mail sender must stay undelivered")
	}
	var links, outbox int
	if err := env.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM sesame_support_access_links WHERE ticket_id = 'ticket-link-off'`).Scan(&links); err != nil {
		t.Fatalf("count links: %v", err)
	}
	if err := env.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM sesame_email_outbox`).Scan(&outbox); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	if links != 0 || outbox != 0 {
		t.Fatalf("links = %d outbox = %d, want none when mail is off", links, outbox)
	}
}

func TestSupportGuestReplyLinkRollsBackWithFailedEnqueue(t *testing.T) {
	env := newSupportTestEnv(t)
	handler := env.emailHandler(supportFailingEmail{})
	staff := env.seedAdmin(t, "admin-link-rollback", "staff-link-rollback@example.invalid", adminstore.RoleSupport)
	env.seedTicket(t, "ticket-link-rollback", "", "link-rollback@example.invalid")

	response := adminRequestOn(t, handler, env, staff, http.MethodPost, "/v1/admin/support/ticket-link-rollback/reply", map[string]any{"body": "A fictional rolled-back answer."})
	if response.Code != http.StatusServiceUnavailable || errorCode(t, response) != "admin_action_unavailable" {
		t.Fatalf("failed enqueue = %d %q", response.Code, errorCode(t, response))
	}
	var messages, links, outbox int
	if err := env.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM sesame_support_messages WHERE ticket_id = 'ticket-link-rollback' AND author_role = 'staff'`).Scan(&messages); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if err := env.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM sesame_support_access_links WHERE ticket_id = 'ticket-link-rollback'`).Scan(&links); err != nil {
		t.Fatalf("count links: %v", err)
	}
	if err := env.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM sesame_email_outbox`).Scan(&outbox); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	if messages != 0 || links != 0 || outbox != 0 {
		t.Fatalf("after a failed enqueue: messages %d links %d outbox %d, want none", messages, links, outbox)
	}
	var status string
	if err := env.db.QueryRowContext(context.Background(), `SELECT status FROM sesame_support_requests WHERE id = 'ticket-link-rollback'`).Scan(&status); err != nil {
		t.Fatalf("read ticket status: %v", err)
	}
	if status != "open" {
		t.Fatalf("ticket status after a failed enqueue = %q, want open", status)
	}
}
