package notifications

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"usesesame.app/backend/internal/accounts"
	"usesesame.app/backend/internal/httpapi"
)

const outboxTestRecipient = "outbox-test@example.invalid"

type outboxRowState struct {
	status        string
	attempts      int
	errorMessage  sql.NullString
	nextAttemptAt time.Time
	leaseUntil    sql.NullTime
}

func testOutbox(t *testing.T) (*PostgresOutbox, *sql.DB) {
	t.Helper()
	databaseURL := os.Getenv("SESAME_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("SESAME_TEST_DATABASE_URL is required")
	}
	ctx := context.Background()
	accountStore, err := accounts.Open(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open account store: %v", err)
	}
	t.Cleanup(func() { _ = accountStore.Close() })
	db := accountStore.DB()
	lockDatabaseTest(t, db)
	if _, err := db.ExecContext(ctx, `TRUNCATE sesame_email_outbox`); err != nil {
		t.Fatalf("clear email outbox: %v", err)
	}
	return NewPostgresOutbox(db), db
}

func lockDatabaseTest(t *testing.T, db *sql.DB) {
	t.Helper()
	const lockID int64 = 762374923
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("reserve test database connection: %v", err)
	}
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, lockID); err != nil {
		_ = conn.Close()
		t.Fatalf("lock test database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, lockID)
		_ = conn.Close()
	})
}

func enqueueTestMessage(t *testing.T, outbox *PostgresOutbox, expiresAt time.Time) string {
	t.Helper()
	id, err := outbox.Enqueue(context.Background(), httpapi.AccountEmail{
		Kind:      "verify-email",
		To:        outboxTestRecipient,
		ActionURL: "https://account.example.invalid/verify?token=fictional",
		ExpiresAt: expiresAt,
		Subject:   "Verify your Sesame account email",
		Body:      "Open this link to verify your Sesame account.",
	})
	if err != nil {
		t.Fatalf("enqueue message: %v", err)
	}
	return id
}

func readOutboxRow(t *testing.T, db *sql.DB, id string) outboxRowState {
	t.Helper()
	var state outboxRowState
	if err := db.QueryRowContext(context.Background(), `
		SELECT status, attempts, error_message, next_attempt_at, lease_until
		FROM sesame_email_outbox WHERE id = $1`, id).
		Scan(&state.status, &state.attempts, &state.errorMessage, &state.nextAttemptAt, &state.leaseUntil); err != nil {
		t.Fatalf("read outbox row %s: %v", id, err)
	}
	return state
}

func TestPollExpiresMessagesPastTheirDeadline(t *testing.T) {
	outbox, db := testOutbox(t)
	ctx := context.Background()
	id := enqueueTestMessage(t, outbox, time.Now().UTC().Add(-time.Minute))

	items, err := outbox.Poll(ctx, 10)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("poll claimed %d expired messages, want none: %+v", len(items), items)
	}
	state := readOutboxRow(t, db, id)
	if state.status != "failed" || !state.errorMessage.Valid || state.errorMessage.String != "message_expired" {
		t.Fatalf("expired row = %+v, want failed with message_expired", state)
	}
	if state.leaseUntil.Valid {
		t.Fatalf("expired row kept a lease: %+v", state)
	}
	items, err = outbox.Poll(ctx, 10)
	if err != nil || len(items) != 0 {
		t.Fatalf("second poll = %+v, %v; want no messages", items, err)
	}
}

func TestPollExpiresAClaimedMessagePastItsDeadline(t *testing.T) {
	outbox, db := testOutbox(t)
	ctx := context.Background()
	id := enqueueTestMessage(t, outbox, time.Now().UTC().Add(time.Hour))
	if _, err := db.ExecContext(ctx, `UPDATE sesame_email_outbox SET status = 'processing', lease_until = now() + interval '5 minutes' WHERE id = $1`, id); err != nil {
		t.Fatalf("claim message: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE sesame_email_outbox SET expires_at = now() - interval '1 second' WHERE id = $1`, id); err != nil {
		t.Fatalf("expire message: %v", err)
	}
	items, err := outbox.Poll(ctx, 10)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("poll claimed an expired processing message: %+v", items)
	}
	state := readOutboxRow(t, db, id)
	if state.status != "failed" || !state.errorMessage.Valid || state.errorMessage.String != "message_expired" || state.leaseUntil.Valid {
		t.Fatalf("expired processing row = %+v, want failed without a lease", state)
	}
}

func TestPollReturnsOnlyDueMessagesAndMarksDelivery(t *testing.T) {
	outbox, db := testOutbox(t)
	ctx := context.Background()
	due := enqueueTestMessage(t, outbox, time.Now().UTC().Add(time.Hour))
	notYetDue := enqueueTestMessage(t, outbox, time.Now().UTC().Add(time.Hour))
	if _, err := db.ExecContext(ctx, `UPDATE sesame_email_outbox SET next_attempt_at = now() + interval '1 hour' WHERE id = $1`, notYetDue); err != nil {
		t.Fatalf("delay message: %v", err)
	}
	expired := enqueueTestMessage(t, outbox, time.Now().UTC().Add(-time.Minute))

	items, err := outbox.Poll(ctx, 10)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if len(items) != 1 || items[0].ID != due {
		t.Fatalf("poll = %+v, want only %s", items, due)
	}
	item := items[0]
	if item.Kind != "verify-email" || item.To != outboxTestRecipient || item.Subject != "Verify your Sesame account email" || item.Body != "Open this link to verify your Sesame account." {
		t.Fatalf("claimed item = %+v", item)
	}
	if item.ActionURL != "https://account.example.invalid/verify?token=fictional" || item.Attempts != 0 {
		t.Fatalf("claimed item = %+v", item)
	}
	if time.Until(item.ExpiresAt) < 50*time.Minute {
		t.Fatalf("claimed expiresAt = %s, want the enqueued deadline", item.ExpiresAt)
	}
	claimed := readOutboxRow(t, db, due)
	if claimed.status != "processing" || !claimed.leaseUntil.Valid || claimed.leaseUntil.Time.Before(time.Now().UTC()) {
		t.Fatalf("claimed row = %+v, want processing with a future lease", claimed)
	}

	if err := outbox.MarkDelivered(ctx, due); err != nil {
		t.Fatalf("mark delivered: %v", err)
	}
	delivered := readOutboxRow(t, db, due)
	if delivered.status != "delivered" || delivered.attempts != 1 || delivered.errorMessage.Valid || delivered.leaseUntil.Valid {
		t.Fatalf("delivered row = %+v", delivered)
	}
	if err := outbox.MarkDelivered(ctx, due); err == nil {
		t.Fatal("a delivered message was marked delivered twice")
	}
	if err := outbox.MarkFailed(ctx, due, errors.New("late failure")); err == nil {
		t.Fatal("a delivered message was marked failed")
	}

	items, err = outbox.Poll(ctx, 10)
	if err != nil {
		t.Fatalf("second poll: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("second poll = %+v, want no messages", items)
	}
	notDue := readOutboxRow(t, db, notYetDue)
	if notDue.status != "pending" {
		t.Fatalf("delayed row = %+v, want pending", notDue)
	}
	failed := readOutboxRow(t, db, expired)
	if failed.status != "failed" {
		t.Fatalf("expired row = %+v, want failed", failed)
	}
}

func TestMarkFailedBacksOffRetriesAndCapsTheInterval(t *testing.T) {
	outbox, db := testOutbox(t)
	outbox.maxAttempts = 100
	outbox.maxRetryInterval = 90 * time.Second
	ctx := context.Background()
	id := enqueueTestMessage(t, outbox, time.Now().UTC().Add(time.Hour))

	failOnce := func(message string) outboxRowState {
		t.Helper()
		items, err := outbox.Poll(ctx, 10)
		if err != nil {
			t.Fatalf("poll: %v", err)
		}
		if len(items) != 1 || items[0].ID != id {
			t.Fatalf("poll = %+v, want message %s", items, id)
		}
		if err := outbox.MarkFailed(ctx, id, errors.New(message)); err != nil {
			t.Fatalf("mark failed: %v", err)
		}
		return readOutboxRow(t, db, id)
	}

	first := failOnce("smtp refused the message")
	if first.status != "pending" || first.attempts != 1 || first.errorMessage.String != "smtp refused the message" {
		t.Fatalf("row after first failure = %+v", first)
	}
	if delay := time.Until(first.nextAttemptAt); delay < 50*time.Second || delay > 70*time.Second {
		t.Fatalf("first retry delay = %s, want about one minute", delay)
	}
	if items, err := outbox.Poll(ctx, 10); err != nil || len(items) != 0 {
		t.Fatalf("poll during backoff = %+v, %v; want no messages", items, err)
	}

	if _, err := db.ExecContext(ctx, `UPDATE sesame_email_outbox SET next_attempt_at = now() - interval '1 second' WHERE id = $1`, id); err != nil {
		t.Fatalf("make message due: %v", err)
	}
	second := failOnce("smtp refused again")
	if second.status != "pending" || second.attempts != 2 {
		t.Fatalf("row after second failure = %+v", second)
	}
	if delay := time.Until(second.nextAttemptAt); delay < 80*time.Second || delay > 100*time.Second {
		t.Fatalf("second retry delay = %s, want the 90s cap", delay)
	}

	if _, err := db.ExecContext(ctx, `UPDATE sesame_email_outbox SET attempts = 20, next_attempt_at = now() - interval '1 second' WHERE id = $1`, id); err != nil {
		t.Fatalf("age message: %v", err)
	}
	longError := strings.Repeat("x", 600)
	capped := failOnce(longError)
	if capped.status != "pending" || capped.attempts != 21 {
		t.Fatalf("row after a late failure = %+v", capped)
	}
	if delay := time.Until(capped.nextAttemptAt); delay < 80*time.Second || delay > 100*time.Second {
		t.Fatalf("late retry delay = %s, want the 90s cap", delay)
	}
	if len(capped.errorMessage.String) != 512 {
		t.Fatalf("stored error message length = %d, want 512", len(capped.errorMessage.String))
	}
}

func TestMarkFailedStopsAtTheAttemptCutoff(t *testing.T) {
	outbox, db := testOutbox(t)
	outbox.maxAttempts = 3
	ctx := context.Background()
	id := enqueueTestMessage(t, outbox, time.Now().UTC().Add(time.Hour))

	for attempt := 1; attempt <= 3; attempt++ {
		if attempt > 1 {
			if _, err := db.ExecContext(ctx, `UPDATE sesame_email_outbox SET next_attempt_at = now() - interval '1 second' WHERE id = $1`, id); err != nil {
				t.Fatalf("make message due: %v", err)
			}
		}
		items, err := outbox.Poll(ctx, 10)
		if err != nil {
			t.Fatalf("poll attempt %d: %v", attempt, err)
		}
		if len(items) != 1 || items[0].ID != id || items[0].Attempts != attempt-1 {
			t.Fatalf("poll attempt %d = %+v, want message %s with %d attempts", attempt, items, id, attempt-1)
		}
		if err := outbox.MarkFailed(ctx, id, errors.New("smtp refused")); err != nil {
			t.Fatalf("mark failed attempt %d: %v", attempt, err)
		}
		state := readOutboxRow(t, db, id)
		wantStatus := "pending"
		if attempt == 3 {
			wantStatus = "failed"
		}
		if state.status != wantStatus || state.attempts != attempt {
			t.Fatalf("row after attempt %d = %+v, want %s with %d attempts", attempt, state, wantStatus, attempt)
		}
	}
	if items, err := outbox.Poll(ctx, 10); err != nil || len(items) != 0 {
		t.Fatalf("poll after cutoff = %+v, %v; want no messages", items, err)
	}
	if err := outbox.MarkFailed(ctx, id, errors.New("after the cutoff")); err == nil {
		t.Fatal("a terminally failed message was marked failed again")
	}
}

func TestPollReclaimsAMessageAfterItsLeaseExpires(t *testing.T) {
	outbox, db := testOutbox(t)
	ctx := context.Background()
	id := enqueueTestMessage(t, outbox, time.Now().UTC().Add(time.Hour))

	items, err := outbox.Poll(ctx, 10)
	if err != nil || len(items) != 1 || items[0].ID != id {
		t.Fatalf("first poll = %+v, %v", items, err)
	}
	claimed := readOutboxRow(t, db, id)
	if !claimed.leaseUntil.Valid || claimed.leaseUntil.Time.Before(time.Now().UTC()) {
		t.Fatalf("claimed row = %+v, want a future lease", claimed)
	}
	if items, err := outbox.Poll(ctx, 10); err != nil || len(items) != 0 {
		t.Fatalf("poll inside the lease = %+v, %v; want no messages", items, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE sesame_email_outbox SET lease_until = now() - interval '1 second' WHERE id = $1`, id); err != nil {
		t.Fatalf("expire the lease: %v", err)
	}
	items, err = outbox.Poll(ctx, 10)
	if err != nil || len(items) != 1 || items[0].ID != id {
		t.Fatalf("poll after lease expiry = %+v, %v", items, err)
	}
	if items[0].Attempts != 0 {
		t.Fatalf("reclaimed item attempts = %d, want the original count", items[0].Attempts)
	}
}

func TestConcurrentPollsClaimEachMessageOnce(t *testing.T) {
	outbox, db := testOutbox(t)
	ctx := context.Background()
	const messages = 20
	expected := map[string]bool{}
	for index := 0; index < messages; index++ {
		expected[enqueueTestMessage(t, outbox, time.Now().UTC().Add(time.Hour))] = true
	}

	const workers = 5
	start := make(chan struct{})
	items := make(chan []OutboxItem, workers)
	errs := make(chan error, workers)
	var group sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			claimed, err := outbox.Poll(context.Background(), messages/workers+2)
			if err != nil {
				errs <- err
				return
			}
			items <- claimed
		}()
	}
	close(start)
	group.Wait()
	close(items)
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent poll: %v", err)
	}
	claimedOnce := map[string]int{}
	for batch := range items {
		for _, item := range batch {
			if !expected[item.ID] {
				t.Fatalf("a worker claimed an unknown message: %+v", item)
			}
			claimedOnce[item.ID]++
		}
	}
	if len(claimedOnce) != messages {
		t.Fatalf("claimed %d messages, want %d", len(claimedOnce), messages)
	}
	for id, count := range claimedOnce {
		if count != 1 {
			t.Fatalf("message %s was claimed %d times", id, count)
		}
	}
	var processing int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sesame_email_outbox WHERE status = 'processing'`).Scan(&processing); err != nil {
		t.Fatalf("count processing: %v", err)
	}
	if processing != messages {
		t.Fatalf("processing rows = %d, want %d", processing, messages)
	}
}

func TestPingAndOperationalSummaryReportOutboxState(t *testing.T) {
	outbox, db := testOutbox(t)
	ctx := context.Background()
	if err := outbox.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	id := enqueueTestMessage(t, outbox, time.Now().UTC().Add(time.Hour))
	summary, err := outbox.OperationalSummary(ctx)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.Pending != 1 || summary.Failed != 0 || summary.Status != httpapi.OperationalReady {
		t.Fatalf("summary = %+v, want one pending message and ready", summary)
	}
	if _, err := db.ExecContext(ctx, `UPDATE sesame_email_outbox SET status = 'failed', error_message = 'smtp refused' WHERE id = $1`, id); err != nil {
		t.Fatalf("fail the message: %v", err)
	}
	summary, err = outbox.OperationalSummary(ctx)
	if err != nil {
		t.Fatalf("summary after failure: %v", err)
	}
	if summary.Pending != 0 || summary.Failed != 1 || summary.Status != httpapi.OperationalDegraded {
		t.Fatalf("summary after failure = %+v, want one failed message and degraded", summary)
	}
}

func TestOutboxEmailSenderQueuesTheMessage(t *testing.T) {
	outbox, db := testOutbox(t)
	sender := NewOutboxEmailSender(outbox)
	if err := sender.SendAccountEmail(context.Background(), httpapi.AccountEmail{
		Kind:      "recover-password",
		To:        outboxTestRecipient,
		ActionURL: "https://account.example.invalid/recover?token=fictional",
		ExpiresAt: time.Now().UTC().Add(time.Hour),
		Subject:   "Reset your Sesame account password",
		Body:      "Open this link to choose a new password.",
	}); err != nil {
		t.Fatalf("send account email: %v", err)
	}
	var kind, to string
	if err := db.QueryRowContext(context.Background(), `SELECT kind, to_email FROM sesame_email_outbox`).Scan(&kind, &to); err != nil {
		t.Fatalf("read queued message: %v", err)
	}
	if kind != "recover-password" || to != outboxTestRecipient {
		t.Fatalf("queued message = %s to %s", kind, to)
	}
}

func TestPurgeRemovesOldDeliveredAndFailedMessages(t *testing.T) {
	outbox, db := testOutbox(t)
	ctx := context.Background()
	delivered := enqueueTestMessage(t, outbox, time.Now().UTC().Add(time.Hour))
	failed := enqueueTestMessage(t, outbox, time.Now().UTC().Add(time.Hour))
	recent := enqueueTestMessage(t, outbox, time.Now().UTC().Add(time.Hour))
	if _, err := db.ExecContext(ctx, `
		UPDATE sesame_email_outbox
		SET status = 'delivered', updated_at = now() - interval '2 hours' WHERE id = $1`, delivered); err != nil {
		t.Fatalf("age delivered message: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE sesame_email_outbox
		SET status = 'failed', error_message = 'smtp refused', updated_at = now() - interval '2 hours' WHERE id = $1`, failed); err != nil {
		t.Fatalf("age failed message: %v", err)
	}
	purged, err := outbox.PurgeDeliveredOlderThan(ctx, time.Hour)
	if err != nil || purged != 1 {
		t.Fatalf("purge delivered = %d, %v", purged, err)
	}
	purged, err = outbox.PurgeFailedOlderThan(ctx, time.Hour)
	if err != nil || purged != 1 {
		t.Fatalf("purge failed = %d, %v", purged, err)
	}
	var remaining int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sesame_email_outbox`).Scan(&remaining); err != nil {
		t.Fatalf("count remaining: %v", err)
	}
	if remaining != 1 {
		t.Fatalf("remaining messages = %d, want the recent pending message", remaining)
	}
	if state := readOutboxRow(t, db, recent); state.status != "pending" {
		t.Fatalf("recent row = %+v, want pending", state)
	}
}
