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
	secretAssignmentPattern = regexp.MustCompile(`(?i)((?:^|[^a-z])pin|password|passwd|pwd|passphrase|totp|otp|seed|secret|token|api[ _-]?key|backup[ _-]?code|recovery[ _-]?code|private[ _-]?key)\s*[:=：]`)
	pinNumberPattern        = regexp.MustCompile(`(?i)\bpin\b[ \t]*(?:[:=：]|(?:is|was)[ \t]+)?[ \t]*\d{4,8}\b`)
	longTokenPattern        = regexp.MustCompile(`(?:^|[^[:alnum:]_-])(?:[A-Fa-f0-9]{40,}|[A-Za-z0-9_-]{48,})(?:$|[^[:alnum:]_-])`)
	diagnosticCodePattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	recoveryKitPattern      = regexp.MustCompile(`(?i)(?:^|[^[:alnum:]-])[ABCDEFGHJKLMNPQRSTUVWXYZ23456789]{5}(?:-[ABCDEFGHJKLMNPQRSTUVWXYZ23456789]{5}){4}(?:$|[^[:alnum:]-])`)
	base32SecretPattern     = regexp.MustCompile(`(?:^|[^[:alnum:]])[A-Z2-7]{16,}(?:$|[^[:alnum:]])`)
	base32DigitPattern      = regexp.MustCompile(`[2-7]`)
	base64SecretPattern     = regexp.MustCompile(`(?:^|[^A-Za-z0-9+/=_-])([A-Za-z0-9+/_-]{31,}={0,2})(?:$|[^A-Za-z0-9+/=_-])`)
	seedPhrasePattern       = regexp.MustCompile(`(?i:\b(?:seed|mnemonic|recovery)\s+(?:phrase|words?)\b)[\s\S]{0,24}?(?:(?:[a-z]{3,8}[ \t]+){11,}[a-z]{3,8}|(?:[A-Z]{3,8}[ \t]+){11,}[A-Z]{3,8})`)
	secretProsePattern      = regexp.MustCompile(`(?i)\b(?:master\s+)?(?:password|passphrase|recovery\s+kit|recovery\s+code|backup\s+code|totp|otp|2fa\s+code|seed|api\s+key|secret\s+key|private\s+key|access\s+token|session\s+token)s?\s+(?:is|was|are|were)\s+["']?([^\s"']{8,})`)
)

func (a *api) createSupportRequest(response http.ResponseWriter, request *http.Request) {
	if !a.requireAccounts(response) || !a.allowRequest(response, request, "support", 5, time.Hour) {
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
	if containsSecretShapedText(input.Subject + "\n" + input.Message + "\n" + input.AppVersion) {
		writeError(response, http.StatusBadRequest, "secret_shaped_content", "Remove passwords, codes, keys, tokens, and vault data before sending this request.")
		return
	}
	accountID := ""
	if cookie, err := request.Cookie(a.sessionCookieName()); err == nil && cookie.Value != "" {
		if user, err := a.config.Accounts.UserBySession(request.Context(), accounts.HashSessionToken(cookie.Value)); err == nil {
			accountID = user.ID
			if accountEmail, ok := normalizedEmail(user.Email); ok {
				email = accountEmail
			}
		}
	}
	if a.config.EmailSender != nil && !a.allowIdentity(response, request, "support-receipt", email, 3, time.Hour) {
		return
	}
	id, err := store.CreateSupportRequest(request.Context(), accounts.SupportRequest{
		AccountID: accountID, Email: email, Subject: input.Subject, Message: input.Message, Category: input.Category,
		AppVersion: input.AppVersion, DiagnosticCode: input.DiagnosticCode, BrowserIntegration: input.BrowserIntegration, RequestID: input.RequestID,
	})
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "support_unavailable", "Support intake is temporarily unavailable.")
		return
	}
	for _, message := range a.supportIntakeEmails(id, email, input.Category) {
		if err := a.config.EmailSender.SendAccountEmail(request.Context(), message); err != nil {
			requestLog(request.Context()).Error("Sesame support intake email could not be queued", "kind", message.Kind, "error", err)
		}
	}
	writeJSON(response, http.StatusAccepted, map[string]any{"requestId": id, "status": "open"})
}

func (a *api) supportIntakeEmails(reference, email, category string) []AccountEmail {
	if a.config.EmailSender == nil {
		return nil
	}
	expiresAt := time.Now().UTC().Add(7 * 24 * time.Hour)
	messages := []AccountEmail{{
		Kind:      "support-receipt",
		To:        email,
		Subject:   "Sesame received your support request",
		Body:      "Your support request reference is " + reference + ". Open the support portal for updates.",
		ActionURL: strings.TrimRight(a.config.WebBaseURL, "/") + "/support",
		ExpiresAt: expiresAt,
	}}
	if notice, ok := a.supportStaffNotice(reference, category); ok {
		messages = append(messages, notice)
	}
	return messages
}

func (a *api) supportStaffNotice(reference, category string) (AccountEmail, bool) {
	if a.config.SupportNotifyEmail == "" {
		return AccountEmail{}, false
	}
	return AccountEmail{
		Kind:      "support-staff-notify",
		To:        a.config.SupportNotifyEmail,
		Subject:   "New Sesame support request",
		Body:      "Support request " + reference + " arrived in the " + category + " category.",
		ActionURL: strings.TrimRight(a.config.AdminOrigin, "/"),
		ExpiresAt: time.Now().UTC().Add(7 * 24 * time.Hour),
	}, true
}

func (a *api) supportStaffFollowUpNotice(reference, category string) (AccountEmail, bool) {
	if a.config.SupportNotifyEmail == "" {
		return AccountEmail{}, false
	}
	return AccountEmail{
		Kind:      "support-staff-notify",
		To:        a.config.SupportNotifyEmail,
		Subject:   "A requester replied on a Sesame support request",
		Body:      "Support request " + reference + " received a requester follow-up in the " + category + " category.",
		ActionURL: strings.TrimRight(a.config.AdminOrigin, "/"),
		ExpiresAt: time.Now().UTC().Add(7 * 24 * time.Hour),
	}, true
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
	if secretAssignmentPattern.MatchString(value) || pinNumberPattern.MatchString(value) || longTokenPattern.MatchString(value) || seedPhrasePattern.MatchString(value) {
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
	for _, match := range base64SecretPattern.FindAllStringSubmatchIndex(value, -1) {
		if value[match[0]] == '.' {
			continue
		}
		candidate := value[match[2]:match[3]]
		if strings.HasPrefix(candidate, "/") {
			continue
		}
		if base64ShapedValue(candidate) {
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

func base64ShapedValue(value string) bool {
	if len(value) < 32 {
		return false
	}
	if len(value) < 43 && !strings.ContainsAny(value, "+=") {
		return false
	}
	hasDigit, hasUpper, hasLower := false, false, false
	for _, r := range value {
		switch {
		case r >= '0' && r <= '9':
			hasDigit = true
		case r >= 'A' && r <= 'Z':
			hasUpper = true
		case r >= 'a' && r <= 'z':
			hasLower = true
		}
	}
	return hasDigit && hasUpper && hasLower
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
