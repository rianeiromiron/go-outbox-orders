// Package notify es el efecto que el worker aplica por cada evento. Aquí solo
// se simula (log); en un sistema real sería un email, un SMS, un webhook…
package notify

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
)

type Notification struct {
	EventID       uuid.UUID
	OrderID       uuid.UUID
	CustomerEmail string
	TotalCents    int64
	Currency      string
}

type Notifier interface {
	Notify(ctx context.Context, n Notification) error
}

// LogNotifier "envía" la notificación escribiéndola en el log.
type LogNotifier struct {
	Log *slog.Logger
}

func (l LogNotifier) Notify(_ context.Context, n Notification) error {
	l.Log.Info("notificación enviada",
		"event_id", n.EventID,
		"order_id", n.OrderID,
		"to", n.CustomerEmail,
		"total_cents", n.TotalCents,
		"currency", n.Currency,
	)
	return nil
}
