package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"usesesame.app/backend/internal/accounts"
)

type failingSessionLookup struct{ *accounts.PostgresStore }

func (failingSessionLookup) UserBySession(context.Context, []byte) (accounts.User, error) {
	return accounts.User{}, errors.New("fictional database outage")
}

func intakeBody() map[string]any {
	return map[string]any{
		"email":   supportGuestIntakeEmail(),
		"subject": "Cannot import records",
		"message": "The importer stops after the first file and the log shows no error.",
	}
}

func storedTickets(t *testing.T, env *supportTestEnv) int {
	t.Helper()
	var count int
	if err := env.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM sesame_support_requests`).Scan(&count); err != nil {
		t.Fatalf("count tickets: %v", err)
	}
	return count
}

func TestAnExpiredSessionStillFilesAGuestRequest(t *testing.T) {
	env := newSupportTestEnv(t)
	response := env.request(http.MethodPost, "/v1/support/requests", intakeBody(), supportSession("fictional-expired-session-token"), supportWebMutation())
	if response.Code != http.StatusAccepted {
		t.Fatalf("intake with an unknown session = %d: %s", response.Code, response.Body.String())
	}
	var attributed int
	if err := env.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM sesame_support_requests WHERE account_id IS NOT NULL`).Scan(&attributed); err != nil {
		t.Fatalf("count attributed tickets: %v", err)
	}
	if storedTickets(t, env) != 1 || attributed != 0 {
		t.Fatalf("stored %d tickets, %d attributed, want one guest ticket", storedTickets(t, env), attributed)
	}
}

func TestADatabaseFailureLookingUpTheSessionIsNotMistakenForAGuest(t *testing.T) {
	env := newSupportTestEnv(t, func(config *Config) {
		config.Accounts = failingSessionLookup{config.Accounts.(*accounts.PostgresStore)}
	})
	response := env.request(http.MethodPost, "/v1/support/requests", intakeBody(), supportSession("fictional-live-session-token"), supportWebMutation())
	if response.Code != http.StatusServiceUnavailable || errorCode(t, response) != "support_unavailable" {
		t.Fatalf("intake during an outage = %d %q, want 503 support_unavailable", response.Code, errorCode(t, response))
	}
	if storedTickets(t, env) != 0 {
		t.Fatal("a request filed during a session lookup failure was stored as a guest ticket")
	}
}

func TestAttachingASupportRequestIsRateLimitedLikeTheOtherAccountActions(t *testing.T) {
	env := newSupportTestEnv(t)
	token := env.seedAccount(t, "acct-attach-limit", "attach-limit@example.invalid")
	if _, err := env.db.ExecContext(context.Background(), `UPDATE sesame_accounts SET email_verified_at = NOW() WHERE id = 'acct-attach-limit'`); err != nil {
		t.Fatalf("verify account: %v", err)
	}
	path := "/v1/account/support/" + strings.Repeat("a", 32) + "/attach"
	limited := 0
	for attempt := 0; attempt < 20; attempt++ {
		sameClient := func(request *http.Request) { request.RemoteAddr = env.ipPrefix + "99:40000" }
		response := env.request(http.MethodPost, path, nil, sameClient, supportSession(token), supportWebMutation())
		if response.Code == http.StatusTooManyRequests {
			limited++
		}
	}
	if limited == 0 {
		t.Fatal("twenty attach attempts in a row were never limited")
	}
}
