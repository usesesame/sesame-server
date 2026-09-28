package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	adminstore "usesesame.app/backend/internal/admin"
)

func TestAdminSupportReadAuthorization(t *testing.T) {
	env := newSupportTestEnv(t)
	env.seedAdmin(t, "admin-support", "support@example.invalid", adminstore.RoleSupport)
	readonly := env.seedAdmin(t, "admin-readonly", "readonly@example.invalid", adminstore.RoleReadonly)
	ops := env.seedAdmin(t, "admin-ops", "ops@example.invalid", adminstore.RoleOps)
	env.seedAdmin(t, "admin-super", "super@example.invalid", adminstore.RoleSuper)
	env.seedTicket(t, "ticket-admin-auth", "", "guest@example.invalid")

	response := env.request(http.MethodGet, "/v1/admin/support", nil, func(request *http.Request) {
		request.Header.Set("Origin", supportTestAdminOrigin)
	})
	if response.Code != http.StatusUnauthorized || errorCode(t, response) != "admin_not_authenticated" {
		t.Fatalf("anonymous admin list = %d %q", response.Code, errorCode(t, response))
	}
	for _, path := range []string{"/v1/admin/support", "/v1/admin/support/ticket-admin-auth", "/v1/admin/support/assignees"} {
		response = env.adminRequest(t, ops, http.MethodGet, path, nil)
		if response.Code != http.StatusForbidden || errorCode(t, response) != "admin_forbidden" {
			t.Fatalf("ops %s = %d %q", path, response.Code, errorCode(t, response))
		}
	}
	response = env.adminRequest(t, readonly, http.MethodGet, "/v1/admin/support", nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "ticket-admin-auth") {
		t.Fatalf("readonly list = %d: %s", response.Code, response.Body.String())
	}
	response = env.adminRequest(t, readonly, http.MethodGet, "/v1/admin/support/ticket-admin-auth", nil)
	if response.Code != http.StatusOK || ticketStatus(t, response) != "open" {
		t.Fatalf("readonly detail = %d: %s", response.Code, response.Body.String())
	}
	response = env.adminRequest(t, readonly, http.MethodGet, "/v1/admin/support/assignees", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("readonly assignees = %d: %s", response.Code, response.Body.String())
	}
	var assignees struct {
		Assignees []struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		} `json:"assignees"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &assignees); err != nil {
		t.Fatalf("decode assignees: %v", err)
	}
	assigneeIDs := map[string]bool{}
	for _, assignee := range assignees.Assignees {
		assigneeIDs[assignee.ID] = true
	}
	if !assigneeIDs["admin-support"] || !assigneeIDs["admin-super"] {
		t.Fatalf("assignees = %v, want the support and super admins", assigneeIDs)
	}
	if assigneeIDs["admin-ops"] || assigneeIDs["admin-readonly"] {
		t.Fatalf("assignees expose non-support roles: %v", assigneeIDs)
	}

	for _, action := range []string{"reply", "notes", "status", "priority", "assign"} {
		response = env.adminRequest(t, readonly, http.MethodPost, "/v1/admin/support/ticket-admin-auth/"+action, map[string]any{"body": "Please help.", "status": "waiting", "priority": "low", "adminId": ""})
		if response.Code != http.StatusForbidden || errorCode(t, response) != "admin_forbidden" {
			t.Fatalf("readonly %s = %d %q", action, response.Code, errorCode(t, response))
		}
	}
}

func TestAdminSupportListValidation(t *testing.T) {
	env := newSupportTestEnv(t)
	readonly := env.seedAdmin(t, "admin-readonly", "readonly@example.invalid", adminstore.RoleReadonly)
	env.seedTicket(t, "ticket-admin-general", "", "guest@example.invalid")
	env.seedTicket(t, "ticket-admin-bug", "", "bug@example.invalid")
	if _, err := env.db.ExecContext(context.Background(), `UPDATE sesame_support_requests SET category = 'bug' WHERE id = 'ticket-admin-bug'`); err != nil {
		t.Fatalf("categorize bug ticket: %v", err)
	}

	for _, query := range []string{
		"status=bogus",
		"priority=bogus",
		"category=bogus",
		"query=" + strings.Repeat("q", 255),
		"assigned=" + strings.Repeat("a", 129),
	} {
		response := env.adminRequest(t, readonly, http.MethodGet, "/v1/admin/support?"+query, nil)
		wantCode := "invalid_ticket_filter"
		if strings.HasPrefix(query, "query=") {
			wantCode = "invalid_ticket_search"
		}
		if response.Code != http.StatusBadRequest || errorCode(t, response) != wantCode {
			t.Fatalf("%s = %d %q, want 400 %s", query, response.Code, errorCode(t, response), wantCode)
		}
	}
	response := env.adminRequest(t, readonly, http.MethodGet, "/v1/admin/support?category=bug", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("category filter = %d: %s", response.Code, response.Body.String())
	}
	var payload struct {
		Tickets []struct {
			ID string `json:"id"`
		} `json:"tickets"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode filtered list: %v", err)
	}
	if payload.Total != 1 || len(payload.Tickets) != 1 || payload.Tickets[0].ID != "ticket-admin-bug" {
		t.Fatalf("category filter = %+v, want only the bug ticket", payload)
	}
}

func TestAdminSupportReplyValidation(t *testing.T) {
	env := newSupportTestEnv(t)
	support := env.seedAdmin(t, "admin-support", "support@example.invalid", adminstore.RoleSupport)
	env.seedTicket(t, "ticket-admin-reply", "", "guest@example.invalid")

	response := env.adminRequest(t, support, http.MethodPost, "/v1/admin/support/ticket-admin-reply/reply", map[string]any{"body": ""})
	if response.Code != http.StatusBadRequest || errorCode(t, response) != "invalid_reply" {
		t.Fatalf("empty reply = %d %q", response.Code, errorCode(t, response))
	}
	response = env.adminRequest(t, support, http.MethodPost, "/v1/admin/support/ticket-admin-reply/reply", map[string]any{"body": strings.Repeat("r", 8001)})
	if response.Code != http.StatusBadRequest || errorCode(t, response) != "invalid_reply" {
		t.Fatalf("long reply = %d %q", response.Code, errorCode(t, response))
	}
	response = env.adminRequest(t, support, http.MethodPost, "/v1/admin/support/ticket-admin-reply/reply", map[string]any{"body": "The password: correct-horse-battery should be removed from the log."})
	if response.Code != http.StatusBadRequest || errorCode(t, response) != "secret_shaped_content" {
		t.Fatalf("secret reply = %d %q", response.Code, errorCode(t, response))
	}
	response = env.adminRequest(t, support, http.MethodPost, "/v1/admin/support/ticket-admin-missing/reply", map[string]any{"body": "We are looking into this."})
	if response.Code != http.StatusNotFound || errorCode(t, response) != "admin_record_not_found" {
		t.Fatalf("missing ticket reply = %d %q", response.Code, errorCode(t, response))
	}

	response = env.adminRequest(t, support, http.MethodPost, "/v1/admin/support/ticket-admin-reply/reply", map[string]any{"body": "We are looking into this."})
	if response.Code != http.StatusOK {
		t.Fatalf("valid reply = %d: %s", response.Code, response.Body.String())
	}
	ticket := decodeTicket(t, response)
	if ticket.Status != "waiting" {
		t.Fatalf("ticket status after reply = %q, want waiting", ticket.Status)
	}
	if len(ticket.Messages) != 2 {
		t.Fatalf("ticket messages = %d, want the intake message and the reply", len(ticket.Messages))
	}
	last := ticket.Messages[len(ticket.Messages)-1]
	if last.AuthorRole != "staff" || last.Body != "We are looking into this." {
		t.Fatalf("staff reply message = %+v", last)
	}
}

func TestAdminSupportStatusClosesAndReopens(t *testing.T) {
	env := newSupportTestEnv(t)
	support := env.seedAdmin(t, "admin-support", "support@example.invalid", adminstore.RoleSupport)
	env.seedTicket(t, "ticket-admin-status", "", "guest@example.invalid")

	response := env.adminRequest(t, support, http.MethodPost, "/v1/admin/support/ticket-admin-status/status", map[string]any{"status": "deleted"})
	if response.Code != http.StatusBadRequest || errorCode(t, response) != "invalid_status" {
		t.Fatalf("invalid status = %d %q", response.Code, errorCode(t, response))
	}
	response = env.adminRequest(t, support, http.MethodPost, "/v1/admin/support/ticket-admin-missing/status", map[string]any{"status": "waiting"})
	if response.Code != http.StatusNotFound || errorCode(t, response) != "admin_record_not_found" {
		t.Fatalf("missing ticket status = %d %q", response.Code, errorCode(t, response))
	}

	response = env.adminRequest(t, support, http.MethodPost, "/v1/admin/support/ticket-admin-status/status", map[string]any{"status": "waiting"})
	if response.Code != http.StatusNoContent {
		t.Fatalf("waiting status = %d: %s", response.Code, response.Body.String())
	}
	if row := readAdminTicketStatus(t, env, "ticket-admin-status"); row != "waiting" {
		t.Fatalf("stored status = %q, want waiting", row)
	}

	response = env.adminRequest(t, support, http.MethodPost, "/v1/admin/support/ticket-admin-status/status", map[string]any{"status": "closed"})
	if response.Code != http.StatusNoContent {
		t.Fatalf("closed status = %d: %s", response.Code, response.Body.String())
	}
	response = env.adminRequest(t, support, http.MethodGet, "/v1/admin/support/ticket-admin-status", nil)
	ticket := decodeTicket(t, response)
	if ticket.Status != "closed" {
		t.Fatalf("closed ticket = %+v", ticket)
	}
	var closedBy string
	var reopenUntil *string
	if err := env.db.QueryRowContext(context.Background(), `SELECT closed_by::text, account_reopen_until::text FROM sesame_support_requests WHERE id = 'ticket-admin-status'`).Scan(&closedBy, &reopenUntil); err != nil {
		t.Fatalf("read closed ticket: %v", err)
	}
	if closedBy != "admin-support" || reopenUntil == nil {
		t.Fatalf("closed_by = %q, reopenUntil = %v", closedBy, reopenUntil)
	}

	response = env.adminRequest(t, support, http.MethodPost, "/v1/admin/support/ticket-admin-status/reply", map[string]any{"body": "One more update."})
	if response.Code < 400 {
		t.Fatalf("reply to a closed ticket = %d: %s", response.Code, response.Body.String())
	}
	var storedStaffReplies int
	if err := env.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM sesame_support_messages WHERE ticket_id = 'ticket-admin-status' AND author_role = 'staff'`).Scan(&storedStaffReplies); err != nil {
		t.Fatalf("count staff replies: %v", err)
	}
	if storedStaffReplies != 0 {
		t.Fatalf("a closed ticket stored %d staff replies", storedStaffReplies)
	}

	response = env.adminRequest(t, support, http.MethodPost, "/v1/admin/support/ticket-admin-status/status", map[string]any{"status": "open"})
	if response.Code != http.StatusNoContent {
		t.Fatalf("reopen status = %d: %s", response.Code, response.Body.String())
	}
	if row := readAdminTicketStatus(t, env, "ticket-admin-status"); row != "open" {
		t.Fatalf("stored status after admin reopen = %q, want open", row)
	}
	var clearedClosedBy *string
	var clearedReopenUntil *string
	if err := env.db.QueryRowContext(context.Background(), `SELECT closed_by::text, account_reopen_until::text FROM sesame_support_requests WHERE id = 'ticket-admin-status'`).Scan(&clearedClosedBy, &clearedReopenUntil); err != nil {
		t.Fatalf("read reopened ticket: %v", err)
	}
	if clearedClosedBy != nil || clearedReopenUntil != nil {
		t.Fatalf("reopened ticket kept close bookkeeping: %v %v", clearedClosedBy, clearedReopenUntil)
	}
}

func TestAdminSupportNotesAssignmentAndPriority(t *testing.T) {
	env := newSupportTestEnv(t)
	support := env.seedAdmin(t, "admin-support", "support@example.invalid", adminstore.RoleSupport)
	target := env.seedAdmin(t, "admin-target", "target@example.invalid", adminstore.RoleSupport)
	ops := env.seedAdmin(t, "admin-ops", "ops@example.invalid", adminstore.RoleOps)
	env.seedTicket(t, "ticket-admin-note", "", "guest@example.invalid")

	response := env.adminRequest(t, support, http.MethodPost, "/v1/admin/support/ticket-admin-note/notes", map[string]any{"body": ""})
	if response.Code != http.StatusBadRequest || errorCode(t, response) != "invalid_note" {
		t.Fatalf("empty note = %d %q", response.Code, errorCode(t, response))
	}
	response = env.adminRequest(t, support, http.MethodPost, "/v1/admin/support/ticket-admin-note/notes", map[string]any{"body": "The access token is 0e6d2f1a9b8c7d4e5f6a and it should be rotated."})
	if response.Code != http.StatusBadRequest || errorCode(t, response) != "secret_shaped_content" {
		t.Fatalf("secret note = %d %q", response.Code, errorCode(t, response))
	}
	response = env.adminRequest(t, support, http.MethodPost, "/v1/admin/support/ticket-admin-note/notes", map[string]any{"body": "The user is on the beta channel."})
	if response.Code != http.StatusCreated {
		t.Fatalf("valid note = %d: %s", response.Code, response.Body.String())
	}
	var note struct {
		Note struct {
			AdminEmail string `json:"adminEmail"`
			Body       string `json:"body"`
		} `json:"note"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &note); err != nil {
		t.Fatalf("decode note: %v", err)
	}
	if note.Note.AdminEmail != "support@example.invalid" || note.Note.Body != "The user is on the beta channel." {
		t.Fatalf("note = %+v", note.Note)
	}

	response = env.adminRequest(t, support, http.MethodPost, "/v1/admin/support/ticket-admin-note/assign", map[string]any{"adminId": strings.Repeat("a", 129)})
	if response.Code != http.StatusBadRequest || errorCode(t, response) != "invalid_assign" {
		t.Fatalf("overlong assign = %d %q", response.Code, errorCode(t, response))
	}
	response = env.adminRequest(t, support, http.MethodPost, "/v1/admin/support/ticket-admin-note/assign", map[string]any{"adminId": "admin-missing"})
	if response.Code < 400 {
		t.Fatalf("unknown assignee = %d: %s", response.Code, response.Body.String())
	}
	response = env.adminRequest(t, support, http.MethodPost, "/v1/admin/support/ticket-admin-note/assign", map[string]any{"adminId": ops.ID})
	if response.Code != http.StatusConflict || errorCode(t, response) != "admin_action_not_allowed" {
		t.Fatalf("non-support assignee = %d %q", response.Code, errorCode(t, response))
	}

	response = env.adminRequest(t, support, http.MethodPost, "/v1/admin/support/ticket-admin-note/assign", map[string]any{"adminId": target.ID})
	if response.Code != http.StatusNoContent {
		t.Fatalf("valid assignee = %d: %s", response.Code, response.Body.String())
	}
	var assignedID *string
	var status string
	if err := env.db.QueryRowContext(context.Background(), `SELECT assigned_admin_id, status FROM sesame_support_requests WHERE id = 'ticket-admin-note'`).Scan(&assignedID, &status); err != nil {
		t.Fatalf("read assignment: %v", err)
	}
	if assignedID == nil || *assignedID != target.ID || status != "in_progress" {
		t.Fatalf("assignment = %v %q, want %s in_progress", assignedID, status, target.ID)
	}

	response = env.adminRequest(t, support, http.MethodPost, "/v1/admin/support/ticket-admin-note/priority", map[string]any{"priority": "whenever"})
	if response.Code != http.StatusBadRequest || errorCode(t, response) != "invalid_priority" {
		t.Fatalf("invalid priority = %d %q", response.Code, errorCode(t, response))
	}
	response = env.adminRequest(t, support, http.MethodPost, "/v1/admin/support/ticket-admin-note/priority", map[string]any{"priority": "urgent"})
	if response.Code != http.StatusNoContent {
		t.Fatalf("valid priority = %d: %s", response.Code, response.Body.String())
	}
	var priority string
	if err := env.db.QueryRowContext(context.Background(), `SELECT priority FROM sesame_support_requests WHERE id = 'ticket-admin-note'`).Scan(&priority); err != nil {
		t.Fatalf("read priority: %v", err)
	}
	if priority != "urgent" {
		t.Fatalf("stored priority = %q, want urgent", priority)
	}
}

func readAdminTicketStatus(t *testing.T, env *supportTestEnv, ticketID string) string {
	t.Helper()
	var status string
	if err := env.db.QueryRowContext(context.Background(), `SELECT status FROM sesame_support_requests WHERE id = $1`, ticketID).Scan(&status); err != nil {
		t.Fatalf("read ticket %s status: %v", ticketID, err)
	}
	return status
}
