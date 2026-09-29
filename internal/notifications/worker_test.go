package notifications

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"usesesame.app/backend/internal/httpapi"
)

type recordingSender struct {
	mu       sync.Mutex
	messages []httpapi.AccountEmail
	err      error
}

func (s *recordingSender) SendAccountEmail(ctx context.Context, message httpapi.AccountEmail) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, message)
	return s.err
}

func (s *recordingSender) sent() []httpapi.AccountEmail {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]httpapi.AccountEmail(nil), s.messages...)
}

func TestWorkerDeliversClaimedMessages(t *testing.T) {
	outbox, sealer, db := testOutbox(t)
	id := enqueueTestMessage(t, outbox, time.Now().UTC().Add(time.Hour))
	sender := &recordingSender{}
	NewWorker(outbox, sender, sealer).DeliverAllOnce(context.Background())

	sent := sender.sent()
	if len(sent) != 1 {
		t.Fatalf("sender received %d messages, want 1", len(sent))
	}
	message := sent[0]
	if message.Kind != "verify-email" || message.To != outboxTestRecipient || message.Subject != "Verify your Sesame account email" {
		t.Fatalf("delivered message = %+v", message)
	}
	if message.ActionURL != "https://account.example.invalid/verify?token=fictional" || message.Body != "Open this link to verify your Sesame account." {
		t.Fatalf("delivered message = %+v", message)
	}
	state := readOutboxRow(t, db, id)
	if state.status != "delivered" || state.attempts != 1 || state.errorMessage.Valid || state.leaseUntil.Valid {
		t.Fatalf("delivered row = %+v", state)
	}
}

func TestWorkerReturnsAFailedDeliveryToRetry(t *testing.T) {
	outbox, sealer, db := testOutbox(t)
	id := enqueueTestMessage(t, outbox, time.Now().UTC().Add(time.Hour))
	sender := &recordingSender{err: errors.New("smtp unavailable")}
	NewWorker(outbox, sender, sealer).DeliverAllOnce(context.Background())

	if len(sender.sent()) != 1 {
		t.Fatalf("sender received %d messages, want 1", len(sender.sent()))
	}
	state := readOutboxRow(t, db, id)
	if state.status != "pending" || state.attempts != 1 || state.errorMessage.String != "smtp unavailable" {
		t.Fatalf("failed row = %+v, want pending with the sender error", state)
	}
	if !state.nextAttemptAt.After(time.Now().UTC()) {
		t.Fatalf("failed row is immediately due: %+v", state)
	}
}

func TestWorkerWithoutASenderFailsDelivery(t *testing.T) {
	outbox, sealer, db := testOutbox(t)
	id := enqueueTestMessage(t, outbox, time.Now().UTC().Add(time.Hour))
	NewWorker(outbox, nil, sealer).DeliverAllOnce(context.Background())

	state := readOutboxRow(t, db, id)
	if state.status != "pending" || state.attempts != 1 || state.errorMessage.String != "no email sender configured" {
		t.Fatalf("row without a sender = %+v", state)
	}
}

func TestWorkerFailsClosedWithAWrongKey(t *testing.T) {
	outbox, _, db := testOutbox(t)
	id := enqueueTestMessage(t, outbox, time.Now().UTC().Add(time.Hour))
	sender := &recordingSender{}
	NewWorker(outbox, sender, otherActionURLSealer(t)).DeliverAllOnce(context.Background())

	if sent := sender.sent(); len(sent) != 0 {
		t.Fatalf("sender received %d messages with a wrong key, want none: %+v", len(sent), sent)
	}
	state := readOutboxRow(t, db, id)
	if state.status != "pending" || state.attempts != 1 || !strings.Contains(state.errorMessage.String, "action URL") {
		t.Fatalf("row after a wrong key = %+v, want pending with an action URL error", state)
	}
}

func TestWorkerFailsClosedWithoutActionURLEncryption(t *testing.T) {
	outbox, _, db := testOutbox(t)
	id := enqueueTestMessage(t, outbox, time.Now().UTC().Add(time.Hour))
	sender := &recordingSender{}
	NewWorker(outbox, sender, nil).DeliverAllOnce(context.Background())

	if sent := sender.sent(); len(sent) != 0 {
		t.Fatalf("sender received %d messages without a key, want none: %+v", len(sent), sent)
	}
	state := readOutboxRow(t, db, id)
	if state.status != "pending" || state.attempts != 1 || state.errorMessage.String != "no action URL encryption configured" {
		t.Fatalf("row without action URL encryption = %+v", state)
	}
}
