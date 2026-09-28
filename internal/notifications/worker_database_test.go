package notifications

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"usesesame.app/backend/internal/accounts"
	"usesesame.app/backend/internal/httpapi"
)

type blockingDeliverySender struct {
	started chan struct{}
}

func (s *blockingDeliverySender) SendAccountEmail(ctx context.Context, _ httpapi.AccountEmail) error {
	s.started <- struct{}{}
	<-ctx.Done()
	return ctx.Err()
}

func TestWorkerRunStopsBeforeThePoolCloses(t *testing.T) {
	databaseURL := os.Getenv("SESAME_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("SESAME_TEST_DATABASE_URL is required")
	}
	var logs strings.Builder
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	store, err := accounts.Open(context.Background(), databaseURL)
	if err != nil {
		t.Fatalf("open account store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	db := store.DB()
	lockDatabaseTest(t, db)
	if _, err := db.ExecContext(context.Background(), `TRUNCATE sesame_email_outbox`); err != nil {
		t.Fatalf("clear outbox: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO sesame_email_outbox (kind, to_email, action_url, expires_at, subject, body)
		VALUES ('verify-email', 'worker@example.invalid', 'https://account.test.invalid/verify',
		        now() + interval '1 hour', 'Verify your email', 'Body')`); err != nil {
		t.Fatalf("queue outbox message: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sender := &blockingDeliverySender{started: make(chan struct{}, 1)}
	worker := NewWorker(NewPostgresOutbox(db), sender)
	done := runWorker(t, worker, ctx)

	select {
	case <-sender.started:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not deliver the queued message")
	}
	cancel()
	waitForRun(t, done)

	var status string
	if err := db.QueryRowContext(context.Background(), `SELECT status FROM sesame_email_outbox`).Scan(&status); err != nil {
		t.Fatalf("read outbox row: %v", err)
	}
	if status != "processing" {
		t.Fatalf("outbox status = %q after cancellation, want processing for lease-expiry retry", status)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	if strings.Contains(logs.String(), "database is closed") {
		t.Fatalf("worker used the pool after it closed: %s", logs.String())
	}
}
