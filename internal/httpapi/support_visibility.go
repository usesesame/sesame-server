package httpapi

import (
	"context"

	adminstore "usesesame.app/backend/internal/admin"
)

type supportMailState struct {
	DeliveryConfigured    bool `json:"deliveryConfigured"`
	StaffNotifyConfigured bool `json:"staffNotifyConfigured"`
}

func (a *api) supportMailState() supportMailState {
	return supportMailState{
		DeliveryConfigured:    a.config.EmailSender != nil,
		StaffNotifyConfigured: a.config.SupportNotifyEmail != "",
	}
}

func supportOutboxReason(status string) (string, bool) {
	switch status {
	case "delivered":
		return "delivered", true
	case "pending", "processing":
		return "pending", true
	case "failed":
		return "failed", true
	default:
		return "", false
	}
}

func (a *api) supportUnqueuedReason(ctx context.Context, accountID string) string {
	if accountID == "" {
		return "guest"
	}
	if a.config.EmailSender == nil {
		return "mail-off"
	}
	enabled, err := a.supportReplyEmailEnabled(ctx, accountID)
	if err != nil {
		requestLog(ctx).Error("Sesame support delivery reason lookup failed", "error", err)
		return "not-queued"
	}
	if !enabled {
		return "opted-out"
	}
	return "not-queued"
}

func (a *api) enrichSupportDelivery(ctx context.Context, ticket *adminstore.TicketDetail) {
	fallback := ""
	resolvedFallback := false
	for index := range ticket.Messages {
		message := &ticket.Messages[index]
		if message.AuthorRole != "staff" {
			continue
		}
		if reason, ok := supportOutboxReason(message.EmailDeliveryStatus); ok {
			message.EmailDeliveryReason = reason
			continue
		}
		if !resolvedFallback {
			fallback = a.supportUnqueuedReason(ctx, ticket.AccountID)
			resolvedFallback = true
		}
		message.EmailDeliveryReason = fallback
	}
}
