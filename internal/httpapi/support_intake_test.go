package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestSupportIntakeRejectsInvalidInput(t *testing.T) {
	env := newSupportTestEnv(t)
	validBody := func() map[string]any {
		return map[string]any{
			"email":   supportGuestIntakeEmail(),
			"subject": "Cannot import records",
			"message": "The importer stops after the first file and the log shows no error.",
		}
	}
	cases := []struct {
		name        string
		body        any
		contentType string
		wantStatus  int
		wantCode    string
	}{
		{name: "empty body", body: map[string]any{}, wantStatus: http.StatusBadRequest, wantCode: "invalid_support_request"},
		{name: "missing email", body: map[string]any{"subject": "Cannot import records", "message": "The importer stops after the first file and the log shows no error."}, wantStatus: http.StatusBadRequest, wantCode: "invalid_support_request"},
		{name: "invalid email", body: map[string]any{"email": "not-an-address", "subject": "Cannot import records", "message": "The importer stops after the first file and the log shows no error."}, wantStatus: http.StatusBadRequest, wantCode: "invalid_support_request"},
		{name: "short subject", body: map[string]any{"email": supportGuestIntakeEmail(), "subject": "ab", "message": "The importer stops after the first file and the log shows no error."}, wantStatus: http.StatusBadRequest, wantCode: "invalid_support_request"},
		{name: "long subject", body: map[string]any{"email": supportGuestIntakeEmail(), "subject": strings.Repeat("s", 121), "message": "The importer stops after the first file and the log shows no error."}, wantStatus: http.StatusBadRequest, wantCode: "invalid_support_request"},
		{name: "short message", body: map[string]any{"email": supportGuestIntakeEmail(), "subject": "Cannot import records", "message": "short"}, wantStatus: http.StatusBadRequest, wantCode: "invalid_support_request"},
		{name: "long message", body: map[string]any{"email": supportGuestIntakeEmail(), "subject": "Cannot import records", "message": strings.Repeat("m", 4001)}, wantStatus: http.StatusBadRequest, wantCode: "invalid_support_request"},
		{name: "long app version", body: map[string]any{"email": supportGuestIntakeEmail(), "subject": "Cannot import records", "message": "The importer stops after the first file and the log shows no error.", "appVersion": strings.Repeat("v", 41)}, wantStatus: http.StatusBadRequest, wantCode: "invalid_support_request"},
		{name: "unknown category", body: map[string]any{"email": supportGuestIntakeEmail(), "subject": "Cannot import records", "message": "The importer stops after the first file and the log shows no error.", "category": "vault"}, wantStatus: http.StatusBadRequest, wantCode: "invalid_ticket_category"},
		{name: "invalid diagnostic code", body: map[string]any{"email": supportGuestIntakeEmail(), "subject": "Cannot import records", "message": "The importer stops after the first file and the log shows no error.", "diagnosticCode": "bad code!"}, wantStatus: http.StatusBadRequest, wantCode: "invalid_diagnostic_code"},
		{name: "invalid browser integration", body: map[string]any{"email": supportGuestIntakeEmail(), "subject": "Cannot import records", "message": "The importer stops after the first file and the log shows no error.", "browserIntegration": "bad/state"}, wantStatus: http.StatusBadRequest, wantCode: "invalid_browser_integration"},
		{name: "invalid request id", body: map[string]any{"email": supportGuestIntakeEmail(), "subject": "Cannot import records", "message": "The importer stops after the first file and the log shows no error.", "requestId": "bad id"}, wantStatus: http.StatusBadRequest, wantCode: "invalid_request_id"},
		{name: "unknown field", body: map[string]any{"email": supportGuestIntakeEmail(), "subject": "Cannot import records", "message": "The importer stops after the first file and the log shows no error.", "vaultPassword": "fictional"}, wantStatus: http.StatusBadRequest, wantCode: "invalid_support_request"},
		{name: "multipart body", body: validBody(), contentType: "multipart/form-data; boundary=test", wantStatus: http.StatusUnsupportedMediaType, wantCode: "attachments_not_accepted"},
		{name: "missing content type", body: nil, wantStatus: http.StatusUnsupportedMediaType, wantCode: "json_required"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			options := []func(*http.Request){supportWebMutation()}
			if testCase.contentType != "" {
				options = append(options, func(request *http.Request) {
					request.Header.Set("Content-Type", testCase.contentType)
				})
			}
			response := env.request(http.MethodPost, "/v1/support/requests", testCase.body, options...)
			if response.Code != testCase.wantStatus || errorCode(t, response) != testCase.wantCode {
				t.Fatalf("intake = %d %q, want %d %q", response.Code, errorCode(t, response), testCase.wantStatus, testCase.wantCode)
			}
			var tickets int
			if err := env.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM sesame_support_requests`).Scan(&tickets); err != nil {
				t.Fatalf("count tickets: %v", err)
			}
			if tickets != 0 {
				t.Fatalf("a rejected request stored %d tickets", tickets)
			}
		})
	}
}

func TestSupportIntakeRejectsSecretShapedContent(t *testing.T) {
	env := newSupportTestEnv(t)
	for _, testCase := range []struct {
		name    string
		subject string
		message string
	}{
		{name: "assignment", subject: "Cannot sign in", message: "I wrote down password: correct-horse-battery before the reset."},
		{name: "pwd assignment", subject: "Cannot sign in", message: "The log line shows pwd: correct-horse-battery next to my address."},
		{name: "pin number", subject: "Cannot sign in", message: "The app asks for PIN 482913 even after the reset."},
		{name: "seed phrase", subject: "Cannot sign in", message: "My seed phrase is apple banana cherry dog eagle fence grape house igloo jacket kite lemon and the app rejects it."},
		{name: "seed phrase on its own line", subject: "Cannot sign in", message: "I pasted this by mistake:\nseed phrase:\napple banana cherry dog eagle fence grape house igloo jacket kite lemon\nPlease remove it from the ticket."},
		{name: "base64 key", subject: "Cannot sign in", message: "The exported key ejgFI6bjzttikWD1325ZdjhohM88ycZWJS84RCntRdU= will not import."},
		{name: "fullwidth colon", subject: "Cannot sign in", message: "I wrote password：correct-horse-battery in my notes before the reset."},
		{name: "recovery kit", subject: "Cannot sign in", message: "My recovery kit is ABCDE-FGHJK-MNPQR-STUVW-XYZ23 and it will not work."},
		{name: "otpauth uri", subject: "Cannot sign in", message: "The code comes from otpauth://totp/Sesame:user@example.invalid?secret=JBSWY3DPEHPK3PXP every time."},
		{name: "private key block", subject: "Cannot sign in", message: "I pasted -----BEGIN PRIVATE KEY----- into the notes field by mistake."},
		{name: "prose token", subject: "Cannot sign in", message: "The access token is 0e6d2f1a9b8c7d4e5f6a and the app rejects it."},
		{name: "long token", subject: "Cannot sign in", message: "The request id 0123456789abcdef0123456789abcdef01234567 is rejected."},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			response := env.request(http.MethodPost, "/v1/support/requests", map[string]any{
				"email":   supportGuestIntakeEmail(),
				"subject": testCase.subject,
				"message": testCase.message,
			}, supportWebMutation())
			if response.Code != http.StatusBadRequest || errorCode(t, response) != "secret_shaped_content" {
				t.Fatalf("intake = %d %q, want 400 secret_shaped_content", response.Code, errorCode(t, response))
			}
		})
	}
}

func TestSupportIntakeRejectsSecretShapedAppVersion(t *testing.T) {
	env := newSupportTestEnv(t)
	response := env.request(http.MethodPost, "/v1/support/requests", map[string]any{
		"email":      supportGuestIntakeEmail(),
		"subject":    "Cannot import records",
		"message":    "The importer stops after the first file and the log shows no error.",
		"appVersion": "pwd: correct-horse-battery",
	}, supportWebMutation())
	if response.Code != http.StatusBadRequest || errorCode(t, response) != "secret_shaped_content" {
		t.Fatalf("intake = %d %q, want 400 secret_shaped_content", response.Code, errorCode(t, response))
	}
}

func TestSupportIntakeAcceptsOrdinaryTextNearSecretWords(t *testing.T) {
	env := newSupportTestEnv(t)
	response := env.request(http.MethodPost, "/v1/support/requests", map[string]any{
		"email":   supportGuestIntakeEmail(),
		"subject": "Cannot find the pin settings",
		"message": "I want to change the seed phrase storage location and the app pin length, but https://example.invalid/download/Sesame+Release+Notes+September+2026 will not open after the update.",
	}, supportWebMutation())
	if response.Code != http.StatusAccepted {
		t.Fatalf("intake status = %d: %s", response.Code, response.Body.String())
	}
}

func TestSupportIntakeAcceptsAValidGuestRequest(t *testing.T) {
	env := newSupportTestEnv(t)
	response := env.request(http.MethodPost, "/v1/support/requests", map[string]any{
		"email":              supportGuestIntakeEmail(),
		"subject":            "Cannot import records",
		"message":            "The importer stops after the first file and the log shows no error.",
		"category":           "import",
		"appVersion":         "0.2.5",
		"diagnosticCode":     "import-1.2.3",
		"browserIntegration": "connected",
		"requestId":          "req-123",
	}, supportWebMutation())
	if response.Code != http.StatusAccepted {
		t.Fatalf("intake status = %d: %s", response.Code, response.Body.String())
	}
	var payload struct {
		RequestID string `json:"requestId"`
		Status    string `json:"status"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode intake response: %v", err)
	}
	if payload.RequestID == "" || payload.Status != "open" {
		t.Fatalf("intake response = %+v", payload)
	}
	var accountID sql.NullString
	var email, category, status, appVersion string
	if err := env.db.QueryRowContext(context.Background(), `
		SELECT account_id, email, category, status, app_version
		FROM sesame_support_requests WHERE id = $1`, payload.RequestID).
		Scan(&accountID, &email, &category, &status, &appVersion); err != nil {
		t.Fatalf("read stored request: %v", err)
	}
	if accountID.Valid {
		t.Fatalf("guest request stored account %q", accountID.String)
	}
	if email != supportGuestIntakeEmail() || category != "import" || status != "open" || appVersion != "0.2.5" {
		t.Fatalf("stored request = %s %s %s %s", email, category, status, appVersion)
	}
	var body string
	if err := env.db.QueryRowContext(context.Background(), `SELECT body FROM sesame_support_messages WHERE ticket_id = $1`, payload.RequestID).Scan(&body); err != nil {
		t.Fatalf("read stored message: %v", err)
	}
	if body != "The importer stops after the first file and the log shows no error." {
		t.Fatalf("stored message = %q", body)
	}
}

func TestSupportIntakeAttributesASignedInRequestToTheAccount(t *testing.T) {
	env := newSupportTestEnv(t)
	token := env.seedAccount(t, "acct-intake-owner", supportTestOwnerEmail)
	response := env.request(http.MethodPost, "/v1/support/requests", map[string]any{
		"email":   supportTestOtherEmail,
		"subject": "Cannot import records",
		"message": "The importer stops after the first file and the log shows no error.",
	}, supportSession(token), supportWebMutation())
	if response.Code != http.StatusAccepted {
		t.Fatalf("intake status = %d: %s", response.Code, response.Body.String())
	}
	var payload struct {
		RequestID string `json:"requestId"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode intake response: %v", err)
	}
	var accountID sql.NullString
	var email string
	if err := env.db.QueryRowContext(context.Background(), `SELECT account_id, email FROM sesame_support_requests WHERE id = $1`, payload.RequestID).Scan(&accountID, &email); err != nil {
		t.Fatalf("read stored request: %v", err)
	}
	if !accountID.Valid || accountID.String != "acct-intake-owner" {
		t.Fatalf("stored account = %+v, want acct-intake-owner", accountID)
	}
	if email != supportTestOwnerEmail {
		t.Fatalf("stored email = %q, want the signed-in account address", email)
	}
}
