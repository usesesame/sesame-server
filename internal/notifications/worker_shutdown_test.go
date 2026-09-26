package notifications

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"usesesame.app/backend/internal/httpapi"
)

type stubOutbox struct {
	mu        sync.Mutex
	pending   []OutboxItem
	marks     []string
	delivered chan string
	failed    chan string
}

func (o *stubOutbox) Ping(context.Context) error { return nil }

func (o *stubOutbox) Enqueue(context.Context, httpapi.AccountEmail) (string, error) { return "", nil }

func (o *stubOutbox) Poll(context.Context, int) ([]OutboxItem, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	items := o.pending
	o.pending = nil
	return items, nil
}

func (o *stubOutbox) MarkDelivered(_ context.Context, id string) error {
	o.mu.Lock()
	o.marks = append(o.marks, "delivered:"+id)
	o.mu.Unlock()
	if o.delivered != nil {
		o.delivered <- id
	}
	return nil
}

func (o *stubOutbox) MarkFailed(_ context.Context, id string, _ error) error {
	o.mu.Lock()
	o.marks = append(o.marks, "failed:"+id)
	o.mu.Unlock()
	if o.failed != nil {
		o.failed <- id
	}
	return nil
}

func (o *stubOutbox) PurgeDeliveredOlderThan(context.Context, time.Duration) (int64, error) {
	return 0, nil
}

func (o *stubOutbox) PurgeFailedOlderThan(context.Context, time.Duration) (int64, error) {
	return 0, nil
}

func (o *stubOutbox) markCalls() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.marks...)
}

type blockingPollOutbox struct {
	stubOutbox
}

func (o *blockingPollOutbox) Poll(ctx context.Context, limit int) ([]OutboxItem, error) {
	<-ctx.Done()
	return o.stubOutbox.Poll(ctx, limit)
}

type blockingSender struct {
	started chan struct{}
}

func (s *blockingSender) SendAccountEmail(ctx context.Context, _ httpapi.AccountEmail) error {
	if s.started != nil {
		s.started <- struct{}{}
	}
	<-ctx.Done()
	return ctx.Err()
}

type stubSender struct {
	mu    sync.Mutex
	sends []httpapi.AccountEmail
	err   error
}

func (s *stubSender) SendAccountEmail(_ context.Context, message httpapi.AccountEmail) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sends = append(s.sends, message)
	return s.err
}

func runWorker(t *testing.T, worker *Worker, ctx context.Context) <-chan struct{} {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		worker.Run(ctx)
	}()
	return done
}

func waitForRun(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker.Run did not return after cancellation")
	}
}

func TestWorkerRunStopsWithoutWritesAfterCancellation(t *testing.T) {
	outbox := &blockingPollOutbox{stubOutbox: stubOutbox{pending: []OutboxItem{{ID: "message-1", Kind: "verify-email", To: "user@example.invalid"}}}}
	worker := NewWorker(outbox, &blockingSender{})
	ctx, cancel := context.WithCancel(context.Background())
	done := runWorker(t, worker, ctx)

	cancel()
	waitForRun(t, done)

	if marks := outbox.markCalls(); len(marks) != 0 {
		t.Fatalf("worker wrote after cancellation: %v", marks)
	}
}

func TestWorkerRunSkipsMarksWhenDeliveryIsInterrupted(t *testing.T) {
	outbox := &stubOutbox{pending: []OutboxItem{{ID: "message-1", Kind: "verify-email", To: "user@example.invalid"}}}
	sender := &blockingSender{started: make(chan struct{}, 1)}
	worker := NewWorker(outbox, sender)
	ctx, cancel := context.WithCancel(context.Background())
	done := runWorker(t, worker, ctx)

	select {
	case <-sender.started:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not attempt delivery")
	}
	cancel()
	waitForRun(t, done)

	if marks := outbox.markCalls(); len(marks) != 0 {
		t.Fatalf("worker wrote after cancellation: %v", marks)
	}
}

func TestWorkerRunDeliversAndMarksMessages(t *testing.T) {
	delivered := make(chan string, 1)
	outbox := &stubOutbox{
		pending:   []OutboxItem{{ID: "message-1", Kind: "verify-email", To: "user@example.invalid"}},
		delivered: delivered,
	}
	sender := &stubSender{}
	worker := NewWorker(outbox, sender)
	ctx, cancel := context.WithCancel(context.Background())
	done := runWorker(t, worker, ctx)

	select {
	case id := <-delivered:
		if id != "message-1" {
			t.Fatalf("delivered id = %q", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not mark the message delivered")
	}
	cancel()
	waitForRun(t, done)

	if marks := outbox.markCalls(); len(marks) != 1 || marks[0] != "delivered:message-1" {
		t.Fatalf("marks = %v", marks)
	}
	if len(sender.sends) != 1 || sender.sends[0].To != "user@example.invalid" {
		t.Fatalf("sends = %#v", sender.sends)
	}
}

func TestWorkerRunMarksFailedMessagesBeforeCancellation(t *testing.T) {
	failed := make(chan string, 1)
	outbox := &stubOutbox{
		pending: []OutboxItem{{ID: "message-1", Kind: "verify-email", To: "user@example.invalid"}},
		failed:  failed,
	}
	sender := &stubSender{err: errors.New("smtp unavailable")}
	worker := NewWorker(outbox, sender)
	ctx, cancel := context.WithCancel(context.Background())
	done := runWorker(t, worker, ctx)

	select {
	case id := <-failed:
		if id != "message-1" {
			t.Fatalf("failed id = %q", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not mark the message failed")
	}
	cancel()
	waitForRun(t, done)

	if marks := outbox.markCalls(); len(marks) != 1 || marks[0] != "failed:message-1" {
		t.Fatalf("marks = %v", marks)
	}
}
