package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	adminstore "usesesame.app/backend/internal/admin"
)

type savedReplyView struct {
	ID               string `json:"id"`
	Title            string `json:"title"`
	Body             string `json:"body"`
	CreatedByAdminID string `json:"createdByAdminId"`
}

func decodeSavedReply(t *testing.T, response *httptest.ResponseRecorder) savedReplyView {
	t.Helper()
	var payload struct {
		SavedReply savedReplyView `json:"savedReply"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode saved reply %q: %v", response.Body.String(), err)
	}
	return payload.SavedReply
}

func listSavedReplies(t *testing.T, env *supportTestEnv, actor adminstore.Account) []savedReplyView {
	t.Helper()
	response := env.adminRequest(t, actor, http.MethodGet, "/v1/admin/saved-replies", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("list saved replies = %d: %s", response.Code, response.Body.String())
	}
	var payload struct {
		SavedReplies []savedReplyView `json:"savedReplies"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode saved replies: %v", err)
	}
	return payload.SavedReplies
}

func TestAdminSavedRepliesGuardCRUDAndReplyPath(t *testing.T) {
	env := newSupportTestEnv(t)
	support := env.seedAdmin(t, "admin-support", "support@example.invalid", adminstore.RoleSupport)
	readonly := env.seedAdmin(t, "admin-readonly", "readonly@example.invalid", adminstore.RoleReadonly)
	ops := env.seedAdmin(t, "admin-ops", "ops@example.invalid", adminstore.RoleOps)
	env.seedTicket(t, "ticket-saved-reply", "", "guest@example.invalid")

	if replies := listSavedReplies(t, env, readonly); len(replies) != 0 {
		t.Fatalf("saved replies = %+v, want none", replies)
	}
	response := env.adminRequest(t, readonly, http.MethodPost, "/v1/admin/saved-replies", map[string]any{"title": "Password reset", "body": "Use the recovery kit."})
	if response.Code != http.StatusForbidden || errorCode(t, response) != "admin_forbidden" {
		t.Fatalf("readonly create = %d %q", response.Code, errorCode(t, response))
	}
	response = env.adminRequest(t, ops, http.MethodGet, "/v1/admin/saved-replies", nil)
	if response.Code != http.StatusForbidden || errorCode(t, response) != "admin_forbidden" {
		t.Fatalf("ops list = %d %q", response.Code, errorCode(t, response))
	}

	cleanBody := "Open Security settings and use your recovery kit. Support cannot reset a vault."
	for name, payload := range map[string]map[string]any{
		"empty title": {"title": "  ", "body": cleanBody},
		"long title":  {"title": strings.Repeat("t", 121), "body": cleanBody},
		"long body":   {"title": "Password reset", "body": strings.Repeat("b", 8001)},
		"secret body": {"title": "Password reset", "body": "The password: correct-horse-battery should be removed from the log."},
	} {
		wantCode := "invalid_saved_reply"
		if name == "secret body" {
			wantCode = "secret_shaped_content"
		}
		response = env.adminRequest(t, support, http.MethodPost, "/v1/admin/saved-replies", payload)
		if response.Code != http.StatusBadRequest || errorCode(t, response) != wantCode {
			t.Fatalf("%s = %d %q, want 400 %s", name, response.Code, errorCode(t, response), wantCode)
		}
	}

	response = env.adminRequest(t, support, http.MethodPost, "/v1/admin/saved-replies", map[string]any{"title": "Password reset guidance", "body": cleanBody})
	if response.Code != http.StatusCreated {
		t.Fatalf("create saved reply = %d: %s", response.Code, response.Body.String())
	}
	created := decodeSavedReply(t, response)
	if created.ID == "" || created.Title != "Password reset guidance" || created.Body != cleanBody || created.CreatedByAdminID != "admin-support" {
		t.Fatalf("created saved reply = %+v", created)
	}
	replies := listSavedReplies(t, env, support)
	if len(replies) != 1 || replies[0].ID != created.ID {
		t.Fatalf("saved replies after create = %+v", replies)
	}

	response = env.adminRequest(t, support, http.MethodPatch, "/v1/admin/saved-replies/saved-reply-missing", map[string]any{"title": "Password reset", "body": cleanBody})
	if response.Code != http.StatusNotFound || errorCode(t, response) != "admin_record_not_found" {
		t.Fatalf("missing update = %d %q", response.Code, errorCode(t, response))
	}
	response = env.adminRequest(t, support, http.MethodPatch, "/v1/admin/saved-replies/"+created.ID, map[string]any{"title": "Password reset", "body": "The password: correct-horse-battery should be removed from the log."})
	if response.Code != http.StatusBadRequest || errorCode(t, response) != "secret_shaped_content" {
		t.Fatalf("secret update = %d %q", response.Code, errorCode(t, response))
	}
	response = env.adminRequest(t, support, http.MethodPatch, "/v1/admin/saved-replies/"+created.ID, map[string]any{"title": "Vault reset guidance", "body": cleanBody})
	if response.Code != http.StatusOK {
		t.Fatalf("update saved reply = %d: %s", response.Code, response.Body.String())
	}
	if updated := decodeSavedReply(t, response); updated.Title != "Vault reset guidance" || updated.Body != cleanBody {
		t.Fatalf("updated saved reply = %+v", updated)
	}

	if _, err := env.db.ExecContext(context.Background(), `UPDATE sesame_support_saved_replies SET body = $2 WHERE id = $1`, created.ID, "The password: correct-horse-battery should be removed from the log."); err != nil {
		t.Fatalf("store a bypassed secret body: %v", err)
	}
	response = env.adminRequest(t, support, http.MethodPost, "/v1/admin/support/ticket-saved-reply/reply", map[string]any{"body": "The password: correct-horse-battery should be removed from the log."})
	if response.Code != http.StatusBadRequest || errorCode(t, response) != "secret_shaped_content" {
		t.Fatalf("secret reply from a saved reply = %d %q", response.Code, errorCode(t, response))
	}
	response = env.adminRequest(t, support, http.MethodPost, "/v1/admin/support/ticket-saved-reply/reply", map[string]any{"body": cleanBody})
	if response.Code != http.StatusOK {
		t.Fatalf("reply with the saved reply body = %d: %s", response.Code, response.Body.String())
	}
	ticket := decodeTicket(t, response)
	if len(ticket.Messages) != 2 || ticket.Messages[1].Body != cleanBody {
		t.Fatalf("reply messages = %+v, want the saved reply body", ticket.Messages)
	}

	response = env.adminRequest(t, support, http.MethodDelete, "/v1/admin/saved-replies/saved-reply-missing", nil)
	if response.Code != http.StatusNotFound || errorCode(t, response) != "admin_record_not_found" {
		t.Fatalf("missing delete = %d %q", response.Code, errorCode(t, response))
	}
	response = env.adminRequest(t, support, http.MethodDelete, "/v1/admin/saved-replies/"+created.ID, nil)
	if response.Code != http.StatusNoContent {
		t.Fatalf("delete saved reply = %d: %s", response.Code, response.Body.String())
	}
	if replies := listSavedReplies(t, env, support); len(replies) != 0 {
		t.Fatalf("saved replies after delete = %+v", replies)
	}
}

func TestAdminSavedRepliesRejectNewSecretShapes(t *testing.T) {
	env := newSupportTestEnv(t)
	support := env.seedAdmin(t, "admin-saved-reply-shapes", "support@example.invalid", adminstore.RoleSupport)
	env.seedTicket(t, "ticket-saved-reply-shapes", "", "guest@example.invalid")
	shapes := map[string]string{
		"pwd assignment":  "The pwd: correct-horse-battery should be removed from the log.",
		"pin number":      "The lock screen shows PIN 482913 after the last update.",
		"seed phrase":     "The seed phrase is apple banana cherry dog eagle fence grape house igloo jacket kite lemon.",
		"base64 key":      "The exported key ejgFI6bjzttikWD1325ZdjhohM88ycZWJS84RCntRdU= will not import.",
		"fullwidth colon": "The password：correct-horse-battery is in the log.",
	}
	for name, body := range shapes {
		t.Run(name, func(t *testing.T) {
			response := env.adminRequest(t, support, http.MethodPost, "/v1/admin/saved-replies", map[string]any{"title": "Fictional guidance", "body": body})
			if response.Code != http.StatusBadRequest || errorCode(t, response) != "secret_shaped_content" {
				t.Fatalf("saved reply = %d %q, want 400 secret_shaped_content", response.Code, errorCode(t, response))
			}
			response = env.adminRequest(t, support, http.MethodPost, "/v1/admin/support/ticket-saved-reply-shapes/reply", map[string]any{"body": body})
			if response.Code != http.StatusBadRequest || errorCode(t, response) != "secret_shaped_content" {
				t.Fatalf("reply = %d %q, want 400 secret_shaped_content", response.Code, errorCode(t, response))
			}
		})
	}
}
