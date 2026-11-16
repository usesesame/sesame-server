package notifications

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"usesesame.app/backend/internal/httpapi"
)

type pagingOutbox struct {
	stubOutbox
	limits []int
}

func (o *pagingOutbox) Poll(_ context.Context, limit int) ([]OutboxItem, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.limits = append(o.limits, limit)
	count := min(limit, len(o.pending))
	items := append([]OutboxItem(nil), o.pending[:count]...)
	o.pending = o.pending[count:]
	return items, nil
}

func (o *pagingOutbox) polls() []int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]int(nil), o.limits...)
}

type failingPollOutbox struct {
	stubOutbox
	polls int
}

func (o *failingPollOutbox) Poll(context.Context, int) ([]OutboxItem, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.polls++
	return nil, errors.New("database unavailable")
}

type batchRecorder struct {
	mu     sync.Mutex
	opened int
	closed int
	sends  map[int]int
	failTo string
}

func (r *batchRecorder) SendAccountEmail(context.Context, httpapi.AccountEmail) error {
	return errors.New("the worker must send through a batch")
}

func (r *batchRecorder) OpenBatch() EmailBatch {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.opened++
	if r.sends == nil {
		r.sends = map[int]int{}
	}
	return &recordedBatch{recorder: r, number: r.opened}
}

type recordedBatch struct {
	recorder *batchRecorder
	number   int
}

func (b *recordedBatch) SendAccountEmail(_ context.Context, message httpapi.AccountEmail) error {
	b.recorder.mu.Lock()
	defer b.recorder.mu.Unlock()
	if message.To == b.recorder.failTo {
		return errors.New("mailbox unavailable")
	}
	b.recorder.sends[b.number]++
	return nil
}

func (b *recordedBatch) Close() error {
	b.recorder.mu.Lock()
	defer b.recorder.mu.Unlock()
	b.recorder.closed++
	return nil
}

func pendingItems(count int) []OutboxItem {
	items := make([]OutboxItem, 0, count)
	for index := range count {
		items = append(items, OutboxItem{ID: fmt.Sprintf("message-%d", index), Kind: "security-sign-in", To: fmt.Sprintf("reader-%d@example.invalid", index), Subject: "Subject", Body: "Body"})
	}
	return items
}

func TestWorkerKeepsClaimingBatchesUntilTheQueueIsEmpty(t *testing.T) {
	outbox := &pagingOutbox{stubOutbox: stubOutbox{pending: pendingItems(120)}}
	sender := &stubSender{}
	NewWorker(outbox, sender, testActionURLSealer(t)).DeliverAllOnce(context.Background())

	if got := len(sender.sends); got != 120 {
		t.Fatalf("sender received %d messages, want 120", got)
	}
	if polls := outbox.polls(); len(polls) != 4 || polls[0] != 50 || polls[1] != 50 || polls[2] != 50 || polls[3] != 50 {
		t.Fatalf("polls = %v, want four claims of 50 ending with an empty one", polls)
	}
	if marks := outbox.markCalls(); len(marks) != 120 {
		t.Fatalf("marked %d messages, want 120", len(marks))
	}
}

func TestWorkerStopsDrainingWhenTheContextEnds(t *testing.T) {
	outbox := &pagingOutbox{stubOutbox: stubOutbox{pending: pendingItems(200)}}
	ctx, cancel := context.WithCancel(context.Background())
	sender := &cancellingSender{after: 60, cancel: cancel}
	NewWorker(outbox, sender, testActionURLSealer(t)).DeliverAllOnce(ctx)

	if sender.count() != 60 {
		t.Fatalf("sender received %d messages, want 60 before cancellation", sender.count())
	}
	if polls := outbox.polls(); len(polls) != 2 {
		t.Fatalf("polls = %v, want the drain to stop after cancellation", polls)
	}
}

func TestWorkerStopsDrainingWhenAPollFails(t *testing.T) {
	outbox := &failingPollOutbox{}
	NewWorker(outbox, &stubSender{}, testActionURLSealer(t)).DeliverAllOnce(context.Background())
	if outbox.polls != 1 {
		t.Fatalf("polls = %d, want one failed poll and no retry loop", outbox.polls)
	}
}

func TestWorkerSendsEachBatchThroughOneSession(t *testing.T) {
	items := pendingItems(70)
	items[3].To = "unreachable@example.invalid"
	outbox := &pagingOutbox{stubOutbox: stubOutbox{pending: items}}
	sender := &batchRecorder{failTo: "unreachable@example.invalid"}
	NewWorker(outbox, sender, testActionURLSealer(t)).DeliverAllOnce(context.Background())

	if sender.opened != 2 || sender.closed != 2 {
		t.Fatalf("opened %d and closed %d sessions, want 2 and 2", sender.opened, sender.closed)
	}
	if sender.sends[1] != 49 || sender.sends[2] != 20 {
		t.Fatalf("sends per session = %v, want 49 then 20", sender.sends)
	}
	failed := 0
	for _, mark := range outbox.markCalls() {
		if mark == "failed:message-3" {
			failed++
		}
	}
	if failed != 1 {
		t.Fatalf("marks = %v, want the unreachable message marked failed once", outbox.markCalls())
	}
}

type cancellingSender struct {
	mu     sync.Mutex
	sent   int
	after  int
	cancel context.CancelFunc
}

func (s *cancellingSender) SendAccountEmail(context.Context, httpapi.AccountEmail) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent++
	if s.sent == s.after {
		s.cancel()
	}
	return nil
}

func (s *cancellingSender) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sent
}

func TestWorkerDrainsAQueueLargerThanOneBatch(t *testing.T) {
	outbox, sealer, db := testOutbox(t)
	for range 120 {
		enqueueTestMessage(t, outbox, time.Now().UTC().Add(time.Hour))
	}
	sender := &recordingSender{}
	NewWorker(outbox, sender, sealer).DeliverAllOnce(context.Background())

	if sent := len(sender.sent()); sent != 120 {
		t.Fatalf("sender received %d messages, want 120", sent)
	}
	var waiting int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM sesame_email_outbox WHERE status <> 'delivered'`).Scan(&waiting); err != nil {
		t.Fatalf("count undelivered rows: %v", err)
	}
	if waiting != 0 {
		t.Fatalf("%d rows are not delivered after one drain", waiting)
	}
}

func TestPollClaimsActionMailBeforeSignInNotices(t *testing.T) {
	outbox, _, db := testOutbox(t)
	ctx := context.Background()
	for index := range 3 {
		if _, err := outbox.Enqueue(ctx, httpapi.AccountEmail{
			Kind: "security-sign-in", To: fmt.Sprintf("notice-%d@example.invalid", index),
			Subject: "New sign-in", Body: "A new sign-in happened.", ExpiresAt: time.Now().UTC().Add(time.Hour),
		}); err != nil {
			t.Fatalf("enqueue notice: %v", err)
		}
	}
	if _, err := db.ExecContext(ctx, `UPDATE sesame_email_outbox SET created_at = now() - interval '10 minutes', next_attempt_at = now() - interval '10 minutes'`); err != nil {
		t.Fatalf("age the notices: %v", err)
	}
	enqueueTestMessage(t, outbox, time.Now().UTC().Add(time.Hour))
	if _, err := outbox.Enqueue(ctx, httpapi.AccountEmail{
		Kind: "recover-password", To: outboxTestRecipient, ActionURL: "https://account.example.invalid/reset-password#token=fictional",
		ExpiresAt: time.Now().UTC().Add(30 * time.Minute),
	}); err != nil {
		t.Fatalf("enqueue recovery: %v", err)
	}

	items, err := outbox.Poll(ctx, 2)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	kinds := map[string]bool{}
	for _, item := range items {
		kinds[item.Kind] = true
	}
	if len(items) != 2 || !kinds["verify-email"] || !kinds["recover-password"] {
		t.Fatalf("first claim = %+v, want the verification and recovery mail before older notices", items)
	}
	rest, err := outbox.Poll(ctx, 10)
	if err != nil {
		t.Fatalf("second poll: %v", err)
	}
	if len(rest) != 3 {
		t.Fatalf("second claim returned %d notices, want 3", len(rest))
	}
}

func TestPollReturnsClaimedMessagesInPriorityOrder(t *testing.T) {
	outbox, _, db := testOutbox(t)
	ctx := context.Background()
	const notices = 60
	for index := range notices {
		if _, err := outbox.Enqueue(ctx, httpapi.AccountEmail{
			Kind: "security-sign-in", To: fmt.Sprintf("ordered-notice-%d@example.invalid", index),
			Subject: "New sign-in", Body: "A new sign-in happened.", ExpiresAt: time.Now().UTC().Add(time.Hour),
		}); err != nil {
			t.Fatalf("enqueue notice: %v", err)
		}
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE sesame_email_outbox AS message
		SET created_at = now() - make_interval(mins => ordered.position::int), next_attempt_at = now() - make_interval(mins => ordered.position::int)
		FROM (SELECT id, row_number() OVER (ORDER BY random()) AS position FROM sesame_email_outbox) AS ordered
		WHERE message.id = ordered.id`); err != nil {
		t.Fatalf("age the notices: %v", err)
	}
	if _, err := outbox.Enqueue(ctx, httpapi.AccountEmail{
		Kind: "recover-password", To: outboxTestRecipient, ActionURL: "https://account.example.invalid/reset-password#token=fictional",
		ExpiresAt: time.Now().UTC().Add(30 * time.Minute),
	}); err != nil {
		t.Fatalf("enqueue recovery: %v", err)
	}

	items, err := outbox.Poll(ctx, notices+1)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if len(items) != notices+1 {
		t.Fatalf("poll returned %d messages, want %d", len(items), notices+1)
	}
	if items[0].Kind != "recover-password" {
		t.Fatalf("poll returned %q first, want the recovery mail", items[0].Kind)
	}
	for index := 2; index < len(items); index++ {
		if items[index].CreatedAt.Before(items[index-1].CreatedAt) {
			t.Fatalf("notice %d was created at %s, before the notice ahead of it at %s", index, items[index].CreatedAt, items[index-1].CreatedAt)
		}
	}
}

func TestTwoDrainingWorkersDeliverEachMessageOnce(t *testing.T) {
	outbox, sealer, _ := testOutbox(t)
	for range 150 {
		enqueueTestMessage(t, outbox, time.Now().UTC().Add(time.Hour))
	}
	first, second := &recordingSender{}, &recordingSender{}
	var wait sync.WaitGroup
	for _, sender := range []*recordingSender{first, second} {
		wait.Add(1)
		go func() {
			defer wait.Done()
			NewWorker(outbox, sender, sealer).DeliverAllOnce(context.Background())
		}()
	}
	wait.Wait()

	if total := len(first.sent()) + len(second.sent()); total != 150 {
		t.Fatalf("two workers sent %d messages, want each of the 150 exactly once", total)
	}
}
