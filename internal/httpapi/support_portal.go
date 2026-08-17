package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"usesesame.app/backend/internal/accounts"
)

type supportReplyInput struct {
	Message string `json:"message"`
}

func (a *api) accountSupportTickets(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodGet) || !a.requireAccounts(response) {
		return
	}
	store, ok := a.accountSecurity(response)
	if !ok {
		return
	}
	user, ok := a.userForRequest(response, request)
	if !ok {
		return
	}
	tickets, err := store.SupportTicketsForAccount(request.Context(), user.ID)
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "support_unavailable", "Your support requests are temporarily unavailable.")
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"tickets": tickets})
}

func (a *api) accountSupportTicket(response http.ResponseWriter, request *http.Request) {
	if !a.requireAccounts(response) {
		return
	}
	path := strings.TrimPrefix(request.URL.Path, "/v1/account/support/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" || len(parts[0]) > 128 {
		a.notFound(response, request)
		return
	}
	store, ok := a.accountSecurity(response)
	if !ok {
		return
	}
	user, ok := a.userForRequest(response, request)
	if !ok {
		return
	}
	ticketID := parts[0]
	if len(parts) == 1 && request.Method == http.MethodGet {
		ticket, err := store.SupportTicketForAccount(request.Context(), user.ID, ticketID)
		if err != nil {
			accountSupportError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, map[string]any{"ticket": ticket})
		return
	}
	if len(parts) == 2 && parts[1] == "reply" && request.Method == http.MethodPost {
		if !a.allowRequest(response, request, "support-reply", 12, time.Hour) {
			return
		}
		var input supportReplyInput
		if !decodeJSONBodyWith(response, request, &input, "invalid_support_reply", "The support reply could not be read.") {
			return
		}
		message := strings.TrimSpace(input.Message)
		if len(message) < 2 || len(message) > 4000 {
			writeError(response, http.StatusBadRequest, "invalid_support_reply", "Write a reply between 2 and 4,000 characters.")
			return
		}
		if containsSecretShapedText(message) {
			writeError(response, http.StatusBadRequest, "secret_shaped_content", "Remove passwords, codes, keys, tokens, and vault data before sending this reply.")
			return
		}
		ticket, err := store.ReplyToSupportTicket(request.Context(), user.ID, ticketID, message)
		if err != nil {
			accountSupportError(response, err)
			return
		}
		writeJSON(response, http.StatusCreated, map[string]any{"ticket": ticket})
		return
	}
	if len(parts) == 2 && parts[1] == "close" && request.Method == http.MethodPost {
		if !a.allowRequest(response, request, "support-close", 12, time.Hour) {
			return
		}
		ticket, err := store.CloseSupportTicket(request.Context(), user.ID, ticketID, time.Now().UTC())
		if err != nil {
			accountSupportError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, map[string]any{"ticket": ticket})
		return
	}
	if len(parts) == 2 && parts[1] == "reopen" && request.Method == http.MethodPost {
		if !a.allowRequest(response, request, "support-reopen", 12, time.Hour) {
			return
		}
		ticket, err := store.ReopenSupportTicket(request.Context(), user.ID, ticketID, time.Now().UTC())
		if err != nil {
			accountSupportError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, map[string]any{"ticket": ticket})
		return
	}
	if len(parts) == 1 {
		response.Header().Set("Allow", http.MethodGet)
	} else if len(parts) == 2 && (parts[1] == "reply" || parts[1] == "close" || parts[1] == "reopen") {
		response.Header().Set("Allow", http.MethodPost)
	}
	if response.Header().Get("Allow") != "" {
		writeError(response, http.StatusMethodNotAllowed, "method_not_allowed", "That method is not available for this support request.")
		return
	}
	a.notFound(response, request)
}

func accountSupportError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, accounts.ErrNotFound):
		writeError(response, http.StatusNotFound, "support_request_not_found", "That support request was not found.")
	case errors.Is(err, accounts.ErrSupportTicketClosed):
		writeError(response, http.StatusConflict, "support_request_closed", "This request is closed. Start a new request if you still need help.")
	case errors.Is(err, accounts.ErrSupportTicketReopenExpired):
		writeError(response, http.StatusConflict, "support_request_reopen_expired", "This request can no longer be reopened. Start a new request if you still need help.")
	default:
		writeError(response, http.StatusServiceUnavailable, "support_unavailable", "Support is temporarily unavailable.")
	}
}
