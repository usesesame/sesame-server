package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func decodeSupportTickets(t *testing.T, response *httptest.ResponseRecorder) []struct {
	ID     string `json:"id"`
	Status string `json:"status"`
} {
	t.Helper()
	var payload struct {
		Tickets []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"tickets"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode ticket list %q: %v", response.Body.String(), err)
	}
	return payload.Tickets
}

func TestAccountSupportListAndDetailOwnership(t *testing.T) {
	env := newSupportTestEnv(t)
	ownerToken := env.seedAccount(t, "acct-support-owner", supportTestOwnerEmail)
	otherToken := env.seedAccount(t, "acct-support-other", supportTestOtherEmail)
	env.seedTicket(t, "ticket-support-owner", "acct-support-owner", supportTestOwnerEmail)
	env.seedTicket(t, "ticket-support-guest", "", "guest-ticket@example.invalid")

	response := env.request(http.MethodGet, "/v1/account/support", nil)
	if response.Code != http.StatusUnauthorized || errorCode(t, response) != "not_authenticated" {
		t.Fatalf("anonymous list = %d %q", response.Code, errorCode(t, response))
	}
	response = env.request(http.MethodGet, "/v1/account/support", nil, supportSession("fictional-wrong-token"))
	if response.Code != http.StatusUnauthorized || errorCode(t, response) != "session_expired" {
		t.Fatalf("invalid session list = %d %q", response.Code, errorCode(t, response))
	}

	response = env.request(http.MethodGet, "/v1/account/support", nil, supportSession(ownerToken))
	if response.Code != http.StatusOK {
		t.Fatalf("owner list = %d: %s", response.Code, response.Body.String())
	}
	tickets := decodeSupportTickets(t, response)
	if len(tickets) != 1 || tickets[0].ID != "ticket-support-owner" || tickets[0].Status != "open" {
		t.Fatalf("owner tickets = %+v, want only the owned open ticket", tickets)
	}

	response = env.request(http.MethodGet, "/v1/account/support/ticket-support-owner", nil, supportSession(ownerToken))
	if response.Code != http.StatusOK || ticketStatus(t, response) != "open" {
		t.Fatalf("owner detail = %d %q: %s", response.Code, ticketStatus(t, response), response.Body.String())
	}
	response = env.request(http.MethodGet, "/v1/account/support/ticket-support-owner", nil, supportSession(otherToken))
	if response.Code != http.StatusNotFound || errorCode(t, response) != "support_request_not_found" {
		t.Fatalf("foreign detail = %d %q", response.Code, errorCode(t, response))
	}
	response = env.request(http.MethodGet, "/v1/account/support/"+strings.Repeat("a", 129), nil, supportSession(ownerToken))
	if response.Code != http.StatusNotFound || errorCode(t, response) != "not_found" {
		t.Fatalf("overlong ticket id = %d %q", response.Code, errorCode(t, response))
	}
}

func TestAccountSupportReplyCloseAndReopenRules(t *testing.T) {
	env := newSupportTestEnv(t)
	ownerToken := env.seedAccount(t, "acct-support-owner", supportTestOwnerEmail)
	otherToken := env.seedAccount(t, "acct-support-other", supportTestOtherEmail)
	env.seedTicket(t, "ticket-support-rules", "acct-support-owner", supportTestOwnerEmail)

	response := env.request(http.MethodPost, "/v1/account/support/ticket-support-rules/reply", map[string]any{"message": "Any update on this?"}, supportSession(otherToken), supportWebMutation())
	if response.Code != http.StatusNotFound || errorCode(t, response) != "support_request_not_found" {
		t.Fatalf("foreign reply = %d %q", response.Code, errorCode(t, response))
	}

	response = env.request(http.MethodPost, "/v1/account/support/ticket-support-rules/reply", map[string]any{"message": "x"}, supportSession(ownerToken), supportWebMutation())
	if response.Code != http.StatusBadRequest || errorCode(t, response) != "invalid_support_reply" {
		t.Fatalf("short reply = %d %q", response.Code, errorCode(t, response))
	}
	response = env.request(http.MethodPost, "/v1/account/support/ticket-support-rules/reply", map[string]any{"message": "My recovery kit is ABCDE-FGHJK-MNPQR-STUVW-XYZ23 and it will not work."}, supportSession(ownerToken), supportWebMutation())
	if response.Code != http.StatusBadRequest || errorCode(t, response) != "secret_shaped_content" {
		t.Fatalf("secret reply = %d %q", response.Code, errorCode(t, response))
	}

	response = env.request(http.MethodPost, "/v1/account/support/ticket-support-rules/reply", map[string]any{"message": "Any update on this?"}, supportSession(ownerToken), supportWebMutation())
	if response.Code != http.StatusCreated || ticketStatus(t, response) != "open" {
		t.Fatalf("owner reply = %d %q: %s", response.Code, ticketStatus(t, response), response.Body.String())
	}
	var messageCount int
	if err := env.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM sesame_support_messages WHERE ticket_id = 'ticket-support-rules'`).Scan(&messageCount); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if messageCount != 2 {
		t.Fatalf("messages after reply = %d, want the intake message and the reply", messageCount)
	}

	response = env.request(http.MethodPost, "/v1/account/support/ticket-support-rules/close", nil, supportSession(otherToken), supportWebMutation())
	if response.Code != http.StatusNotFound || errorCode(t, response) != "support_request_not_found" {
		t.Fatalf("foreign close = %d %q", response.Code, errorCode(t, response))
	}
	response = env.request(http.MethodPost, "/v1/account/support/ticket-support-rules/close", nil, supportSession(ownerToken), supportWebMutation())
	if response.Code != http.StatusOK || ticketStatus(t, response) != "closed" {
		t.Fatalf("owner close = %d %q: %s", response.Code, ticketStatus(t, response), response.Body.String())
	}
	response = env.request(http.MethodPost, "/v1/account/support/ticket-support-rules/close", nil, supportSession(ownerToken), supportWebMutation())
	if response.Code != http.StatusConflict || errorCode(t, response) != "support_request_closed" {
		t.Fatalf("second close = %d %q", response.Code, errorCode(t, response))
	}
	response = env.request(http.MethodPost, "/v1/account/support/ticket-support-rules/reply", map[string]any{"message": "One more question."}, supportSession(ownerToken), supportWebMutation())
	if response.Code != http.StatusConflict || errorCode(t, response) != "support_request_closed" {
		t.Fatalf("reply to a closed ticket = %d %q", response.Code, errorCode(t, response))
	}

	response = env.request(http.MethodPost, "/v1/account/support/ticket-support-rules/reopen", nil, supportSession(otherToken), supportWebMutation())
	if response.Code != http.StatusNotFound || errorCode(t, response) != "support_request_not_found" {
		t.Fatalf("foreign reopen = %d %q", response.Code, errorCode(t, response))
	}
	response = env.request(http.MethodPost, "/v1/account/support/ticket-support-rules/reopen", nil, supportSession(ownerToken), supportWebMutation())
	if response.Code != http.StatusOK || ticketStatus(t, response) != "open" {
		t.Fatalf("owner reopen = %d %q: %s", response.Code, ticketStatus(t, response), response.Body.String())
	}
	response = env.request(http.MethodPost, "/v1/account/support/ticket-support-rules/reopen", nil, supportSession(ownerToken), supportWebMutation())
	if response.Code != http.StatusConflict || errorCode(t, response) != "support_request_reopen_expired" {
		t.Fatalf("reopen of an open ticket = %d %q", response.Code, errorCode(t, response))
	}
}

func TestAccountSupportReopenWindowExpires(t *testing.T) {
	env := newSupportTestEnv(t)
	ownerToken := env.seedAccount(t, "acct-support-owner", supportTestOwnerEmail)
	env.seedTicket(t, "ticket-support-expired", "acct-support-owner", supportTestOwnerEmail)
	response := env.request(http.MethodPost, "/v1/account/support/ticket-support-expired/close", nil, supportSession(ownerToken), supportWebMutation())
	if response.Code != http.StatusOK || ticketStatus(t, response) != "closed" {
		t.Fatalf("close = %d %q: %s", response.Code, ticketStatus(t, response), response.Body.String())
	}
	if _, err := env.db.ExecContext(context.Background(), `UPDATE sesame_support_requests SET account_reopen_until = now() - interval '1 second' WHERE id = 'ticket-support-expired'`); err != nil {
		t.Fatalf("expire reopen window: %v", err)
	}
	response = env.request(http.MethodPost, "/v1/account/support/ticket-support-expired/reopen", nil, supportSession(ownerToken), supportWebMutation())
	if response.Code != http.StatusConflict || errorCode(t, response) != "support_request_reopen_expired" {
		t.Fatalf("expired reopen = %d %q", response.Code, errorCode(t, response))
	}
}

func TestAccountSupportReportsAutomaticClosure(t *testing.T) {
	env := newSupportTestEnv(t)
	ownerToken := env.seedAccount(t, "acct-support-owner", supportTestOwnerEmail)
	env.seedTicket(t, "ticket-support-auto", "acct-support-owner", supportTestOwnerEmail)
	if _, err := env.db.ExecContext(context.Background(), `
		UPDATE sesame_support_requests
		SET status = 'closed', closed_at = NOW(), closed_by_system = TRUE, account_reopen_until = NOW() + INTERVAL '30 days'
		WHERE id = 'ticket-support-auto'
	`); err != nil {
		t.Fatalf("system close ticket: %v", err)
	}

	response := env.request(http.MethodGet, "/v1/account/support", nil, supportSession(ownerToken))
	if response.Code != http.StatusOK {
		t.Fatalf("owner list = %d: %s", response.Code, response.Body.String())
	}
	var list struct {
		Tickets []struct {
			ID         string `json:"id"`
			AutoClosed bool   `json:"autoClosed"`
			CanReopen  bool   `json:"canReopen"`
		} `json:"tickets"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode ticket list: %v", err)
	}
	if len(list.Tickets) != 1 || !list.Tickets[0].AutoClosed || !list.Tickets[0].CanReopen {
		t.Fatalf("account ticket list = %+v, want the automatically closed reopenable ticket", list.Tickets)
	}

	response = env.request(http.MethodGet, "/v1/account/support/ticket-support-auto", nil, supportSession(ownerToken))
	if response.Code != http.StatusOK {
		t.Fatalf("owner detail = %d: %s", response.Code, response.Body.String())
	}
	var detail struct {
		Ticket struct {
			Status     string `json:"status"`
			AutoClosed bool   `json:"autoClosed"`
			CanReopen  bool   `json:"canReopen"`
		} `json:"ticket"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode ticket detail: %v", err)
	}
	if detail.Ticket.Status != "closed" || !detail.Ticket.AutoClosed || !detail.Ticket.CanReopen {
		t.Fatalf("account ticket detail = %+v, want closed automatically with the reopen path", detail.Ticket)
	}
}
