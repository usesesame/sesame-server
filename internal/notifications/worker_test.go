package notifications

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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

func TestWorkerSendsNonSecretNotificationsQueuedBeforeTheActionURLMigration(t *testing.T) {
	outbox, sealer, db := testOutbox(t)
	ctx := context.Background()
	seed := func(kind, actionURL string) string {
		t.Helper()
		var id string
		if err := db.QueryRowContext(ctx, `
			INSERT INTO sesame_email_outbox (kind, to_email, action_url, expires_at, subject, body)
			VALUES ($1, 'upgrade@example.invalid', $2, NOW() + INTERVAL '1 hour', 'Subject', 'Body')
			RETURNING id`, kind, actionURL).Scan(&id); err != nil {
			t.Fatalf("seed outbox row: %v", err)
		}
		return id
	}
	supportID := seed("support-reply", "https://account.example.invalid/support")
	recoveryID := seed("recover-password", "https://account.example.invalid/reset-password#token=fictional-legacy")

	statement, err := os.ReadFile(filepath.Join("..", "accounts", "migrations", "0042_email_outbox_action_url_encryption.sql"))
	if err != nil {
		t.Fatalf("read the action URL encryption migration: %v", err)
	}
	if _, err := db.ExecContext(ctx, string(statement)); err != nil {
		t.Fatalf("apply the action URL encryption migration: %v", err)
	}

	sender := &recordingSender{}
	NewWorker(outbox, sender, sealer).DeliverAllOnce(ctx)

	sent := sender.sent()
	if len(sent) != 1 || sent[0].Kind != "support-reply" || sent[0].ActionURL != "" {
		t.Fatalf("sender received %+v, want only the support reply without an action link", sent)
	}
	if state := readOutboxRow(t, db, supportID); state.status != "delivered" {
		t.Fatalf("support reply row after delivery = %+v, want delivered", state)
	}
	recovery := readOutboxRow(t, db, recoveryID)
	if recovery.status != "failed" || recovery.errorMessage.String != "action_url_encryption_upgrade" {
		t.Fatalf("recovery row after migration = %+v, want failed with action_url_encryption_upgrade", recovery)
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
