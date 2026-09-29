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
	emailTaken      bool
	registrationGap bool
	recoveryFound   bool
	registrations   []accounts.Registration
	recoveries      []string
}

func (s *enumerationStoreStub) RegisterEligible(_ context.Context, input accounts.Registration) (accounts.User, error) {
	s.registrations = append(s.registrations, input)
	switch {
	case s.registrationGap:
		return accounts.User{}, accounts.ErrRegistrationNotCreated
	case s.emailTaken:
		return accounts.User{}, accounts.ErrEmailTaken
	}
	return accounts.User{ID: "account-enumeration-new", Email: input.Email}, nil
}

func (s *enumerationStoreStub) CreatePasswordRecovery(_ context.Context, email string, _ []byte, _ time.Time) (accounts.User, bool, error) {
	s.recoveries = append(s.recoveries, email)
	if s.recoveryFound {
		return accounts.User{ID: "account-enumeration-known", Email: email}, true, nil
	}
	return accounts.User{Email: email}, false, nil
}

func newEnumerationAPI(store accounts.Store, sender EmailSender, mode string) *api {
	return &api{
		config: Config{
			Accounts: store, EmailSender: sender, AdminIPPepper: "test-pepper",
			RegistrationMode: mode, WebBaseURL: "https://account.example.invalid",
		},
		limits: &authLimiter{attempts: map[string]*limitEntry{}, recency: list.New()},
	}
}

func enumerationJSONRequest(path, body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	return request
}

func registrationBody(email, inviteCode string) string {
	invite := ""
	if inviteCode != "" {
		invite = fmt.Sprintf(`,"inviteCode":%q`, inviteCode)
	}
	return fmt.Sprintf(`{"email":%q,"password":%q,"termsAccepted":true,"termsVersion":%q,"privacyAcknowledged":true,"privacyVersion":%q%s}`,
		email, enumerationPassword, termsVersion, privacyVersion, invite)
}

func recoveryBody(email string) string {
	return fmt.Sprintf(`{"email":%q}`, email)
}

func registerRequest(handler *api, address, body string) *httptest.ResponseRecorder {
	request := enumerationJSONRequest("/v1/auth/register", body)
	request.RemoteAddr = address
	response := httptest.NewRecorder()
	handler.register(response, request)
	return response
}

func sendRecoveryRequest(handler *api, address, body string) *httptest.ResponseRecorder {
	request := enumerationJSONRequest("/v1/auth/password/recovery/request", body)
	request.RemoteAddr = address
	response := httptest.NewRecorder()
	handler.requestPasswordRecovery(response, request)
	return response
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
	if len(first.Result().Cookies()) != 0 || len(second.Result().Cookies()) != 0 {
		t.Fatalf("%s set a cookie", label)
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
	responses := make([]*httptest.ResponseRecorder, 0, 3)
	for _, testCase := range []struct {
		name       string
		mode       string
		inviteCode string
		emailTaken bool
		uniform    bool
		wantSent   int
	}{
		{name: "unknown address", mode: "public", wantSent: 1},
		{name: "known address", mode: "public", emailTaken: true},
		{name: "registered address on the eligibility list", mode: "invite", uniform: true},
		{name: "address with a used invitation", mode: "invite", inviteCode: "fictional-used-invite", uniform: true},
	} {
		store := &enumerationStoreStub{emailTaken: testCase.emailTaken, registrationGap: testCase.uniform}
		sender := &recordingEmailSender{}
		handler := newEnumerationAPI(store, sender, testCase.mode)
		email := fmt.Sprintf("registration-%d@example.invalid", len(responses))
		response := registerRequest(handler, "10.0.0.1:40000", registrationBody(email, testCase.inviteCode))
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
		if store.registrations[0].AllowPublic != (testCase.mode == "public") {
			t.Fatalf("%s allowPublic = %v", testCase.name, store.registrations[0].AllowPublic)
		}
		if testCase.inviteCode != "" && !bytes.Equal(store.registrations[0].InviteHash, accounts.HashSessionToken(testCase.inviteCode)) {
			t.Fatalf("%s invite hash = %x", testCase.name, store.registrations[0].InviteHash)
		}
		if len(sender.sent) != testCase.wantSent {
			t.Fatalf("%s emails = %d, want %d", testCase.name, len(sender.sent), testCase.wantSent)
		}
		for _, message := range sender.sent {
			if message.Kind != "verify-email" || message.To != email {
				t.Fatalf("%s email = %+v", testCase.name, message)
			}
		}
		responses = append(responses, response)
	}
	for index := 1; index < len(responses); index++ {
		assertUniformResponse(t, "registration", responses[0], responses[index])
	}
}

func TestPasswordRecoveryResponseDoesNotDependOnAccountExistence(t *testing.T) {
	responses := make([]*httptest.ResponseRecorder, 0, 2)
	for _, testCase := range []struct {
		name  string
		email string
		found bool
	}{
		{name: "unknown address", email: "recovery-missing@example.invalid"},
		{name: "known address", email: "recovery-known@example.invalid", found: true},
	} {
		store := &enumerationStoreStub{recoveryFound: testCase.found}
		sender := &recordingEmailSender{}
		handler := newEnumerationAPI(store, sender, "public")
		response := sendRecoveryRequest(handler, "10.0.0.1:40000", recoveryBody(testCase.email))
		if len(store.recoveries) != 1 || store.recoveries[0] != testCase.email {
			t.Fatalf("%s store calls = %v, want one call for the submitted address", testCase.name, store.recoveries)
		}
		if len(sender.sent) != 1 {
			t.Fatalf("%s emails = %d, want 1", testCase.name, len(sender.sent))
		}
		if message := sender.sent[0]; message.Kind != "recover-password" || message.To != testCase.email || message.Discard != !testCase.found {
			t.Fatalf("%s email = %+v", testCase.name, message)
		}
		responses = append(responses, response)
	}
	assertUniformResponse(t, "password recovery", responses[0], responses[1])
}

func clearEnumerationFixtures(t *testing.T, store *accounts.PostgresStore, address string, emails ...string) {
	t.Helper()
	cleanup := func() {
		ctx := context.Background()
		for _, email := range emails {
			_, _ = store.DB().ExecContext(ctx, `DELETE FROM sesame_beta_eligibility WHERE email = $1`, email)
			_, _ = store.DB().ExecContext(ctx, `DELETE FROM sesame_accounts WHERE email = $1`, email)
		}
		_, _ = store.DB().ExecContext(ctx, `DELETE FROM sesame_rate_limits WHERE key LIKE $1`, "%"+address+"%")
	}
	cleanup()
	t.Cleanup(cleanup)
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
	fixture := func(name string) string {
		return fmt.Sprintf("enumeration-%s-%d@example.invalid", name, seed)
	}
	address := func(octet int) string {
		return fmt.Sprintf("10.%d.%d.%d:40000", byte(seed>>16), byte(seed>>8), octet)
	}

	t.Run("public", func(t *testing.T) {
		client := address(7)
		email := fixture("public")
		clearEnumerationFixtures(t, store, client, email)
		sender := &recordingEmailSender{}
		handler := newEnumerationAPI(store, sender, "public")
		fresh := registerRequest(handler, client, registrationBody(email, ""))
		taken := registerRequest(handler, client, registrationBody(email, ""))
		assertUniformResponse(t, "public registration", fresh, taken)
		if len(sender.sent) != 1 {
			t.Fatalf("emails = %d, want 1", len(sender.sent))
		}
		var accountID string
		if err := store.DB().QueryRowContext(ctx, `SELECT id FROM sesame_accounts WHERE email = $1`, email).Scan(&accountID); err != nil {
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
	})

	t.Run("invite", func(t *testing.T) {
		client := address(8)
		joined := fixture("invite-joined")
		replayed := fixture("invite-replayed")
		expiredEmail := fixture("invite-expired")
		code := fmt.Sprintf("fictional-invite-%d", seed)
		expiredCode := fmt.Sprintf("fictional-expired-invite-%d", seed)
		clearEnumerationFixtures(t, store, client, joined, replayed, expiredEmail)
		if _, err := store.DB().ExecContext(ctx, `INSERT INTO sesame_beta_invites (code_hash, email, max_uses, uses, expires_at) VALUES ($1, NULL, 1, 0, NOW() + INTERVAL '1 hour')`, accounts.HashSessionToken(code)); err != nil {
			t.Fatalf("seed invite: %v", err)
		}
		if _, err := store.DB().ExecContext(ctx, `INSERT INTO sesame_beta_invites (code_hash, email, max_uses, uses, expires_at) VALUES ($1, NULL, 1, 0, NOW() - INTERVAL '1 minute')`, accounts.HashSessionToken(expiredCode)); err != nil {
			t.Fatalf("seed expired invite: %v", err)
		}
		t.Cleanup(func() {
			_, _ = store.DB().ExecContext(context.Background(), `DELETE FROM sesame_beta_invites WHERE code_hash = $1`, accounts.HashSessionToken(code))
			_, _ = store.DB().ExecContext(context.Background(), `DELETE FROM sesame_beta_invites WHERE code_hash = $1`, accounts.HashSessionToken(expiredCode))
		})
		sender := &recordingEmailSender{}
		handler := newEnumerationAPI(store, sender, "invite")
		eligible := registerRequest(handler, client, registrationBody(joined, code))
		known := registerRequest(handler, client, registrationBody(joined, code))
		unused := registerRequest(handler, client, registrationBody(replayed, code))
		assertUniformResponse(t, "invite registration", eligible, known)
		assertUniformResponse(t, "invite registration", eligible, unused)
		expired := registerRequest(handler, client, registrationBody(expiredEmail, expiredCode))
		if expired.Code != http.StatusForbidden || !strings.Contains(expired.Body.String(), "registration_not_eligible") {
			t.Fatalf("expired invite registration = %d %s", expired.Code, expired.Body.String())
		}
		var uses, created int
		if err := store.DB().QueryRowContext(ctx, `SELECT uses FROM sesame_beta_invites WHERE code_hash = $1`, accounts.HashSessionToken(code)).Scan(&uses); err != nil {
			t.Fatalf("read invite uses: %v", err)
		}
		if uses != 1 {
			t.Fatalf("invite uses = %d, want 1", uses)
		}
		if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM sesame_accounts WHERE email = $1`, replayed).Scan(&created); err != nil {
			t.Fatalf("count replayed account: %v", err)
		}
		if created != 0 {
			t.Fatalf("a consumed invite created an account")
		}
		if len(sender.sent) != 1 {
			t.Fatalf("emails = %d, want one verification email", len(sender.sent))
		}
	})

	t.Run("eligibility list", func(t *testing.T) {
		client := address(9)
		listed := fixture("listed")
		unlisted := fixture("unlisted")
		clearEnumerationFixtures(t, store, client, listed, unlisted)
		if _, err := store.DB().ExecContext(ctx, `INSERT INTO sesame_beta_eligibility (email, status) VALUES ($1, 'eligible')`, listed); err != nil {
			t.Fatalf("seed eligibility: %v", err)
		}
		sender := &recordingEmailSender{}
		handler := newEnumerationAPI(store, sender, "invite")
		eligible := registerRequest(handler, client, registrationBody(listed, ""))
		registered := registerRequest(handler, client, registrationBody(listed, ""))
		assertUniformResponse(t, "eligibility registration", eligible, registered)
		refused := registerRequest(handler, client, registrationBody(unlisted, ""))
		if refused.Code != http.StatusForbidden || !strings.Contains(refused.Body.String(), "registration_not_eligible") {
			t.Fatalf("unlisted registration = %d %s", refused.Code, refused.Body.String())
		}
		var status string
		if err := store.DB().QueryRowContext(ctx, `SELECT status FROM sesame_beta_eligibility WHERE email = $1`, listed).Scan(&status); err != nil {
			t.Fatalf("read eligibility status: %v", err)
		}
		if status != "registered" {
			t.Fatalf("eligibility status = %q, want registered", status)
		}
		if len(sender.sent) != 1 {
			t.Fatalf("emails = %d, want one verification email", len(sender.sent))
		}
	})

	t.Run("recovery", func(t *testing.T) {
		client := address(10)
		known := fixture("recovery-known")
		unknown := fixture("recovery-unknown")
		clearEnumerationFixtures(t, store, client, known, unknown)
		sender := &recordingEmailSender{}
		handler := newEnumerationAPI(store, sender, "public")
		if response := registerRequest(handler, client, registrationBody(known, "")); response.Code != http.StatusAccepted {
			t.Fatalf("seed registration status = %d: %s", response.Code, response.Body.String())
		}
		knownResponse := sendRecoveryRequest(handler, client, recoveryBody(known))
		unknownResponse := sendRecoveryRequest(handler, client, recoveryBody(unknown))
		assertUniformResponse(t, "password recovery", knownResponse, unknownResponse)
		var tokens, unknownAccounts int
		if err := store.DB().QueryRowContext(ctx, `
			SELECT COUNT(*) FROM sesame_account_tokens AS token
			JOIN sesame_accounts AS account ON account.id = token.account_id
			WHERE account.email = $1 AND token.purpose = 'recover_password'`, known).Scan(&tokens); err != nil {
			t.Fatalf("count recovery tokens: %v", err)
		}
		if tokens != 1 {
			t.Fatalf("recovery tokens = %d, want 1", tokens)
		}
		if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM sesame_accounts WHERE email = $1`, unknown).Scan(&unknownAccounts); err != nil {
			t.Fatalf("count unknown account: %v", err)
		}
		if unknownAccounts != 0 {
			t.Fatalf("an unknown address created an account")
		}
		delivered := make([]AccountEmail, 0, 2)
		for _, message := range sender.sent {
			if message.Kind == "recover-password" {
				delivered = append(delivered, message)
			}
		}
		if len(delivered) != 2 {
			t.Fatalf("recovery emails = %d, want 2", len(delivered))
		}
		if delivered[0].To != known || delivered[0].Discard {
			t.Fatalf("known recovery email = %+v", delivered[0])
		}
		if delivered[1].To != unknown || !delivered[1].Discard {
			t.Fatalf("unknown recovery email = %+v", delivered[1])
		}
	})
}
