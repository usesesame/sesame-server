package notifications

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"usesesame.app/backend/internal/httpapi"
)

type Worker struct {
	outbox       Outbox
	sender       httpapi.EmailSender
	sealer       *ActionURLSealer
	pollInterval time.Duration
	batchSize    int
}

func NewWorker(outbox Outbox, sender httpapi.EmailSender, sealer *ActionURLSealer) *Worker {
	return &Worker{
		outbox:       outbox,
		sender:       sender,
		sealer:       sealer,
		pollInterval: 30 * time.Second,
		batchSize:    50,
	}
}

type EmailBatch interface {
	httpapi.EmailSender
	Close() error
}

type batchOpener interface {
	OpenBatch() EmailBatch
}

func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	w.pollOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.pollOnce(ctx)
		}
	}
}

func (w *Worker) pollOnce(ctx context.Context) {
	for ctx.Err() == nil {
		if !w.deliverBatch(ctx) {
			return
		}
	}
}

func (w *Worker) deliverBatch(ctx context.Context) bool {
	items, err := w.outbox.Poll(ctx, w.batchSize)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("email outbox poll failed", "error", err)
		}
		return false
	}
	if len(items) == 0 {
		return false
	}
	sender := w.sender
	if opener, ok := sender.(batchOpener); ok {
		batch := opener.OpenBatch()
		defer func() {
			if closeErr := batch.Close(); closeErr != nil && ctx.Err() == nil {
				slog.Warn("email outbox connection close failed", "error", closeErr)
			}
		}()
		sender = batch
	}
	for _, item := range items {
		if ctx.Err() != nil {
			return false
		}
		if err := w.deliver(ctx, sender, item); err != nil {
			if ctx.Err() != nil {
				return false
			}
			slog.Warn("email outbox delivery failed", "id", item.ID, "kind", item.Kind, "error", err)
			if markErr := w.outbox.MarkFailed(ctx, item.ID, err); markErr != nil {
				slog.Warn("email outbox mark-failed failed", "id", item.ID, "error", markErr)
			}
			continue
		}
		if ctx.Err() != nil {
			return false
		}
		if markErr := w.outbox.MarkDelivered(ctx, item.ID); markErr != nil {
			slog.Warn("email outbox mark-delivered failed", "id", item.ID, "error", markErr)
		}
	}
	return true
}

func (w *Worker) deliver(ctx context.Context, sender httpapi.EmailSender, item OutboxItem) error {
	if sender == nil {
		return errors.New("no email sender configured")
	}
	if w.sealer == nil {
		return errors.New("no action URL encryption configured")
	}
	actionURL, err := w.sealer.Open(item.Kind, item.To, item.SealedActionURL)
	if err != nil {
		return fmt.Errorf("open action URL: %w", err)
	}
	message := httpapi.AccountEmail{
		Kind:      item.Kind,
		To:        item.To,
		ActionURL: actionURL,
		ExpiresAt: item.ExpiresAt,
		Subject:   item.Subject,
		Body:      item.Body,
	}
	return sender.SendAccountEmail(ctx, message)
}

// Synchronous helper for tests and graceful shutdown.
func (w *Worker) DeliverAllOnce(ctx context.Context) {
	w.pollOnce(ctx)
}
