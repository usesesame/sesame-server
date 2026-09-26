package httpapi

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"usesesame.app/backend/internal/accounts"
)

type recordingEmailSender struct {
	sent []AccountEmail
}

func (s *recordingEmailSender) SendAccountEmail(_ context.Context, message AccountEmail) error {
	s.sent = append(s.sent, message)
	return nil
}

type intakeStoreStub struct {
	accounts.Store
	accounts.AccountSecurityStore
	requests []accounts.SupportRequest
}

func (s *intakeStoreStub) CreateSupportRequest(_ context.Context, input accounts.SupportRequest) (string, error) {
	s.requests = append(s.requests, input)
	return fmt.Sprintf("reference-%d", len(s.requests)), nil
}

type preferenceStoreStub struct {
	accounts.Store
	preferences accounts.NotificationPreferences
	err         error
}

func (s preferenceStoreStub) NotificationPreferences(context.Context, string) (accounts.NotificationPreferences, error) {
	return s.preferences, s.err
}

func (s preferenceStoreStub) UpdateNotificationPreferences(context.Context, string, accounts.NotificationPreferences) error {
	return nil
}

type plainStoreStub struct {
	accounts.Store
}

func TestSupportIntakeReceiptBurstIsRejectedPerAddress(t *testing.T) {
	sender := &recordingEmailSender{}
	store := &intakeStoreStub{}
	a := &api{
		config: Config{
			Accounts:      store,
			EmailSender:   sender,
			AdminIPPepper: "test-pepper",
			WebBaseURL:    "https://account.example.invalid",
		},
		limits: &authLimiter{attempts: map[string]*limitEntry{}, recency: list.New()},
	}
	for index := 1; index <= 4; index++ {
		body := fmt.Sprintf(`{"email":"burst@example.invalid","subject":"Fictional issue %d","message":"This is fictional report number %d."}`, index, index)
		request := httptest.NewRequest(http.MethodPost, "/v1/support/requests", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		a.createSupportRequest(response, request)
		if index <= 3 && response.Code != http.StatusAccepted {
			t.Fatalf("request %d status = %d, want 202: %s", index, response.Code, response.Body.String())
		}
		if index == 4 && response.Code != http.StatusTooManyRequests {
			t.Fatalf("burst request status = %d, want 429: %s", response.Code, response.Body.String())
		}
	}
	if len(store.requests) != 3 {
		t.Fatalf("stored requests = %d, want 3", len(store.requests))
	}
	receipts := 0
	for _, message := range sender.sent {
		if message.Kind == "support-receipt" {
			receipts++
		}
	}
	if receipts != 3 {
		t.Fatalf("receipts = %d, want 3", receipts)
	}
}

func TestSupportReplyEmailDecision(t *testing.T) {
	lookupErr := errors.New("notification preferences are unavailable")
	sender := &recordingEmailSender{}
	for _, tc := range []struct {
		name      string
		accountID string
		sender    EmailSender
		store     accounts.Store
		want      bool
		wantErr   bool
	}{
		{
			name: "sender unset", accountID: "account-1",
			store: preferenceStoreStub{preferences: accounts.NotificationPreferences{SupportReplies: true}},
			want:  false, wantErr: false,
		},
		{
			name: "no account", accountID: "", sender: sender,
			store: preferenceStoreStub{preferences: accounts.NotificationPreferences{SupportReplies: true}},
			want:  false, wantErr: false,
		},
		{
			name: "preference store missing", accountID: "account-1", sender: sender,
			store: plainStoreStub{}, want: false, wantErr: true,
		},
		{
			name: "preference lookup error", accountID: "account-1", sender: sender,
			store: preferenceStoreStub{err: lookupErr}, want: false, wantErr: true,
		},
		{
			name: "preference off", accountID: "account-1", sender: sender,
			store: preferenceStoreStub{preferences: accounts.NotificationPreferences{SupportReplies: false}},
			want:  false, wantErr: false,
		},
		{
			name: "preference on", accountID: "account-1", sender: sender,
			store: preferenceStoreStub{preferences: accounts.NotificationPreferences{SupportReplies: true}},
			want:  true, wantErr: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &api{config: Config{EmailSender: tc.sender, Accounts: tc.store}}
			enabled, err := a.supportReplyEmailEnabled(context.Background(), tc.accountID)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tc.wantErr)
			}
			if enabled != tc.want {
				t.Fatalf("enabled = %v, want %v", enabled, tc.want)
			}
		})
	}
}

func TestSupportIntakeEmailPlan(t *testing.T) {
	const (
		requester = "requester@example.invalid"
		staff     = "staff@example.invalid"
		reference = "0123456789abcdef"
	)
	sender := &recordingEmailSender{}
	for _, tc := range []struct {
		name      string
		sender    EmailSender
		staff     string
		wantKinds []string
		wantTo    []string
	}{
		{
			name: "receipt when sender is set", sender: sender,
			wantKinds: []string{"support-receipt"}, wantTo: []string{requester},
		},
		{
			name: "no receipt when sender is unset", sender: nil, staff: staff,
			wantKinds: nil, wantTo: nil,
		},
		{
			name: "staff notice when the address is set", sender: sender, staff: staff,
			wantKinds: []string{"support-receipt", "support-staff-notify"}, wantTo: []string{requester, staff},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &api{config: Config{
				EmailSender:        tc.sender,
				SupportNotifyEmail: tc.staff,
				WebBaseURL:         "https://account.example.invalid",
				AdminOrigin:        "https://admin.example.invalid",
			}}
			messages := a.supportIntakeEmails(reference, requester, "bug")
			if len(messages) != len(tc.wantKinds) {
				t.Fatalf("messages = %d, want %d", len(messages), len(tc.wantKinds))
			}
			for index, message := range messages {
				if message.Kind != tc.wantKinds[index] {
					t.Fatalf("message %d kind = %q, want %q", index, message.Kind, tc.wantKinds[index])
				}
				if message.To != tc.wantTo[index] {
					t.Fatalf("message %d to = %q, want %q", index, message.To, tc.wantTo[index])
				}
				if message.ExpiresAt.IsZero() {
					t.Fatalf("message %d has no expiry", index)
				}
			}
		})
	}
}
