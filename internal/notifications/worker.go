package notifications

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"usesesame.app/backend/internal/httpapi"
)

type Worker struct {
	outbox       Outbox
	sender       httpapi.EmailSender
	pollInterval time.Duration
	batchSize    int
}

func NewWorker(outbox Outbox, sender httpapi.EmailSender) *Worker {
	return &Worker{
		outbox:       outbox,
		sender:       sender,
		pollInterval: 30 * time.Second,
		batchSize:    10,
	}
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
	items, err := w.outbox.Poll(ctx, w.batchSize)
	if err != nil {
		slog.Warn("email outbox poll failed", "error", err)
		return
	}
	for _, item := range items {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := w.deliver(ctx, item); err != nil {
			slog.Warn("email outbox delivery failed", "id", item.ID, "kind", item.Kind, "error", err)
			if markErr := w.outbox.MarkFailed(ctx, item.ID, err); markErr != nil {
				slog.Warn("email outbox mark-failed failed", "id", item.ID, "error", markErr)
			}
			continue
		}
		if markErr := w.outbox.MarkDelivered(ctx, item.ID); markErr != nil {
			slog.Warn("email outbox mark-delivered failed", "id", item.ID, "error", markErr)
		}
	}
}

func (w *Worker) deliver(ctx context.Context, item OutboxItem) error {
	if w.sender == nil {
		return errors.New("no email sender configured")
	}
	message := httpapi.AccountEmail{
		Kind:      item.Kind,
		To:        item.To,
		ActionURL: item.ActionURL,
		ExpiresAt: item.ExpiresAt,
		Subject:   item.Subject,
		Body:      item.Body,
	}
	return w.sender.SendAccountEmail(ctx, message)
}

// Synchronous helper for tests and graceful shutdown.
func (w *Worker) DeliverAllOnce(ctx context.Context) {
	w.pollOnce(ctx)
}
