package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type loginNetwork struct {
	env    *preHijackEnv
	prefix string
}

func newLoginNetwork(t *testing.T, env *preHijackEnv) *loginNetwork {
	t.Helper()
	seed := time.Now().UnixNano()
	network := &loginNetwork{env: env, prefix: fmt.Sprintf("2001:db8:%x:", uint16(seed>>8))}
	t.Cleanup(network.forgetClientLimits)
	return network
}

func (n *loginNetwork) address(subnet int, host int) string {
	return fmt.Sprintf("[%s%x::%x]:40000", n.prefix, subnet, host)
}

func (n *loginNetwork) forgetClientLimits() {
	_, _ = n.env.db.ExecContext(context.Background(), `DELETE FROM sesame_rate_limits WHERE key LIKE $1`, "login:"+n.prefix+"%")
}

func (n *loginNetwork) login(t *testing.T, remoteAddr, email, password string) *httptest.ResponseRecorder {
	t.Helper()
	n.forgetClientLimits()
	encoded, err := json.Marshal(map[string]any{"email": email, "password": password})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/auth/login", bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", preHijackOrigin)
	request.Header.Set("X-Sesame-CSRF", "login-budget-csrf")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "login-budget-csrf"})
	request.RemoteAddr = remoteAddr
	response := httptest.NewRecorder()
	n.env.handler.ServeHTTP(response, request)
	return response
}

func TestWrongPasswordsFromOneAddressDoNotLockOutAnother(t *testing.T) {
	env := newPreHijackEnv(t)
	network := newLoginNetwork(t, env)
	email := env.uniqueEmail("login-budget")
	env.registerAccount(t, newPreHijackClient(t, env), email)
	stranger := network.address(1, 1)
	strangerNeighbour := network.address(1, 2)
	owner := network.address(2, 1)

	for attempt := 1; attempt <= identityGuessLimit; attempt++ {
		if response := network.login(t, stranger, email, "fictional-wrong-password"); response.Code != http.StatusUnauthorized {
			t.Fatalf("wrong password %d status = %d: %s", attempt, response.Code, response.Body.String())
		}
	}
	if response := network.login(t, stranger, email, "fictional-wrong-password"); response.Code != http.StatusTooManyRequests {
		t.Fatalf("wrong password %d status = %d, want 429", identityGuessLimit+1, response.Code)
	}

	signedIn := network.login(t, owner, email, "fictional-attacker-password")
	if signedIn.Code != http.StatusOK {
		t.Fatalf("the owner's sign-in from another address status = %d: %s", signedIn.Code, signedIn.Body.String())
	}
	if user := decodeResponseUser(t, signedIn); user.Email != email {
		t.Fatalf("signed in as %q, want %q", user.Email, email)
	}

	if response := network.login(t, stranger, email, "fictional-attacker-password"); response.Code != http.StatusTooManyRequests {
		t.Fatalf("the refused address with the right password status = %d, want 429", response.Code)
	}
	if response := network.login(t, strangerNeighbour, email, "fictional-attacker-password"); response.Code != http.StatusTooManyRequests {
		t.Fatalf("another address in the refused /64 status = %d, want 429", response.Code)
	}
}

func TestOneEmailIsRefusedAfterItsHourlyCeilingFromManyAddresses(t *testing.T) {
	env := newPreHijackEnv(t)
	network := newLoginNetwork(t, env)
	email := env.uniqueEmail("login-ceiling")
	env.registerAccount(t, newPreHijackClient(t, env), email)
	spentByRegistration := 1

	for attempt := spentByRegistration + 1; attempt <= identityCeilingLimit; attempt++ {
		response := network.login(t, network.address(attempt, 1), email, "fictional-wrong-password")
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("wrong password %d from a fresh address status = %d: %s", attempt, response.Code, response.Body.String())
		}
	}
	refused := network.login(t, network.address(identityCeilingLimit+1, 1), email, "fictional-attacker-password")
	if refused.Code != http.StatusTooManyRequests {
		t.Fatalf("attempt %d from a fresh address status = %d, want 429: %s", identityCeilingLimit+1, refused.Code, refused.Body.String())
	}
	if refused.Header().Get("Retry-After") == "" {
		t.Fatal("the ceiling refusal has no Retry-After")
	}

	other := env.uniqueEmail("login-ceiling-other")
	env.registerAccount(t, newPreHijackClient(t, env), other)
	if response := network.login(t, network.address(identityCeilingLimit+2, 1), other, "fictional-attacker-password"); response.Code != http.StatusOK {
		t.Fatalf("another email from a fresh address status = %d: %s", response.Code, response.Body.String())
	}
}
