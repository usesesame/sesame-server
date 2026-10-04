package httpapi

import (
	"context"
	"errors"
	"testing"

	"usesesame.app/backend/internal/accounts"
	adminstore "usesesame.app/backend/internal/admin"
)

type visibilityPreferenceStore struct {
	accounts.Store
	preferences accounts.NotificationPreferences
	err         error
	calls       int
}

func (s *visibilityPreferenceStore) NotificationPreferences(context.Context, string) (accounts.NotificationPreferences, error) {
	s.calls++
	return s.preferences, s.err
}

func (s *visibilityPreferenceStore) UpdateNotificationPreferences(context.Context, string, accounts.NotificationPreferences) error {
	return nil
}

func TestSupportDeliveryReasonMapping(t *testing.T) {
	sender := &recordingEmailSender{}
	lookupErr := errors.New("notification preferences are unavailable")
	for _, tc := range []struct {
		name         string
		outboxStatus string
		accountID    string
		sender       EmailSender
		store        accounts.Store
		want         string
	}{
		{name: "delivered", outboxStatus: "delivered", want: "delivered"},
		{name: "pending", outboxStatus: "pending", want: "pending"},
		{name: "processing", outboxStatus: "processing", want: "pending"},
		{name: "failed", outboxStatus: "failed", want: "failed"},
		{name: "guest ticket", accountID: "", want: "guest"},
		{name: "mail not configured", accountID: "account-1", want: "mail-off"},
		{
			name: "account opted out", accountID: "account-1", sender: sender,
			store: &visibilityPreferenceStore{preferences: accounts.NotificationPreferences{SupportReplies: false}},
			want:  "opted-out",
		},
		{
			name: "preference on without an outbox row", accountID: "account-1", sender: sender,
			store: &visibilityPreferenceStore{preferences: accounts.NotificationPreferences{SupportReplies: true}},
			want:  "not-queued",
		},
		{
			name: "preference lookup error", accountID: "account-1", sender: sender,
			store: &visibilityPreferenceStore{err: lookupErr}, want: "not-queued",
		},
		{name: "preference store missing", accountID: "account-1", sender: sender, store: plainStoreStub{}, want: "not-queued"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &api{config: Config{EmailSender: tc.sender, Accounts: tc.store}}
			ticket := adminstore.TicketDetail{
				AccountID: tc.accountID,
				Messages: []adminstore.TicketMessage{
					{ID: "message-user", AuthorRole: "user"},
					{ID: "message-staff", AuthorRole: "staff", EmailDeliveryStatus: tc.outboxStatus},
				},
			}
			a.enrichSupportDelivery(context.Background(), &ticket)
			if reason := ticket.Messages[0].EmailDeliveryReason; reason != "" {
				t.Fatalf("user message reason = %q, want empty", reason)
			}
			if reason := ticket.Messages[1].EmailDeliveryReason; reason != tc.want {
				t.Fatalf("staff message reason = %q, want %q", reason, tc.want)
			}
		})
	}
}

func TestSupportDeliveryReasonLookupRunsOncePerTicket(t *testing.T) {
	store := &visibilityPreferenceStore{err: errors.New("notification preferences are unavailable")}
	a := &api{config: Config{EmailSender: &recordingEmailSender{}, Accounts: store}}
	ticket := adminstore.TicketDetail{
		AccountID: "account-1",
		Messages: []adminstore.TicketMessage{
			{ID: "message-one", AuthorRole: "staff"},
			{ID: "message-two", AuthorRole: "staff"},
			{ID: "message-three", AuthorRole: "staff"},
		},
	}
	a.enrichSupportDelivery(context.Background(), &ticket)
	if store.calls != 1 {
		t.Fatalf("preference lookups = %d, want 1", store.calls)
	}
	for _, message := range ticket.Messages {
		if message.EmailDeliveryReason != "not-queued" {
			t.Fatalf("message %s reason = %q, want not-queued", message.ID, message.EmailDeliveryReason)
		}
	}
}
