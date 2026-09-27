package httpapi

import (
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"usesesame.app/backend/internal/accounts"
)

const supportLinkInvalidMessage = "That support link is invalid or expired. Send a new request from the support form if you still need help."

type supportAccessInput struct {
	Token string `json:"token"`
}

type supportAccessReplyInput struct {
	Token   string `json:"token"`
	Message string `json:"message"`
}

func (a *api) redeemSupportAccess(response http.ResponseWriter, request *http.Request) {
	if !a.requireAccounts(response) {
		return
	}
	if !a.allowRequest(response, request, "support-access", 60, time.Minute) {
		return
	}
	store, ok := a.accountSecurity(response)
	if !ok {
		return
	}
	var input supportAccessInput
	if !decodeJSONBodyWith(response, request, &input, "support_link_invalid", supportLinkInvalidMessage) {
		return
	}
	tokenHash, ok := supportAccessTokenHash(input.Token)
	if !ok {
		slog.Warn("Sesame support link rejected", "outcome", "invalid")
		writeError(response, http.StatusBadRequest, "support_link_invalid", supportLinkInvalidMessage)
		return
	}
	ticket, err := store.SupportTicketForAccessToken(request.Context(), tokenHash)
	if errors.Is(err, accounts.ErrNotFound) {
		slog.Warn("Sesame support link rejected", "outcome", "invalid")
		writeError(response, http.StatusBadRequest, "support_link_invalid", supportLinkInvalidMessage)
		return
	}
	if err != nil {
		slog.Error("Sesame support link lookup failed", "error", err)
		writeError(response, http.StatusServiceUnavailable, "support_access_unavailable", "Support links are temporarily unavailable.")
		return
	}
	slog.Info("Sesame support link redeemed", "request", ticket.ID, "outcome", "opened")
	writeJSON(response, http.StatusOK, map[string]any{"ticket": ticket})
}

func (a *api) replyToSupportAccess(response http.ResponseWriter, request *http.Request) {
	if !a.requireAccounts(response) {
		return
	}
	if !a.allowRequest(response, request, "support-access-reply", 12, time.Hour) {
		return
	}
	store, ok := a.accountSecurity(response)
	if !ok {
		return
	}
	var input supportAccessReplyInput
	if !decodeJSONBodyWith(response, request, &input, "support_link_invalid", supportLinkInvalidMessage) {
		return
	}
	tokenHash, ok := supportAccessTokenHash(input.Token)
	if !ok {
		slog.Warn("Sesame support link rejected", "outcome", "invalid")
		writeError(response, http.StatusBadRequest, "support_link_invalid", supportLinkInvalidMessage)
		return
	}
	if !a.allowKeyed(response, request, "support-access-link:"+hex.EncodeToString(tokenHash), 20, time.Hour) {
		return
	}
	resolved, err := store.SupportTicketForAccessToken(request.Context(), tokenHash)
	if errors.Is(err, accounts.ErrNotFound) {
		slog.Warn("Sesame support link rejected", "outcome", "invalid")
		writeError(response, http.StatusBadRequest, "support_link_invalid", supportLinkInvalidMessage)
		return
	}
	if err != nil {
		slog.Error("Sesame support link lookup failed", "error", err)
		writeError(response, http.StatusServiceUnavailable, "support_access_unavailable", "Support links are temporarily unavailable.")
		return
	}
	message := strings.TrimSpace(input.Message)
	if len(message) < 2 || len(message) > 4000 {
		slog.Info("Sesame support link reply rejected", "request", resolved.ID, "outcome", "invalid_reply")
		writeError(response, http.StatusBadRequest, "invalid_support_reply", "Write a reply between 2 and 4,000 characters.")
		return
	}
	if containsSecretShapedText(message) {
		slog.Info("Sesame support link reply rejected", "request", resolved.ID, "outcome", "secret_shaped")
		writeError(response, http.StatusBadRequest, "secret_shaped_content", "Remove passwords, codes, keys, tokens, and vault data before sending this reply.")
		return
	}
	ticket, err := store.ReplyToSupportTicketWithAccessToken(request.Context(), tokenHash, message)
	if errors.Is(err, accounts.ErrNotFound) {
		slog.Warn("Sesame support link rejected", "outcome", "invalid")
		writeError(response, http.StatusBadRequest, "support_link_invalid", supportLinkInvalidMessage)
		return
	}
	if err != nil {
		slog.Error("Sesame support link reply failed", "request", resolved.ID, "error", err)
		writeError(response, http.StatusServiceUnavailable, "support_access_unavailable", "Support links are temporarily unavailable.")
		return
	}
	slog.Info("Sesame support link replied", "request", ticket.ID, "outcome", "replied")
	if a.config.EmailSender != nil {
		if notice, ok := a.supportStaffFollowUpNotice(ticket.ID, ticket.Category); ok {
			if err := a.config.EmailSender.SendAccountEmail(request.Context(), notice); err != nil {
				slog.Error("Sesame support follow-up notice could not be queued", "error", err)
			}
		}
	}
	writeJSON(response, http.StatusCreated, map[string]any{"ticket": ticket})
}

func supportAccessTokenHash(value string) ([]byte, bool) {
	if !validActionToken(value) {
		return nil, false
	}
	return accounts.HashSessionToken(value), true
}
