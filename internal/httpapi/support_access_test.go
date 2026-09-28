package httpapi

import (
	"bytes"
	"container/list"
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"usesesame.app/backend/internal/accounts"
)

type supportAccessStoreStub struct {
	accounts.Store
	accounts.AccountSecurityStore
	ticket    accounts.SupportTicketDetail
	err       error
	readCalls int
	lastHash  []byte
	replies   int
}

func (s *supportAccessStoreStub) SupportTicketForAccessToken(_ context.Context, tokenHash []byte) (accounts.SupportTicketDetail, error) {
	s.readCalls++
	s.lastHash = tokenHash
	if s.err != nil {
		return accounts.SupportTicketDetail{}, s.err
	}
	if s.ticket.ID == "" {
		return accounts.SupportTicketDetail{}, accounts.ErrNotFound
	}
	return s.ticket, nil
}

func (s *supportAccessStoreStub) ReplyToSupportTicketWithAccessToken(_ context.Context, tokenHash []byte, _ string) (accounts.SupportTicketDetail, error) {
	s.replies++
	s.lastHash = tokenHash
	if s.err != nil {
		return accounts.SupportTicketDetail{}, s.err
	}
	if s.ticket.ID == "" {
		return accounts.SupportTicketDetail{}, accounts.ErrNotFound
	}
	return s.ticket, nil
}

func newSupportAccessUnitAPI(store *supportAccessStoreStub) *api {
	return &api{
		config: Config{Accounts: store, AllowedOrigin: supportTestOrigin, WebBaseURL: supportTestOrigin},
		limits: &authLimiter{attempts: map[string]*limitEntry{}, recency: list.New()},
	}
}

func supportAccessToken(seed byte) string {
	raw := make([]byte, 32)
	for index := range raw {
		raw[index] = seed + byte(index)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func supportAccessPost(t *testing.T, handle func(http.ResponseWriter, *http.Request), path, body string, options ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = "10.9.9.9:40000"
	for _, option := range options {
		option(request)
	}
	response := httptest.NewRecorder()
	handle(response, request)
	return response
}

func TestSupportAccessRejectsMalformedTokensIndistinguishably(t *testing.T) {
	const expectedBody = `{"error":{"code":"support_link_invalid","message":"That support link is invalid or expired. Send a new request from the support form if you still need help."}}` + "\n"
	store := &supportAccessStoreStub{}
	api := newSupportAccessUnitAPI(store)
	cases := []struct {
		name           string
		body           string
		lookupExpected bool
	}{
		{name: "empty", body: `{"token":""}`},
		{name: "missing", body: `{}`},
		{name: "too short", body: `{"token":"` + strings.Repeat("a", 31) + `"}`},
		{name: "too long", body: `{"token":"` + strings.Repeat("a", 129) + `"}`},
		{name: "whitespace", body: `{"token":"` + strings.Repeat("a", 21) + ` ` + strings.Repeat("a", 21) + `"}`},
		{name: "non base64", body: `{"token":"` + strings.Repeat("!", 43) + `"}`, lookupExpected: true},
		{name: "unknown", body: `{"token":"` + supportAccessToken(1) + `"}`, lookupExpected: true},
		{name: "unknown field", body: `{"token":"` + supportAccessToken(2) + `","requestId":"ticket-other"}`},
		{name: "malformed json", body: `{"token":`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			store.readCalls = 0
			response := supportAccessPost(t, api.redeemSupportAccess, "/v1/support/access", testCase.body)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", response.Code, response.Body.String())
			}
			if response.Body.String() != expectedBody {
				t.Fatalf("body = %q, want %q", response.Body.String(), expectedBody)
			}
			if (store.readCalls == 1) != testCase.lookupExpected {
				t.Fatalf("store lookups = %d, lookupExpected = %v", store.readCalls, testCase.lookupExpected)
			}
		})
	}
}

func TestSupportAccessSuccessAndStoreFailure(t *testing.T) {
	store := &supportAccessStoreStub{ticket: accounts.SupportTicketDetail{SupportTicketSummary: accounts.SupportTicketSummary{ID: "ticket-accessible", Subject: "Fictional subject"}}}
	api := newSupportAccessUnitAPI(store)
	response := supportAccessPost(t, api.redeemSupportAccess, "/v1/support/access", `{"token":"`+supportAccessToken(3)+`"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
	}
	if got := decodeTicket(t, response); got.CanClose || got.CanReopen {
		t.Fatal("a guest redemption must not offer close or reopen")
	}
	if len(store.lastHash) != 32 {
		t.Fatalf("lookup hash length = %d, want 32", len(store.lastHash))
	}

	store.err = errors.New("database unavailable")
	response = supportAccessPost(t, api.redeemSupportAccess, "/v1/support/access", `{"token":"`+supportAccessToken(4)+`"}`)
	if response.Code != http.StatusServiceUnavailable || errorCode(t, response) != "support_access_unavailable" {
		t.Fatalf("store failure = %d %q", response.Code, errorCode(t, response))
	}
}

func TestSupportAccessReplyResolvesTheTokenBeforeMessageRules(t *testing.T) {
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previousLogger)

	store := &supportAccessStoreStub{}
	api := newSupportAccessUnitAPI(store)
	secret := `{"token":"` + supportAccessToken(5) + `","message":"password = hunter2"}`
	response := supportAccessPost(t, api.replyToSupportAccess, "/v1/support/access/reply", secret)
	if response.Code != http.StatusBadRequest || errorCode(t, response) != "support_link_invalid" {
		t.Fatalf("unknown token reply = %d %q, want the generic link failure", response.Code, errorCode(t, response))
	}
	if store.replies != 0 {
		t.Fatal("an unknown token must not reach the reply store")
	}

	store.ticket = accounts.SupportTicketDetail{SupportTicketSummary: accounts.SupportTicketSummary{ID: "ticket-accessible"}}
	response = supportAccessPost(t, api.replyToSupportAccess, "/v1/support/access/reply", secret)
	if response.Code != http.StatusBadRequest || errorCode(t, response) != "secret_shaped_content" {
		t.Fatalf("secret reply = %d %q", response.Code, errorCode(t, response))
	}
	if store.replies != 0 {
		t.Fatal("a rejected reply must not reach the reply store")
	}
	if strings.Contains(response.Body.String(), "hunter2") {
		t.Fatal("the rejected secret appeared in the response")
	}
	if strings.Contains(logs.String(), "hunter2") || strings.Contains(logs.String(), supportAccessToken(5)) {
		t.Fatalf("the rejected secret or token reached the logs: %s", logs.String())
	}

	response = supportAccessPost(t, api.replyToSupportAccess, "/v1/support/access/reply", `{"token":"`+supportAccessToken(5)+`","message":"x"}`)
	if response.Code != http.StatusBadRequest || errorCode(t, response) != "invalid_support_reply" {
		t.Fatalf("short reply = %d %q", response.Code, errorCode(t, response))
	}

	response = supportAccessPost(t, api.replyToSupportAccess, "/v1/support/access/reply", `{"token":"`+supportAccessToken(5)+`","message":"Thanks, this is fixed now."}`)
	if response.Code != http.StatusCreated || store.replies != 1 {
		t.Fatalf("valid reply = %d with %d stored replies, want 201 and 1", response.Code, store.replies)
	}
}

func TestSupportAccessBurstsAreLimitedBeforeLookup(t *testing.T) {
	store := &supportAccessStoreStub{}
	api := newSupportAccessUnitAPI(store)
	unknown := `{"token":"` + supportAccessToken(6) + `"}`
	for attempt := 1; attempt <= 60; attempt++ {
		response := supportAccessPost(t, api.redeemSupportAccess, "/v1/support/access", unknown)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("read attempt %d = %d, want 400", attempt, response.Code)
		}
	}
	response := supportAccessPost(t, api.redeemSupportAccess, "/v1/support/access", unknown)
	if response.Code != http.StatusTooManyRequests || errorCode(t, response) != "too_many_attempts" || response.Header().Get("Retry-After") == "" {
		t.Fatalf("read burst = %d %q retry %q", response.Code, errorCode(t, response), response.Header().Get("Retry-After"))
	}
	before := store.readCalls
	response = supportAccessPost(t, api.redeemSupportAccess, "/v1/support/access", `{"token":"`+supportAccessToken(7)+`"}`)
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("a valid token after the burst = %d, want 429", response.Code)
	}
	if store.readCalls != before {
		t.Fatal("the burst limiter must stop the request before any token lookup")
	}

	replyAPI := newSupportAccessUnitAPI(&supportAccessStoreStub{})
	for attempt := 1; attempt <= 12; attempt++ {
		response := supportAccessPost(t, replyAPI.replyToSupportAccess, "/v1/support/access/reply", unknown)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("reply attempt %d = %d, want 400", attempt, response.Code)
		}
	}
	response = supportAccessPost(t, replyAPI.replyToSupportAccess, "/v1/support/access/reply", unknown)
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("reply client burst = %d, want 429", response.Code)
	}

	perLinkAPI := newSupportAccessUnitAPI(&supportAccessStoreStub{})
	for attempt := 1; attempt <= 20; attempt++ {
		request := httptest.NewRequest(http.MethodPost, "/v1/support/access/reply", strings.NewReader(unknown))
		request.Header.Set("Content-Type", "application/json")
		request.RemoteAddr = "10.7.0." + strconv.Itoa(attempt) + ":40000"
		response := httptest.NewRecorder()
		perLinkAPI.replyToSupportAccess(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("per-link attempt %d = %d, want 400", attempt, response.Code)
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/support/access/reply", strings.NewReader(unknown))
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = "10.7.0.250:40000"
	response = httptest.NewRecorder()
	perLinkAPI.replyToSupportAccess(response, request)
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("per-link burst = %d, want 429", response.Code)
	}
}

func TestSupportAccessNeedsOriginAndCSRFAndSetsNoCookie(t *testing.T) {
	store := &supportAccessStoreStub{ticket: accounts.SupportTicketDetail{SupportTicketSummary: accounts.SupportTicketSummary{ID: "ticket-accessible"}}}
	handler := New(Config{Accounts: store, AllowedOrigin: supportTestOrigin, WebBaseURL: supportTestOrigin})
	body := `{"token":"` + supportAccessToken(8) + `"}`
	response := supportAccessPost(t, handler.ServeHTTP, "/v1/support/access", body)
	if response.Code != http.StatusForbidden || errorCode(t, response) != "origin_not_allowed" {
		t.Fatalf("missing origin = %d %q", response.Code, errorCode(t, response))
	}
	response = supportAccessPost(t, handler.ServeHTTP, "/v1/support/access", body, func(request *http.Request) {
		request.Header.Set("Origin", supportTestOrigin)
	})
	if response.Code != http.StatusForbidden || errorCode(t, response) != "invalid_csrf" {
		t.Fatalf("missing csrf = %d %q", response.Code, errorCode(t, response))
	}
	response = supportAccessPost(t, handler.ServeHTTP, "/v1/support/access", body, supportWebMutation(), supportSession("fictional-unrelated-session"))
	if response.Code != http.StatusOK {
		t.Fatalf("guarded redemption = %d: %s", response.Code, response.Body.String())
	}
	if cookies := response.Result().Cookies(); len(cookies) != 0 {
		t.Fatalf("guest redemption set %d cookies", len(cookies))
	}
	response = supportAccessPost(t, handler.ServeHTTP, "/v1/support/access/reply", `{"token":"`+supportAccessToken(8)+`","message":"A fictional follow-up."}`, supportWebMutation())
	if response.Code != http.StatusCreated {
		t.Fatalf("guarded reply = %d: %s", response.Code, response.Body.String())
	}
	if cookies := response.Result().Cookies(); len(cookies) != 0 {
		t.Fatalf("guest reply set %d cookies", len(cookies))
	}
}
