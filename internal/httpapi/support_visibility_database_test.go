package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	adminstore "usesesame.app/backend/internal/admin"
)

type transactionalRecordingSender struct {
	recordingEmailSender
}

func (s *transactionalRecordingSender) SendAccountEmailTx(ctx context.Context, tx *sql.Tx, message AccountEmail) error {
	s.sent = append(s.sent, message)
	_, err := tx.ExecContext(ctx, `
		INSERT INTO sesame_email_outbox (kind, to_email, action_url, expires_at, subject, body, support_message_id, status, next_attempt_at)
		VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, ''), 'pending', now())
	`, message.Kind, message.To, message.ActionURL, message.ExpiresAt.UTC(), message.Subject, message.Body, message.SupportMessageID)
	return err
}

func TestAdminSupportTicketMailState(t *testing.T) {
	for _, tc := range []struct {
		name         string
		sender       EmailSender
		staffEmail   string
		wantDelivery bool
		wantStaff    bool
	}{
		{name: "mail not configured"},
		{
			name: "mail and staff notification configured", sender: &recordingEmailSender{},
			staffEmail: "staff@example.invalid", wantDelivery: true, wantStaff: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newSupportTestEnv(t, func(config *Config) {
				config.EmailSender = tc.sender
				config.SupportNotifyEmail = tc.staffEmail
			})
			readonly := env.seedAdmin(t, "admin-readonly", "readonly@example.invalid", adminstore.RoleReadonly)
			env.seedTicket(t, "ticket-mail-state", "", "guest@example.invalid")

			response := env.adminRequest(t, readonly, http.MethodGet, "/v1/admin/support/ticket-mail-state", nil)
			if response.Code != http.StatusOK {
				t.Fatalf("ticket detail = %d: %s", response.Code, response.Body.String())
			}
			var payload struct {
				Mail supportMailState `json:"mail"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode ticket detail: %v", err)
			}
			if payload.Mail.DeliveryConfigured != tc.wantDelivery || payload.Mail.StaffNotifyConfigured != tc.wantStaff {
				t.Fatalf("mail state = %+v, want delivery %v staff %v", payload.Mail, tc.wantDelivery, tc.wantStaff)
			}
		})
	}
}

func TestAdminSupportTicketDeliveryReasons(t *testing.T) {
	env := newSupportTestEnv(t, func(config *Config) { config.EmailSender = &recordingEmailSender{} })
	support := env.seedAdmin(t, "admin-support", "support@example.invalid", adminstore.RoleSupport)
	env.seedAccount(t, "account-reasons", "owner@example.invalid")
	env.seedTicket(t, "ticket-reasons", "account-reasons", "owner@example.invalid")
	env.seedTicket(t, "ticket-guest-reasons", "", "guest@example.invalid")
	ctx := context.Background()

	if _, err := env.db.ExecContext(ctx, `
		INSERT INTO sesame_account_notification_preferences (account_id, support_replies)
		VALUES ('account-reasons', FALSE)
	`); err != nil {
		t.Fatalf("set support reply preference: %v", err)
	}
	seedStaffMessage := func(id, ticketID, outboxStatus string) {
		t.Helper()
		if _, err := env.db.ExecContext(ctx, `
			INSERT INTO sesame_support_messages (id, ticket_id, author_role, admin_email, body, sent_via_email)
			VALUES ($1, $2, 'staff', 'support@example.invalid', 'Fictional staff reply', $3)
		`, id, ticketID, outboxStatus != ""); err != nil {
			t.Fatalf("seed staff message %s: %v", id, err)
		}
		if outboxStatus == "" {
			return
		}
		if _, err := env.db.ExecContext(ctx, `
			INSERT INTO sesame_email_outbox (kind, to_email, action_url, expires_at, subject, body, support_message_id, status)
			VALUES ('support-reply', 'owner@example.invalid', 'https://account.example.invalid/support', NOW() + INTERVAL '7 days', 'Subject', 'Body', $1, $2)
		`, id, outboxStatus); err != nil {
			t.Fatalf("seed outbox row for %s: %v", id, err)
		}
	}
	seedStaffMessage("message-delivered", "ticket-reasons", "delivered")
	seedStaffMessage("message-pending", "ticket-reasons", "pending")
	seedStaffMessage("message-processing", "ticket-reasons", "processing")
	seedStaffMessage("message-failed", "ticket-reasons", "failed")
	seedStaffMessage("message-none", "ticket-reasons", "")
	seedStaffMessage("message-guest", "ticket-guest-reasons", "")

	response := env.adminRequest(t, support, http.MethodGet, "/v1/admin/support/ticket-reasons", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("ticket detail = %d: %s", response.Code, response.Body.String())
	}
	reasons := ticketMessageReasons(t, response)
	want := map[string]string{
		"ticket-reasons-message": "",
		"message-delivered":      "delivered",
		"message-pending":        "pending",
		"message-processing":     "pending",
		"message-failed":         "failed",
		"message-none":           "opted-out",
	}
	for id, expected := range want {
		if reasons[id] != expected {
			t.Fatalf("message %s reason = %q, want %q", id, reasons[id], expected)
		}
	}

	response = env.adminRequest(t, support, http.MethodGet, "/v1/admin/support/ticket-guest-reasons", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("guest ticket detail = %d: %s", response.Code, response.Body.String())
	}
	if reason := ticketMessageReasons(t, response)["message-guest"]; reason != "guest" {
		t.Fatalf("guest staff message reason = %q, want guest", reason)
	}
}

func TestAdminSupportReplyReportsPendingDelivery(t *testing.T) {
	env := newSupportTestEnv(t, func(config *Config) { config.EmailSender = &transactionalRecordingSender{} })
	support := env.seedAdmin(t, "admin-support", "support@example.invalid", adminstore.RoleSupport)
	env.seedAccount(t, "account-reply", "owner@example.invalid")
	env.seedTicket(t, "ticket-reply", "account-reply", "owner@example.invalid")

	response := env.adminRequest(t, support, http.MethodPost, "/v1/admin/support/ticket-reply/reply", map[string]any{"body": "Fictional staff reply"})
	if response.Code != http.StatusOK {
		t.Fatalf("staff reply = %d: %s", response.Code, response.Body.String())
	}
	var payload struct {
		Ticket struct {
			Messages []struct {
				AuthorRole          string `json:"authorRole"`
				EmailDeliveryReason string `json:"emailDeliveryReason"`
			} `json:"messages"`
		} `json:"ticket"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode reply: %v", err)
	}
	last := payload.Ticket.Messages[len(payload.Ticket.Messages)-1]
	if last.AuthorRole != "staff" || last.EmailDeliveryReason != "pending" {
		t.Fatalf("reply message = %+v, want a staff message waiting to send", last)
	}

	var delivered int
	if err := env.db.QueryRowContext(context.Background(), `
		SELECT COUNT(*) FROM sesame_email_outbox
		WHERE support_message_id = (SELECT id FROM sesame_support_messages WHERE ticket_id = 'ticket-reply' AND author_role = 'staff')
	`).Scan(&delivered); err != nil {
		t.Fatalf("count reply outbox rows: %v", err)
	}
	if delivered != 1 {
		t.Fatalf("reply outbox rows = %d, want 1", delivered)
	}
}

func ticketMessageReasons(t *testing.T, response *httptest.ResponseRecorder) map[string]string {
	t.Helper()
	var payload struct {
		Ticket struct {
			Messages []struct {
				ID                  string `json:"id"`
				AuthorRole          string `json:"authorRole"`
				EmailDeliveryReason string `json:"emailDeliveryReason"`
			} `json:"messages"`
		} `json:"ticket"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode ticket detail: %v", err)
	}
	reasons := make(map[string]string, len(payload.Ticket.Messages))
	for _, message := range payload.Ticket.Messages {
		reasons[message.ID] = message.EmailDeliveryReason
	}
	return reasons
}
