package httpapi

import (
	"bytes"
	"container/list"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"usesesame.app/backend/internal/accounts"
)

const enumerationPassword = "fictional-correct-horse-battery"

type enumerationStoreStub struct {
	accounts.Store
	accounts.AccountSecurityStore
	emailTaken    bool
	recoveryFound bool
	registrations []accounts.Registration
}

func (s *enumerationStoreStub) RegisterEligible(_ context.Context, input accounts.Registration) (accounts.User, error) {
	s.registrations = append(s.registrations, input)
	if s.emailTaken {
		return accounts.User{}, accounts.ErrEmailTaken
	}
	return accounts.User{ID: "account-enumeration-new", Email: input.Email}, nil
}

func (s *enumerationStoreStub) CreatePasswordRecovery(_ context.Context, email string, _ []byte, _ time.Time) (accounts.User, bool, error) {
	if s.recoveryFound {
		return accounts.User{ID: "account-enumeration-known", Email: email}, true, nil
	}
	return accounts.User{}, false, nil
}

func newEnumerationAPI(store accounts.Store, sender EmailSender) *api {
	return &api{
		config: Config{
			Accounts: store, EmailSender: sender, AdminIPPepper: "test-pepper",
			RegistrationMode: "public", WebBaseURL: "https://account.example.invalid",
		},
		limits: &authLimiter{attempts: map[string]*limitEntry{}, recency: list.New()},
	}
}

func enumerationJSONRequest(path, body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	return request
}

func registrationBody(email string) string {
	return fmt.Sprintf(`{"email":%q,"password":%q,"termsAccepted":true,"termsVersion":%q,"privacyAcknowledged":true,"privacyVersion":%q}`,
		email, enumerationPassword, termsVersion, privacyVersion)
}

func recoveryBody(email string) string {
	return fmt.Sprintf(`{"email":%q}`, email)
}

func assertUniformResponse(t *testing.T, label string, first, second *httptest.ResponseRecorder) {
	t.Helper()
	for _, response := range []*httptest.ResponseRecorder{first, second} {
		if response.Code != http.StatusAccepted {
			t.Fatalf("%s status = %d, want 202: %s", label, response.Code, response.Body.String())
		}
		if response.Body.Len() != 0 {
			t.Fatalf("%s body = %q, want an empty body", label, response.Body.String())
		}
	}
	if first.Code != second.Code {
		t.Fatalf("%s status = %d and %d", label, first.Code, second.Code)
	}
	if !bytes.Equal(first.Body.Bytes(), second.Body.Bytes()) {
		t.Fatalf("%s body = %q and %q", label, first.Body.String(), second.Body.String())
	}
	firstHeaders := first.Result().Header.Clone()
	secondHeaders := second.Result().Header.Clone()
	firstHeaders.Del("X-Request-ID")
	secondHeaders.Del("X-Request-ID")
	if !reflect.DeepEqual(firstHeaders, secondHeaders) {
		t.Fatalf("%s headers = %v and %v", label, firstHeaders, secondHeaders)
	}
}

func TestRegistrationResponseDoesNotDependOnAccountExistence(t *testing.T) {
	responses := make([]*httptest.ResponseRecorder, 0, 2)
	for _, testCase := range []struct {
		name       string
		email      string
		emailTaken bool
		wantSent   int
	}{
		{name: "unknown address", email: "registration-fresh@example.invalid", wantSent: 1},
		{name: "known address", email: "registration-taken@example.invalid", emailTaken: true},
	} {
		store := &enumerationStoreStub{emailTaken: testCase.emailTaken}
		sender := &recordingEmailSender{}
		handler := newEnumerationAPI(store, sender)
		response := httptest.NewRecorder()
		handler.register(response, enumerationJSONRequest("/v1/auth/register", registrationBody(testCase.email)))
		if response.Code != http.StatusAccepted {
			t.Fatalf("%s status = %d, want 202: %s", testCase.name, response.Code, response.Body.String())
		}
		if len(response.Result().Cookies()) != 0 {
			t.Fatalf("%s set a session cookie", testCase.name)
		}
		if len(store.registrations) != 1 {
			t.Fatalf("%s registrations = %d, want 1", testCase.name, len(store.registrations))
		}
		if !accounts.VerifyPassword(store.registrations[0].PasswordHash, enumerationPassword) {
			t.Fatalf("%s reached the store without hashing the password first", testCase.name)
		}
		if len(sender.sent) != testCase.wantSent {
			t.Fatalf("%s emails = %d, want %d", testCase.name, len(sender.sent), testCase.wantSent)
		}
		for _, message := range sender.sent {
			if message.Kind != "verify-email" || message.To != testCase.email {
				t.Fatalf("%s email = %+v", testCase.name, message)
			}
		}
		responses = append(responses, response)
	}
	assertUniformResponse(t, "registration", responses[0], responses[1])
}

func TestPasswordRecoveryResponseDoesNotDependOnAccountExistence(t *testing.T) {
	responses := make([]*httptest.ResponseRecorder, 0, 2)
	for _, testCase := range []struct {
		name     string
		email    string
		found    bool
		wantSent int
	}{
		{name: "unknown address", email: "recovery-missing@example.invalid"},
		{name: "known address", email: "recovery-known@example.invalid", found: true, wantSent: 1},
	} {
		store := &enumerationStoreStub{recoveryFound: testCase.found}
		sender := &recordingEmailSender{}
		handler := newEnumerationAPI(store, sender)
		dummyCalls := 0
		handler.dummyPasswordVerify = func() { dummyCalls++ }
		response := httptest.NewRecorder()
		handler.requestPasswordRecovery(response, enumerationJSONRequest("/v1/auth/password/recovery/request", recoveryBody(testCase.email)))
		if dummyCalls != 1 {
			t.Fatalf("%s dummy verifications = %d, want 1", testCase.name, dummyCalls)
		}
		if len(sender.sent) != testCase.wantSent {
			t.Fatalf("%s emails = %d, want %d", testCase.name, len(sender.sent), testCase.wantSent)
		}
		responses = append(responses, response)
	}
	assertUniformResponse(t, "password recovery", responses[0], responses[1])
}

func TestAccountFlowsDoNotDiscloseAccountExistenceAgainstTheDatabase(t *testing.T) {
	databaseURL := os.Getenv("SESAME_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("SESAME_TEST_DATABASE_URL is required")
	}
	ctx := context.Background()
	store, err := accounts.Open(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open account store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	lockDatabaseTests(t, store.DB())
	seed := time.Now().UnixNano()
	knownEmail := fmt.Sprintf("enumeration-known-%d@example.invalid", seed)
	unknownEmail := fmt.Sprintf("enumeration-unknown-%d@example.invalid", seed)
	clientAddress := fmt.Sprintf("10.%d.%d.7:40000", byte(seed>>16), byte(seed>>8))
	for _, email := range []string{knownEmail, unknownEmail} {
		if _, err := store.DB().ExecContext(ctx, `DELETE FROM sesame_accounts WHERE email = $1`, email); err != nil {
			t.Fatalf("clear fixture account: %v", err)
		}
	}
	t.Cleanup(func() {
		for _, email := range []string{knownEmail, unknownEmail} {
			_, _ = store.DB().ExecContext(context.Background(), `DELETE FROM sesame_accounts WHERE email = $1`, email)
		}
		_, _ = store.DB().ExecContext(context.Background(), `DELETE FROM sesame_rate_limits WHERE key LIKE $1`, "%"+clientAddress+"%")
	})

	sender := &recordingEmailSender{}
	handler := newEnumerationAPI(store, sender)
	dummyCalls := 0
	handler.dummyPasswordVerify = func() { dummyCalls++ }

	freshRequest := enumerationJSONRequest("/v1/auth/register", registrationBody(knownEmail))
	freshRequest.RemoteAddr = clientAddress
	freshResponse := httptest.NewRecorder()
	handler.register(freshResponse, freshRequest)
	takenRequest := enumerationJSONRequest("/v1/auth/register", registrationBody(knownEmail))
	takenRequest.RemoteAddr = clientAddress
	takenResponse := httptest.NewRecorder()
	handler.register(takenResponse, takenRequest)
	assertUniformResponse(t, "registration", freshResponse, takenResponse)

	var accountID string
	if err := store.DB().QueryRowContext(ctx, `SELECT id FROM sesame_accounts WHERE email = $1`, knownEmail).Scan(&accountID); err != nil {
		t.Fatalf("read registered account: %v", err)
	}
	var sessions, tokens int
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM sesame_sessions WHERE account_id = $1`, accountID).Scan(&sessions); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM sesame_account_tokens WHERE account_id = $1`, accountID).Scan(&tokens); err != nil {
		t.Fatalf("count account tokens: %v", err)
	}
	if sessions != 0 || tokens != 1 {
		t.Fatalf("sessions = %d and tokens = %d, want a token and no session", sessions, tokens)
	}
	if len(sender.sent) != 1 || sender.sent[0].Kind != "verify-email" || sender.sent[0].To != knownEmail {
		t.Fatalf("emails = %+v", sender.sent)
	}

	recoveryKnownRequest := enumerationJSONRequest("/v1/auth/password/recovery/request", recoveryBody(knownEmail))
	recoveryKnownRequest.RemoteAddr = clientAddress
	recoveryKnown := httptest.NewRecorder()
	handler.requestPasswordRecovery(recoveryKnown, recoveryKnownRequest)
	recoveryUnknownRequest := enumerationJSONRequest("/v1/auth/password/recovery/request", recoveryBody(unknownEmail))
	recoveryUnknownRequest.RemoteAddr = clientAddress
	recoveryUnknown := httptest.NewRecorder()
	handler.requestPasswordRecovery(recoveryUnknown, recoveryUnknownRequest)
	assertUniformResponse(t, "password recovery", recoveryKnown, recoveryUnknown)
	if dummyCalls != 2 {
		t.Fatalf("dummy verifications = %d, want 2", dummyCalls)
	}
}
