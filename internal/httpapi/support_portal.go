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
	if !a.requireAccounts(response) {
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
	ticketID := request.PathValue("ticketID")
	if ticketID == "" || len(ticketID) > 128 {
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
	action := request.PathValue("action")
	if action == "" {
		ticket, err := store.SupportTicketForAccount(request.Context(), user.ID, ticketID)
		if err != nil {
			accountSupportError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, map[string]any{"ticket": ticket})
		return
	}
	if action == "reply" {
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
	if action == "close" {
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
	if action == "reopen" {
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
	a.notFound(response, request)
}

func (a *api) accountSupportTicketAction(action string) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		request.SetPathValue("action", action)
		a.accountSupportTicket(response, request)
	}
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
