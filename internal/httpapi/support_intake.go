package httpapi

import (
	"net/http"
	"regexp"
	"strings"
	"time"

	"usesesame.app/backend/internal/accounts"
	adminstore "usesesame.app/backend/internal/admin"
)

type supportRequestInput struct {
	Email              string `json:"email"`
	Subject            string `json:"subject"`
	Message            string `json:"message"`
	Category           string `json:"category,omitempty"`
	AppVersion         string `json:"appVersion,omitempty"`
	DiagnosticCode     string `json:"diagnosticCode,omitempty"`
	BrowserIntegration string `json:"browserIntegration,omitempty"`
	RequestID          string `json:"requestId,omitempty"`
}

var (
	secretAssignmentPattern = regexp.MustCompile(`(?i)(password|passphrase|totp|otp|seed|secret|token|api[ _-]?key|backup[ _-]?code|recovery[ _-]?code|private[ _-]?key)\s*[:=]`)
	longTokenPattern        = regexp.MustCompile(`(?:^|[^[:alnum:]_-])(?:[A-Fa-f0-9]{40,}|[A-Za-z0-9_-]{48,})(?:$|[^[:alnum:]_-])`)
	diagnosticCodePattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	recoveryKitPattern      = regexp.MustCompile(`(?i)(?:^|[^[:alnum:]-])[ABCDEFGHJKLMNPQRSTUVWXYZ23456789]{5}(?:-[ABCDEFGHJKLMNPQRSTUVWXYZ23456789]{5}){4}(?:$|[^[:alnum:]-])`)
	base32SecretPattern     = regexp.MustCompile(`(?:^|[^[:alnum:]])[A-Z2-7]{16,}(?:$|[^[:alnum:]])`)
	base32DigitPattern      = regexp.MustCompile(`[2-7]`)
	secretProsePattern      = regexp.MustCompile(`(?i)\b(?:master\s+)?(?:password|passphrase|recovery\s+kit|recovery\s+code|backup\s+code|totp|otp|2fa\s+code|seed|api\s+key|secret\s+key|private\s+key|access\s+token|session\s+token)s?\s+(?:is|was|are|were)\s+["']?([^\s"']{8,})`)
)

func (a *api) createSupportRequest(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodPost) || !a.requireAccounts(response) || !a.allowRequest(response, request, "support", 5, time.Hour) {
		return
	}
	if strings.HasPrefix(strings.ToLower(request.Header.Get("Content-Type")), "multipart/") {
		writeError(response, http.StatusUnsupportedMediaType, "attachments_not_accepted", "Sesame support does not accept attachments.")
		return
	}
	store, ok := a.accountSecurity(response)
	if !ok {
		return
	}
	var input supportRequestInput
	if !decodeJSONBodyWith(response, request, &input, "invalid_support_request", "Support details could not be read.") {
		return
	}
	email, valid := normalizedEmail(input.Email)
	input.Subject = strings.TrimSpace(input.Subject)
	input.Message = strings.TrimSpace(input.Message)
	input.Category = strings.TrimSpace(input.Category)
	if input.Category == "" {
		input.Category = string(adminstore.CategoryGeneral)
	}
	input.AppVersion = strings.TrimSpace(input.AppVersion)
	input.DiagnosticCode = strings.TrimSpace(input.DiagnosticCode)
	input.BrowserIntegration = strings.TrimSpace(input.BrowserIntegration)
	input.RequestID = strings.TrimSpace(input.RequestID)
	if !valid || len(input.Subject) < 3 || len(input.Subject) > 120 || len(input.Message) < 10 || len(input.Message) > 4000 || len(input.AppVersion) > 40 {
		writeError(response, http.StatusBadRequest, "invalid_support_request", "Add a valid email, short subject, and a message under 4,000 characters.")
		return
	}
	if !adminstore.ValidTicketCategory(input.Category) {
		writeError(response, http.StatusBadRequest, "invalid_ticket_category", "That category is not recognized.")
		return
	}
	if input.DiagnosticCode != "" && !diagnosticCodePattern.MatchString(input.DiagnosticCode) {
		writeError(response, http.StatusBadRequest, "invalid_diagnostic_code", "The diagnostic code format is invalid.")
		return
	}
	if input.BrowserIntegration != "" && !diagnosticCodePattern.MatchString(input.BrowserIntegration) {
		writeError(response, http.StatusBadRequest, "invalid_browser_integration", "The browser integration state format is invalid.")
		return
	}
	if input.RequestID != "" && !diagnosticCodePattern.MatchString(input.RequestID) {
		writeError(response, http.StatusBadRequest, "invalid_request_id", "The request ID format is invalid.")
		return
	}
	if containsSecretShapedText(input.Subject + "\n" + input.Message) {
		writeError(response, http.StatusBadRequest, "secret_shaped_content", "Remove passwords, codes, keys, tokens, and vault data before sending this request.")
		return
	}
	accountID := ""
	if cookie, err := request.Cookie(a.sessionCookieName()); err == nil && cookie.Value != "" {
		if user, err := a.config.Accounts.UserBySession(request.Context(), accounts.HashSessionToken(cookie.Value)); err == nil {
			accountID = user.ID
		}
	}
	id, err := store.CreateSupportRequest(request.Context(), accounts.SupportRequest{
		AccountID: accountID, Email: email, Subject: input.Subject, Message: input.Message, Category: input.Category,
		AppVersion: input.AppVersion, DiagnosticCode: input.DiagnosticCode, BrowserIntegration: input.BrowserIntegration, RequestID: input.RequestID,
	})
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "support_unavailable", "Support intake is temporarily unavailable.")
		return
	}
	writeJSON(response, http.StatusAccepted, map[string]any{"requestId": id, "status": "open"})
}

// A guard rail, not a guarantee: no filter can recognise every secret.
func containsSecretShapedText(value string) bool {
	lower := strings.ToLower(value)
	markers := []string{
		"otpauth://", "-----begin private key-----", "-----begin pgp private key block-----",
		"-----begin rsa private key-----", "-----begin openssh private key-----",
		"-----begin ec private key-----", "-----begin encrypted private key-----",
	}
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	if secretAssignmentPattern.MatchString(value) || longTokenPattern.MatchString(value) {
		return true
	}
	if recoveryKitPattern.MatchString(value) {
		return true
	}
	for _, candidate := range base32SecretPattern.FindAllString(value, -1) {
		if base32DigitPattern.MatchString(candidate) {
			return true
		}
	}
	for _, match := range secretProsePattern.FindAllStringSubmatch(value, -1) {
		if secretShapedValue(match[1]) {
			return true
		}
	}
	return false
}

func secretShapedValue(value string) bool {
	value = strings.Trim(value, ".,;:!?)]}\"'")
	if len(value) < 8 {
		return false
	}
	classes := 0
	for _, contains := range []func(rune) bool{
		func(r rune) bool { return r >= 'a' && r <= 'z' },
		func(r rune) bool { return r >= 'A' && r <= 'Z' },
		func(r rune) bool { return r >= '0' && r <= '9' },
		func(r rune) bool { return strings.ContainsRune("!@#$%^&*()-_=+[]{}|\\/<>?~`", r) },
	} {
		if strings.ContainsFunc(value, contains) {
			classes++
		}
	}
	return classes >= 2
}
